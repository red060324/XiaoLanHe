#!/usr/bin/env python3
"""Deterministic OpenAI-compatible server for an isolated CI host.

It binds to loopback by default. An authenticated, exact private-address listener
is available only through an explicit opt-in for a CI container bridge. The server
records request categories and counts only; it never logs, stores, hashes, or exposes
prompt, message, tool-result, or embedding input content.
"""

from __future__ import annotations

import argparse
import hmac
import ipaddress
import json
import os
import signal
import socket
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import urlsplit


DEFAULT_MAX_BODY_BYTES = 1 << 20
EMBEDDING_DIMENSIONS = 1024
ANSWER_MARKER = "CI_OPENAI_STUB_ANSWER"
TOOL_COMPLETION_MARKER = "done"
LIGHTRAG_COMPLETION_MARKER = "CI_LIGHTRAG_COMPLETION"
ROUTER_CONTENT = (
    '{"routeType":"EVIDENCE_ANSWER","responseMode":"qa",'
    '"needLocalKnowledge":true,"needWebSearch":false,'
    '"subQueries":[],"notes":[]}'
)

COUNTER_NAMES = (
    "requests_total",
    "chat_requests",
    "router_responses",
    "tool_call_responses",
    "tool_result_responses",
    "answer_responses",
    "lightrag_keyword_responses",
    "lightrag_extraction_responses",
    "lightrag_other_responses",
    "embedding_requests",
    "embedded_inputs",
    "malformed_requests",
    "oversized_requests",
    "auth_failures",
)


class Counters:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._values = {name: 0 for name in COUNTER_NAMES}

    def add(self, name: str, amount: int = 1) -> int:
        with self._lock:
            self._values[name] += amount
            return self._values[name]

    def snapshot(self) -> dict[str, int]:
        with self._lock:
            return dict(self._values)


class CIStubServer(ThreadingHTTPServer):
    allow_reuse_address = True
    daemon_threads = True
    request_queue_size = 32

    def __init__(
        self,
        server_address: tuple[str, int],
        handler_class: type[BaseHTTPRequestHandler],
        *,
        api_key: str | None,
        max_body_bytes: int,
    ) -> None:
        self.api_key = api_key
        self.max_body_bytes = max_body_bytes
        self.counters = Counters()
        super().__init__(server_address, handler_class)

    def handle_error(self, _request: Any, _client_address: Any) -> None:
        # BaseServer's default prints a traceback. Suppress it so unexpected
        # parser/client failures can never cause request content to reach logs.
        return


class CIStubServerIPv6(CIStubServer):
    address_family = socket.AF_INET6


class RequestError(Exception):
    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message


class DuplicateJSONKey(ValueError):
    pass


def _unique_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateJSONKey("duplicate JSON key")
        result[key] = value
    return result


def _invalid_constant(_value: str) -> None:
    raise ValueError("non-finite JSON number")


def _message_text(message: dict[str, Any]) -> str:
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if isinstance(item, dict) and item.get("type") == "text":
                text = item.get("text")
                if isinstance(text, str):
                    parts.append(text)
        return "\n".join(parts)
    return ""


def _combined_message_text(messages: list[dict[str, Any]]) -> str:
    return "\n".join(_message_text(message) for message in messages)


def _has_tool_result(messages: list[dict[str, Any]]) -> bool:
    return any(message.get("role") in {"tool", "function"} for message in messages)


def _tool_query(messages: list[dict[str, Any]]) -> str:
    """Extract the baseline research query without retaining it anywhere."""
    for message in reversed(messages):
        if message.get("role") != "user":
            continue
        text = _message_text(message).strip()
        if not text:
            continue
        try:
            value = json.loads(text)
        except (json.JSONDecodeError, TypeError, ValueError):
            continue
        if isinstance(value, dict):
            queries = value.get("queries")
            if isinstance(queries, list):
                for query in queries:
                    if isinstance(query, str) and query.strip():
                        return query.strip()[:500]
            objective = value.get("objective")
            if isinstance(objective, str) and objective.strip():
                return objective.strip()[:500]
    return "CI_LIGHTRAG_QUERY"


