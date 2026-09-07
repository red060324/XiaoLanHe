import contextlib
import importlib.util
import io
import json
import os
import pathlib
import signal
import sys
import tempfile
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("guarded_start.py")
MODULE_DIR = str(MODULE_PATH.parent)
if MODULE_DIR not in sys.path:
    sys.path.insert(0, MODULE_DIR)
SPEC = importlib.util.spec_from_file_location("guarded_start", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)

BASE_ENV = {
    "XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR": "/host/rebuild-fence",
    "XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR": "/host/writer-evidence",
    "XLH_LIGHTRAG_ATTEMPT_ID": "attempt-secret-canary",
    "XLH_LIGHTRAG_DEPLOYMENT_GENERATION": "generation-secret-canary",
    "XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256": "sha256:" + "a" * 64,
    "XLH_EXPECTED_CONTRACT_SHA256": "sha256:" + "b" * 64,
}


def parse_lines(output):
    return [json.loads(line) for line in output.getvalue().splitlines() if line]


class TrackingOutput(io.StringIO):
    def __init__(self):
        super().__init__()
        self.flush_calls = 0

    def flush(self):
        self.flush_calls += 1
        return super().flush()


class GuardedStartTests(unittest.TestCase):
    def setUp(self):
        self.environment = mock.patch.dict(os.environ, BASE_ENV, clear=True)
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def assert_event(self, event, *, mode, stage, outcome, reason):
        self.assertEqual(set(event), MODULE.GUARD_EVENT_KEYS)
        self.assertEqual(
            event,
            {
                "schema_version": 1,
                "event": "lightrag.guard",
                "mode": mode,
                "stage": stage,
                "outcome": outcome,
                "reason": reason,
            },
        )

    def test_fixed_schema_and_vocabulary(self):
        for mode in MODULE.GUARD_MODES:
            for stage in MODULE.GUARD_STAGES:
                self.assert_event(
                    MODULE.guard_event(mode, stage, "ok", "ok"),
                    mode=mode,
                    stage=stage,
                    outcome="ok",
                    reason="ok",
                )
        for values in (
            ("unknown", "exec", "ok", "ok"),
            ("steady", "unknown", "ok", "ok"),
            ("steady", "exec", "unknown", "ok"),
            ("steady", "exec", "error", "secret detail"),
            ("steady", "exec", "ok", "unknown"),
            ("steady", "exec", "attempt", "ok"),
            ("steady", "exec", "ok", "starting"),
            ("steady", "exec", "error", "starting"),
        ):
            with self.subTest(values=values), self.assertRaises(ValueError):
                MODULE.guard_event(*values)

    def test_bootstrap_flushes_exec_event_before_exec(self):
        output = TrackingOutput()
        observed = []

        def execvp(executable, arguments):
            observed.append(
                (executable, arguments, output.getvalue(), output.flush_calls)
            )
            raise SystemExit(0)

        with contextlib.redirect_stdout(output), mock.patch.object(
            MODULE.os, "execvp", side_effect=execvp
        ), self.assertRaises(SystemExit) as raised:
            MODULE.bootstrap()

        self.assertEqual(raised.exception.code, 0)
        self.assertEqual(observed[0][0], "python")
        self.assertIn("/opt/xlh/rebuild_fence.py", observed[0][1])
        self.assertTrue(observed[0][2].endswith("\n"))
        self.assertGreaterEqual(observed[0][3], 1)
        events = parse_lines(output)
        self.assertEqual(len(events), 1)
        self.assert_event(
            events[0],
            mode="bootstrap",
            stage="exec",
            outcome="attempt",
            reason="starting",
        )

    def test_steady_holds_shared_lease_through_supervised_server_lifecycle(self):
        output = TrackingOutput()
        observed = []
        lease_active = False

        def verify(_store, generation, contract):
            self.assertTrue(lease_active)
            observed.append(("verify", generation, contract, output.getvalue()))

        @contextlib.contextmanager
        def lease(_store, *, exclusive):
            nonlocal lease_active
            self.assertFalse(exclusive)
            self.assertFalse(lease_active)
            lease_active = True
            observed.append(("lease-enter",))
            try:
                yield 123
            finally:
                observed.append(("lease-exit",))
                lease_active = False

        def supervise(executable, arguments):
            self.assertTrue(lease_active)
            observed.append(
                (
                    "supervise",
                    executable,
                    arguments,
                    output.getvalue(),
                    output.flush_calls,
                )
            )
            raise SystemExit(0)

        with contextlib.redirect_stdout(output), mock.patch.object(
            MODULE.rebuild_fence.FenceStore,
            "serving_lease",
            autospec=True,
            side_effect=lease,
        ), mock.patch.object(
            MODULE.rebuild_fence.FenceStore,
            "verify_current",
            autospec=True,
            side_effect=verify,
        ), mock.patch.object(
            MODULE, "supervise_guarded", side_effect=supervise
        ), self.assertRaises(SystemExit) as raised:
            MODULE.steady()

        self.assertEqual(raised.exception.code, 0)
        self.assertEqual([event[0] for event in observed], ["lease-enter", "verify", "supervise", "lease-exit"])
        self.assertEqual(observed[1][3], "")
        self.assertEqual(observed[2][1], "lightrag-gunicorn")
        self.assertTrue(observed[2][3].endswith("\n"))
        events = parse_lines(output)
        self.assertEqual([(event["stage"], event["outcome"]) for event in events], [("fence", "ok")])

    def test_main_classifies_failures_without_traceback_or_raw_canary(self):
        canary = "raw-exception-credential-content-canary"
        cases = (
            ("bootstrap", {}, "preflight", "required_config"),
            ("bootstrap", {**BASE_ENV, "XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR": "relative"}, "preflight", "invalid_path"),
            ("steady", BASE_ENV, "fence", "fence_rejected"),
        )
        for mode, environment, stage, reason in cases:
            with self.subTest(mode=mode, reason=reason), mock.patch.dict(
                os.environ, environment, clear=True
            ), mock.patch.object(sys, "argv", [str(MODULE_PATH), mode]), mock.patch.object(
                MODULE.rebuild_fence.FenceStore,
                "verify_current",
                side_effect=RuntimeError(canary),
            ), contextlib.redirect_stdout(io.StringIO()) as output, contextlib.redirect_stderr(
                io.StringIO()
            ) as errors, self.assertRaises(SystemExit) as raised:
                MODULE.main()
            self.assertEqual(raised.exception.code, 1)
            events = parse_lines(output)
            self.assertEqual(len(events), 1)
            self.assert_event(
                events[0], mode=mode, stage=stage, outcome="error", reason=reason
            )
            combined = output.getvalue() + errors.getvalue()
            self.assertNotIn(canary, combined)
            self.assertNotIn("Traceback", combined)
            for forbidden in (
                "attempt-secret-canary",
                "generation-secret-canary",
                "sha256",
                "/host/",
                "content",
                "credential",
            ):
                self.assertNotIn(forbidden, combined)

    def test_exec_failure_emits_bounded_failure_after_attempt(self):
        canary = "raw-exec-password-canary"
        output = io.StringIO()
        errors = io.StringIO()
        with mock.patch.object(
            sys, "argv", [str(MODULE_PATH), "bootstrap"]
        ), mock.patch.object(
            MODULE.os, "execvp", side_effect=OSError(canary)
        ), contextlib.redirect_stdout(output), contextlib.redirect_stderr(
            errors
        ), self.assertRaises(SystemExit) as raised:
            MODULE.main()

        self.assertEqual(raised.exception.code, 1)
        events = parse_lines(output)
        self.assertEqual(
            [(event["stage"], event["outcome"], event["reason"]) for event in events],
            [("exec", "attempt", "starting"), ("exec", "error", "exec_failed")],
        )
        combined = output.getvalue() + errors.getvalue()
        self.assertNotIn(canary, combined)
        self.assertNotIn("Traceback", combined)

    def test_supervisor_holds_real_shared_lease_until_child_exit(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MODULE.rebuild_fence.FenceStore(pathlib.Path(directory))
            lifecycle = []
            test_case = self

            class Process:
                pid = 321

                def wait(self, timeout=None):
                    lifecycle.append("wait")
                    contender = MODULE.rebuild_fence.FenceStore(
                        pathlib.Path(directory)
                    )
                    with test_case.assertRaises(
                        MODULE.rebuild_fence.ClassifiedFailure
                    ) as raised:
                        with contender.serving_lease(exclusive=True):
                            pass
                    test_case.assertEqual(
                        raised.exception.code, "serving_lock_busy"
                    )
                    return 0

            with store.serving_lease(exclusive=False), mock.patch.object(
                MODULE.subprocess, "Popen", return_value=Process()
            ), mock.patch.object(
                MODULE, "_process_group_alive", return_value=False
            ), mock.patch.object(
                MODULE, "_propagate_process_status", side_effect=SystemExit(0)
            ), contextlib.redirect_stdout(io.StringIO()) as output, self.assertRaises(
                SystemExit
            ) as raised:
                MODULE.supervise_guarded(
                    "lightrag-gunicorn", ["lightrag-gunicorn"]
                )

            self.assertEqual(raised.exception.code, 0)
            self.assertEqual(lifecycle, ["wait"])
            with MODULE.rebuild_fence.FenceStore(
                pathlib.Path(directory)
            ).serving_lease(exclusive=True):
                pass
            self.assert_event(
                parse_lines(output)[0],
                mode="steady",
                stage="exec",
                outcome="attempt",
                reason="starting",
            )

    def test_controller_exclusion_rejects_steady_before_verify_or_spawn(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            store = MODULE.rebuild_fence.FenceStore(root)
            with store.serving_lease(exclusive=True), mock.patch.object(
                MODULE, "FENCE_CONTAINER_DIR", root
            ), mock.patch.object(
                MODULE.rebuild_fence.FenceStore, "verify_current", autospec=True
            ) as verify, mock.patch.object(
                MODULE.subprocess, "Popen"
            ) as spawn, self.assertRaises(MODULE.GuardFailure) as raised:
                MODULE.steady()

            self.assertEqual(raised.exception.stage, "fence")
            self.assertEqual(raised.exception.reason, "fence_rejected")
            verify.assert_not_called()
            spawn.assert_not_called()

    def test_supervisor_forwards_signals_and_propagates_child_status(self):
        handlers = {}
        forwarded = []

        class Process:
            pid = 654

            def wait(self, timeout=None):
                handlers[signal.SIGTERM](signal.SIGTERM, None)
                return -signal.SIGTERM

        def install(signum, handler):
            previous = handlers.get(signum, signal.SIG_DFL)
            handlers[signum] = handler
            return previous

        with mock.patch.object(MODULE.signal, "signal", side_effect=install), mock.patch.object(
            MODULE.subprocess, "Popen", return_value=Process()
        ), mock.patch.object(
            MODULE, "_signal_process_group", side_effect=lambda _process, signum: forwarded.append(signum)
        ), mock.patch.object(
            MODULE, "_process_group_alive", return_value=False
        ), mock.patch.object(
            MODULE, "_propagate_process_status", side_effect=SystemExit(143)
        ) as propagate, contextlib.redirect_stdout(io.StringIO()), self.assertRaises(
            SystemExit
        ) as raised:
            MODULE.supervise_guarded("server", ["server"])

        self.assertEqual(raised.exception.code, 143)
        self.assertEqual(forwarded, [signal.SIGTERM])
        propagate.assert_called_once_with(-signal.SIGTERM)

    def test_supervisor_propagates_normal_child_exit_code(self):
        class Process:
            pid = 987

            def wait(self, timeout=None):
                return 23

        output = io.StringIO()
        with mock.patch.object(
            MODULE.subprocess, "Popen", return_value=Process()
        ), mock.patch.object(
            MODULE, "_process_group_alive", return_value=False
        ), contextlib.redirect_stdout(output), self.assertRaises(SystemExit) as raised:
            MODULE.supervise_guarded("server", ["server"])

        self.assertEqual(raised.exception.code, 23)
        events = parse_lines(output)
        self.assertEqual(
            [(event["outcome"], event["reason"]) for event in events],
            [("attempt", "starting"), ("error", "process_failed")],
        )

    def test_process_cleanup_failure_emits_one_error_event(self):
        class Process:
            pid = 988

            def wait(self, timeout=None):
                return 0

        output = io.StringIO()
        with mock.patch.object(
            MODULE.subprocess, "Popen", return_value=Process()
        ), mock.patch.object(
            MODULE,
            "_quiesce_process_group",
            side_effect=MODULE.GuardFailure("exec", "process_failed"),
        ), mock.patch.object(
            sys, "argv", [str(MODULE_PATH), "steady"]
        ), mock.patch.object(
            MODULE.rebuild_fence.FenceStore, "serving_lease", autospec=True
        ) as lease, mock.patch.object(
            MODULE.rebuild_fence.FenceStore, "verify_current", autospec=True
        ), contextlib.redirect_stdout(output), self.assertRaises(SystemExit) as raised:
            lease.return_value = contextlib.nullcontext(123)
            MODULE.main()

        self.assertEqual(raised.exception.code, 1)
        events = parse_lines(output)
        self.assertEqual(
            [(event["outcome"], event["reason"]) for event in events],
            [
                ("ok", "ok"),
                ("attempt", "starting"),
                ("error", "process_failed"),
            ],
        )

    def test_supervisor_escalates_ignored_termination_within_bound(self):
        handlers = {}
        forwarded = []

        class Process:
            pid = 765
            attempts = 0

            def wait(self, timeout=None):
                self.attempts += 1
                if self.attempts == 1:
                    handlers[signal.SIGTERM](signal.SIGTERM, None)
                    raise MODULE.subprocess.TimeoutExpired("server", timeout)
                return -signal.SIGKILL

        def install(signum, handler):
            previous = handlers.get(signum, signal.SIG_DFL)
            handlers[signum] = handler
            return previous

        with mock.patch.object(
            MODULE.signal, "signal", side_effect=install
        ), mock.patch.object(
            MODULE.subprocess, "Popen", return_value=Process()
        ), mock.patch.object(
            MODULE,
            "_signal_process_group",
            side_effect=lambda _process, signum: forwarded.append(signum),
        ), mock.patch.object(
            MODULE, "_process_group_alive", return_value=False
        ), mock.patch.object(
            MODULE, "SERVER_TERMINATION_GRACE_SECONDS", 0
        ), mock.patch.object(
            MODULE, "_propagate_process_status", side_effect=SystemExit(137)
        ), contextlib.redirect_stdout(io.StringIO()), self.assertRaises(
            SystemExit
        ) as raised:
            MODULE.supervise_guarded("server", ["server"])

        self.assertEqual(raised.exception.code, 137)
        self.assertEqual(forwarded, [signal.SIGTERM, signal.SIGKILL])


if __name__ == "__main__":
    unittest.main()
