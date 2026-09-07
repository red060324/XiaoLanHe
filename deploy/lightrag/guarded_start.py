#!/usr/bin/env python3
"""Fail-closed entry point for LightRAG bootstrap and steady state."""

from __future__ import annotations

import json
import os
import pathlib
import signal
import subprocess
import sys
import time

import rebuild_fence


GUARD_EVENT_KEYS = frozenset(
    {"schema_version", "event", "mode", "stage", "outcome", "reason"}
)
GUARD_MODES = frozenset({"bootstrap", "steady"})
GUARD_STAGES = frozenset({"preflight", "fence", "exec"})
GUARD_OUTCOMES = frozenset({"attempt", "ok", "error"})
GUARD_REASONS = frozenset(
    {
        "ok",
        "starting",
        "required_config",
        "invalid_path",
        "fence_rejected",
        "exec_failed",
        "process_failed",
        "unknown",
    }
)
GUARD_CLASSIFICATIONS = frozenset(
    {
        ("attempt", "starting"),
        ("ok", "ok"),
        ("error", "required_config"),
        ("error", "invalid_path"),
        ("error", "fence_rejected"),
        ("error", "exec_failed"),
        ("error", "process_failed"),
        ("error", "unknown"),
    }
)
FENCE_CONTAINER_DIR = pathlib.Path("/rebuild-fence")
SERVER_TERMINATION_GRACE_SECONDS = 30.0
PROCESS_GROUP_POLL_SECONDS = 0.05


class GuardFailure(Exception):
    def __init__(self, stage: str, reason: str):
        super().__init__(reason)
        self.stage = stage
        self.reason = reason


def guard_event(
    mode: str, stage: str, outcome: str, reason: str
) -> dict[str, object]:
    if (
        mode not in GUARD_MODES
        or stage not in GUARD_STAGES
        or outcome not in GUARD_OUTCOMES
        or reason not in GUARD_REASONS
        or (outcome, reason) not in GUARD_CLASSIFICATIONS
    ):
        raise ValueError("invalid guarded-start event classification")
    return {
        "schema_version": 1,
        "event": "lightrag.guard",
        "mode": mode,
        "stage": stage,
        "outcome": outcome,
        "reason": reason,
    }


def emit_guard(mode: str, stage: str, outcome: str, reason: str) -> None:
    event = guard_event(mode, stage, outcome, reason)
    print(
        json.dumps(event, sort_keys=True, separators=(",", ":")),
        flush=True,
    )