def _schema_value(schema: Any) -> Any:
    if not isinstance(schema, dict):
        return {}
    if "const" in schema:
        return schema["const"]
    enum = schema.get("enum")
    if isinstance(enum, list) and enum:
        return enum[0]
    if "default" in schema:
        return schema["default"]
    for keyword in ("oneOf", "anyOf", "allOf"):
        options = schema.get(keyword)
        if isinstance(options, list) and options:
            return _schema_value(options[0])
    kind = schema.get("type")
    if isinstance(kind, list):
        kind = next((item for item in kind if item != "null"), "null")
    if kind == "object" or isinstance(schema.get("properties"), dict):
        properties = schema.get("properties", {})
        required = schema.get("required", [])
        if not isinstance(required, list):
            required = []
        return {
            name: _schema_value(properties[name])
            for name in required
            if isinstance(name, str) and name in properties
        }
    if kind == "array":
        count = schema.get("minItems", 0)
        count = count if isinstance(count, int) and 0 < count <= 4 else 0
        return [_schema_value(schema.get("items")) for _ in range(count)]
    if kind == "string":
        return "ci"
    if kind == "integer":
        minimum = schema.get("minimum", 0)
        return int(minimum) if isinstance(minimum, (int, float)) else 0
    if kind == "number":
        minimum = schema.get("minimum", 0.0)
        return float(minimum) if isinstance(minimum, (int, float)) else 0.0
    if kind == "boolean":
        return False
    if kind == "null":
        return None
    return {}


def _structured_content(response_format: dict[str, Any]) -> str:
    if response_format.get("type") == "json_schema":
        wrapper = response_format.get("json_schema")
        if isinstance(wrapper, dict):
            schema = wrapper.get("schema")
            return json.dumps(_schema_value(schema), separators=(",", ":"))
    return '{"status":"ok"}'


def _classify_chat(payload: dict[str, Any]) -> tuple[str, str | None, list[dict[str, Any]] | None]:
    messages = payload["messages"]
    tools = payload.get("tools")
    if tools:
        if _has_tool_result(messages):
            return "tool_result", TOOL_COMPLETION_MARKER, None
        arguments = json.dumps(
            {"query": _tool_query(messages), "mode": "mix"},
            ensure_ascii=False,
            separators=(",", ":"),
        )
        tool_calls = [
            {
                "id": "call_ci_search_lightrag",
                "type": "function",
                "function": {
                    "name": "search_lightrag",
                    "arguments": arguments,
                },
            }
        ]
        return "tool_call", "", tool_calls

    combined = _combined_message_text(messages)
    lowered = combined.lower()
    last_user = ""
    for message in reversed(messages):
        if message.get("role") == "user":
            last_user = _message_text(message)
            break
    last_user_lower = last_user.lower()
    response_format = payload.get("response_format")

    if (
        "based on the last extraction task" in last_user_lower
        or "missed or incorrectly formatted entities" in last_user_lower
        or "missed or incorrectly described" in last_user_lower
    ):
        if isinstance(response_format, dict):
            return "lightrag_extraction", '{"entities":[],"relationships":[]}', None
        return "lightrag_extraction", "<|COMPLETE|>", None

    if (
        "expert keyword extractor" in lowered
        or ("high_level_keywords" in combined and "low_level_keywords" in combined)
    ):
        content = (
            '{"high_level_keywords":["game guide"],'
            '"low_level_keywords":["CI_LIGHTRAG_QUERY"]}'
        )
        return "lightrag_keyword", content, None

    if (
        "knowledge graph specialist" in lowered
        or "extract entities and relationships" in lowered
    ):
        if isinstance(response_format, dict):
            content = (
                '{"entities":[{"name":"CI_LIGHTRAG_QUERY",'
                '"type":"Concept","description":"Deterministic CI query entity"},'
                '{"name":"CI_LIGHTRAG_EVIDENCE","type":"Concept",'
                '"description":"Deterministic CI evidence entity"}],'
                '"relationships":[{"source":"CI_LIGHTRAG_QUERY",'
                '"target":"CI_LIGHTRAG_EVIDENCE","keywords":"game guide",'
                '"description":"The CI query retrieves the CI evidence."}]}'
            )
        else:
            content = (
                "entity<|#|>CI_LIGHTRAG_QUERY<|#|>Concept<|#|>"
                "Deterministic CI query entity\n"
                "entity<|#|>CI_LIGHTRAG_EVIDENCE<|#|>Concept<|#|>"
                "Deterministic CI evidence entity\n"
                "relation<|#|>CI_LIGHTRAG_QUERY<|#|>CI_LIGHTRAG_EVIDENCE<|#|>"
                "game guide<|#|>The CI query retrieves the CI evidence.\n"
                "<|COMPLETE|>"
            )
        return "lightrag_extraction", content, None

    if "router node" in lowered and "routetype" in lowered:
        return "router", ROUTER_CONTENT, None

    if (
        "answer node" in lowered
        or "答案生成节点" in combined
        or "基于给定证据回答用户" in combined
        or "【主路由】" in combined
    ):
        return "answer", ANSWER_MARKER, None

    if isinstance(response_format, dict):
        return "lightrag_other", _structured_content(response_format), None
    return "lightrag_other", LIGHTRAG_COMPLETION_MARKER, None


