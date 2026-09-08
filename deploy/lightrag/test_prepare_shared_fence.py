import contextlib
import fcntl
import importlib.util
import io
import os
import pathlib
import select
import signal
import stat
import sys
import tempfile
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("prepare_shared_fence.py")
SPEC = importlib.util.spec_from_file_location("prepare_shared_fence", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class _StatOverlay:
    def __init__(self, original, **overrides):
        self._original = original
        self._overrides = overrides

    def __getattr__(self, name):
        if name in self._overrides:
            return self._overrides[name]
        return getattr(self._original, name)


class PrepareSharedFenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        base = pathlib.Path(self.temporary.name)
        self.fence = base / "rebuild-fence"
        self.writer = base / "writer-evidence"
        self.fence.mkdir(mode=0o777)
        self.writer.mkdir(mode=0o777)
        self.paths = mock.patch.multiple(
            MODULE,
            FENCE_ROOT=str(self.fence),
            WRITER_EVIDENCE_ROOT=str(self.writer),
        )
        self.paths.start()
        self.addCleanup(self.paths.stop)

    def populate_valid_layout(self):
        locks = [
            self.fence / "serving.lock",
            self.fence / "rebuild.lock",
        ]
        regulars = [
            *locks,
            self.fence / "current.json",
        ]
        for path in regulars:
            path.write_bytes(b"private-content-canary")
            path.chmod(0o640 if path in locks else 0o666)

        reports = self.fence / "reports"
        attempts = self.fence / "attempts"
        reports.mkdir(mode=0o777)
        attempts.mkdir(mode=0o777)
        report = reports / "attempt-1_2.3.json"
        attempt = attempts / "A.json"
        writer = self.writer / "writer.evidence-1.json"
        for path in (report, attempt, writer):
            path.write_bytes(b"private-content-canary")
            path.chmod(0o666)
        return (reports, attempts), tuple(regulars) + (report, attempt, writer)

    def populate_verify_layout(self, expected_attempt=None):
        reports = self.fence / "reports"
        attempts = self.fence / "attempts"
        reports.mkdir()
        attempts.mkdir()
        files = [self.fence / "serving.lock", self.fence / "rebuild.lock"]
        if expected_attempt is not None:
            files.extend(
                [
                    self.fence / "current.json",
                    reports / (expected_attempt + ".json"),
                    attempts / (expected_attempt + ".json"),
                    self.writer / (expected_attempt + ".json"),
                ]
            )
        for path in files:
            path.write_bytes(b"private-content-canary")
            path.chmod(0o640)
        for directory in (self.fence, reports, attempts, self.writer):
            directory.chmod(0o750)
        return reports, attempts, tuple(files)

    @contextlib.contextmanager
    def verification_metadata(self, overrides=None):
        overrides = overrides or {}
        records = {}
        for root, owner_uid, group_gid in (
            (self.fence, MODULE.LIGHTRAG_UID, 1234),
            (self.writer, 501, MODULE.LIGHTRAG_GID),
        ):
            for path in (root, *root.rglob("*")):
                node_status = path.lstat()
                key = (node_status.st_dev, node_status.st_ino)
                mode = node_status.st_mode
                if stat.S_ISDIR(mode):
                    mode = (mode & ~0o7777) | 0o2750
                records[key] = {
                    "st_dev": node_status.st_dev,
                    "st_ino": node_status.st_ino,
                    "st_mode": mode,
                    "st_nlink": node_status.st_nlink,
                    "st_uid": owner_uid,
                    "st_gid": group_gid,
                }
        for path, values in overrides.items():
            node_status = path.lstat()
            records[(node_status.st_dev, node_status.st_ino)].update(values)

        real_fstat = os.fstat

        def deployment_fstat(descriptor):
            node_status = real_fstat(descriptor)
            values = records.get((node_status.st_dev, node_status.st_ino))
            if values is None:
                return node_status
            return _StatOverlay(node_status, **values)

        with mock.patch.object(MODULE.os, "fstat", side_effect=deployment_fstat):
            yield

    @contextlib.contextmanager
    def simulated_root_metadata(
        self,
        on_fchown=None,
        on_fchmod=None,
        initially_frozen=(),
        stable_locks_gid=None,
    ):
        """Record metadata writes while exposing Linux-like stat results."""

        real_fstat = os.fstat
        real_fchmod = os.fchmod
        metadata = {}
        chown_identities = []
        chmod_identities = []
        for directory in initially_frozen:
            node_status = directory.stat()
            metadata[(node_status.st_dev, node_status.st_ino)] = {
                "st_uid": 0,
                "st_gid": 0,
                "st_mode": (node_status.st_mode & ~0o7777)
                | MODULE.FROZEN_ROOT_MODE,
            }
        if stable_locks_gid is not None:
            for name in MODULE._LOCK_NAMES:
                path = self.fence / name
                if not path.exists():
                    continue
                node_status = path.stat()
                metadata[(node_status.st_dev, node_status.st_ino)] = {
                    "st_uid": MODULE.LIGHTRAG_UID,
                    "st_gid": stable_locks_gid,
                    "st_mode": (node_status.st_mode & ~0o7777)
                    | MODULE.REGULAR_MODE,
                }

        def identity(descriptor):
            node_status = real_fstat(descriptor)
            return node_status, (node_status.st_dev, node_status.st_ino)

        def deployment_fstat(descriptor):
            node_status, key = identity(descriptor)
            values = metadata.get(key)
            if values is None:
                return node_status
            return _StatOverlay(node_status, **values)

        def deployment_fchown(descriptor, owner_uid, group_gid):
            node_status, key = identity(descriptor)
            metadata.setdefault(key, {}).update(
                {"st_uid": owner_uid, "st_gid": group_gid}
            )
            chown_identities.append((key, owner_uid, group_gid))
            if on_fchown is not None:
                on_fchown(descriptor, owner_uid, group_gid)

        def deployment_fchmod(descriptor, mode):
            node_status, key = identity(descriptor)
            real_fchmod(descriptor, mode)
            metadata.setdefault(key, {})["st_mode"] = (
                node_status.st_mode & ~0o7777
            ) | mode
            chmod_identities.append((key, mode))
            if on_fchmod is not None:
                on_fchmod(descriptor, mode)

        with mock.patch.object(
            MODULE.os, "geteuid", return_value=0
        ), mock.patch.object(
            MODULE.os, "fstat", side_effect=deployment_fstat
        ), mock.patch.object(
            MODULE.os, "fchown", side_effect=deployment_fchown
        ) as fchown, mock.patch.object(
            MODULE.os, "fchmod", side_effect=deployment_fchmod
        ) as fchmod:
            fchown.inode_calls = chown_identities
            fchmod.inode_calls = chmod_identities
            yield fchown, fchmod

    def link_from_preheld_fd(self, descriptor, managed, external):
        held_status = os.fstat(descriptor)
        self.assertEqual(
            (held_status.st_dev, held_status.st_ino),
            (managed.stat().st_dev, managed.stat().st_ino),
        )
        if sys.platform.startswith("linux"):
            # Linux-only real pre-held-FD path: follow /proc/self/fd through
            # linkat(2), without resolving the managed name again.
            os.link(
                f"/proc/self/fd/{descriptor}",
                external,
                follow_symlinks=True,
            )
        else:
            # Darwin has no Linux AT_EMPTY_PATH and rejects /dev/fd linkat.
            # Linking the still-bound name at this exact hook simulates the
            # same old-inode nlink transition for the portable assertions.
            os.link(managed, external)

    def run_as_root(self, operator_uid=501, shared_gid=1234):
        with self.simulated_root_metadata(
            stable_locks_gid=shared_gid
        ) as (fchown, fchmod), mock.patch.object(
            MODULE,
            "_verify_final_attributes",
            wraps=MODULE._verify_final_attributes,
        ) as verify_attributes:
            MODULE.prepare_shared_fence(operator_uid, shared_gid)
        return fchown, fchmod, verify_attributes

    def assert_mode(self, path, expected):
        actual = stat.S_IMODE(path.stat().st_mode)
        # macOS does not preserve a setgid bit on directories; the syscall
        # argument is asserted separately so the Linux deployment contract is
        # still tested exactly.
        self.assertEqual(actual & 0o777, expected & 0o777)
        self.assertEqual(actual & 0o007, 0, "other permission bits must be clear")

    def assert_rejected_without_mutation(self):
        with mock.patch.object(MODULE.os, "geteuid", return_value=0), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(MODULE.os, "fchmod") as fchmod, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)
        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()

    def assert_final_stage_failure_is_sealed(self, operation):
        base = pathlib.Path(self.temporary.name) / ("final-stage-" + operation)
        fence = base / "rebuild-fence"
        writer = base / "writer-evidence"
        fence.mkdir(parents=True, mode=0o777)
        writer.mkdir(mode=0o777)
        real_stage = MODULE._stage_root_for_publish
        fence_identity = (fence.stat().st_dev, fence.stat().st_ino)

        def fail_fence_stage(root, owner_uid, group_gid):
            if (root.device, root.inode) != fence_identity:
                return real_stage(root, owner_uid, group_gid)
            MODULE.os.fchmod(root.descriptor, MODULE.SEALED_ROOT_MODE)
            if operation in {"fchown", "fstat", "fsync"}:
                MODULE.os.fchown(root.descriptor, owner_uid, group_gid)
            if operation in {"fstat", "fsync"}:
                MODULE.os.fstat(root.descriptor)
            if operation == "fsync":
                MODULE.os.fsync(root.descriptor)
            raise OSError("injected final staging failure")

        with mock.patch.multiple(
            MODULE, FENCE_ROOT=str(fence), WRITER_EVIDENCE_ROOT=str(writer)
        ), mock.patch.object(self, "fence", fence), mock.patch.object(
            self, "writer", writer
        ), self.simulated_root_metadata() as _, mock.patch.object(
            MODULE, "_stage_root_for_publish", side_effect=fail_fence_stage
        ), mock.patch.object(
            MODULE, "_attempt_reseal_directories", return_value=False
        ) as reseal, mock.patch.object(
            MODULE, "_publish_root"
        ) as publish, self.assertRaises(MODULE.PreparationError) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "mutation_failed")
        reseal.assert_called_once()
        publish.assert_not_called()
        self.assert_mode(fence, MODULE.SEALED_ROOT_MODE)
        self.assert_mode(writer, MODULE.FROZEN_ROOT_MODE)
        fence.chmod(0o700)
        writer.chmod(0o700)

    def assert_sigkill_publish_checkpoint(self, checkpoint):
        if not hasattr(os, "fork"):
            self.skipTest("POSIX fork required")
        self.run_as_root()
        roots = [
            os.open(path, os.O_RDONLY | os.O_DIRECTORY)
            for path in (self.fence, self.writer)
        ]
        locks = [
            os.open(self.fence / name, os.O_RDONLY)
            for name in MODULE._LOCK_NAMES
        ]
        for descriptor in (*roots, *locks):
            self.addCleanup(os.close, descriptor)
        target_path = self.writer if checkpoint == "writer" else self.fence
        target_identity = (target_path.stat().st_dev, target_path.stat().st_ino)
        ready_read, ready_write = os.pipe()
        block_read, block_write = os.pipe()
        child_pid = os.fork()
        if child_pid == 0:
            try:
                os.close(ready_read)
                os.close(block_write)
                real_publish = MODULE._publish_root

                def publish_then_pause(root):
                    real_publish(root)
                    if (root.device, root.inode) == target_identity:
                        os.write(ready_write, b"1")
                        os.read(block_read, 1)

                with self.simulated_root_metadata(
                    stable_locks_gid=1234
                ), mock.patch.object(
                    MODULE, "_publish_root", side_effect=publish_then_pause
                ):
                    MODULE.prepare_shared_fence(501, 1234)
                os._exit(0)
            except BaseException:
                os._exit(97)

        os.close(ready_write)
        os.close(block_read)
        reaped = False
        try:
            readable, _, _ = select.select([ready_read], [], [], 10)
            self.assertTrue(readable, "child did not reach publish checkpoint")
            self.assertEqual(os.read(ready_read, 1), b"1")
            for descriptor in (*roots, *locks):
                with self.assertRaises((BlockingIOError, OSError)):
                    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            os.kill(child_pid, signal.SIGKILL)
            waited_pid, status = os.waitpid(child_pid, 0)
            reaped = True
            self.assertEqual(waited_pid, child_pid)
            self.assertTrue(os.WIFSIGNALED(status))
            self.assertEqual(os.WTERMSIG(status), signal.SIGKILL)
            expected_fence_mode = (
                MODULE.SEALED_ROOT_MODE
                if checkpoint == "writer"
                else MODULE.SHARED_DIRECTORY_MODE
            )
            self.assert_mode(self.fence, expected_fence_mode)
            self.assert_mode(self.writer, MODULE.SHARED_DIRECTORY_MODE)
            for descriptor in (*roots, *locks):
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                fcntl.flock(descriptor, fcntl.LOCK_UN)
        finally:
            os.close(ready_read)
            os.close(block_write)
            if not reaped:
                try:
                    os.kill(child_pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                os.waitpid(child_pid, 0)
            # A pre-commit kill deliberately leaves the fence sealed; restore
            # owner-access only so TemporaryDirectory cleanup can remove it.
            self.fence.chmod(0o700)
            self.writer.chmod(0o700)

    def test_valid_tree_is_prevalidated_then_converged(self):
        directories, regulars = self.populate_valid_layout()
        lock_paths = regulars[:2]
        copied_paths = regulars[2:]
        old_regular_identities = {
            path: (path.stat().st_dev, path.stat().st_ino) for path in regulars
        }

        fchown, fchmod, verify_attributes = self.run_as_root()

        self.assert_mode(self.fence, 0o2750)
        for directory in (self.writer,) + directories:
            self.assert_mode(directory, 0o2750)
        for regular in lock_paths:
            self.assert_mode(regular, 0o0640)
            self.assertEqual(
                (regular.stat().st_dev, regular.stat().st_ino),
                old_regular_identities[regular],
            )
        for regular in copied_paths:
            self.assert_mode(regular, 0o0640)
            self.assertNotEqual(
                (regular.stat().st_dev, regular.stat().st_ino),
                old_regular_identities[regular],
            )
        self.assertTrue(
            set(old_regular_identities.values()).isdisjoint(
                {identity for identity, _, _ in fchown.inode_calls}
            ),
            "copy-up must not chown any pre-existing regular inode",
        )
        self.assertTrue(
            set(old_regular_identities.values()).isdisjoint(
                {identity for identity, _ in fchmod.inode_calls}
            ),
            "copy-up must not chmod any pre-existing regular inode",
        )
        self.assertEqual(fchown.call_count, 12)
        freeze_calls = fchown.call_args_list[:4]
        fence_regular_calls = fchown.call_args_list[4:7]
        writer_calls = fchown.call_args_list[7:8]
        fence_directory_calls = fchown.call_args_list[8:10]
        final_root_calls = fchown.call_args_list[10:]
        self.assertEqual(
            [call.args[1:] for call in freeze_calls], [(0, 0)] * 4
        )
        self.assertTrue(fence_regular_calls)
        self.assertTrue(writer_calls)
        for call in (*fence_regular_calls, *fence_directory_calls):
            self.assertEqual(call.args[1:], (MODULE.LIGHTRAG_UID, 1234))
        for call in writer_calls:
            self.assertEqual(call.args[1:], (501, MODULE.LIGHTRAG_GID))
        self.assertEqual(
            [call.args[1:] for call in final_root_calls],
            [(MODULE.LIGHTRAG_UID, 1234), (501, MODULE.LIGHTRAG_GID)],
        )
        requested_modes = [call.args[1] for call in fchmod.call_args_list]
        self.assertEqual(requested_modes.count(0o0700), 4)
        self.assertEqual(requested_modes.count(0o2750), 4)
        self.assertEqual(requested_modes.count(0o0640), 4)
        self.assertEqual(requested_modes.count(MODULE.SEALED_ROOT_MODE), 6)
        verified_owners = [
            call.args[1:] for call in verify_attributes.call_args_list
        ]
        self.assertIn((MODULE.LIGHTRAG_UID, 1234), verified_owners)
        self.assertIn((501, MODULE.LIGHTRAG_GID), verified_owners)

    def test_complete_tree_is_enumerated_before_first_mutation(self):
        directories, _ = self.populate_valid_layout()
        real_listdir = os.listdir
        enumerated = set()
        enumeration_at_first_mutation = []
        expected = {
            (path.stat().st_dev, path.stat().st_ino)
            for path in (self.fence, self.writer, *directories)
        }

        def record_enumeration(descriptor):
            descriptor_status = os.fstat(descriptor)
            enumerated.add((descriptor_status.st_dev, descriptor_status.st_ino))
            return real_listdir(descriptor)

        def record_first_mutation(*_):
            if not enumeration_at_first_mutation:
                enumeration_at_first_mutation.append(frozenset(enumerated))

        with self.simulated_root_metadata(
            on_fchown=record_first_mutation, stable_locks_gid=1234
        ), mock.patch.object(
            MODULE.os, "listdir", side_effect=record_enumeration
        ):
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(enumeration_at_first_mutation, [frozenset(expected)])

    def test_single_regular_byte_budget_boundary_precedes_metadata_mutation(self):
        current = self.fence / "current.json"
        current.write_bytes(b"ABCD")

        with mock.patch.object(
            MODULE, "MAX_REGULAR_BYTES", 4
        ), mock.patch.object(MODULE, "MAX_TREE_BYTES", 16):
            self.run_as_root()
        self.assertEqual(current.read_bytes(), b"ABCD")

        # Recreate an unprepared inode so the over-limit branch is proved to
        # reject before even the directory freeze metadata writes.
        current.write_bytes(b"ABCD")
        with mock.patch.object(
            MODULE, "MAX_REGULAR_BYTES", 3
        ), mock.patch.object(MODULE, "MAX_TREE_BYTES", 16), mock.patch.object(
            MODULE.os, "geteuid", return_value=0
        ), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, mock.patch.object(
            MODULE.os, "mkdir"
        ) as mkdir, mock.patch.object(
            MODULE.os, "replace"
        ) as replace, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()
        mkdir.assert_not_called()
        replace.assert_not_called()

    def test_total_tree_byte_budget_boundary_precedes_metadata_mutation(self):
        current = self.fence / "current.json"
        evidence = self.writer / "evidence.json"
        current.write_bytes(b"ABC")
        evidence.write_bytes(b"DE")

        with mock.patch.object(
            MODULE, "MAX_REGULAR_BYTES", 3
        ), mock.patch.object(MODULE, "MAX_TREE_BYTES", 5):
            self.run_as_root()
        self.assertEqual(current.read_bytes(), b"ABC")
        self.assertEqual(evidence.read_bytes(), b"DE")

        with mock.patch.object(
            MODULE, "MAX_REGULAR_BYTES", 3
        ), mock.patch.object(MODULE, "MAX_TREE_BYTES", 4), mock.patch.object(
            MODULE.os, "geteuid", return_value=0
        ), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, mock.patch.object(
            MODULE.os, "mkdir"
        ) as mkdir, mock.patch.object(
            MODULE.os, "replace"
        ) as replace, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()
        mkdir.assert_not_called()
        replace.assert_not_called()

    def test_file_budget_counts_missing_locks_before_metadata_mutation(self):
        # The two synthesized locks are created sequentially. Their exact peak
        # namespace footprint is two: one completed lock plus one private temp.
        with mock.patch.object(MODULE, "MAX_REGULAR_FILES", 2):
            self.run_as_root()
        for name in ("serving.lock", "rebuild.lock"):
            self.assertTrue((self.fence / name).is_file())

        for path in tuple(self.fence.iterdir()):
            if path.is_dir():
                path.rmdir()
            else:
                path.unlink()
        self.fence.chmod(0o777)
        self.writer.chmod(0o777)
        with mock.patch.object(
            MODULE, "MAX_REGULAR_FILES", 1
        ), mock.patch.object(
            MODULE.os, "geteuid", return_value=0
        ), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, mock.patch.object(
            MODULE.os, "mkdir"
        ) as mkdir, mock.patch.object(
            MODULE.os, "replace"
        ) as replace, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()
        mkdir.assert_not_called()
        replace.assert_not_called()

    def test_file_budget_includes_copy_temp_with_missing_locks(self):
        current = self.fence / "current.json"
        current.write_bytes(b"snapshot")

        with mock.patch.object(
            MODULE, "MAX_REGULAR_FILES", 3
        ), mock.patch.object(
            MODULE.os, "geteuid", return_value=0
        ), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, mock.patch.object(
            MODULE.os, "mkdir"
        ) as mkdir, mock.patch.object(
            MODULE.os, "replace"
        ) as replace, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()
        mkdir.assert_not_called()
        replace.assert_not_called()

        with mock.patch.object(MODULE, "MAX_REGULAR_FILES", 4):
            self.run_as_root()
        self.assertEqual(current.read_bytes(), b"snapshot")

    def test_empty_allowed_roots_are_supported(self):
        fchown, fchmod, verify_attributes = self.run_as_root(
            operator_uid=0, shared_gid=7
        )

        self.assertEqual(fchown.call_count, 8)
        self.assertEqual(
            [call.args[1:] for call in fchown.call_args_list],
            [
                (0, 0),
                (0, 0),
                (MODULE.LIGHTRAG_UID, 7),
                (MODULE.LIGHTRAG_UID, 7),
                (MODULE.LIGHTRAG_UID, 7),
                (MODULE.LIGHTRAG_UID, 7),
                (MODULE.LIGHTRAG_UID, 7),
                (0, MODULE.LIGHTRAG_GID),
            ],
        )
        self.assertEqual(
            [call.args[1] for call in fchmod.call_args_list],
            [
                MODULE.SEALED_ROOT_MODE,
                0o0700,
                MODULE.SEALED_ROOT_MODE,
                0o0700,
                0o0640,
                0o0640,
                0o2750,
                0o2750,
                MODULE.SEALED_ROOT_MODE,
                MODULE.SEALED_ROOT_MODE,
                0o2750,
                0o2750,
            ],
        )
        self.assert_mode(self.fence, 0o2750)
        self.assert_mode(self.writer, 0o2750)
        self.assertGreaterEqual(verify_attributes.call_count, 7)
        self.assertFalse((self.fence / "current.json").exists())
        for directory in (self.fence / "reports", self.fence / "attempts"):
            self.assertTrue(directory.is_dir())
            self.assert_mode(directory, 0o2750)
        for regular in (
            self.fence / "serving.lock",
            self.fence / "rebuild.lock",
        ):
            self.assertTrue(regular.is_file())
            self.assertEqual(regular.read_bytes(), b"")
            self.assert_mode(regular, 0o0640)

    def test_missing_lock_files_are_created_exclusively_via_fence_dir_fd(self):
        real_open = os.open
        with mock.patch.object(MODULE.os, "open", wraps=real_open) as open_file:
            self.run_as_root()

        create_calls = [
            call
            for call in open_file.call_args_list
            if call.args[1] & os.O_CREAT
        ]
        self.assertEqual(len(create_calls), 2)
        for call in create_calls:
            self.assertIn(
                call.args[0],
                {
                    MODULE._private_temp_name("serving.lock"),
                    MODULE._private_temp_name("rebuild.lock"),
                },
            )
            self.assertTrue(call.args[1] & os.O_EXCL)
            self.assertTrue(call.args[1] & os.O_NOFOLLOW)
            self.assertIn("dir_fd", call.kwargs)
        for name in ("serving.lock", "rebuild.lock"):
            self.assertTrue((self.fence / name).is_file())
            self.assertFalse(
                (self.fence / MODULE._private_temp_name(name)).exists()
            )

    def test_existing_lock_inodes_remain_stable_and_share_one_flock_domain(self):
        self.run_as_root()
        paths = tuple(self.fence / name for name in MODULE._LOCK_NAMES)
        held = [os.open(path, os.O_RDONLY) for path in paths]
        for descriptor in held:
            self.addCleanup(os.close, descriptor)
        original_identities = [
            (status.st_dev, status.st_ino)
            for status in (os.fstat(descriptor) for descriptor in held)
        ]

        with self.simulated_root_metadata(stable_locks_gid=1234):
            MODULE.prepare_shared_fence(501, 1234)

        for path, old_descriptor, original_identity in zip(
            paths, held, original_identities
        ):
            with self.subTest(lock=path.name):
                current_descriptor = os.open(path, os.O_RDONLY)
                self.addCleanup(os.close, current_descriptor)
                current_status = os.fstat(current_descriptor)
                self.assertEqual(
                    (current_status.st_dev, current_status.st_ino),
                    original_identity,
                )
                fcntl.flock(
                    old_descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB
                )
                try:
                    with self.assertRaises((BlockingIOError, OSError)):
                        fcntl.flock(
                            current_descriptor,
                            fcntl.LOCK_EX | fcntl.LOCK_NB,
                        )
                finally:
                    fcntl.flock(old_descriptor, fcntl.LOCK_UN)
                fcntl.flock(
                    current_descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB
                )
                fcntl.flock(current_descriptor, fcntl.LOCK_UN)

    def test_initializer_directory_locks_reject_a_competing_helper(self):
        descriptors = []

        def open_root(path):
            descriptor = os.open(
                path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
            )
            descriptors.append(descriptor)
            node_status = os.fstat(descriptor)
            return MODULE._OpenedNode(
                descriptor,
                MODULE.SHARED_DIRECTORY_MODE,
                node_status.st_dev,
                node_status.st_ino,
                True,
            )

        self.addCleanup(
            lambda: [os.close(descriptor) for descriptor in descriptors]
        )
        first_roots = (open_root(self.fence), open_root(self.writer))
        competing_roots = (open_root(self.fence), open_root(self.writer))
        first_locks = MODULE._acquire_initializer_locks(first_roots)
        self.addCleanup(MODULE._release_initial_locks, first_locks)

        with self.assertRaises(MODULE.PreparationError) as raised:
            MODULE._acquire_initializer_locks(competing_roots)
        self.assertEqual(raised.exception.code, "unsafe_layout")

        MODULE._release_initial_locks(first_locks)
        first_locks.clear()
        competing_locks = MODULE._acquire_initializer_locks(competing_roots)
        MODULE._release_initial_locks(competing_locks)

    def test_lock_acquire_failure_releases_the_first_lock(self):
        self.run_as_root()
        descriptors = [
            os.open(self.fence / name, os.O_RDONLY)
            for name in MODULE._LOCK_NAMES
        ]
        for descriptor in descriptors:
            self.addCleanup(os.close, descriptor)
        nodes = []
        for descriptor in descriptors:
            node_status = os.fstat(descriptor)
            nodes.append(
                MODULE._OpenedNode(
                    descriptor,
                    MODULE.REGULAR_MODE,
                    node_status.st_dev,
                    node_status.st_ino,
                    False,
                )
            )
        snapshot = MODULE._DirectorySnapshot(
            directory=mock.sentinel.directory,
            children=tuple(zip(MODULE._LOCK_NAMES, nodes)),
        )
        real_flock = fcntl.flock
        acquire_count = 0

        def fail_second_acquire(descriptor, operation):
            nonlocal acquire_count
            if operation & fcntl.LOCK_UN:
                return real_flock(descriptor, operation)
            acquire_count += 1
            if acquire_count == 2:
                raise BlockingIOError("injected lock contention")
            return real_flock(descriptor, operation)

        with mock.patch.object(
            MODULE.fcntl, "flock", side_effect=fail_second_acquire
        ), self.assertRaises(MODULE.PreparationError) as raised:
            MODULE._acquire_locks(
                snapshot, require_all=True, failure_code="unsafe_layout"
            )
        self.assertEqual(raised.exception.code, "unsafe_layout")

        probe = os.open(self.fence / MODULE._LOCK_NAMES[0], os.O_RDONLY)
        self.addCleanup(os.close, probe)
        real_flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
        real_flock(probe, fcntl.LOCK_UN)

    def test_initializer_second_lock_failure_releases_the_first_root(self):
        descriptors = [
            os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            for path in (self.fence, self.writer)
        ]
        for descriptor in descriptors:
            self.addCleanup(os.close, descriptor)
        roots = []
        for descriptor in descriptors:
            node_status = os.fstat(descriptor)
            roots.append(
                MODULE._OpenedNode(
                    descriptor,
                    MODULE.SHARED_DIRECTORY_MODE,
                    node_status.st_dev,
                    node_status.st_ino,
                    True,
                )
            )
        real_flock = fcntl.flock
        acquire_count = 0

        def fail_second_acquire(descriptor, operation):
            nonlocal acquire_count
            if operation & fcntl.LOCK_UN:
                return real_flock(descriptor, operation)
            acquire_count += 1
            if acquire_count == 2:
                raise BlockingIOError("injected initializer contention")
            return real_flock(descriptor, operation)

        with mock.patch.object(
            MODULE.fcntl, "flock", side_effect=fail_second_acquire
        ), self.assertRaises(MODULE.PreparationError) as raised:
            MODULE._acquire_initializer_locks(roots)
        self.assertEqual(raised.exception.code, "unsafe_layout")

        probe = os.open(self.fence, os.O_RDONLY | os.O_DIRECTORY)
        self.addCleanup(os.close, probe)
        real_flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
        real_flock(probe, fcntl.LOCK_UN)

    def test_existing_lock_contention_rejects_before_mutation(self):
        self.run_as_root()
        held = os.open(self.fence / "serving.lock", os.O_RDONLY)
        self.addCleanup(os.close, held)
        fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.addCleanup(fcntl.flock, held, fcntl.LOCK_UN)

        with self.simulated_root_metadata(
            stable_locks_gid=1234
        ) as (fchown, fchmod), mock.patch.object(
            MODULE.os, "mkdir"
        ) as mkdir, mock.patch.object(
            MODULE.os, "replace"
        ) as replace, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()
        mkdir.assert_not_called()
        replace.assert_not_called()

    def test_competing_first_initializer_fails_before_mutation(self):
        real_acquire = MODULE._acquire_initializer_locks
        competed = False
        competitor_errors = []

        def acquire_then_compete(roots):
            nonlocal competed
            locked = real_acquire(roots)
            if competed:
                return locked
            competed = True
            chown_count = fchown.call_count
            chmod_count = fchmod.call_count
            mkdir_count = mkdir.call_count
            replace_count = replace.call_count
            try:
                MODULE.prepare_shared_fence(501, 1234)
            except MODULE.PreparationError as error:
                competitor_errors.append(error.code)
            else:
                self.fail("competing initializer unexpectedly succeeded")
            self.assertEqual(fchown.call_count, chown_count)
            self.assertEqual(fchmod.call_count, chmod_count)
            self.assertEqual(mkdir.call_count, mkdir_count)
            self.assertEqual(replace.call_count, replace_count)
            return locked

        real_mkdir = os.mkdir
        real_replace = os.replace
        with self.simulated_root_metadata() as (
            fchown,
            fchmod,
        ), mock.patch.object(
            MODULE, "_acquire_initializer_locks", side_effect=acquire_then_compete
        ), mock.patch.object(
            MODULE.os, "mkdir", wraps=real_mkdir
        ) as mkdir, mock.patch.object(
            MODULE.os, "replace", wraps=real_replace
        ) as replace:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertTrue(competed)
        self.assertEqual(competitor_errors, ["unsafe_layout"])
        self.assertEqual(
            {path.name for path in self.fence.iterdir()},
            {"attempts", "reports", "rebuild.lock", "serving.lock"},
        )

    def test_failed_private_temp_create_never_unlinks_competing_inode(self):
        parent_descriptor = os.open(
            self.writer, os.O_RDONLY | os.O_DIRECTORY
        )
        self.addCleanup(os.close, parent_descriptor)
        parent_status = os.fstat(parent_descriptor)
        parent = MODULE._OpenedNode(
            parent_descriptor,
            MODULE.SHARED_DIRECTORY_MODE,
            parent_status.st_dev,
            parent_status.st_ino,
            True,
        )
        temporary = self.writer / MODULE._private_temp_name("evidence.json")
        temporary.write_bytes(b"competing-helper-owned")

        with self.assertRaises(MODULE.PreparationError) as raised:
            MODULE._create_private_regular(
                parent,
                "evidence.json",
                b"this-helper-content",
                501,
                MODULE.LIGHTRAG_GID,
                None,
            )

        self.assertEqual(raised.exception.code, "mutation_failed")
        self.assertEqual(temporary.read_bytes(), b"competing-helper-owned")

    def test_rejects_symlinks_in_each_tree(self):
        outside = pathlib.Path(self.temporary.name) / "outside.json"
        outside.write_text("private-content-canary", encoding="utf-8")
        for parent, name in (
            (self.fence, "current.json"),
            (self.writer, "writer.json"),
        ):
            with self.subTest(parent=parent.name):
                link = parent / name
                link.symlink_to(outside)
                self.assert_rejected_without_mutation()
                link.unlink()

    def test_rejects_symlink_at_either_fixed_root(self):
        for root in (self.fence, self.writer):
            with self.subTest(root=root.name):
                replacement = pathlib.Path(self.temporary.name) / (root.name + "-real")
                replacement.mkdir()
                root.rmdir()
                root.symlink_to(replacement, target_is_directory=True)
                self.assert_rejected_without_mutation()
                root.unlink()
                root.mkdir()

    def test_rejects_symlink_for_reserved_fence_directory(self):
        target = pathlib.Path(self.temporary.name) / "outside-directory"
        target.mkdir()
        (self.fence / "reports").symlink_to(target, target_is_directory=True)
        self.assert_rejected_without_mutation()

    def test_rejects_unknown_or_invalid_names(self):
        cases = (
            (self.fence, "secret-content-canary.txt"),
            (self.writer, ".hidden.json"),
            (self.writer, "a" * 129 + ".json"),
        )
        for parent, name in cases:
            with self.subTest(name=name):
                path = parent / name
                path.write_bytes(b"private-content-canary")
                self.assert_rejected_without_mutation()
                path.unlink()

    def test_validation_failure_does_not_mutate_either_root(self):
        (self.writer / "unknown").write_bytes(b"private-content-canary")
        original_modes = tuple(
            stat.S_IMODE(path.stat().st_mode) for path in (self.fence, self.writer)
        )
        with mock.patch.object(MODULE.os, "geteuid", return_value=0), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(MODULE.os, "fchmod") as fchmod, self.assertRaises(
            MODULE.PreparationError
        ):
            MODULE.prepare_shared_fence(501, 1234)

        fchown.assert_not_called()
        fchmod.assert_not_called()
        self.assertEqual(
            tuple(
                stat.S_IMODE(path.stat().st_mode)
                for path in (self.fence, self.writer)
            ),
            original_modes,
        )

    def test_second_root_open_failure_does_not_mutate_first_root(self):
        self.writer.rmdir()
        self.writer.symlink_to(self.fence, target_is_directory=True)
        original_mode = stat.S_IMODE(self.fence.stat().st_mode)

        with mock.patch.object(MODULE.os, "geteuid", return_value=0), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(MODULE.os, "fchmod") as fchmod, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()
        self.assertEqual(stat.S_IMODE(self.fence.stat().st_mode), original_mode)

    @unittest.skipUnless(hasattr(os, "mkfifo"), "POSIX FIFO required")
    def test_rejects_non_regular_identifier_entry(self):
        os.mkfifo(self.writer / "fifo.json")
        self.assert_rejected_without_mutation()

    def test_rejects_wrong_type_for_reserved_fence_node(self):
        (self.fence / "reports").write_bytes(b"not-a-directory")
        self.assert_rejected_without_mutation()

    def test_rejects_directory_where_regular_file_is_required(self):
        (self.fence / "current.json").mkdir()
        self.assert_rejected_without_mutation()

    def test_rejects_unknown_and_nonregular_entries_in_fence_subdirectories(self):
        reports = self.fence / "reports"
        reports.mkdir()
        invalid = reports / "unknown.txt"
        invalid.write_bytes(b"private-content-canary")
        self.assert_rejected_without_mutation()
        invalid.unlink()

        directory = reports / "directory.json"
        directory.mkdir()
        self.assert_rejected_without_mutation()
        directory.rmdir()

        target = pathlib.Path(self.temporary.name) / "outside.json"
        target.write_bytes(b"private-content-canary")
        link = reports / "link.json"
        link.symlink_to(target)
        self.assert_rejected_without_mutation()

    @unittest.skipUnless(hasattr(os, "link"), "POSIX hard links required")
    def test_rejects_hard_link_that_would_extend_mutation_scope(self):
        outside = pathlib.Path(self.temporary.name) / "outside.json"
        outside.write_bytes(b"private-content-canary")
        os.link(outside, self.writer / "evidence.json")
        self.assert_rejected_without_mutation()

    @unittest.skipUnless(hasattr(os, "link"), "POSIX hard links required")
    def test_pre_freeze_hardlink_from_preheld_fd_fails_without_mutation(self):
        managed = self.writer / "evidence.json"
        managed.write_bytes(b"validated-content")
        managed.chmod(0o666)
        held = os.open(managed, os.O_RDONLY)
        self.addCleanup(os.close, held)
        old_status = os.fstat(held)
        old_identity = (old_status.st_dev, old_status.st_ino)
        old_owner = (old_status.st_uid, old_status.st_gid)
        old_mode = stat.S_IMODE(old_status.st_mode)
        outside = pathlib.Path(self.temporary.name) / "outside-late.json"
        real_acquire = MODULE._acquire_locks
        linked = False

        def acquire_then_link(
            snapshot, *, require_all, failure_code
        ):
            nonlocal linked
            descriptors = real_acquire(
                snapshot,
                require_all=require_all,
                failure_code=failure_code,
            )
            if not require_all and not linked:
                # The first complete validation has passed, but no content has
                # been copied and no managed metadata has been mutated yet.
                self.link_from_preheld_fd(held, managed, outside)
                linked = True
            return descriptors

        with mock.patch.object(
            MODULE.os, "geteuid", return_value=0
        ), mock.patch.object(
            MODULE, "_acquire_locks", side_effect=acquire_then_link
        ), mock.patch.object(
            MODULE, "_read_bounded_regular", wraps=MODULE._read_bounded_regular
        ) as read_regular, mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, mock.patch.object(
            MODULE.os, "mkdir"
        ) as mkdir, mock.patch.object(
            MODULE.os, "replace"
        ) as replace, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        self.assertTrue(linked)
        read_regular.assert_not_called()
        fchown.assert_not_called()
        fchmod.assert_not_called()
        mkdir.assert_not_called()
        replace.assert_not_called()
        self.assertEqual(
            (managed.stat().st_dev, managed.stat().st_ino), old_identity
        )
        self.assertEqual(
            (outside.stat().st_dev, outside.stat().st_ino), old_identity
        )
        self.assertEqual((outside.stat().st_uid, outside.stat().st_gid), old_owner)
        self.assertEqual(stat.S_IMODE(outside.stat().st_mode), old_mode)
        self.assertEqual(outside.read_bytes(), b"validated-content")

    @unittest.skipUnless(hasattr(os, "link"), "POSIX hard links required")
    def test_post_freeze_hardlink_isolated_by_private_inode_replace(self):
        managed = self.writer / "evidence.json"
        content = b"validated-content"
        managed.write_bytes(content)
        managed.chmod(0o666)
        held = os.open(managed, os.O_RDONLY)
        self.addCleanup(os.close, held)
        old_status = os.fstat(held)
        old_identity = (old_status.st_dev, old_status.st_ino)
        old_owner = (old_status.st_uid, old_status.st_gid)
        old_mode = stat.S_IMODE(old_status.st_mode)
        outside = pathlib.Path(self.temporary.name) / "outside-late.json"
        real_freeze = MODULE._freeze_directories
        real_verify_snapshots = MODULE._verify_snapshots
        injected = False
        post_link_verifications = []

        def freeze_then_link(directories):
            nonlocal injected
            real_freeze(directories)
            self.assertFalse(injected)
            self.assert_mode(self.fence, MODULE.FROZEN_ROOT_MODE)
            self.assert_mode(self.writer, MODULE.FROZEN_ROOT_MODE)
            self.link_from_preheld_fd(held, managed, outside)
            injected = True

        def record_post_link_verification(
            roots, snapshots, *, allow_linked_regulars=False
        ):
            if injected:
                post_link_verifications.append(allow_linked_regulars)
            return real_verify_snapshots(
                roots,
                snapshots,
                allow_linked_regulars=allow_linked_regulars,
            )

        with self.simulated_root_metadata() as (fchown, fchmod), mock.patch.object(
            MODULE, "_freeze_directories", side_effect=freeze_then_link
        ), mock.patch.object(
            MODULE,
            "_verify_snapshots",
            side_effect=record_post_link_verification,
        ):
            MODULE.prepare_shared_fence(501, 1234)

        self.assertTrue(injected)
        self.assertTrue(post_link_verifications)
        self.assertTrue(post_link_verifications[0])
        managed_status = managed.stat()
        outside_status = outside.stat()
        managed_identity = (managed_status.st_dev, managed_status.st_ino)
        self.assertNotEqual(managed_identity, old_identity)
        self.assertEqual(
            (outside_status.st_dev, outside_status.st_ino), old_identity
        )
        self.assertEqual(
            (outside_status.st_uid, outside_status.st_gid), old_owner
        )
        self.assertEqual(stat.S_IMODE(outside_status.st_mode), old_mode)
        self.assertEqual(outside.read_bytes(), content)
        self.assertEqual(managed.read_bytes(), content)
        self.assertEqual(stat.S_IMODE(managed_status.st_mode), MODULE.REGULAR_MODE)
        self.assertNotIn(
            old_identity, {identity for identity, _, _ in fchown.inode_calls}
        )
        self.assertNotIn(
            old_identity, {identity for identity, _ in fchmod.inode_calls}
        )
        self.assertIn(
            (managed_identity, 501, MODULE.LIGHTRAG_GID),
            fchown.inode_calls,
        )
        self.assertIn(
            (managed_identity, MODULE.REGULAR_MODE), fchmod.inode_calls
        )
        self.assertFalse(
            (self.writer / MODULE._private_temp_name(managed.name)).exists()
        )

    def test_same_size_preheld_fd_write_with_new_timestamps_fails_closed(self):
        managed = self.writer / "evidence.json"
        original = b"AAAAAAAAAAAAAAAA"
        replacement = b"BBBBBBBBBBBBBBBB"
        managed.write_bytes(original)
        held = os.open(managed, os.O_RDWR)
        self.addCleanup(os.close, held)
        before = os.fstat(held)
        target_identity = (before.st_dev, before.st_ino)
        real_read = MODULE._read_bounded_regular
        injected = False

        def write_before_copy(node, remaining):
            nonlocal injected
            if (node.device, node.inode) == target_identity and not injected:
                if hasattr(os, "pwrite"):
                    written = os.pwrite(held, replacement, 0)
                else:
                    os.lseek(held, 0, os.SEEK_SET)
                    written = os.write(held, replacement)
                self.assertEqual(written, len(replacement))
                os.fsync(held)
                changed = os.fstat(held)
                if (
                    changed.st_mtime_ns == before.st_mtime_ns
                    and changed.st_ctime_ns == before.st_ctime_ns
                ):
                    os.utime(
                        managed,
                        ns=(
                            changed.st_atime_ns,
                            before.st_mtime_ns + 1_000_000_000,
                        ),
                    )
                    changed = os.fstat(held)
                self.assertEqual(changed.st_size, before.st_size)
                self.assertTrue(
                    changed.st_mtime_ns != before.st_mtime_ns
                    or changed.st_ctime_ns != before.st_ctime_ns
                )
                injected = True
            return real_read(node, remaining)

        with self.simulated_root_metadata() as (fchown, fchmod), mock.patch.object(
            MODULE, "_read_bounded_regular", side_effect=write_before_copy
        ), mock.patch.object(
            MODULE.os, "mkdir"
        ) as mkdir, mock.patch.object(
            MODULE.os, "replace"
        ) as replace, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertTrue(injected)
        self.assertEqual(raised.exception.code, "unsafe_layout")
        self.assertNotIn(
            target_identity,
            {identity for identity, _, _ in fchown.inode_calls},
        )
        self.assertNotIn(
            target_identity,
            {identity for identity, _ in fchmod.inode_calls},
        )
        mkdir.assert_not_called()
        replace.assert_not_called()
        self.assertEqual(managed.stat().st_size, len(original))
        self.assertEqual(managed.read_bytes(), replacement)
        self.assertEqual(
            (managed.stat().st_dev, managed.stat().st_ino), target_identity
        )

    def test_topology_rejects_cross_device_nodes_and_inode_aliases(self):
        root = MODULE._OpenedNode(10, 0o2750, 1, 10, True)
        reports = MODULE._OpenedNode(11, 0o2750, 1, 11, True)
        report = MODULE._OpenedNode(12, 0o0640, 1, 12, False)
        snapshots = [
            MODULE._DirectorySnapshot(root, (("reports", reports),)),
            MODULE._DirectorySnapshot(reports, (("attempt.json", report),)),
        ]
        MODULE._verify_tree_topology(snapshots)

        invalid_snapshots = (
            MODULE._DirectorySnapshot(
                reports,
                (("cross-device.json", report._replace(device=2, inode=13)),),
            ),
            MODULE._DirectorySnapshot(
                reports,
                (("inode-alias.json", report._replace(descriptor=13)),),
            ),
        )
        for invalid_snapshot in invalid_snapshots:
            with self.subTest(
                invalid_snapshot=invalid_snapshot
            ), self.assertRaises(MODULE.PreparationError) as raised:
                MODULE._verify_tree_topology([*snapshots, invalid_snapshot])
            self.assertEqual(raised.exception.code, "unsafe_layout")

    def test_topology_failure_precedes_all_mutation(self):
        self.populate_valid_layout()
        with mock.patch.object(MODULE.os, "geteuid", return_value=0), mock.patch.object(
            MODULE, "_verify_tree_topology", side_effect=MODULE.PreparationError("unsafe_layout")
        ), mock.patch.object(MODULE.os, "fchown") as fchown, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, self.assertRaises(MODULE.PreparationError) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()

    def test_rejects_namespace_change_before_child_mutation(self):
        self.populate_valid_layout()
        verify_snapshots = MODULE._verify_snapshots
        call_count = 0

        def add_late_unknown_node(
            roots, snapshots, *, allow_linked_regulars=False
        ):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                (self.writer / "late-unknown").write_bytes(
                    b"private-content-canary"
                )
            return verify_snapshots(
                roots,
                snapshots,
                allow_linked_regulars=allow_linked_regulars,
            )

        with mock.patch.object(MODULE.os, "geteuid", return_value=0), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(MODULE.os, "fchmod") as fchmod, mock.patch.object(
            MODULE, "_verify_snapshots", side_effect=add_late_unknown_node
        ), self.assertRaises(MODULE.PreparationError) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        fchown.assert_not_called()
        fchmod.assert_not_called()

    def test_revalidation_catches_namespace_change_after_child_mutation(self):
        self.populate_valid_layout()
        verify_snapshots = MODULE._verify_snapshots
        call_count = 0

        def add_late_unknown_node(
            roots, snapshots, *, allow_linked_regulars=False
        ):
            nonlocal call_count
            call_count += 1
            if call_count == 4:
                (self.writer / "late-unknown").write_bytes(
                    b"private-content-canary"
                )
            return verify_snapshots(
                roots,
                snapshots,
                allow_linked_regulars=allow_linked_regulars,
            )

        with self.simulated_root_metadata(
            stable_locks_gid=1234
        ) as (fchown, _), mock.patch.object(
            MODULE, "_verify_snapshots", side_effect=add_late_unknown_node
        ), mock.patch.object(
            MODULE, "_verify_final_attributes", wraps=MODULE._verify_final_attributes
        ), self.assertRaises(MODULE.PreparationError) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "unsafe_layout")
        self.assertEqual(call_count, 4)
        root_descriptors = [call.args[0] for call in fchown.call_args_list[:2]]
        for descriptor in root_descriptors:
            root_calls = [
                call.args[1:]
                for call in fchown.call_args_list
                if call.args[0] == descriptor
            ]
            self.assertTrue(root_calls)
            self.assertTrue(all(owner == (0, 0) for owner in root_calls))
        self.assert_mode(self.fence, 0o0700)
        self.assert_mode(self.writer, 0o0700)

    def test_all_opens_are_no_follow_and_children_are_relative_to_directory_fds(self):
        self.populate_valid_layout()
        real_open = os.open
        with mock.patch.object(MODULE.os, "open", wraps=real_open) as open_file:
            self.run_as_root()

        self.assertTrue(open_file.call_args_list)
        for call in open_file.call_args_list:
            self.assertTrue(call.args[1] & os.O_NOFOLLOW)
            if call.args[0] not in (str(self.fence), str(self.writer)):
                self.assertIn("dir_fd", call.kwargs)

    def test_each_node_is_chowned_before_its_final_mode_is_applied(self):
        manager = mock.Mock()
        with self.simulated_root_metadata() as (fchown, fchmod):
            manager.attach_mock(fchown, "chown")
            manager.attach_mock(fchmod, "chmod")
            MODULE.prepare_shared_fence(0, 1234)

        self.assertEqual(len(manager.mock_calls), 20)
        self.assertEqual(
            [call[0] for call in manager.mock_calls[:4]],
            ["chmod", "chown", "chmod", "chmod"],
        )
        self.assertEqual(manager.mock_calls[0].args[1], MODULE.SEALED_ROOT_MODE)
        self.assertEqual(manager.mock_calls[3].args[1], MODULE.SEALED_ROOT_MODE)
        self.assertEqual(manager.mock_calls[0].args[0], manager.mock_calls[1].args[0])
        self.assertEqual(manager.mock_calls[1].args[0], manager.mock_calls[2].args[0])
        self.assertNotEqual(manager.mock_calls[0].args[0], manager.mock_calls[3].args[0])
        self.assertEqual(
            [call[0] for call in manager.mock_calls[4:14]],
            ["chown", "chmod"] * 5,
        )
        for chown_call, chmod_call in zip(
            manager.mock_calls[4:14:2], manager.mock_calls[5:14:2]
        ):
            self.assertEqual(chown_call.args[0], chmod_call.args[0])
        root_stage = manager.mock_calls[14:18]
        self.assertEqual(
            [call[0] for call in root_stage],
            ["chmod", "chown", "chmod", "chown"],
        )
        self.assertEqual(
            [call.args[1] for call in root_stage[::2]],
            [MODULE.SEALED_ROOT_MODE, MODULE.SEALED_ROOT_MODE],
        )
        for stage_chmod, stage_chown in zip(
            root_stage[::2], root_stage[1::2]
        ):
            self.assertEqual(stage_chmod.args[0], stage_chown.args[0])
        self.assertEqual(
            [call[0] for call in manager.mock_calls[18:]],
            ["chmod", "chmod"],
        )
        self.assertEqual(
            [call.args[0] for call in manager.mock_calls[18:]],
            [root_stage[2].args[0], root_stage[0].args[0]],
        )

    def test_partial_replace_failure_recovers_fixed_temp_and_retries(self):
        _, regulars = self.populate_valid_layout()
        original_contents = {path: path.read_bytes() for path in regulars}
        held_originals = [os.open(path, os.O_RDONLY) for path in regulars]
        for descriptor in held_originals:
            self.addCleanup(os.close, descriptor)
        original_identities = {
            path: (node_status.st_dev, node_status.st_ino)
            for path, node_status in zip(
                regulars, (os.fstat(descriptor) for descriptor in held_originals)
            )
        }
        real_replace = os.replace
        replace_count = 0

        def fail_second_replace(*args, **kwargs):
            nonlocal replace_count
            replace_count += 1
            if replace_count == 2:
                raise OSError("injected replace interruption")
            return real_replace(*args, **kwargs)

        with self.simulated_root_metadata(
            stable_locks_gid=1234
        ) as _, mock.patch.object(
            MODULE.os, "replace", side_effect=fail_second_replace
        ), self.assertRaises(MODULE.PreparationError) as raised:
            MODULE.prepare_shared_fence(501, 1234)
        self.assertEqual(raised.exception.code, "mutation_failed")
        self.assertEqual(replace_count, 2)
        self.assertTrue(
            any(
                (path.stat().st_dev, path.stat().st_ino)
                != original_identities[path]
                for path in regulars
            )
        )

        recovery_target = self.fence / "current.json"
        recovery_temp = self.fence / MODULE._private_temp_name(
            recovery_target.name
        )
        recovery_temp.write_bytes(b"stale-partial-copy")
        recovery_temp.chmod(MODULE.PRIVATE_TEMP_MODE)
        frozen = (
            self.fence,
            self.writer,
            self.fence / "reports",
            self.fence / "attempts",
        )
        with self.simulated_root_metadata(
            initially_frozen=frozen, stable_locks_gid=1234
        ):
            MODULE.prepare_shared_fence(501, 1234)

        self.assertFalse(recovery_temp.exists())
        self.assertFalse(
            any(
                MODULE._private_temp_target(path.name) is not None
                for root in (self.fence, self.writer)
                for path in root.rglob("*")
            )
        )
        for path, content in original_contents.items():
            self.assertEqual(path.read_bytes(), content)
            if path.name in MODULE._LOCK_NAMES:
                self.assertEqual(
                    (path.stat().st_dev, path.stat().st_ino),
                    original_identities[path],
                )
            else:
                self.assertNotEqual(
                    (path.stat().st_dev, path.stat().st_ino),
                    original_identities[path],
                )

    def test_private_regular_fsync_replace_parent_fsync_order(self):
        parent_descriptor = os.open(
            self.writer, os.O_RDONLY | os.O_DIRECTORY
        )
        self.addCleanup(os.close, parent_descriptor)
        parent_status = os.fstat(parent_descriptor)
        parent = MODULE._OpenedNode(
            parent_descriptor,
            MODULE.SHARED_DIRECTORY_MODE,
            parent_status.st_dev,
            parent_status.st_ino,
            True,
        )
        events = []
        real_fsync = os.fsync
        real_replace = os.replace

        def record_fsync(descriptor):
            events.append(
                "parent_fsync"
                if descriptor == parent_descriptor
                else "file_fsync"
            )
            return real_fsync(descriptor)

        def record_replace(*args, **kwargs):
            events.append("replace")
            self.assertEqual(kwargs["src_dir_fd"], parent_descriptor)
            self.assertEqual(kwargs["dst_dir_fd"], parent_descriptor)
            return real_replace(*args, **kwargs)

        with self.simulated_root_metadata(
            stable_locks_gid=1234
        ), mock.patch.object(
            MODULE.os, "fsync", side_effect=record_fsync
        ), mock.patch.object(
            MODULE.os, "replace", side_effect=record_replace
        ):
            MODULE._create_private_regular(
                parent,
                "evidence.json",
                b"durable-content",
                501,
                MODULE.LIGHTRAG_GID,
                None,
            )

        self.assertEqual(events, ["file_fsync", "replace", "parent_fsync"])
        self.assertEqual(
            (self.writer / "evidence.json").read_bytes(), b"durable-content"
        )
        self.assertFalse(
            (self.writer / MODULE._private_temp_name("evidence.json")).exists()
        )

    def test_prepare_finishes_with_final_directory_fsyncs_after_replaces(self):
        self.populate_valid_layout()
        events = []
        real_fsync = os.fsync
        real_replace = os.replace
        root_identities = {
            (path.stat().st_dev, path.stat().st_ino)
            for path in (self.fence, self.writer)
        }

        def record_fsync(descriptor):
            node_status = os.fstat(descriptor)
            identity = (node_status.st_dev, node_status.st_ino)
            events.append(
                (
                    "directory_fsync"
                    if stat.S_ISDIR(node_status.st_mode)
                    else "file_fsync",
                    identity,
                )
            )
            return real_fsync(descriptor)

        def record_replace(*args, **kwargs):
            events.append(("replace", args[1]))
            return real_replace(*args, **kwargs)

        with self.simulated_root_metadata(
            stable_locks_gid=1234
        ), mock.patch.object(
            MODULE.os, "fsync", side_effect=record_fsync
        ), mock.patch.object(
            MODULE.os, "replace", side_effect=record_replace
        ):
            MODULE.prepare_shared_fence(501, 1234)

        replace_indexes = [
            index for index, event in enumerate(events) if event[0] == "replace"
        ]
        self.assertTrue(replace_indexes)
        for index in replace_indexes:
            self.assertEqual(events[index - 1][0], "file_fsync")
            self.assertEqual(events[index + 1][0], "directory_fsync")
        final_root_syncs = [
            event
            for event in events[replace_indexes[-1] + 1 :]
            if event[0] == "directory_fsync" and event[1] in root_identities
        ]
        self.assertEqual(
            {identity for _, identity in final_root_syncs}, root_identities
        )
        writer_identity = (
            self.writer.stat().st_dev,
            self.writer.stat().st_ino,
        )
        self.assertEqual(events[-1], ("directory_fsync", writer_identity))

    def test_failure_before_gate_commit_remains_closed_when_reseal_fails(self):
        real_publish = MODULE._publish_root
        publish_count = 0

        def fail_writer_publish(root):
            nonlocal publish_count
            publish_count += 1
            self.assertEqual(publish_count, 1)
            raise OSError("injected writer publish failure")

        with self.simulated_root_metadata() as _, mock.patch.object(
            MODULE, "_publish_root", side_effect=fail_writer_publish
        ), mock.patch.object(
            MODULE, "_attempt_reseal_directories", return_value=False
        ) as reseal, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "mutation_failed")
        reseal.assert_called_once()
        self.assert_mode(self.fence, MODULE.SEALED_ROOT_MODE)
        self.assert_mode(self.writer, MODULE.SEALED_ROOT_MODE)
        self.assertEqual(real_publish.__name__, "_publish_root")

    def test_writer_publish_fsync_failure_keeps_fence_sealed(self):
        writer_identity = (self.writer.stat().st_dev, self.writer.stat().st_ino)
        real_fsync = os.fsync
        writer_was_published = False

        def fail_published_writer_fsync(descriptor):
            nonlocal writer_was_published
            node_status = os.fstat(descriptor)
            if (node_status.st_dev, node_status.st_ino) == writer_identity:
                exposed = stat.S_IMODE(node_status.st_mode)
                if exposed == MODULE.SHARED_DIRECTORY_MODE:
                    writer_was_published = True
                    raise OSError("injected writer publish fsync failure")
            return real_fsync(descriptor)

        with self.simulated_root_metadata() as _, mock.patch.object(
            MODULE.os, "fsync", side_effect=fail_published_writer_fsync
        ), mock.patch.object(
            MODULE, "_attempt_reseal_directories", return_value=False
        ) as reseal, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "mutation_failed")
        self.assertTrue(writer_was_published)
        reseal.assert_called_once()
        self.assert_mode(self.writer, MODULE.SHARED_DIRECTORY_MODE)
        self.assert_mode(self.fence, MODULE.SEALED_ROOT_MODE)

    def test_fence_publish_is_last_fallible_operation_and_not_rolled_back(self):
        publish_roots = []
        real_publish = MODULE._publish_root
        real_flock = fcntl.flock

        def record_publish(root):
            publish_roots.append((root.device, root.inode))
            return real_publish(root)

        with self.simulated_root_metadata() as _, mock.patch.object(
            MODULE, "_publish_root", side_effect=record_publish
        ), mock.patch.object(
            MODULE, "_attempt_reseal_directories"
        ) as reseal, mock.patch.object(
            MODULE.fcntl, "flock", wraps=real_flock
        ) as flock:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(
            publish_roots,
            [
                (self.writer.stat().st_dev, self.writer.stat().st_ino),
                (self.fence.stat().st_dev, self.fence.stat().st_ino),
            ],
        )
        reseal.assert_not_called()
        self.assertFalse(
            any(call.args[1] & fcntl.LOCK_UN for call in flock.call_args_list),
            "successful commit releases locks only by non-raising descriptor close",
        )
        self.assert_mode(self.writer, MODULE.SHARED_DIRECTORY_MODE)
        self.assert_mode(self.fence, MODULE.SHARED_DIRECTORY_MODE)

    def test_final_fence_publish_failure_leaves_only_fence_sealed(self):
        real_publish = MODULE._publish_root
        publish_count = 0

        def fail_fence_publish(root):
            nonlocal publish_count
            publish_count += 1
            if publish_count == 2:
                raise OSError("injected final fence publish failure")
            return real_publish(root)

        with self.simulated_root_metadata() as _, mock.patch.object(
            MODULE, "_publish_root", side_effect=fail_fence_publish
        ), mock.patch.object(
            MODULE, "_attempt_reseal_directories"
        ) as reseal, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "mutation_failed")
        self.assertEqual(publish_count, 2)
        reseal.assert_not_called()
        self.assert_mode(self.writer, MODULE.SHARED_DIRECTORY_MODE)
        self.assert_mode(self.fence, MODULE.SEALED_ROOT_MODE)

    def test_ambiguous_final_publish_is_complete_and_retry_is_idempotent(self):
        real_publish = MODULE._publish_root
        publish_count = 0

        def publish_then_interrupt(root):
            nonlocal publish_count
            publish_count += 1
            real_publish(root)
            if publish_count == 2:
                raise KeyboardInterrupt("injected post-commit interruption")

        with self.simulated_root_metadata() as _, mock.patch.object(
            MODULE, "_publish_root", side_effect=publish_then_interrupt
        ), mock.patch.object(
            MODULE, "_attempt_reseal_directories"
        ) as reseal, self.assertRaises(KeyboardInterrupt):
            MODULE.prepare_shared_fence(501, 1234)

        reseal.assert_not_called()
        self.assert_mode(self.writer, MODULE.SHARED_DIRECTORY_MODE)
        self.assert_mode(self.fence, MODULE.SHARED_DIRECTORY_MODE)
        lock_identities = {
            name: (path.stat().st_dev, path.stat().st_ino)
            for name in MODULE._LOCK_NAMES
            if (path := self.fence / name).is_file()
        }
        with self.simulated_root_metadata(stable_locks_gid=1234):
            MODULE.prepare_shared_fence(501, 1234)
        self.assertEqual(
            {
                name: (path.stat().st_dev, path.stat().st_ino)
                for name in MODULE._LOCK_NAMES
                if (path := self.fence / name).is_file()
            },
            lock_identities,
        )

    def test_final_staging_failures_remain_sealed_when_reseal_fails(self):
        for operation in ("fchmod", "fchown", "fstat", "fsync"):
            with self.subTest(operation=operation):
                self.assert_final_stage_failure_is_sealed(operation)

    def test_sigkill_after_writer_publish_leaves_fence_sealed(self):
        self.assert_sigkill_publish_checkpoint("writer")

    def test_sigkill_after_fence_publish_leaves_complete_tree_and_unlocks(self):
        self.assert_sigkill_publish_checkpoint("fence")

    def test_missing_lock_final_acquire_failure_cleans_all_locks(self):
        real_flock = fcntl.flock
        lock_acquires = 0

        def fail_second_new_lock(descriptor, operation):
            nonlocal lock_acquires
            if operation & fcntl.LOCK_UN:
                return real_flock(descriptor, operation)
            node_status = os.fstat(descriptor)
            if stat.S_ISREG(node_status.st_mode):
                lock_acquires += 1
                if lock_acquires == 2:
                    raise BlockingIOError("injected second new-lock contention")
            return real_flock(descriptor, operation)

        with self.simulated_root_metadata() as _, mock.patch.object(
            MODULE.fcntl, "flock", side_effect=fail_second_new_lock
        ), mock.patch.object(
            MODULE, "_publish_root"
        ) as publish, self.assertRaises(MODULE.PreparationError) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "mutation_failed")
        self.assertEqual(lock_acquires, 2)
        publish.assert_not_called()
        self.assert_mode(self.fence, MODULE.FROZEN_ROOT_MODE)
        self.assert_mode(self.writer, MODULE.FROZEN_ROOT_MODE)
        for name in MODULE._LOCK_NAMES:
            descriptor = os.open(self.fence / name, os.O_RDONLY)
            self.addCleanup(os.close, descriptor)
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.flock(descriptor, fcntl.LOCK_UN)

    def test_final_attribute_verification_is_exact(self):
        node = MODULE._OpenedNode(
            descriptor=17,
            mode=0o2750,
            device=1,
            inode=2,
            directory=True,
        )
        exact = mock.Mock(st_uid=501, st_gid=1000, st_mode=stat.S_IFDIR | 0o2750)
        with mock.patch.object(MODULE.os, "fstat", return_value=exact):
            MODULE._verify_final_attributes([node], 501, 1000)

        mismatches = (
            mock.Mock(st_uid=502, st_gid=1000, st_mode=stat.S_IFDIR | 0o2750),
            mock.Mock(st_uid=501, st_gid=1001, st_mode=stat.S_IFDIR | 0o2750),
            mock.Mock(st_uid=501, st_gid=1000, st_mode=stat.S_IFDIR | 0o0750),
        )
        for node_status in mismatches:
            with self.subTest(node_status=node_status), mock.patch.object(
                MODULE.os, "fstat", return_value=node_status
            ), self.assertRaises(MODULE.PreparationError) as raised:
                MODULE._verify_final_attributes([node], 501, 1000)
            self.assertEqual(raised.exception.code, "mutation_failed")

        regular = MODULE._OpenedNode(
            descriptor=18,
            mode=0o0640,
            device=1,
            inode=3,
            directory=False,
        )
        linked = mock.Mock(
            st_uid=501,
            st_gid=1000,
            st_mode=stat.S_IFREG | 0o0640,
            st_nlink=2,
        )
        with mock.patch.object(
            MODULE.os, "fstat", return_value=linked
        ), self.assertRaises(MODULE.PreparationError) as raised:
            MODULE._verify_final_attributes([regular], 501, 1000)
        self.assertEqual(raised.exception.code, "mutation_failed")

    def test_verify_complete_layout_without_root_or_mutation(self):
        self.populate_verify_layout()
        real_open = os.open
        with self.verification_metadata(), mock.patch.object(
            MODULE.os, "geteuid", side_effect=AssertionError("must not inspect euid")
        ), mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, mock.patch.object(
            MODULE.os, "mkdir"
        ) as mkdir, mock.patch.object(
            MODULE.os, "open", wraps=real_open
        ) as open_file, mock.patch.object(
            MODULE,
            "_verify_exact_attributes",
            wraps=MODULE._verify_exact_attributes,
        ) as verify_attributes:
            MODULE.verify_shared_fence(501, 1234)

        fchown.assert_not_called()
        fchmod.assert_not_called()
        mkdir.assert_not_called()
        self.assertEqual(verify_attributes.call_count, 4)
        self.assertTrue(open_file.call_args_list)
        for call in open_file.call_args_list:
            self.assertTrue(call.args[1] & os.O_NOFOLLOW)
            self.assertFalse(call.args[1] & os.O_CREAT)
            if call.args[0] not in (str(self.fence), str(self.writer)):
                self.assertIn("dir_fd", call.kwargs)

    def test_verify_expected_attempt_requires_all_four_artifacts(self):
        attempt = "attempt-1_2.3"
        self.populate_verify_layout(attempt)
        required = (
            self.fence / "current.json",
            self.fence / "reports" / (attempt + ".json"),
            self.fence / "attempts" / (attempt + ".json"),
            self.writer / (attempt + ".json"),
        )
        with self.verification_metadata():
            MODULE.verify_shared_fence(501, 1234, attempt)

        for path in required:
            with self.subTest(path=path):
                data = path.read_bytes()
                path.unlink()
                with self.verification_metadata(), self.assertRaises(
                    MODULE.PreparationError
                ) as raised:
                    MODULE.verify_shared_fence(501, 1234, attempt)
                self.assertEqual(raised.exception.code, "verification_failed")
                path.write_bytes(data)
                path.chmod(0o640)

    def test_verify_requires_structural_fence_nodes_but_not_current(self):
        self.populate_verify_layout()
        required = (
            self.fence / "reports",
            self.fence / "attempts",
            self.fence / "serving.lock",
            self.fence / "rebuild.lock",
        )
        with self.verification_metadata():
            MODULE.verify_shared_fence(501, 1234)

        for path in required:
            with self.subTest(path=path):
                if path.is_dir():
                    path.rmdir()
                else:
                    path.unlink()
                with self.verification_metadata(), self.assertRaises(
                    MODULE.PreparationError
                ) as raised:
                    MODULE.verify_shared_fence(501, 1234)
                self.assertEqual(raised.exception.code, "verification_failed")
                if path.name in ("reports", "attempts"):
                    path.mkdir()
                else:
                    path.write_bytes(b"")
                    path.chmod(0o640)

    def test_verify_rejects_every_attribute_class_mismatch(self):
        attempt = "expected-attempt"
        reports, attempts, _ = self.populate_verify_layout(attempt)
        cases = (
            (self.fence, {"st_uid": 999}),
            (self.writer, {"st_gid": 999}),
            (reports, {"st_mode": stat.S_IFDIR | 0o0750}),
            (attempts / (attempt + ".json"), {"st_mode": stat.S_IFREG | 0o600}),
            (self.fence / "serving.lock", {"st_gid": 999}),
            (self.writer / (attempt + ".json"), {"st_uid": 999}),
        )
        for path, mismatch in cases:
            with self.subTest(path=path), self.verification_metadata(
                {path: mismatch}
            ), self.assertRaises(MODULE.PreparationError) as raised:
                MODULE.verify_shared_fence(501, 1234, attempt)
            self.assertEqual(raised.exception.code, "verification_failed")

    def test_verify_rejects_symlink_unknown_nonregular_and_hardlink(self):
        cases = []

        def add_unknown():
            path = self.writer / "unknown"
            path.write_bytes(b"private-content-canary")
            return path

        def add_symlink():
            outside = pathlib.Path(self.temporary.name) / "outside-verify.json"
            outside.write_bytes(b"private-content-canary")
            path = self.writer / "link.json"
            path.symlink_to(outside)
            return path

        def add_directory():
            path = self.writer / "directory.json"
            path.mkdir()
            return path

        cases.extend((add_unknown, add_symlink, add_directory))
        if hasattr(os, "mkfifo"):
            def add_fifo():
                path = self.writer / "fifo.json"
                os.mkfifo(path)
                return path
            cases.append(add_fifo)
        if hasattr(os, "link"):
            def add_hardlink():
                outside = pathlib.Path(self.temporary.name) / "outside-hardlink.json"
                outside.write_bytes(b"private-content-canary")
                path = self.writer / "hardlink.json"
                os.link(outside, path)
                return path
            cases.append(add_hardlink)

        for add_invalid in cases:
            with self.subTest(case=add_invalid.__name__):
                self.populate_verify_layout()
                invalid = add_invalid()
                with self.verification_metadata(), self.assertRaises(
                    MODULE.PreparationError
                ) as raised:
                    MODULE.verify_shared_fence(501, 1234)
                self.assertEqual(raised.exception.code, "verification_failed")
                if invalid.is_dir() and not invalid.is_symlink():
                    invalid.rmdir()
                else:
                    invalid.unlink()
                for child in self.fence.iterdir():
                    if child.is_dir():
                        child.rmdir()
                    else:
                        child.unlink()

    def test_verify_rechecks_namespace_and_never_mutates_on_failure(self):
        self.populate_verify_layout()
        verify_snapshots = MODULE._verify_snapshots
        call_count = 0

        def mutate_before_second_snapshot(roots, snapshots):
            nonlocal call_count
            call_count += 1
            if call_count == 2:
                (self.writer / "late-unknown").write_bytes(
                    b"private-content-canary"
                )
            return verify_snapshots(roots, snapshots)

        with self.verification_metadata(), mock.patch.object(
            MODULE, "_verify_snapshots", side_effect=mutate_before_second_snapshot
        ), mock.patch.object(MODULE.os, "fchown") as fchown, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, mock.patch.object(MODULE.os, "mkdir") as mkdir, self.assertRaises(
            MODULE.PreparationError
        ) as raised:
            MODULE.verify_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "verification_failed")
        self.assertEqual(call_count, 2)
        fchown.assert_not_called()
        fchmod.assert_not_called()
        mkdir.assert_not_called()

    def test_verify_rejects_invalid_attempt_before_opening_roots(self):
        for attempt in ("", ".hidden", "slash/name", "a" * 129):
            with self.subTest(attempt=attempt), mock.patch.object(
                MODULE.os, "open"
            ) as open_file, self.assertRaises(MODULE.PreparationError) as raised:
                MODULE.verify_shared_fence(501, 1234, attempt)
            self.assertEqual(raised.exception.code, "invalid_attempt")
            open_file.assert_not_called()

    def test_verify_dotted_attempt_uses_literal_identifier_plus_json_suffix(self):
        attempt = "x.json"
        self.populate_verify_layout(attempt)
        with self.verification_metadata():
            MODULE.verify_shared_fence(501, 1234, attempt)
        self.assertTrue((self.writer / "x.json.json").is_file())

    def test_verify_cli_dispatch_preserves_legacy_prepare(self):
        with mock.patch.object(MODULE, "prepare_shared_fence") as prepare, mock.patch.object(
            MODULE, "verify_shared_fence"
        ) as verify:
            self.assertEqual(MODULE.main(["501", "1234"]), 0)
            prepare.assert_called_once_with(501, 1234)
            verify.assert_not_called()

        for arguments, expected in (
            (["verify", "501", "1234"], (501, 1234, None)),
            (["verify", "501", "1234", "attempt"], (501, 1234, "attempt")),
        ):
            with self.subTest(arguments=arguments), mock.patch.object(
                MODULE, "prepare_shared_fence"
            ) as prepare, mock.patch.object(
                MODULE, "verify_shared_fence"
            ) as verify:
                self.assertEqual(MODULE.main(arguments), 0)
                verify.assert_called_once_with(*expected)
                prepare.assert_not_called()

    def test_verify_cli_real_success_and_fixed_bounded_failure(self):
        self.populate_verify_layout()
        with self.verification_metadata():
            self.assertEqual(MODULE.main(["verify", "501", "1234"]), 0)

        canary = "secret-verification-path-canary"
        (self.writer / canary).write_bytes(canary.encode("utf-8"))
        stderr = io.StringIO()
        with self.verification_metadata(), contextlib.redirect_stderr(stderr):
            self.assertEqual(MODULE.main(["verify", "501", "1234"]), 1)
        self.assertEqual(
            stderr.getvalue(), "prepare_shared_fence: verification_failed\n"
        )
        self.assertNotIn(canary, stderr.getvalue())
        self.assertLessEqual(len(stderr.getvalue()), 64)

    def test_non_root_is_rejected_before_opening_mounts(self):
        with mock.patch.object(MODULE.os, "geteuid", return_value=501), mock.patch.object(
            MODULE.os, "open"
        ) as open_file, self.assertRaises(MODULE.PreparationError) as raised:
            MODULE.prepare_shared_fence(501, 1234)

        self.assertEqual(raised.exception.code, "root_required")
        open_file.assert_not_called()

    def test_invalid_gid_values_are_rejected(self):
        invalid_cli_values = (
            "",
            "0",
            "-1",
            "+1",
            " 1",
            "1.0",
            "2147483648",
            "not-a-gid",
        )
        for value in invalid_cli_values:
            with self.subTest(value=value), self.assertRaises(
                MODULE.PreparationError
            ) as raised:
                MODULE.parse_shared_gid(value)
            self.assertEqual(raised.exception.code, "invalid_gid")

        for value in (0, -1, 2_147_483_648, True, "1234", None):
            with self.subTest(value=value), self.assertRaises(
                MODULE.PreparationError
            ) as raised:
                MODULE.prepare_shared_fence(501, value)
            self.assertEqual(raised.exception.code, "invalid_gid")

        self.assertEqual(MODULE.parse_shared_gid("1"), 1)
        self.assertEqual(MODULE.parse_shared_gid("2147483647"), 2_147_483_647)

    def test_invalid_operator_uid_values_are_rejected_and_zero_is_valid(self):
        for value in ("", "-1", "+1", " 1", "1.0", "2147483648"):
            with self.subTest(value=value), self.assertRaises(
                MODULE.PreparationError
            ) as raised:
                MODULE.parse_operator_uid(value)
            self.assertEqual(raised.exception.code, "invalid_uid")

        for value in (-1, 2_147_483_648, True, "501", None):
            with self.subTest(value=value), self.assertRaises(
                MODULE.PreparationError
            ) as raised:
                MODULE.prepare_shared_fence(value, 1234)
            self.assertEqual(raised.exception.code, "invalid_uid")

        self.assertEqual(MODULE.parse_operator_uid("0"), 0)
        self.assertEqual(MODULE.parse_operator_uid("2147483647"), 2_147_483_647)

    def test_cli_errors_are_fixed_bounded_and_do_not_leak_names(self):
        canary = "secret-path-and-content-canary"
        (self.writer / canary).write_bytes(canary.encode("utf-8"))
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr), mock.patch.object(
            MODULE.os, "geteuid", return_value=0
        ), mock.patch.object(
            MODULE.os, "fchown"
        ):
            result = MODULE.main(["501", "1234"])

        self.assertEqual(result, 1)
        self.assertEqual(stderr.getvalue(), "prepare_shared_fence: unsafe_layout\n")
        self.assertNotIn(canary, stderr.getvalue())
        self.assertLessEqual(len(stderr.getvalue()), 64)

    def test_cli_bounds_mutation_and_unexpected_failures(self):
        canary = "secret-path-and-content-canary"
        with mock.patch.object(MODULE.os, "geteuid", return_value=0), mock.patch.object(
            MODULE.os, "fchown", side_effect=OSError(canary)
        ), contextlib.redirect_stderr(io.StringIO()) as stderr:
            self.assertEqual(MODULE.main(["501", "1234"]), 1)
        self.assertEqual(
            stderr.getvalue(), "prepare_shared_fence: mutation_failed\n"
        )
        self.assertNotIn(canary, stderr.getvalue())
        self.assertLessEqual(len(stderr.getvalue()), 64)

        for error, expected in (
            (RuntimeError(canary), "prepare_shared_fence: internal_error\n"),
        ):
            stderr = io.StringIO()
            with self.subTest(expected=expected), contextlib.redirect_stderr(
                stderr
            ), mock.patch.object(MODULE, "prepare_shared_fence", side_effect=error):
                self.assertEqual(MODULE.main(["501", "1234"]), 1)
            self.assertEqual(stderr.getvalue(), expected)
            self.assertNotIn(canary, stderr.getvalue())
            self.assertLessEqual(len(stderr.getvalue()), 64)

    def test_cli_rejects_argument_count_and_non_root(self):
        cases = (
            ([], 0, "prepare_shared_fence: invalid_arguments\n"),
            (["1"], 0, "prepare_shared_fence: invalid_arguments\n"),
            (["1", "2", "3"], 0, "prepare_shared_fence: invalid_arguments\n"),
            (["invalid", "1"], 0, "prepare_shared_fence: invalid_uid\n"),
            (["0", "0"], 0, "prepare_shared_fence: invalid_gid\n"),
            (["1", "2"], 501, "prepare_shared_fence: root_required\n"),
        )
        for arguments, euid, expected in cases:
            with self.subTest(arguments=arguments), mock.patch.object(
                MODULE.os, "geteuid", return_value=euid
            ), contextlib.redirect_stderr(io.StringIO()) as stderr:
                self.assertEqual(MODULE.main(arguments), 1)
                self.assertEqual(stderr.getvalue(), expected)


if __name__ == "__main__":
    unittest.main()