def required(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise GuardFailure("preflight", "required_config")
    return value


def require_absolute_host_path(name: str) -> None:
    value = pathlib.PurePath(required(name))
    if not value.is_absolute():
        raise GuardFailure("preflight", "invalid_path")


def exec_guarded(mode: str, executable: str, arguments: list[str]) -> None:
    emit_guard(mode, "exec", "attempt", "starting")
    try:
        os.execvp(executable, arguments)
    except OSError:
        emit_guard(mode, "exec", "error", "exec_failed")
        raise GuardFailure("exec", "exec_failed") from None


def _signal_process_group(process: subprocess.Popen[object], signum: int) -> None:
    try:
        os.killpg(process.pid, signum)
    except ProcessLookupError:
        return


def _process_group_alive(process: subprocess.Popen[object]) -> bool:
    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def _quiesce_process_group(process: subprocess.Popen[object]) -> None:
    if not _process_group_alive(process):
        return
    _signal_process_group(process, signal.SIGTERM)
    deadline = time.monotonic() + SERVER_TERMINATION_GRACE_SECONDS
    while _process_group_alive(process) and time.monotonic() < deadline:
        time.sleep(PROCESS_GROUP_POLL_SECONDS)
    if not _process_group_alive(process):
        return
    _signal_process_group(process, signal.SIGKILL)
    deadline = time.monotonic() + SERVER_TERMINATION_GRACE_SECONDS
    while _process_group_alive(process) and time.monotonic() < deadline:
        time.sleep(PROCESS_GROUP_POLL_SECONDS)
    if _process_group_alive(process):
        raise GuardFailure("exec", "process_failed")


def _propagate_process_status(returncode: int) -> None:
    if returncode >= 0:
        raise SystemExit(returncode)
    signum = -returncode
    signal.signal(signum, signal.SIG_DFL)
    os.kill(os.getpid(), signum)
    # Only reached in tests or on a platform that did not deliver the signal.
    raise SystemExit(128 + signum)


def supervise_guarded(executable: str, arguments: list[str]) -> None:
    """Run LightRAG while this process retains the shared serving lease."""

    emit_guard("steady", "exec", "attempt", "starting")
    process: subprocess.Popen[object] | None = None
    pending_signal: int | None = None
    termination_deadline: float | None = None
    kill_deadline: float | None = None
    previous_handlers: dict[int, object] = {}

    def forward(signum: int, _frame: object) -> None:
        nonlocal pending_signal, termination_deadline
        pending_signal = signum
        if termination_deadline is None:
            termination_deadline = (
                time.monotonic() + SERVER_TERMINATION_GRACE_SECONDS
            )
        if process is not None:
            _signal_process_group(process, signum)

    try:
        for signum in (signal.SIGTERM, signal.SIGINT):
            previous_handlers[signum] = signal.signal(signum, forward)
        try:
            process = subprocess.Popen(
                arguments, executable=executable, start_new_session=True
            )
        except Exception:
            emit_guard("steady", "exec", "error", "exec_failed")
            raise GuardFailure("exec", "exec_failed") from None
        if pending_signal is not None:
            _signal_process_group(process, pending_signal)
        while True:
            try:
                returncode = process.wait(timeout=PROCESS_GROUP_POLL_SECONDS)
                break
            except subprocess.TimeoutExpired:
                now = time.monotonic()
                if termination_deadline is not None and now >= termination_deadline:
                    _signal_process_group(process, signal.SIGKILL)
                    termination_deadline = None
                    kill_deadline = now + SERVER_TERMINATION_GRACE_SECONDS
                elif kill_deadline is not None and now >= kill_deadline:
                    emit_guard("steady", "exec", "error", "process_failed")
                    raise GuardFailure("exec", "process_failed")
            except Exception:
                try:
                    _quiesce_process_group(process)
                except GuardFailure:
                    pass
                emit_guard("steady", "exec", "error", "process_failed")
                raise GuardFailure("exec", "process_failed") from None
        try:
            _quiesce_process_group(process)
        except GuardFailure:
            emit_guard("steady", "exec", "error", "process_failed")
            raise
    finally:
        for signum, previous in previous_handlers.items():
            signal.signal(signum, previous)

    if returncode != 0:
        emit_guard("steady", "exec", "error", "process_failed")
    _propagate_process_status(returncode)


def bootstrap() -> None:
    require_absolute_host_path("XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR")
    require_absolute_host_path("XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR")
    attempt = required("XLH_LIGHTRAG_ATTEMPT_ID")
    exec_guarded(
        "bootstrap",
        "python",
        [
            "python",
            "/opt/xlh/rebuild_fence.py",
            "bootstrap",
            "--fence-dir",
            "/rebuild-fence",
            "--generation",
            required("XLH_LIGHTRAG_DEPLOYMENT_GENERATION"),
            "--attempt-id",
            attempt,
            "--writer-evidence",
            f"/writer-evidence/{attempt}.json",
            "--writer-evidence-sha256",
            required("XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256"),
        ],
    )


def steady() -> None:
    require_absolute_host_path("XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR")
    generation = required("XLH_LIGHTRAG_DEPLOYMENT_GENERATION")
    contract = required("XLH_EXPECTED_CONTRACT_SHA256")
    store = rebuild_fence.FenceStore(FENCE_CONTAINER_DIR, create=False)
    try:
        # The shared lease is acquired before verification and is retained by
        # this supervisor until the complete LightRAG process group exits.
        with store.serving_lease(exclusive=False):
            store.verify_current(generation, contract)
            emit_guard("steady", "fence", "ok", "ok")
            supervise_guarded("lightrag-gunicorn", ["lightrag-gunicorn"])
    except GuardFailure:
        raise
    except Exception:
        raise GuardFailure("fence", "fence_rejected") from None


def main() -> None:
    if len(sys.argv) != 2 or sys.argv[1] not in GUARD_MODES:
        raise SystemExit(2)
    mode = sys.argv[1]
    try:
        if mode == "bootstrap":
            bootstrap()
        else:
            steady()
    except GuardFailure as error:
        if error.reason not in {"exec_failed", "process_failed"}:
            emit_guard(mode, error.stage, "error", error.reason)
        raise SystemExit(1) from None
    except Exception:
        emit_guard(mode, "preflight", "error", "unknown")
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