def _usage() -> dict[str, int]:
    return {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}


def _chat_response(
    model: str, content: str | None, tool_calls: list[dict[str, Any]] | None
) -> dict[str, Any]:
    message: dict[str, Any] = {"role": "assistant", "content": content}
    finish_reason = "stop"
    if tool_calls:
        message["tool_calls"] = tool_calls
        finish_reason = "tool_calls"
    return {
        "id": "chatcmpl-ci-openai-stub",
        "object": "chat.completion",
        "created": 0,
        "model": model,
        "choices": [
            {
                "index": 0,
                "message": message,
                "finish_reason": finish_reason,
            }
        ],
        "usage": _usage(),
    }


def _stream_chunks(
    model: str, content: str | None, tool_calls: list[dict[str, Any]] | None
) -> list[dict[str, Any]]:
    first_delta: dict[str, Any] = {"role": "assistant"}
    finish_reason = "stop"
    if tool_calls:
        first_delta["tool_calls"] = [
            {
                "index": index,
                "id": call["id"],
                "type": "function",
                "function": call["function"],
            }
            for index, call in enumerate(tool_calls)
        ]
        finish_reason = "tool_calls"
    else:
        first_delta["content"] = content or ""
    base = {
        "id": "chatcmpl-ci-openai-stub",
        "object": "chat.completion.chunk",
        "created": 0,
        "model": model,
    }
    first = dict(base)
    first["choices"] = [
        {"index": 0, "delta": first_delta, "finish_reason": None}
    ]
    final = dict(base)
    final["choices"] = [
        {"index": 0, "delta": {}, "finish_reason": finish_reason}
    ]
    final["usage"] = _usage()
    return [first, final]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "ci-openai-stub"
    sys_version = ""

    @property
    def stub_server(self) -> CIStubServer:
        return self.server  # type: ignore[return-value]

    def log_message(self, _format: str, *_args: Any) -> None:
        # Deliberately do not log request lines or bodies. A prompt must never
        # appear in CI output, even if a client embeds it in a URL by mistake.
        return

    def do_GET(self) -> None:
        self.stub_server.counters.add("requests_total")
        path = urlsplit(self.path).path
        if path == "/healthz":
            self._send_json(200, {"status": "ok"})
            return
        if path == "/__stats":
            self._send_json(200, self.stub_server.counters.snapshot())
            return
        self._send_error(404, "not_found", "route not found")

    def do_POST(self) -> None:
        self.stub_server.counters.add("requests_total")
        path = urlsplit(self.path).path
        if path not in {"/v1/chat/completions", "/v1/embeddings"}:
            self.close_connection = True
            self._send_error(404, "not_found", "route not found")
            return
        if not self._authorized():
            self.close_connection = True
            self.stub_server.counters.add("auth_failures")
            self._send_error(401, "invalid_api_key", "invalid API key")
            return
        try:
            payload = self._read_json_body()
            if path == "/v1/chat/completions":
                self._handle_chat(payload)
            else:
                self._handle_embeddings(payload)
        except RequestError as error:
            if error.status == 413:
                self.stub_server.counters.add("oversized_requests")
            else:
                self.stub_server.counters.add("malformed_requests")
            self._send_error(error.status, error.code, error.message)
        except Exception:
            self.stub_server.counters.add("malformed_requests")
            self._send_error(500, "internal_error", "request could not be processed")

    def _authorized(self) -> bool:
        expected = self.stub_server.api_key
        if expected is None:
            return True
        supplied = self.headers.get("Authorization", "")
        prefix = "Bearer "
        if not supplied.startswith(prefix):
            return False
        return hmac.compare_digest(supplied[len(prefix) :], expected)

    def _read_json_body(self) -> dict[str, Any]:
        if self.headers.get("Transfer-Encoding"):
            self.close_connection = True
            raise RequestError(400, "unsupported_transfer_encoding", "transfer encoding is not supported")
        if self.headers.get("Content-Encoding", "identity").lower() != "identity":
            raise RequestError(415, "unsupported_content_encoding", "content encoding is not supported")
        content_type = self.headers.get("Content-Type", "").split(";", 1)[0].strip().lower()
        if content_type != "application/json":
            raise RequestError(415, "unsupported_media_type", "Content-Type must be application/json")
        raw_length = self.headers.get("Content-Length")
        if raw_length is None:
            self.close_connection = True
            raise RequestError(411, "length_required", "Content-Length is required")
        try:
            length = int(raw_length, 10)
        except ValueError as error:
            self.close_connection = True
            raise RequestError(400, "invalid_content_length", "Content-Length is invalid") from error
        if length <= 0:
            raise RequestError(400, "invalid_json", "request body must contain one JSON object")
        if length > self.stub_server.max_body_bytes:
            self.close_connection = True
            raise RequestError(413, "request_too_large", "request body is too large")
        body = self.rfile.read(length)
        if len(body) != length:
            self.close_connection = True
            raise RequestError(400, "incomplete_body", "request body is incomplete")
        try:
            text = body.decode("utf-8")
            payload = json.loads(
                text,
                object_pairs_hook=_unique_object,
                parse_constant=_invalid_constant,
            )
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError, RecursionError) as error:
            raise RequestError(400, "invalid_json", "request body must contain one JSON object") from error
        if not isinstance(payload, dict):
            raise RequestError(400, "invalid_request", "request body must be a JSON object")
        return payload

    def _handle_chat(self, payload: dict[str, Any]) -> None:
        model = payload.get("model")
        messages = payload.get("messages")
        if not isinstance(model, str) or not model.strip():
            raise RequestError(400, "invalid_model", "model must be a non-empty string")
        if not isinstance(messages, list) or not messages:
            raise RequestError(400, "invalid_messages", "messages must be a non-empty array")
        for message in messages:
            if not isinstance(message, dict) or not isinstance(message.get("role"), str):
                raise RequestError(400, "invalid_messages", "each message must have a string role")
            content = message.get("content")
            if content is not None and not isinstance(content, (str, list)):
                raise RequestError(400, "invalid_messages", "message content must be text, an array, or null")
        tools = payload.get("tools")
        if tools is not None and not isinstance(tools, list):
            raise RequestError(400, "invalid_tools", "tools must be an array")
        response_format = payload.get("response_format")
        if response_format is not None and not isinstance(response_format, dict):
            raise RequestError(400, "invalid_response_format", "response_format must be an object")
        stream = payload.get("stream", False)
        if not isinstance(stream, bool):
            raise RequestError(400, "invalid_stream", "stream must be a boolean")

        self.stub_server.counters.add("chat_requests")
        category, content, tool_calls = _classify_chat(payload)
        counter = {
            "router": "router_responses",
            "tool_call": "tool_call_responses",
            "tool_result": "tool_result_responses",
            "answer": "answer_responses",
            "lightrag_keyword": "lightrag_keyword_responses",
            "lightrag_extraction": "lightrag_extraction_responses",
            "lightrag_other": "lightrag_other_responses",
        }[category]
        self.stub_server.counters.add(counter)
        if stream:
            self._send_sse(_stream_chunks(model.strip(), content, tool_calls))
        else:
            self._send_json(200, _chat_response(model.strip(), content, tool_calls))

    def _handle_embeddings(self, payload: dict[str, Any]) -> None:
        model = payload.get("model")
        values = payload.get("input")
        if not isinstance(model, str) or not model.strip():
            raise RequestError(400, "invalid_model", "model must be a non-empty string")
        if isinstance(values, str):
            inputs = [values]
        elif isinstance(values, list) and values and all(isinstance(value, str) for value in values):
            inputs = values
        else:
            raise RequestError(400, "invalid_input", "input must be a string or a non-empty array of strings")
        if len(inputs) > 2048:
            raise RequestError(400, "invalid_input", "input contains too many items")
        dimensions = payload.get("dimensions")
        if dimensions is not None and dimensions != EMBEDDING_DIMENSIONS:
            raise RequestError(400, "invalid_dimensions", "dimensions must be 1024 when provided")

        self.stub_server.counters.add("embedding_requests")
        self.stub_server.counters.add("embedded_inputs", len(inputs))
        # A shared non-zero unit vector makes the tiny CI corpus reliably
        # retrievable under cosine similarity, independent of prompt wording.
        vector = [1.0] + [0.0] * (EMBEDDING_DIMENSIONS - 1)
        data = [
            {"object": "embedding", "index": index, "embedding": vector}
            for index, _value in enumerate(inputs)
        ]
        response = {
            "object": "list",
            "model": model.strip(),
            "data": data,
            "usage": {"prompt_tokens": len(inputs), "total_tokens": len(inputs)},
        }
        self._send_json(200, response)

    def _send_sse(self, chunks: list[dict[str, Any]]) -> None:
        lines = [
            "data: " + json.dumps(chunk, ensure_ascii=False, separators=(",", ":")) + "\n\n"
            for chunk in chunks
        ]
        lines.append("data: [DONE]\n\n")
        body = "".join(lines).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_error(self, status: int, code: str, message: str) -> None:
        self._send_json(
            status,
            {
                "error": {
                    "message": message,
                    "type": "invalid_request_error",
                    "param": None,
                    "code": code,
                }
            },
        )

    def _send_json(self, status: int, payload: Any) -> None:
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        if status == 401:
            self.send_header("WWW-Authenticate", "Bearer")
        self.end_headers()
        self.wfile.write(body)


