import contextlib
import errno
import importlib.util
import io
import os
import pathlib
import stat
import sys
import tempfile
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("validate_shared_fence_host.py")
SPEC = importlib.util.spec_from_file_location(
    "validate_shared_fence_host", MODULE_PATH
)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)
_UNSET = object()


class _StatOverlay:
    def __init__(self, original, **overrides):
        self._original = original
        self._overrides = overrides

    def __getattr__(self, name):
        if name in self._overrides:
            return self._overrides[name]
        return getattr(self._original, name)


class ValidateSharedFenceHostTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        # macOS commonly exposes /var as a symlink to /private/var.  Resolve the
        # test sandbox itself so a success fixture has no symlink ancestors.
        self.base = pathlib.Path(self.temporary.name).resolve()
        self.fence = self.base / "rebuild-fence"
        self.writer = self.base / "writer-evidence"
        self.operator_uid = os.getuid()
        self.shared_gid = os.getgid() or 1

    def validate(
        self,
        fence=None,
        writer=None,
        operator_uid=_UNSET,
        shared_gid=_UNSET,
    ):
        MODULE.validate_shared_fence_host(
            str(self.fence if fence is None else fence),
            str(self.writer if writer is None else writer),
            self.operator_uid if operator_uid is _UNSET else operator_uid,
            self.shared_gid if shared_gid is _UNSET else shared_gid,
        )

    def assert_validation_error(
        self,
        code,
        *,
        fence=None,
        writer=None,
        operator_uid=_UNSET,
        shared_gid=_UNSET,
    ):
        with self.assertRaises(MODULE.HostValidationError) as raised:
            self.validate(fence, writer, operator_uid, shared_gid)
        self.assertEqual(raised.exception.code, code)

    def make_empty_roots(self):
        self.fence.mkdir(mode=0o700)
        self.writer.mkdir(mode=0o700)
        self.fence.chmod(0o700)
        self.writer.chmod(0o700)

    def populate_prepared_layout(self):
        self.make_empty_roots()
        reports = self.fence / "reports"
        attempts = self.fence / "attempts"
        reports.mkdir(mode=0o750)
        attempts.mkdir(mode=0o750)

        fence_regulars = (
            self.fence / "serving.lock",
            self.fence / "rebuild.lock",
            self.fence / "current.json",
            reports / "attempt-1.json",
            attempts / "attempt-1.json",
        )
        writer_regular = self.writer / "attempt-1.json"
        for path in (*fence_regulars, writer_regular):
            path.write_bytes(b"private-content-canary")
            path.chmod(0o640)
        for path in (self.fence, self.writer, reports, attempts):
            path.chmod(0o750)
        return reports, attempts, fence_regulars, writer_regular

    def prepared_metadata(self):
        result = {}
        for path in (self.fence, *self.fence.rglob("*")):
            mode = 0o2750 if path.is_dir() else 0o0640
            result[path] = (MODULE.LIGHTRAG_UID, self.shared_gid, mode)
        for path in (self.writer, *self.writer.rglob("*")):
            mode = 0o2750 if path.is_dir() else 0o0640
            result[path] = (self.operator_uid, MODULE.LIGHTRAG_GID, mode)
        return result

    @contextlib.contextmanager
    def metadata(self, expected=None, overrides=None):
        expected = expected or {}
        overrides = overrides or {}
        records = {}
        for path, (uid, gid, permissions) in expected.items():
            node_status = path.lstat()
            records[(node_status.st_dev, node_status.st_ino)] = {
                "st_uid": uid,
                "st_gid": gid,
                "st_mode": (node_status.st_mode & ~0o7777) | permissions,
            }
        for path, changes in overrides.items():
            node_status = path.lstat()
            records.setdefault((node_status.st_dev, node_status.st_ino), {}).update(
                changes
            )

        real_fstat = os.fstat

        def deployment_fstat(descriptor):
            node_status = real_fstat(descriptor)
            changes = records.get((node_status.st_dev, node_status.st_ino), {})
            return _StatOverlay(node_status, **changes)

        with mock.patch.object(MODULE.os, "fstat", side_effect=deployment_fstat):
            yield

    def assert_rejected_without_mutation(self, code="unsafe_layout"):
        operations = (
            mock.patch.object(MODULE.os, "mkdir"),
            mock.patch.object(MODULE.os, "chmod"),
            mock.patch.object(MODULE.os, "fchmod"),
            mock.patch.object(MODULE.os, "chown"),
            mock.patch.object(MODULE.os, "fchown"),
        )
        with contextlib.ExitStack() as stack:
            mutation_mocks = [stack.enter_context(operation) for operation in operations]
            with self.assertRaises(MODULE.HostValidationError) as raised:
                self.validate()
        self.assertEqual(raised.exception.code, code)
        for operation in mutation_mocks:
            operation.assert_not_called()

    def mode_with_type(self, path, permissions):
        return (path.lstat().st_mode & ~0o7777) | permissions

    def test_cli_requires_exactly_four_arguments_and_dispatches_typed_values(self):
        invalid_arguments = (
            [],
            ["/fence"],
            ["/fence", "/writer", "501"],
            ["/fence", "/writer", "501", "1234", "extra"],
        )
        for arguments in invalid_arguments:
            with self.subTest(arguments=arguments), mock.patch.object(
                MODULE, "validate_shared_fence_host"
            ) as validate, contextlib.redirect_stdout(
                io.StringIO()
            ) as stdout, contextlib.redirect_stderr(
                io.StringIO()
            ) as stderr:
                self.assertEqual(MODULE.main(arguments), 1)
                self.assertEqual(stdout.getvalue(), "")
                self.assertEqual(
                    stderr.getvalue(),
                    "validate_shared_fence_host: invalid_arguments\n",
                )
                validate.assert_not_called()

        with mock.patch.object(MODULE, "validate_shared_fence_host") as validate:
            self.assertEqual(
                MODULE.main(["/fence", "/writer", "501", "1234"]), 0
            )
        validate.assert_called_once_with("/fence", "/writer", 501, 1234)

    def test_cli_errors_are_fixed_bounded_and_redacted(self):
        canary = "secret-host-path-and-os-error-canary"
        codes = (
            "invalid_arguments",
            "invalid_path",
            "invalid_uid",
            "invalid_gid",
            "unsafe_ancestor",
            "unsafe_layout",
            "privileged_recovery_required",
            "create_failed",
        )
        for code in codes:
            stdout = io.StringIO()
            stderr = io.StringIO()
            with self.subTest(code=code), mock.patch.object(
                MODULE,
                "validate_shared_fence_host",
                side_effect=MODULE.HostValidationError(code),
            ), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                self.assertEqual(
                    MODULE.main(["/" + canary, "/writer", "501", "1234"]),
                    1,
                )
            self.assertEqual(stdout.getvalue(), "")
            self.assertEqual(
                stderr.getvalue(), f"validate_shared_fence_host: {code}\n"
            )
            self.assertNotIn(canary, stderr.getvalue())
            self.assertLessEqual(len(stderr.getvalue()), 64)

        stdout = io.StringIO()
        stderr = io.StringIO()
        with mock.patch.object(
            MODULE,
            "validate_shared_fence_host",
            side_effect=RuntimeError(canary),
        ), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            self.assertEqual(
                MODULE.main(["/fence", "/writer", "501", "1234"]), 1
            )
        self.assertEqual(stdout.getvalue(), "")
        self.assertEqual(
            stderr.getvalue(), "validate_shared_fence_host: internal_error\n"
        )
        self.assertNotIn(canary, stderr.getvalue())
        self.assertLessEqual(len(stderr.getvalue()), 64)

    def test_real_cli_layout_failure_does_not_leak_an_entry_name(self):
        self.make_empty_roots()
        canary = "secret-layout-entry-canary"
        (self.writer / canary).write_bytes(canary.encode("utf-8"))
        stdout = io.StringIO()
        stderr = io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            self.assertEqual(
                MODULE.main(
                    [
                        str(self.fence),
                        str(self.writer),
                        str(self.operator_uid),
                        str(self.shared_gid),
                    ]
                ),
                1,
            )
        self.assertEqual(stdout.getvalue(), "")
        self.assertEqual(
            stderr.getvalue(), "validate_shared_fence_host: unsafe_layout\n"
        )
        self.assertNotIn(canary, stderr.getvalue())
        self.assertLessEqual(len(stderr.getvalue()), 64)

    def test_numeric_id_parsers_are_decimal_ascii_and_bounded(self):
        self.assertEqual(MODULE.parse_operator_uid("0"), 0)
        self.assertEqual(MODULE.parse_operator_uid("2147483647"), 2_147_483_647)
        self.assertEqual(MODULE.parse_shared_gid("1"), 1)
        self.assertEqual(MODULE.parse_shared_gid("2147483647"), 2_147_483_647)

        for value in ("", "-1", "+1", " 1", "1.0", "2147483648", "١"):
            with self.subTest(parser="uid", value=value), self.assertRaises(
                MODULE.HostValidationError
            ) as raised:
                MODULE.parse_operator_uid(value)
            self.assertEqual(raised.exception.code, "invalid_uid")

        for value in (
            "",
            "0",
            "-1",
            "+1",
            " 1",
            "1.0",
            "2147483648",
            "١",
        ):
            with self.subTest(parser="gid", value=value), self.assertRaises(
                MODULE.HostValidationError
            ) as raised:
                MODULE.parse_shared_gid(value)
            self.assertEqual(raised.exception.code, "invalid_gid")

    def test_api_rejects_non_integer_and_out_of_range_ids_before_filesystem_access(self):
        invalid_uids = (-1, 2_147_483_648, True, "501", None)
        for value in invalid_uids:
            with self.subTest(kind="uid", value=value), mock.patch.object(
                MODULE.os, "open"
            ) as open_file:
                self.assert_validation_error("invalid_uid", operator_uid=value)
                open_file.assert_not_called()

        invalid_gids = (0, -1, 2_147_483_648, True, "1234", None)
        for value in invalid_gids:
            with self.subTest(kind="gid", value=value), mock.patch.object(
                MODULE.os, "open"
            ) as open_file:
                self.assert_validation_error("invalid_gid", shared_gid=value)
                open_file.assert_not_called()

    def test_root_relative_and_lexically_noncanonical_paths_are_rejected(self):
        cases = []
        for index in range(7):
            case_base = self.base / f"lexical-{index}"
            case_base.mkdir(mode=0o700)
            valid_fence = case_base / "fence"
            valid_writer = case_base / "writer"
            if index == 0:
                cases.append(("/", str(valid_writer)))
            elif index == 1:
                cases.append(("relative-fence", str(valid_writer)))
            elif index == 2:
                cases.append((str(case_base) + "//fence", str(valid_writer)))
            elif index == 3:
                cases.append((str(valid_fence) + "/", str(valid_writer)))
            elif index == 4:
                cases.append((str(case_base) + "/./fence", str(valid_writer)))
            elif index == 5:
                cases.append(
                    (str(case_base) + "/parent/../fence", str(valid_writer))
                )
            else:
                cases.append((str(valid_fence), "/"))

        for fence, writer in cases:
            with self.subTest(fence=fence, writer=writer):
                self.assert_validation_error(
                    "invalid_path", fence=fence, writer=writer
                )

    def test_missing_direct_parent_is_rejected_without_creating_any_component(self):
        missing_parent = self.base / "missing-parent"
        fence = missing_parent / "fence"
        writer = self.base / "writer"

        self.assert_validation_error(
            "unsafe_ancestor", fence=fence, writer=writer
        )

        self.assertFalse(missing_parent.exists())
        self.assertFalse(writer.exists())

    def test_symlink_ancestor_and_symlink_root_are_rejected(self):
        target = self.base / "ancestor-target"
        target.mkdir(mode=0o700)
        ancestor_link = self.base / "ancestor-link"
        ancestor_link.symlink_to(target, target_is_directory=True)
        writer = self.base / "writer-for-ancestor"

        self.assert_validation_error(
            "unsafe_ancestor",
            fence=ancestor_link / "fence",
            writer=writer,
        )
        self.assertFalse((target / "fence").exists())
        self.assertFalse(writer.exists())

        root_target = self.base / "root-target"
        root_target.mkdir(mode=0o700)
        root_link = self.base / "root-link"
        root_link.symlink_to(root_target, target_is_directory=True)
        self.assert_validation_error(
            "unsafe_layout",
            fence=root_link,
            writer=self.base / "writer-for-root-link",
        )

    def test_same_or_lexically_nested_roots_are_rejected_before_creation(self):
        cases = (
            (self.base / "same", self.base / "same"),
            (self.base / "outer-a", self.base / "outer-a" / "inner"),
            (self.base / "outer-b" / "inner", self.base / "outer-b"),
        )
        for fence, writer in cases:
            with self.subTest(fence=fence, writer=writer):
                self.assert_validation_error(
                    "unsafe_layout", fence=fence, writer=writer
                )
                self.assertFalse(fence.exists())
                self.assertFalse(writer.exists())

    def test_missing_roots_are_created_fresh_empty_private_and_operator_owned(self):
        with mock.patch.object(MODULE.os, "chmod") as chmod, mock.patch.object(
            MODULE.os, "fchmod"
        ) as fchmod, mock.patch.object(MODULE.os, "chown") as chown, mock.patch.object(
            MODULE.os, "fchown"
        ) as fchown:
            self.validate()

        for root in (self.fence, self.writer):
            node_status = root.stat()
            self.assertTrue(stat.S_ISDIR(node_status.st_mode))
            self.assertEqual(stat.S_IMODE(node_status.st_mode), 0o700)
            self.assertEqual(node_status.st_uid, self.operator_uid)
            self.assertEqual(list(root.iterdir()), [])
        chmod.assert_not_called()
        fchmod.assert_not_called()
        chown.assert_not_called()
        fchown.assert_not_called()

    def test_restrictive_umask_still_creates_exact_retryable_roots(self):
        previous_umask = os.umask(0o777)
        try:
            self.validate()
            restored_umask = os.umask(previous_umask)
        finally:
            os.umask(previous_umask)

        self.assertEqual(restored_umask, 0o777)
        for root in (self.fence, self.writer):
            self.assertEqual(stat.S_IMODE(root.stat().st_mode), 0o700)
            self.assertEqual(root.stat().st_uid, self.operator_uid)
            self.assertEqual(list(root.iterdir()), [])
        self.validate()

    def test_fresh_existing_root_group_is_intentionally_unconstrained(self):
        self.make_empty_roots()
        expected = {
            self.fence: (self.operator_uid, 900_001, 0o700),
            self.writer: (self.operator_uid, 900_002, 0o700),
        }
        with self.metadata(expected):
            self.validate()

    def test_second_create_failure_leaves_one_retryable_fresh_root(self):
        real_mkdir = os.mkdir
        call_count = 0
        canary = "secret-create-error-canary"

        def fail_second_create(path, mode=0o777, *, dir_fd=None):
            nonlocal call_count
            call_count += 1
            if call_count == 2:
                raise OSError(canary)
            return real_mkdir(path, mode, dir_fd=dir_fd)

        with mock.patch.object(
            MODULE.os, "mkdir", side_effect=fail_second_create
        ), self.assertRaises(MODULE.HostValidationError) as raised:
            self.validate()
        self.assertEqual(raised.exception.code, "create_failed")
        self.assertEqual(call_count, 2)

        existing = [root for root in (self.fence, self.writer) if root.exists()]
        missing = [root for root in (self.fence, self.writer) if not root.exists()]
        self.assertEqual(len(existing), 1)
        self.assertEqual(len(missing), 1)
        node_status = existing[0].stat()
        self.assertEqual(stat.S_IMODE(node_status.st_mode), 0o700)
        self.assertEqual(node_status.st_uid, self.operator_uid)
        self.assertEqual(list(existing[0].iterdir()), [])

        self.validate()
        self.assertTrue(self.fence.is_dir())
        self.assertTrue(self.writer.is_dir())

    def test_valid_prepared_trees_are_accepted_with_exact_deployment_metadata(self):
        self.populate_prepared_layout()
        with self.metadata(self.prepared_metadata()):
            self.validate()

    def test_readable_frozen_roots_accept_safe_transitional_content_for_retry(self):
        self.populate_prepared_layout()
        frozen = {
            self.fence: (0, 0, 0o700),
            self.writer: (0, 0, 0o700),
        }
        with self.metadata(frozen):
            self.validate()

    def test_readable_sealed_roots_accept_safe_transitional_content_for_recovery(
        self,
    ):
        self.populate_prepared_layout()
        sealed = {}
        for path in (self.fence, *self.fence.rglob("*")):
            mode = (
                MODULE.SEALED_ROOT_MODE
                if path == self.fence
                else (
                    MODULE.SHARED_DIRECTORY_MODE
                    if path.is_dir()
                    else MODULE.REGULAR_MODE
                )
            )
            sealed[path] = (MODULE.LIGHTRAG_UID, self.shared_gid, mode)
        for path in (self.writer, *self.writer.rglob("*")):
            mode = (
                MODULE.SEALED_ROOT_MODE
                if path == self.writer
                else (
                    MODULE.SHARED_DIRECTORY_MODE
                    if path.is_dir()
                    else MODULE.REGULAR_MODE
                )
            )
            sealed[path] = (self.operator_uid, MODULE.LIGHTRAG_GID, mode)

        with self.metadata(sealed):
            self.validate()

    def test_inaccessible_frozen_root_requires_explicit_privileged_recovery(self):
        self.make_empty_roots()
        frozen = {
            self.fence: (0, 0, 0o700),
            self.writer: (0, 0, 0o700),
        }
        opaque = PermissionError(errno.EACCES, "secret-permission-canary")
        with self.metadata(frozen), mock.patch.object(
            MODULE.os,
            "listdir",
            side_effect=opaque,
        ), self.assertRaises(MODULE.HostValidationError) as raised:
            self.validate()
        self.assertEqual(raised.exception.code, "privileged_recovery_required")

    def test_frozen_root_open_denial_requires_privileged_recovery(self):
        self.make_empty_roots()
        real_open = os.open
        real_stat = os.stat
        frozen_status = _StatOverlay(
            self.fence.lstat(),
            st_uid=0,
            st_gid=0,
            st_mode=stat.S_IFDIR | 0o700,
        )

        def deny_fence_open(path, flags, mode=0o777, *, dir_fd=None):
            if path == self.fence.name and dir_fd is not None:
                raise PermissionError(errno.EACCES, "secret-open-canary")
            return real_open(path, flags, mode, dir_fd=dir_fd)

        def report_frozen(path, *, dir_fd=None, follow_symlinks=True):
            if path == self.fence.name and dir_fd is not None:
                return frozen_status
            return real_stat(
                path, dir_fd=dir_fd, follow_symlinks=follow_symlinks
            )

        with mock.patch.object(
            MODULE.os, "open", side_effect=deny_fence_open
        ), mock.patch.object(
            MODULE.os, "stat", side_effect=report_frozen
        ), self.assertRaises(MODULE.HostValidationError) as raised:
            self.validate()

        self.assertEqual(raised.exception.code, "privileged_recovery_required")
        self.assertEqual(list(self.fence.iterdir()), [])
        self.assertEqual(list(self.writer.iterdir()), [])

    def test_frozen_root_does_not_hide_io_or_structural_failures(self):
        self.make_empty_roots()
        frozen = {
            self.fence: (0, 0, 0o700),
            self.writer: (0, 0, 0o700),
        }
        for failure in (
            OSError(errno.EIO, "secret-io-canary"),
            [f"entry-{index}.json" for index in range(MODULE.MAX_DIRECTORY_ENTRIES + 1)],
        ):
            with self.subTest(failure=type(failure).__name__), self.metadata(
                frozen
            ), mock.patch.object(
                MODULE.os, "listdir", side_effect=failure if isinstance(failure, OSError) else None, return_value=failure if isinstance(failure, list) else None
            ), self.assertRaises(MODULE.HostValidationError) as raised:
                self.validate()
            self.assertEqual(raised.exception.code, "unsafe_layout")

    def test_root_operator_treats_nonempty_root_root_0700_as_frozen(self):
        self.populate_prepared_layout()
        expected = {
            path: (0, 0, 0o700)
            for path in (self.fence, self.writer)
        }
        for ancestor in {self.base, *self.base.parents}:
            node_status = ancestor.lstat()
            expected[ancestor] = (
                0,
                node_status.st_gid,
                stat.S_IMODE(node_status.st_mode) & ~0o022,
            )
        with self.metadata(expected):
            self.validate(operator_uid=0)

    def test_unknown_entry_is_rejected_without_mutation(self):
        self.make_empty_roots()
        (self.fence / "secret-unknown-entry-canary").write_bytes(b"private")
        expected = {
            self.fence: (MODULE.LIGHTRAG_UID, self.shared_gid, 0o2750),
            self.writer: (self.operator_uid, MODULE.LIGHTRAG_GID, 0o2750),
        }
        with self.metadata(expected):
            self.assert_rejected_without_mutation()

    @unittest.skipUnless(hasattr(os, "mkfifo"), "POSIX FIFO required")
    def test_fifo_with_an_allowed_identifier_shape_is_rejected(self):
        self.make_empty_roots()
        os.mkfifo(self.writer / "fifo.json")
        expected = {
            self.fence: (MODULE.LIGHTRAG_UID, self.shared_gid, 0o2750),
            self.writer: (self.operator_uid, MODULE.LIGHTRAG_GID, 0o2750),
        }
        with self.metadata(expected):
            self.assert_rejected_without_mutation()

    def test_symlink_entry_is_rejected_without_touching_its_target(self):
        self.make_empty_roots()
        target = self.base / "outside-canary.json"
        target.write_bytes(b"outside-content-canary")
        (self.writer / "link.json").symlink_to(target)

        expected = {
            self.fence: (MODULE.LIGHTRAG_UID, self.shared_gid, 0o2750),
            self.writer: (self.operator_uid, MODULE.LIGHTRAG_GID, 0o2750),
        }
        with self.metadata(expected):
            self.assert_rejected_without_mutation()
        self.assertEqual(target.read_bytes(), b"outside-content-canary")

    @unittest.skipUnless(hasattr(os, "link"), "POSIX hard links required")
    def test_hard_link_that_extends_the_managed_namespace_is_rejected(self):
        self.make_empty_roots()
        target = self.base / "outside-hardlink.json"
        target.write_bytes(b"outside-content-canary")
        os.link(target, self.writer / "evidence.json")

        expected = {
            self.fence: (MODULE.LIGHTRAG_UID, self.shared_gid, 0o2750),
            self.writer: (self.operator_uid, MODULE.LIGHTRAG_GID, 0o2750),
        }
        with self.metadata(expected):
            self.assert_rejected_without_mutation()
        self.assertEqual(target.read_bytes(), b"outside-content-canary")

    def test_prepared_owner_group_and_mode_mismatches_are_rejected(self):
        reports, _, fence_regulars, writer_regular = self.populate_prepared_layout()
        expected = self.prepared_metadata()
        cases = (
            (self.fence, {"st_uid": MODULE.LIGHTRAG_UID + 1}),
            (self.writer, {"st_gid": MODULE.LIGHTRAG_GID + 1}),
            (reports, {"st_mode": self.mode_with_type(reports, 0o0750)}),
            (fence_regulars[0], {"st_gid": self.shared_gid + 1}),
            (writer_regular, {"st_uid": self.operator_uid + 1}),
            (
                writer_regular,
                {"st_mode": self.mode_with_type(writer_regular, 0o0600)},
            ),
        )
        for path, changes in cases:
            with self.subTest(path=path, changes=changes), self.metadata(
                expected, {path: changes}
            ), self.assertRaises(MODULE.HostValidationError) as raised:
                self.validate()
            self.assertEqual(raised.exception.code, "unsafe_layout")

    def test_fresh_root_wrong_owner_or_mode_is_rejected(self):
        self.make_empty_roots()
        cases = (
            (self.fence, {"st_uid": self.operator_uid + 1}),
            (
                self.writer,
                {"st_mode": self.mode_with_type(self.writer, 0o0750)},
            ),
        )
        for path, changes in cases:
            with self.subTest(path=path), self.metadata(
                overrides={path: changes}
            ), self.assertRaises(MODULE.HostValidationError) as raised:
                self.validate()
            self.assertEqual(raised.exception.code, "unsafe_layout")

    def test_root_child_inode_aliases_and_cross_device_children_are_rejected(self):
        _, _, fence_regulars, _ = self.populate_prepared_layout()
        expected = self.prepared_metadata()
        fence_status = self.fence.lstat()
        writer_status = self.writer.lstat()
        first_status = fence_regulars[0].lstat()
        cases = (
            {
                self.writer: {
                    "st_dev": fence_status.st_dev,
                    "st_ino": fence_status.st_ino,
                }
            },
            {
                fence_regulars[1]: {
                    "st_dev": first_status.st_dev,
                    "st_ino": first_status.st_ino,
                }
            },
            {fence_regulars[0]: {"st_dev": fence_status.st_dev + 1}},
        )
        self.assertNotEqual(
            (fence_status.st_dev, fence_status.st_ino),
            (writer_status.st_dev, writer_status.st_ino),
        )
        for overrides in cases:
            with self.subTest(overrides=overrides), self.metadata(
                expected, overrides
            ), self.assertRaises(MODULE.HostValidationError) as raised:
                self.validate()
            self.assertEqual(raised.exception.code, "unsafe_layout")

    def test_component_opens_are_no_follow_close_on_exec_and_dir_fd_relative(self):
        self.populate_prepared_layout()
        real_open = os.open
        with self.metadata(self.prepared_metadata()), mock.patch.object(
            MODULE.os, "open", wraps=real_open
        ) as open_file:
            self.validate()

        self.assertTrue(open_file.call_args_list)
        for call in open_file.call_args_list:
            path, flags = call.args[:2]
            self.assertTrue(flags & os.O_NOFOLLOW, call)
            self.assertTrue(flags & os.O_CLOEXEC, call)
            self.assertFalse(flags & os.O_CREAT, call)
            self.assertFalse(flags & os.O_TRUNC, call)
            if path == "/":
                self.assertNotIn("dir_fd", call.kwargs)
            else:
                self.assertFalse(os.path.isabs(path), call)
                self.assertNotIn("/", path, call)
                self.assertIn("dir_fd", call.kwargs)

    def test_existing_fresh_and_prepared_roots_are_never_mutated(self):
        fixtures = ("fresh", "prepared")
        for fixture in fixtures:
            with self.subTest(fixture=fixture):
                if fixture == "fresh":
                    self.make_empty_roots()
                    metadata = contextlib.nullcontext()
                else:
                    self.populate_prepared_layout()
                    metadata = self.metadata(self.prepared_metadata())

                before = {
                    path: (
                        path.lstat().st_dev,
                        path.lstat().st_ino,
                        stat.S_IMODE(path.lstat().st_mode),
                    )
                    for path in (self.fence, self.writer)
                }
                with metadata, mock.patch.object(
                    MODULE.os, "mkdir"
                ) as mkdir, mock.patch.object(
                    MODULE.os, "chmod"
                ) as chmod, mock.patch.object(
                    MODULE.os, "fchmod"
                ) as fchmod, mock.patch.object(
                    MODULE.os, "chown"
                ) as chown, mock.patch.object(
                    MODULE.os, "fchown"
                ) as fchown:
                    self.validate()
                for operation in (mkdir, chmod, fchmod, chown, fchown):
                    operation.assert_not_called()
                after = {
                    path: (
                        path.lstat().st_dev,
                        path.lstat().st_ino,
                        stat.S_IMODE(path.lstat().st_mode),
                    )
                    for path in (self.fence, self.writer)
                }
                self.assertEqual(after, before)

                if fixture == "fresh":
                    self.fence.rmdir()
                    self.writer.rmdir()


if __name__ == "__main__":
    unittest.main()
