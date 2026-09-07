#!/usr/bin/env python3
"""Black-box HTTP tests for ci-openai-stub.py using only the stdlib."""

from __future__ import annotations

import contextlib
import http.client
import importlib.util
import io
import json
import pathlib
import threading
import unittest
from typing import Any


SCRIPT_PATH = pathlib.Path(__file__).with_name("ci-openai-stub.py")
SPEC = importlib.util.spec_from_file_location("ci_openai_stub", SCRIPT_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"cannot load {SCRIPT_PATH}")
STUB = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(STUB)

API_KEY = "ci-openai-private-key-at-least-32-chars"


class StubHTTPTest(unittest.TestCase):
    def setUp(self) -> None:
        self.server = STUB.create_server(
            "127.0.0.1",
            0,
            api_key=API_KEY,
            max_body_bytes=32 << 10,
        )
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.port = self.server.server_address[1]

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)
        self.assertFalse(self.thread.is_alive())

    def request(
        self,
        method: str,
        path: str,
        body: bytes | None = None,
        *,
        authenticated: bool = True,
        content_type: str | None = "application/json",
    ) -> tuple[int, dict[str, str], bytes]:
        headers: dict[str, str] = {}
        if authenticated:
            headers["Authorization"] = f"Bearer {API_KEY}"
        if body is not None and content_type is not None:
            headers["Content-Type"] = content_type
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=3)
        try:
            connection.request(method, path, body=body, headers=headers)
            response = connection.getresponse()
            response_body = response.read()
            return response.status, dict(response.getheaders()), response_body
        finally:
            connection.close()

    def json_request(
        self,
        path: str,
        payload: Any,
        *,
        authenticated: bool = True,
    ) -> tuple[int, dict[str, str], Any]:
        status, headers, body = self.request(
            "POST",
            path,
            json.dumps(payload, ensure_ascii=False).encode("utf-8"),
            authenticated=authenticated,
        )
        return status, headers, json.loads(body)

    def stats(self) -> dict[str, int]:
        status, _headers, body = self.request("GET", "/__stats", authenticated=False)
        self.assertEqual(status, 200)
        return json.loads(body)

    def test_health_routes_and_configured_auth(self) -> None:
        status, headers, body = self.request("GET", "/healthz", authenticated=False)
        self.assertEqual(status, 200)
        self.assertTrue(headers["Content-Type"].startswith("application/json"))
        self.assertEqual(json.loads(body), {"status": "ok"})

        status, _headers, error = self.json_request(
            "/v1/chat/completions",
            {"model": "ci", "messages": [{"role": "user", "content": "secret"}]},
            authenticated=False,
        )
        self.assertEqual(status, 401)
        self.assertEqual(error["error"]["code"], "invalid_api_key")

        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=3)
        try:
            body_bytes = b'{"model":"ci","messages":[{"role":"user","content":"secret"}]}'
            connection.request(
                "POST",
                "/v1/chat/completions",
                body=body_bytes,
                headers={
                    "Authorization": "Bearer wrong",
                    "Content-Type": "application/json",
                },
            )
            response = connection.getresponse()
            self.assertEqual(response.status, 401)
            self.assertEqual(response.getheader("WWW-Authenticate"), "Bearer")
            response.read()
        finally:
            connection.close()

        status, _headers, error_body = self.request(
            "GET", "/does-not-exist?prompt=do-not-log-me", authenticated=False
        )
        self.assertEqual(status, 404)
        self.assertEqual(json.loads(error_body)["error"]["code"], "not_found")
        self.assertEqual(self.stats()["auth_failures"], 2)

    def test_router_tool_cycle_answer_and_stats(self) -> None:
        router_request = {
            "model": "qwen3.5-flash",
            "messages": [
                {"role": "system", "content": "You are XiaoLanHe's Router Node. Return routeType JSON."},
                {"role": "user", "content": "private router prompt"},
            ],
        }
        status, _headers, response = self.json_request("/v1/chat/completions", router_request)
        self.assertEqual(status, 200)
        self.assertEqual(response["choices"][0]["message"]["content"], STUB.ROUTER_CONTENT)
        self.assertEqual(
            json.loads(response["choices"][0]["message"]["content"])["routeType"],
            "EVIDENCE_ANSWER",
        )

        tool_request = {
            "model": "qwen3.5-flash",
            "messages": [
                {
                    "role": "user",
                    "content": json.dumps({"queries": ["CI_LIGHTRAG_QUERY"]}),
                }
            ],
            "tools": [
                {
                    "type": "function",
                    "function": {"name": "search_lightrag", "parameters": {"type": "object"}},
                }
            ],
        }
        status, _headers, response = self.json_request("/v1/chat/completions", tool_request)
        self.assertEqual(status, 200)
        choice = response["choices"][0]
        self.assertEqual(choice["finish_reason"], "tool_calls")
        call = choice["message"]["tool_calls"][0]
        self.assertEqual(call["function"]["name"], "search_lightrag")
        self.assertEqual(
            json.loads(call["function"]["arguments"]),
            {"query": "CI_LIGHTRAG_QUERY", "mode": "mix"},
        )

        follow_up = dict(tool_request)
        follow_up["messages"] = tool_request["messages"] + [
            choice["message"],
            {
                "role": "tool",
                "tool_call_id": call["id"],
                "content": '{"items":[{"content":"private evidence"}]}',
            },
        ]
        status, _headers, response = self.json_request("/v1/chat/completions", follow_up)
        self.assertEqual(status, 200)
        self.assertEqual(response["choices"][0]["message"]["content"], "done")

        answer_request = {
            "model": "qwen3.5-flash",
            "messages": [
                {"role": "system", "content": "你是小蓝盒的答案生成节点 Answer Node。"},
                {"role": "user", "content": "【主路由】\nEVIDENCE_ANSWER"},
            ],
        }
        status, _headers, response = self.json_request("/v1/chat/completions", answer_request)
        self.assertEqual(status, 200)
        self.assertEqual(response["choices"][0]["message"]["content"], STUB.ANSWER_MARKER)

        answer_request["stream"] = True
        answer_request["stream_options"] = {"include_usage": True}
        status, headers, body = self.request(
            "POST",
            "/v1/chat/completions",
            json.dumps(answer_request).encode(),
        )
        self.assertEqual(status, 200)
        self.assertTrue(headers["Content-Type"].startswith("text/event-stream"))
        self.assertIn(STUB.ANSWER_MARKER.encode(), body)
        self.assertTrue(body.endswith(b"data: [DONE]\n\n"))

        stats = self.stats()
        self.assertEqual(stats["chat_requests"], 5)
        self.assertEqual(stats["router_responses"], 1)
        self.assertEqual(stats["tool_call_responses"], 1)
        self.assertEqual(stats["tool_result_responses"], 1)
        self.assertEqual(stats["answer_responses"], 2)
        self.assertNotIn("prompts", stats)

    def test_lightrag_keyword_and_extraction_shapes(self) -> None:
        keyword_request = {
            "model": "qwen3.5-flash",
            "messages": [
                {"role": "system", "content": "You are an expert keyword extractor."},
                {
                    "role": "user",
                    "content": "high_level_keywords low_level_keywords ---Real Data--- User Query: secret",
                },
            ],
            "response_format": {"type": "json_object"},
        }
        status, _headers, response = self.json_request("/v1/chat/completions", keyword_request)
        self.assertEqual(status, 200)
        keywords = json.loads(response["choices"][0]["message"]["content"] )
        self.assertEqual(set(keywords), {"high_level_keywords", "low_level_keywords"})
        self.assertTrue(keywords["high_level_keywords"])
        self.assertTrue(keywords["low_level_keywords"])

        extraction_request = {
            "model": "qwen3.5-flash",
            "messages": [
                {"role": "system", "content": "You are a Knowledge Graph Specialist."},
                {"role": "user", "content": "Extract entities and relationships from private text."},
            ],
        }
        status, _headers, response = self.json_request("/v1/chat/completions", extraction_request)
        self.assertEqual(status, 200)
        content = response["choices"][0]["message"]["content"]
        self.assertIn("entity<|#|>CI_LIGHTRAG_QUERY<|#|>Concept<|#|>", content)
        self.assertIn("relation<|#|>CI_LIGHTRAG_QUERY<|#|>CI_LIGHTRAG_EVIDENCE", content)
        self.assertTrue(content.endswith("<|COMPLETE|>"))

        continuation_request = dict(extraction_request)
        continuation_request["messages"] = extraction_request["messages"] + [
            {"role": "assistant", "content": content},
            {
                "role": "user",
                "content": "Based on the last extraction task, identify any missed or incorrectly formatted entities.",
            },
        ]
        status, _headers, response = self.json_request("/v1/chat/completions", continuation_request)
        self.assertEqual(status, 200)
        self.assertEqual(response["choices"][0]["message"]["content"], "<|COMPLETE|>")

        json_extraction_request = dict(extraction_request)
        json_extraction_request["response_format"] = {"type": "json_object"}
        status, _headers, response = self.json_request("/v1/chat/completions", json_extraction_request)
        self.assertEqual(status, 200)
        extraction = json.loads(response["choices"][0]["message"]["content"] )
        self.assertEqual(extraction["entities"][0]["name"], "CI_LIGHTRAG_QUERY")
        self.assertEqual(extraction["relationships"][0]["target"], "CI_LIGHTRAG_EVIDENCE")

        stats = self.stats()
        self.assertEqual(stats["lightrag_keyword_responses"], 1)
        self.assertEqual(stats["lightrag_extraction_responses"], 3)

    def test_embeddings_have_1024_float_values_per_string_input(self) -> None:
        status, _headers, response = self.json_request(
            "/v1/embeddings",
            {
                "model": "text-embedding-v4",
                "input": ["private document", "private query"],
                "encoding_format": "base64",
            },
        )
        self.assertEqual(status, 200)
        self.assertEqual(response["object"], "list")
        self.assertEqual([item["index"] for item in response["data"]], [0, 1])
        for item in response["data"]:
            self.assertEqual(len(item["embedding"]), STUB.EMBEDDING_DIMENSIONS)
            self.assertTrue(all(isinstance(value, float) for value in item["embedding"]))
            self.assertEqual(item["embedding"][0], 1.0)
        stats = self.stats()
        self.assertEqual(stats["embedding_requests"], 1)
        self.assertEqual(stats["embedded_inputs"], 2)

    def test_malformed_content_type_trailing_json_and_fields_are_rejected(self) -> None:
        valid = b'{"model":"ci","messages":[{"role":"user","content":"secret"}]}'
        status, _headers, body = self.request(
            "POST", "/v1/chat/completions", valid, content_type="text/plain"
        )
        self.assertEqual(status, 415)
        self.assertEqual(json.loads(body)["error"]["code"], "unsupported_media_type")

        status, _headers, body = self.request(
            "POST", "/v1/chat/completions", valid + b" []"
        )
        self.assertEqual(status, 400)
        self.assertEqual(json.loads(body)["error"]["code"], "invalid_json")

        duplicate = b'{"model":"ci","model":"other","messages":[]}'
        status, _headers, body = self.request("POST", "/v1/chat/completions", duplicate)
        self.assertEqual(status, 400)
        self.assertEqual(json.loads(body)["error"]["code"], "invalid_json")

        status, _headers, response = self.json_request(
            "/v1/embeddings", {"model": "embed", "input": ["ok", 3]}
        )
        self.assertEqual(status, 400)
        self.assertEqual(response["error"]["code"], "invalid_input")
        self.assertEqual(self.stats()["malformed_requests"], 4)

    def test_oversized_request_is_rejected_before_json_parsing(self) -> None:
        tiny_server = STUB.create_server(
            "127.0.0.1", 0, api_key=None, max_body_bytes=32
        )
        thread = threading.Thread(target=tiny_server.serve_forever, daemon=True)
        thread.start()
        connection = http.client.HTTPConnection(
            "127.0.0.1", tiny_server.server_address[1], timeout=3
        )
        try:
            connection.request(
                "POST",
                "/v1/chat/completions",
                body=b"x" * 33,
                headers={"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            self.assertEqual(response.status, 413)
            self.assertEqual(json.loads(response.read())["error"]["code"], "request_too_large")
            self.assertEqual(tiny_server.counters.snapshot()["oversized_requests"], 1)
        finally:
            connection.close()
            tiny_server.shutdown()
            tiny_server.server_close()
            thread.join(timeout=3)

    def test_no_prompt_content_is_written_to_stderr(self) -> None:
        secret = "SUPER_SECRET_PROMPT_CANARY"
        capture = io.StringIO()
        with contextlib.redirect_stderr(capture):
            status, _headers, _body = self.request(
                "POST",
                "/v1/chat/completions",
                (
                    '{"model":"ci","messages":[{"role":"user","content":"'
                    + secret
                    + '"}]} trailing'
                ).encode(),
            )
            self.assertEqual(status, 400)
        self.assertNotIn(secret, capture.getvalue())


class StubConfigurationTest(unittest.TestCase):
    def test_server_requires_numeric_loopback(self) -> None:
        for host in ("0.0.0.0", "192.0.2.10", "localhost"):
            with self.subTest(host=host), self.assertRaises(ValueError):
                STUB.create_server(host, 0)

    def test_private_listener_requires_exact_address_opt_in_and_auth(self) -> None:
        for host in ("10.1.2.3", "172.18.0.1", "fd00::1"):
            with self.subTest(host=host), self.assertRaises(ValueError):
                STUB._validate_bind_address(
                    host, allow_private_host=False, api_key=API_KEY
                )
            STUB._validate_bind_address(
                host, allow_private_host=True, api_key=API_KEY
            )

        for host in ("0.0.0.0", "::", "169.254.1.1", "ff02::1"):
            with self.subTest(host=host), self.assertRaises(ValueError):
                STUB._validate_bind_address(
                    host, allow_private_host=True, api_key=API_KEY
                )
        for api_key in (None, "too-short", "x" * 513):
            with self.subTest(api_key=api_key), self.assertRaises(ValueError):
                STUB._validate_bind_address(
                    "172.18.0.1", allow_private_host=True, api_key=api_key
                )

    def test_auth_is_optional_when_not_configured(self) -> None:
        server = STUB.create_server("127.0.0.1", 0)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        connection = http.client.HTTPConnection("127.0.0.1", server.server_address[1], timeout=3)
        try:
            payload = json.dumps(
                {
                    "model": "ci",
                    "messages": [
                        {"role": "system", "content": "Answer Node"},
                        {"role": "user", "content": "question"},
                    ],
                }
            )
            connection.request(
                "POST",
                "/v1/chat/completions",
                body=payload,
                headers={"Content-Type": "application/json"},
            )
            response = connection.getresponse()
            self.assertEqual(response.status, 200)
            response.read()
        finally:
            connection.close()
            server.shutdown()
            server.server_close()
            thread.join(timeout=3)


if __name__ == "__main__":
    unittest.main()