def _validate_bind_address(
    host: str, *, allow_private_host: bool, api_key: str | None
) -> ipaddress.IPv4Address | ipaddress.IPv6Address:
    try:
        address = ipaddress.ip_address(host)
    except ValueError as error:
        raise ValueError("host must be an explicit numeric address") from error
    if address.is_loopback:
        return address
    if (
        not allow_private_host
        or not address.is_private
        or address.is_unspecified
        or address.is_multicast
        or address.is_link_local
    ):
        raise ValueError(
            "host must be loopback unless --allow-private-host selects an exact private address"
        )
    if api_key is None or not 32 <= len(api_key) <= 512:
        raise ValueError(
            "a 32-512 character API key is required for a private-network listener"
        )
    return address


def create_server(
    host: str,
    port: int,
    *,
    api_key: str | None = None,
    max_body_bytes: int = DEFAULT_MAX_BODY_BYTES,
    allow_private_host: bool = False,
) -> CIStubServer:
    if api_key == "":
        api_key = None
    if api_key is not None and ("\r" in api_key or "\n" in api_key):
        raise ValueError("API key must not contain line breaks")
    address = _validate_bind_address(
        host, allow_private_host=allow_private_host, api_key=api_key
    )
    if not isinstance(port, int) or not 0 <= port <= 65535:
        raise ValueError("port must be between 0 and 65535")
    if not isinstance(max_body_bytes, int) or not 1 <= max_body_bytes <= 64 << 20:
        raise ValueError("max body bytes must be between 1 and 67108864")
    server_type = CIStubServerIPv6 if address.version == 6 else CIStubServer
    return server_type(
        (host, port),
        Handler,
        api_key=api_key,
        max_body_bytes=max_body_bytes,
    )


def _env_int(name: str, default: str | None = None) -> int | None:
    raw = os.environ.get(name, default)
    if raw is None:
        return None
    try:
        return int(raw, 10)
    except ValueError as error:
        raise argparse.ArgumentTypeError(f"{name} must be an integer") from error


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default=os.environ.get("CI_OPENAI_STUB_HOST"))
    parser.add_argument("--port", type=int, default=_env_int("CI_OPENAI_STUB_PORT"))
    parser.add_argument("--api-key", default=os.environ.get("CI_OPENAI_STUB_API_KEY"))
    parser.add_argument(
        "--allow-private-host",
        action="store_true",
        help=(
            "allow one exact private, non-link-local address when bearer auth is configured; "
            "intended only for an isolated CI container bridge"
        ),
    )
    parser.add_argument(
        "--max-body-bytes",
        type=int,
        default=_env_int("CI_OPENAI_STUB_MAX_BODY_BYTES", str(DEFAULT_MAX_BODY_BYTES)),
    )
    args = parser.parse_args(argv)
    if args.host is None:
        parser.error("--host or CI_OPENAI_STUB_HOST is required")
    if args.port is None:
        parser.error("--port or CI_OPENAI_STUB_PORT is required")
    if not 1 <= args.port <= 65535:
        parser.error("port must be between 1 and 65535")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        server = create_server(
            args.host,
            args.port,
            api_key=args.api_key,
            max_body_bytes=args.max_body_bytes,
            allow_private_host=args.allow_private_host,
        )
    except ValueError as error:
        print(f"ci-openai-stub: {error}", file=sys.stderr)
        return 2

    def stop(_signum: int, _frame: Any) -> None:
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    print(
        f"ci-openai-stub listening on {args.host}:{args.port} "
        f"auth_enabled={args.api_key is not None}",
        file=sys.stderr,
        flush=True,
    )
    try:
        server.serve_forever(poll_interval=0.2)
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
