#!/usr/bin/env python3
"""Safely prepare or verify the dedicated LightRAG shared bind mounts.

The initializer intentionally knows only the two canonical mount points and a
small, fixed layout.  It validates the complete trees without mutation, freezes
every managed directory, and copies mutable regular files into private
replacement inodes. Existing lock inodes remain stable after their one-time
creation, and no pre-existing regular inode is ever chowned or chmodded.
"""

from __future__ import annotations

import contextlib
import fcntl
import os
import re
import stat
import sys
from typing import NamedTuple, Sequence


FENCE_ROOT = "/rebuild-fence"
WRITER_EVIDENCE_ROOT = "/writer-evidence"
LIGHTRAG_UID = 1000
LIGHTRAG_GID = 1000
MAX_ID = 2_147_483_647
SHARED_DIRECTORY_MODE = 0o2750
REGULAR_MODE = 0o0640
FROZEN_ROOT_MODE = 0o0700
SEALED_ROOT_MODE = 0o0000
PRIVATE_TEMP_MODE = 0o0600
MAX_REGULAR_BYTES = 2 * 1024 * 1024
MAX_REGULAR_FILES = 4096
MAX_TREE_BYTES = 64 * 1024 * 1024
COPY_CHUNK_BYTES = 64 * 1024

_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
_IDENTIFIER_JSON = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.json$")
_PRIVATE_TEMP = re.compile(
    r"^\.prepare-shared-fence\.([A-Za-z0-9][A-Za-z0-9._-]{0,127}\.json|serving\.lock|rebuild\.lock|current\.json)\.tmp$"
)
_FENCE_REGULARS = frozenset({"serving.lock", "rebuild.lock", "current.json"})
_FENCE_DIRECTORIES = frozenset({"reports", "attempts"})
_LOCK_NAMES = ("serving.lock", "rebuild.lock")
_ERROR_MESSAGES = {
    "invalid_arguments": "prepare_shared_fence: invalid_arguments",
    "invalid_uid": "prepare_shared_fence: invalid_uid",
    "invalid_gid": "prepare_shared_fence: invalid_gid",
    "invalid_attempt": "prepare_shared_fence: invalid_attempt",
    "root_required": "prepare_shared_fence: root_required",
    "unsafe_layout": "prepare_shared_fence: unsafe_layout",
    "verification_failed": "prepare_shared_fence: verification_failed",
    "mutation_failed": "prepare_shared_fence: mutation_failed",
    "internal_error": "prepare_shared_fence: internal_error",
}

_CLOSE_ON_EXEC = os.O_CLOEXEC
_NO_FOLLOW = os.O_NOFOLLOW
_DIRECTORY_FLAGS = os.O_RDONLY | os.O_DIRECTORY | _NO_FOLLOW | _CLOSE_ON_EXEC
_REGULAR_FLAGS = os.O_RDONLY | os.O_NONBLOCK | _NO_FOLLOW | _CLOSE_ON_EXEC
_CREATE_REGULAR_FLAGS = (
    os.O_RDWR
    | os.O_CREAT
    | os.O_EXCL
    | os.O_NONBLOCK
    | _NO_FOLLOW
    | _CLOSE_ON_EXEC
)


class PreparationError(Exception):
    """A fixed-code failure that is safe to return to an operator."""

    def __init__(self, code: str):
        if code not in _ERROR_MESSAGES:
            raise ValueError("invalid preparation error code")
        super().__init__(code)
        self.code = code


class _OpenedNode(NamedTuple):
    descriptor: int
    mode: int
    device: int
    inode: int
    directory: bool
    size: int = 0
    mtime_ns: int = 0
    ctime_ns: int = 0


class _DirectorySnapshot(NamedTuple):
    directory: _OpenedNode
    children: tuple[tuple[str, _OpenedNode], ...]


class _RecoveryEntry(NamedTuple):
    parent: _OpenedNode
    name: str
    target: str
    node: _OpenedNode


class _RegularEntry(NamedTuple):
    parent: _OpenedNode
    name: str
    node: _OpenedNode | None
    owner_uid: int
    group_gid: int


def _close_descriptor(descriptor: int) -> None:
    """Close an owned descriptor without turning cleanup into a failed commit."""

    try:
        os.close(descriptor)
    except OSError:
        pass


def _parse_numeric_id(value: str, *, allow_zero: bool, code: str) -> int:
    if (
        not isinstance(value, str)
        or not value
        or len(value) > 10
        or not value.isascii()
        or not value.isdecimal()
    ):
        raise PreparationError(code)
    numeric_id = int(value, 10)
    if numeric_id > MAX_ID or (numeric_id == 0 and not allow_zero):
        raise PreparationError(code)
    return numeric_id


def parse_operator_uid(value: str) -> int:
    """Parse a numeric host UID; root is a valid host owner."""

    return _parse_numeric_id(value, allow_zero=True, code="invalid_uid")


def parse_shared_gid(value: str) -> int:
    """Parse the deliberately narrow numeric shared-GID contract."""

    return _parse_numeric_id(value, allow_zero=False, code="invalid_gid")


def parse_expected_attempt(value: str) -> str:
    """Parse the same bounded identifier used by fence attempt files."""

    if not isinstance(value, str) or _IDENTIFIER.fullmatch(value) is None:
        raise PreparationError("invalid_attempt")
    return value


def _require_operator_uid(operator_uid: int) -> None:
    if (
        isinstance(operator_uid, bool)
        or not isinstance(operator_uid, int)
        or operator_uid < 0
        or operator_uid > MAX_ID
    ):
        raise PreparationError("invalid_uid")


def _require_shared_gid(shared_gid: int) -> None:
    if (
        isinstance(shared_gid, bool)
        or not isinstance(shared_gid, int)
        or shared_gid <= 0
        or shared_gid > MAX_ID
    ):
        raise PreparationError("invalid_gid")


def _remember_descriptor(
    stack: contextlib.ExitStack,
    descriptor: int,
    mode: int,
    node_status: os.stat_result,
    *,
    directory: bool,
) -> _OpenedNode:
    stack.callback(_close_descriptor, descriptor)
    return _OpenedNode(
        descriptor=descriptor,
        mode=mode,
        device=node_status.st_dev,
        inode=node_status.st_ino,
        directory=directory,
        size=0 if directory else node_status.st_size,
        mtime_ns=0 if directory else node_status.st_mtime_ns,
        ctime_ns=0 if directory else node_status.st_ctime_ns,
    )


def _require_node_type(
    node_status: os.stat_result,
    *,
    directory: bool,
    require_single_link: bool = True,
) -> None:
    expected_type = stat.S_ISDIR if directory else stat.S_ISREG
    if not expected_type(node_status.st_mode):
        raise PreparationError("unsafe_layout")
    # A multiply-linked regular inode can extend fchown/fchmod outside these
    # two dedicated namespaces, so it is not accepted as a scoped file.
    if not directory and require_single_link and node_status.st_nlink != 1:
        raise PreparationError("unsafe_layout")


def _open_root(
    stack: contextlib.ExitStack, path: str, mode: int
) -> _OpenedNode:
    try:
        descriptor = os.open(path, _DIRECTORY_FLAGS)
        node_status = os.fstat(descriptor)
    except OSError:
        try:
            os.close(descriptor)
        except (OSError, UnboundLocalError):
            pass
        raise PreparationError("unsafe_layout") from None
    try:
        _require_node_type(node_status, directory=True)
    except PreparationError:
        os.close(descriptor)
        raise
    return _remember_descriptor(
        stack, descriptor, mode, node_status, directory=True
    )


def _open_child(
    stack: contextlib.ExitStack,
    parent: int,
    name: str,
    *,
    directory: bool,
    allow_linked_regular: bool = False,
) -> _OpenedNode:
    flags = _DIRECTORY_FLAGS if directory else _REGULAR_FLAGS
    try:
        descriptor = os.open(name, flags, dir_fd=parent)
        node_status = os.fstat(descriptor)
    except OSError:
        try:
            os.close(descriptor)
        except (OSError, UnboundLocalError):
            pass
        raise PreparationError("unsafe_layout") from None

    try:
        _require_node_type(
            node_status,
            directory=directory,
            require_single_link=not allow_linked_regular,
        )
    except PreparationError:
        os.close(descriptor)
        raise
    mode = SHARED_DIRECTORY_MODE if directory else REGULAR_MODE
    return _remember_descriptor(
        stack, descriptor, mode, node_status, directory=directory
    )


def _list_directory(descriptor: int) -> tuple[str, ...]:
    try:
        entries = os.listdir(descriptor)
    except OSError:
        raise PreparationError("unsafe_layout") from None
    if any(not isinstance(entry, str) for entry in entries):
        raise PreparationError("unsafe_layout")
    return tuple(sorted(entries))


def _private_temp_name(target: str) -> str:
    return ".prepare-shared-fence." + target + ".tmp"


def _private_temp_target(name: str) -> str | None:
    matched = _PRIVATE_TEMP.fullmatch(name)
    return matched.group(1) if matched is not None else None


def _consume_file_budget(names: Sequence[str], budget: list[int]) -> None:
    if len(names) > budget[0]:
        raise PreparationError("unsafe_layout")
    budget[0] -= len(names)


def _validate_identifier_directory(
    stack: contextlib.ExitStack,
    directory: _OpenedNode,
    *,
    allow_private_temps: bool = False,
    allow_linked_regulars: bool = False,
    budget: list[int] | None = None,
) -> tuple[list[_OpenedNode], _DirectorySnapshot]:
    names = _list_directory(directory.descriptor)
    if budget is not None:
        _consume_file_budget(names, budget)
    if any(
        _IDENTIFIER_JSON.fullmatch(name) is None
        and not (
            allow_private_temps
            and (target := _private_temp_target(name)) is not None
            and _IDENTIFIER_JSON.fullmatch(target) is not None
        )
        for name in names
    ):
        raise PreparationError("unsafe_layout")
    children = tuple(
        (
            name,
            _open_child(
                stack,
                directory.descriptor,
                name,
                directory=False,
                allow_linked_regular=allow_linked_regulars,
            ),
        )
        for name in names
    )
    return (
        [node for _, node in children],
        _DirectorySnapshot(directory=directory, children=children),
    )


def _open_fence_root_entries(
    stack: contextlib.ExitStack,
    root: _OpenedNode,
    *,
    allow_private_temps: bool = False,
    allow_linked_regulars: bool = False,
    budget: list[int] | None = None,
) -> tuple[list[_OpenedNode], _DirectorySnapshot]:
    names = _list_directory(root.descriptor)
    if any(
        name not in _FENCE_REGULARS
        and name not in _FENCE_DIRECTORIES
        and not (
            allow_private_temps
            and (target := _private_temp_target(name)) is not None
            and target in _FENCE_REGULARS
        )
        for name in names
    ):
        raise PreparationError("unsafe_layout")
    if budget is not None:
        _consume_file_budget(
            [name for name in names if name not in _FENCE_DIRECTORIES], budget
        )

    nodes: list[_OpenedNode] = []
    root_children: list[tuple[str, _OpenedNode]] = []
    for name in names:
        node = _open_child(
            stack,
            root.descriptor,
            name,
            directory=name in _FENCE_DIRECTORIES,
            allow_linked_regular=allow_linked_regulars,
        )
        nodes.append(node)
        root_children.append((name, node))
    return nodes, _DirectorySnapshot(
        directory=root, children=tuple(root_children)
    )


def _validate_fence(
    stack: contextlib.ExitStack,
    root: _OpenedNode,
    *,
    allow_private_temps: bool = False,
    allow_linked_regulars: bool = False,
    budget: list[int] | None = None,
) -> tuple[list[_OpenedNode], list[_DirectorySnapshot]]:
    nodes, root_snapshot = _open_fence_root_entries(
        stack,
        root,
        allow_private_temps=allow_private_temps,
        allow_linked_regulars=allow_linked_regulars,
        budget=budget,
    )
    child_snapshots: list[_DirectorySnapshot] = []
    for name, directory in root_snapshot.children:
        if name not in _FENCE_DIRECTORIES:
            continue
        child_nodes, child_snapshot = _validate_identifier_directory(
            stack,
            directory,
            allow_private_temps=allow_private_temps,
            allow_linked_regulars=allow_linked_regulars,
            budget=budget,
        )
        nodes.extend(child_nodes)
        child_snapshots.append(child_snapshot)
    return nodes, [root_snapshot, *child_snapshots]


def _create_missing_fence_directories(
    root: _OpenedNode, present_names: frozenset[str]
) -> None:
    created = False
    for name in sorted(_FENCE_DIRECTORIES - present_names):
        try:
            # mkdir is an atomic, exclusive create.  The subsequent validation
            # opens the result relative to root with O_NOFOLLOW.
            os.mkdir(name, FROZEN_ROOT_MODE, dir_fd=root.descriptor)
            created = True
        except FileExistsError:
            raise PreparationError("unsafe_layout") from None
    if created:
        os.fsync(root.descriptor)


def _read_bounded_regular(
    node: _OpenedNode,
    remaining: list[int],
    *,
    require_snapshot_metadata: bool = True,
) -> bytes:
    try:
        node_status = os.fstat(node.descriptor)
        _require_node_type(
            node_status, directory=False, require_single_link=False
        )
        if (
            (node_status.st_dev, node_status.st_ino)
            != (node.device, node.inode)
            or node_status.st_size < 0
            or node_status.st_size > MAX_REGULAR_BYTES
            or (
                require_snapshot_metadata
                and (
                    node_status.st_size != node.size
                    or node_status.st_mtime_ns != node.mtime_ns
                    or node_status.st_ctime_ns != node.ctime_ns
                )
            )
        ):
            raise PreparationError("unsafe_layout")
        if node_status.st_size > remaining[0]:
            raise PreparationError("unsafe_layout")
        os.lseek(node.descriptor, 0, os.SEEK_SET)
        chunks: list[bytes] = []
        copied = 0
        while copied <= MAX_REGULAR_BYTES:
            chunk = os.read(
                node.descriptor,
                min(COPY_CHUNK_BYTES, MAX_REGULAR_BYTES + 1 - copied),
            )
            if not chunk:
                break
            chunks.append(chunk)
            copied += len(chunk)
        if copied > MAX_REGULAR_BYTES or copied != node_status.st_size:
            raise PreparationError("unsafe_layout")
        final_status = os.fstat(node.descriptor)
        _require_node_type(
            final_status, directory=False, require_single_link=False
        )
        if (
            (final_status.st_dev, final_status.st_ino)
            != (node.device, node.inode)
            or final_status.st_size != node_status.st_size
            or final_status.st_size != copied
            or final_status.st_mtime_ns != node_status.st_mtime_ns
            or final_status.st_ctime_ns != node_status.st_ctime_ns
        ):
            raise PreparationError("unsafe_layout")
        remaining[0] -= copied
        return b"".join(chunks)
    except PreparationError:
        raise
    except OSError:
        raise PreparationError("unsafe_layout") from None


def _validate_regular_budgets(
    entries: Sequence[_RegularEntry],
    recovery_entries: Sequence[_RecoveryEntry] = (),
) -> list[int]:
    existing_count = (
        sum(entry.node is not None for entry in entries)
        + len(recovery_entries)
    )
    missing_count = sum(entry.node is None for entry in entries)
    copies_existing = any(
        entry.node is not None and entry.name not in _LOCK_NAMES
        for entry in entries
    )
    copy_peak_count = len(entries) + (1 if copies_existing else 0)
    if max(existing_count, copy_peak_count) > MAX_REGULAR_FILES:
        raise PreparationError("unsafe_layout")
    total = 0
    nodes = [entry.node for entry in entries if entry.node is not None]
    nodes.extend(entry.node for entry in recovery_entries)
    for node in nodes:
        if node is None:
            continue
        try:
            node_status = os.fstat(node.descriptor)
        except OSError:
            raise PreparationError("unsafe_layout") from None
        _require_node_type(node_status, directory=False)
        if (
            (node_status.st_dev, node_status.st_ino)
            != (node.device, node.inode)
            or node_status.st_size != node.size
            or node_status.st_mtime_ns != node.mtime_ns
            or node_status.st_ctime_ns != node.ctime_ns
            or node.size < 0
            or node.size > MAX_REGULAR_BYTES
        ):
            raise PreparationError("unsafe_layout")
        total += node.size
        if total > MAX_TREE_BYTES:
            raise PreparationError("unsafe_layout")
    return [MAX_TREE_BYTES]


def _verify_target_before_replace(
    parent: _OpenedNode, target: str, expected: _OpenedNode | None
) -> None:
    if expected is not None:
        try:
            _verify_binding(
                parent.descriptor,
                target,
                expected,
                allow_linked_regular=True,
            )
        except PreparationError:
            raise PreparationError("mutation_failed") from None
        return
    try:
        os.stat(target, dir_fd=parent.descriptor, follow_symlinks=False)
    except FileNotFoundError:
        return
    except OSError:
        raise PreparationError("mutation_failed") from None
    raise PreparationError("mutation_failed")


def _create_private_regular(
    parent: _OpenedNode,
    target: str,
    content: bytes,
    owner_uid: int,
    group_gid: int,
    expected: _OpenedNode | None,
) -> None:
    temporary = _private_temp_name(target)
    descriptor: int | None = None
    created = False
    try:
        descriptor = os.open(
            temporary,
            _CREATE_REGULAR_FLAGS,
            PRIVATE_TEMP_MODE,
            dir_fd=parent.descriptor,
        )
        created = True
        node_status = os.fstat(descriptor)
        _require_node_type(node_status, directory=False)
        if node_status.st_dev != parent.device:
            raise PreparationError("mutation_failed")
        identity = (node_status.st_dev, node_status.st_ino)
        view = memoryview(content)
        written = 0
        while written < len(view):
            count = os.write(descriptor, view[written:])
            if count <= 0:
                raise PreparationError("mutation_failed")
            written += count
        os.fchown(descriptor, owner_uid, group_gid)
        os.fchmod(descriptor, REGULAR_MODE)
        os.fsync(descriptor)
        final_status = os.fstat(descriptor)
        _require_node_type(final_status, directory=False)
        if (
            (final_status.st_dev, final_status.st_ino) != identity
            or final_status.st_size != len(content)
        ):
            raise PreparationError("mutation_failed")
        _verify_final_attributes(
            [
                _OpenedNode(
                    descriptor,
                    REGULAR_MODE,
                    final_status.st_dev,
                    final_status.st_ino,
                    False,
                )
            ],
            owner_uid,
            group_gid,
        )
        _verify_target_before_replace(parent, target, expected)
        os.replace(
            temporary,
            target,
            src_dir_fd=parent.descriptor,
            dst_dir_fd=parent.descriptor,
        )
        try:
            _verify_binding(
                parent.descriptor,
                target,
                _OpenedNode(
                    descriptor,
                    REGULAR_MODE,
                    final_status.st_dev,
                    final_status.st_ino,
                    False,
                ),
            )
        except PreparationError:
            raise PreparationError("mutation_failed") from None
        os.fsync(parent.descriptor)
    except PreparationError:
        raise
    except OSError:
        raise PreparationError("mutation_failed") from None
    finally:
        if descriptor is not None:
            try:
                os.close(descriptor)
            except OSError:
                pass
        cleaned = False
        if created:
            try:
                os.unlink(temporary, dir_fd=parent.descriptor)
                cleaned = True
            except FileNotFoundError:
                pass
            except OSError:
                pass
        if cleaned:
            try:
                os.fsync(parent.descriptor)
            except OSError:
                pass


def _collect_regular_entries(
    fence_snapshots: Sequence[_DirectorySnapshot],
    writer_snapshot: _DirectorySnapshot,
    operator_uid: int,
    shared_gid: int,
) -> list[_RegularEntry]:
    entries: list[_RegularEntry] = []
    for snapshot, owner_uid, group_gid in (
        *(
            (snapshot, LIGHTRAG_UID, shared_gid)
            for snapshot in fence_snapshots
        ),
        (writer_snapshot, operator_uid, LIGHTRAG_GID),
    ):
        for name, child in snapshot.children:
            if not child.directory and _private_temp_target(name) is None:
                entries.append(
                    _RegularEntry(
                        snapshot.directory,
                        name,
                        child,
                        owner_uid,
                        group_gid,
                    )
                )
    fence_names = {
        entry.name
        for entry in entries
        if entry.parent == fence_snapshots[0].directory
    }
    for name in sorted(set(_LOCK_NAMES) - fence_names):
        entries.append(
            _RegularEntry(
                fence_snapshots[0].directory,
                name,
                None,
                LIGHTRAG_UID,
                shared_gid,
            )
        )
    return entries


def _directory_is_frozen(directory: _OpenedNode) -> bool:
    try:
        node_status = os.fstat(directory.descriptor)
    except OSError:
        raise PreparationError("unsafe_layout") from None
    return (
        stat.S_ISDIR(node_status.st_mode)
        and node_status.st_uid == 0
        and node_status.st_gid == 0
        and stat.S_IMODE(node_status.st_mode) == FROZEN_ROOT_MODE
    )


def _recovery_entries(
    snapshots: Sequence[_DirectorySnapshot],
) -> list[_RecoveryEntry]:
    entries: list[_RecoveryEntry] = []
    for snapshot in snapshots:
        for name, node in snapshot.children:
            target = _private_temp_target(name)
            if target is not None:
                entries.append(
                    _RecoveryEntry(snapshot.directory, name, target, node)
                )
    return entries


def _remove_recovery_entries(entries: Sequence[_RecoveryEntry]) -> None:
    synced: set[int] = set()
    for entry in entries:
        try:
            os.unlink(entry.name, dir_fd=entry.parent.descriptor)
        except OSError:
            raise PreparationError("mutation_failed") from None
        synced.add(entry.parent.descriptor)
    for descriptor in synced:
        try:
            os.fsync(descriptor)
        except OSError:
            raise PreparationError("mutation_failed") from None


def _acquire_locks(
    fence_snapshot: _DirectorySnapshot,
    *,
    require_all: bool,
    failure_code: str,
    names: Sequence[str] = _LOCK_NAMES,
) -> list[int]:
    children = dict(fence_snapshot.children)
    if require_all and any(name not in children for name in names):
        raise PreparationError(failure_code)
    locked: list[int] = []
    try:
        for name in names:
            node = children.get(name)
            if node is None:
                continue
            fcntl.flock(node.descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            locked.append(node.descriptor)
    except (BlockingIOError, OSError):
        for descriptor in reversed(locked):
            try:
                fcntl.flock(descriptor, fcntl.LOCK_UN)
            except OSError:
                pass
        raise PreparationError(failure_code) from None
    return locked


def _acquire_initializer_locks(roots: Sequence[_OpenedNode]) -> list[int]:
    """Exclude competing ownership helpers on stable directory inodes."""

    locked: list[int] = []
    try:
        for root in roots:
            fcntl.flock(root.descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            locked.append(root.descriptor)
    except (BlockingIOError, OSError):
        _release_initial_locks(locked)
        raise PreparationError("unsafe_layout") from None
    return locked


def _require_stable_lock_attributes(
    fence_snapshot: _DirectorySnapshot, shared_gid: int
) -> tuple[str, ...]:
    children = dict(fence_snapshot.children)
    missing: list[str] = []
    for name in _LOCK_NAMES:
        node = children.get(name)
        if node is None:
            missing.append(name)
            continue
        try:
            node_status = os.fstat(node.descriptor)
        except OSError:
            raise PreparationError("unsafe_layout") from None
        try:
            _require_node_type(node_status, directory=False)
        except PreparationError:
            raise PreparationError("unsafe_layout") from None
        if (
            (node_status.st_dev, node_status.st_ino)
            != (node.device, node.inode)
            or node_status.st_uid != LIGHTRAG_UID
            or node_status.st_gid != shared_gid
            or stat.S_IMODE(node_status.st_mode) != REGULAR_MODE
        ):
            raise PreparationError("unsafe_layout")
    return tuple(missing)


def _release_initial_locks(descriptors: Sequence[int]) -> None:
    for descriptor in reversed(descriptors):
        try:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
        except OSError:
            pass


def _verify_open_node(
    node: _OpenedNode, *, allow_linked_regular: bool = False
) -> None:
    try:
        node_status = os.fstat(node.descriptor)
    except OSError:
        raise PreparationError("unsafe_layout") from None
    _require_node_type(
        node_status,
        directory=node.directory,
        require_single_link=not allow_linked_regular,
    )
    if (node_status.st_dev, node_status.st_ino) != (node.device, node.inode):
        raise PreparationError("unsafe_layout")


def _verify_binding(
    parent: int,
    name: str,
    expected: _OpenedNode,
    *,
    allow_linked_regular: bool = False,
) -> None:
    flags = _DIRECTORY_FLAGS if expected.directory else _REGULAR_FLAGS
    try:
        descriptor = os.open(name, flags, dir_fd=parent)
        node_status = os.fstat(descriptor)
    except OSError:
        try:
            os.close(descriptor)
        except (OSError, UnboundLocalError):
            pass
        raise PreparationError("unsafe_layout") from None
    try:
        _require_node_type(
            node_status,
            directory=expected.directory,
            require_single_link=not allow_linked_regular,
        )
        if (node_status.st_dev, node_status.st_ino) != (
            expected.device,
            expected.inode,
        ):
            raise PreparationError("unsafe_layout")
    finally:
        os.close(descriptor)


def _verify_root_binding(path: str, expected: _OpenedNode) -> None:
    try:
        descriptor = os.open(path, _DIRECTORY_FLAGS)
        node_status = os.fstat(descriptor)
    except OSError:
        try:
            os.close(descriptor)
        except (OSError, UnboundLocalError):
            pass
        raise PreparationError("unsafe_layout") from None
    try:
        _require_node_type(node_status, directory=True)
        if (node_status.st_dev, node_status.st_ino) != (
            expected.device,
            expected.inode,
        ):
            raise PreparationError("unsafe_layout")
    finally:
        os.close(descriptor)


def _verify_snapshots(
    roots: tuple[tuple[str, _OpenedNode], ...],
    directories: list[_DirectorySnapshot],
    *,
    allow_linked_regulars: bool = False,
) -> None:
    _verify_tree_topology(directories)
    for path, root in roots:
        _verify_open_node(root)
        _verify_root_binding(path, root)
    for snapshot in directories:
        _verify_open_node(snapshot.directory)
        names = _list_directory(snapshot.directory.descriptor)
        if names != tuple(name for name, _ in snapshot.children):
            raise PreparationError("unsafe_layout")
        for name, child in snapshot.children:
            _verify_open_node(
                child, allow_linked_regular=allow_linked_regulars
            )
            _verify_binding(
                snapshot.directory.descriptor,
                name,
                child,
                allow_linked_regular=allow_linked_regulars,
            )


def _verify_tree_topology(directories: Sequence[_DirectorySnapshot]) -> None:
    identities: dict[tuple[int, int], int] = {}

    def register(node: _OpenedNode) -> None:
        identity = (node.device, node.inode)
        previous = identities.get(identity)
        if previous is not None and previous != node.descriptor:
            raise PreparationError("unsafe_layout")
        identities[identity] = node.descriptor

    for snapshot in directories:
        register(snapshot.directory)
        for _, child in snapshot.children:
            if child.device != snapshot.directory.device:
                raise PreparationError("unsafe_layout")
            register(child)


def _converge_directories(
    directories: Sequence[_OpenedNode], owner_uid: int, group_gid: int
) -> None:
    for directory in directories:
        if not directory.directory:
            raise PreparationError("internal_error")
        os.fchown(directory.descriptor, owner_uid, group_gid)
        # chown may clear set-ID bits, so mode convergence comes last.
        os.fchmod(directory.descriptor, directory.mode)
        _verify_final_attributes([directory], owner_uid, group_gid)
        os.fsync(directory.descriptor)


def _verify_final_attributes(
    nodes: Sequence[_OpenedNode], owner_uid: int, group_gid: int
) -> None:
    _verify_exact_attributes(
        nodes, owner_uid, group_gid, failure_code="mutation_failed"
    )


def _verify_exact_attributes(
    nodes: Sequence[_OpenedNode],
    owner_uid: int,
    group_gid: int,
    *,
    failure_code: str,
) -> None:
    for node in nodes:
        try:
            node_status = os.fstat(node.descriptor)
        except OSError:
            raise PreparationError(failure_code) from None
        try:
            _require_node_type(node_status, directory=node.directory)
        except PreparationError:
            raise PreparationError(failure_code) from None
        if (
            node_status.st_uid != owner_uid
            or node_status.st_gid != group_gid
            or stat.S_IMODE(node_status.st_mode) != node.mode
        ):
            raise PreparationError(failure_code)


def _require_complete_verify_layout(
    snapshots: Sequence[_DirectorySnapshot],
    writer_snapshot: _DirectorySnapshot,
    expected_attempt: str | None,
) -> None:
    fence_root_snapshot = snapshots[0]
    fence_children = dict(fence_root_snapshot.children)
    required_fence_names = _FENCE_DIRECTORIES | {
        "serving.lock",
        "rebuild.lock",
    }
    if not required_fence_names.issubset(fence_children):
        raise PreparationError("unsafe_layout")
    if expected_attempt is None:
        return

    expected_name = expected_attempt + ".json"
    if "current.json" not in fence_children:
        raise PreparationError("unsafe_layout")
    directory_snapshots = {
        (snapshot.directory.device, snapshot.directory.inode): snapshot
        for snapshot in snapshots[1:]
    }
    for directory_name in _FENCE_DIRECTORIES:
        directory = fence_children[directory_name]
        snapshot = directory_snapshots.get((directory.device, directory.inode))
        if snapshot is None or expected_name not in dict(snapshot.children):
            raise PreparationError("unsafe_layout")
    if expected_name not in dict(writer_snapshot.children):
        raise PreparationError("unsafe_layout")


def _freeze_directories(directories: Sequence[_OpenedNode]) -> None:
    for directory in directories:
        if not directory.directory:
            raise PreparationError("internal_error")
        # Seal traversal before changing ownership. If any later syscall has an
        # ambiguous or failed outcome, this directory is either still in its
        # previously valid state or already inaccessible to non-root callers;
        # it can never be left root-owned with the old shared mode.
        os.fchmod(directory.descriptor, SEALED_ROOT_MODE)
        os.fchown(directory.descriptor, 0, 0)
        os.fchmod(directory.descriptor, FROZEN_ROOT_MODE)
        node_status = os.fstat(directory.descriptor)
        if (
            not stat.S_ISDIR(node_status.st_mode)
            or node_status.st_uid != 0
            or node_status.st_gid != 0
            or stat.S_IMODE(node_status.st_mode) != FROZEN_ROOT_MODE
        ):
            raise PreparationError("mutation_failed")
        os.fsync(directory.descriptor)


def _attempt_reseal_directories(
    directories: Sequence[_OpenedNode],
) -> bool:
    """Best-effort recovery hygiene; the fence root remains the safety gate.

    A failed recovery must never make a final-owner directory traversable.  Mode
    zero is therefore attempted before ownership changes, and 0700 is restored
    only after the descriptor proves that root owns the directory.  The caller
    deliberately does not rely on this routine for fail-closed behavior.
    """

    resealed = True
    for directory in directories:
        try:
            os.fchmod(directory.descriptor, SEALED_ROOT_MODE)
        except OSError:
            resealed = False
        try:
            os.fchown(directory.descriptor, 0, 0)
        except OSError:
            resealed = False
        try:
            node_status = os.fstat(directory.descriptor)
            root_owned = (
                stat.S_ISDIR(node_status.st_mode)
                and node_status.st_uid == 0
                and node_status.st_gid == 0
            )
        except OSError:
            root_owned = False
            resealed = False
        if root_owned:
            try:
                os.fchmod(directory.descriptor, FROZEN_ROOT_MODE)
            except OSError:
                resealed = False
        else:
            resealed = False
        try:
            os.fsync(directory.descriptor)
        except OSError:
            resealed = False
    return resealed


def _stage_root_for_publish(
    root: _OpenedNode, owner_uid: int, group_gid: int
) -> None:
    """Persist final ownership while the root remains non-traversable."""

    if not root.directory:
        raise PreparationError("internal_error")
    node_status = os.fstat(root.descriptor)
    if (
        not stat.S_ISDIR(node_status.st_mode)
        or node_status.st_uid != 0
        or node_status.st_gid != 0
        or stat.S_IMODE(node_status.st_mode) != FROZEN_ROOT_MODE
    ):
        raise PreparationError("mutation_failed")
    os.fchmod(root.descriptor, SEALED_ROOT_MODE)
    os.fchown(root.descriptor, owner_uid, group_gid)
    try:
        node_status = os.fstat(root.descriptor)
    except OSError:
        raise PreparationError("mutation_failed") from None
    if (
        not stat.S_ISDIR(node_status.st_mode)
        or node_status.st_uid != owner_uid
        or node_status.st_gid != group_gid
        or stat.S_IMODE(node_status.st_mode) != SEALED_ROOT_MODE
    ):
        raise PreparationError("mutation_failed")
    os.fsync(root.descriptor)


def _publish_root(root: _OpenedNode) -> None:
    """Atomically make one fully staged root traversable."""

    if not root.directory:
        raise PreparationError("internal_error")
    # This is intentionally the only syscall in the publish operation.  All
    # validation and durable staging happen while mode is 0000.  A successful
    # fchmod is the commit point; a failed one leaves the root sealed.
    os.fchmod(root.descriptor, root.mode)


def prepare_shared_fence(operator_uid: int, shared_gid: int) -> None:
    """Freeze, validate, and converge both fixed mount trees."""

    _require_operator_uid(operator_uid)
    _require_shared_gid(shared_gid)
    if os.geteuid() != 0:
        raise PreparationError("root_required")

    phase = "validation"
    try:
        with contextlib.ExitStack() as stack:
            protected_directories: list[_OpenedNode] = []
            initializer_locks: list[int] = []
            initial_locks: list[int] = []
            replacement_locks: list[int] = []
            mutation_started = False
            final_gate_commit_started = False
            try:
                fence_root = _open_root(
                    stack, FENCE_ROOT, SHARED_DIRECTORY_MODE
                )
                writer_root = _open_root(
                    stack, WRITER_EVIDENCE_ROOT, SHARED_DIRECTORY_MODE
                )
                roots = (
                    (FENCE_ROOT, fence_root),
                    (WRITER_EVIDENCE_ROOT, writer_root),
                )
                initializer_locks = _acquire_initializer_locks(
                    [fence_root, writer_root]
                )
                file_budget = [MAX_REGULAR_FILES]
                _, initial_snapshots = _validate_fence(
                    stack,
                    fence_root,
                    allow_private_temps=True,
                    budget=file_budget,
                )
                _, initial_writer_snapshot = _validate_identifier_directory(
                    stack,
                    writer_root,
                    allow_private_temps=True,
                    budget=file_budget,
                )
                initial_snapshots.append(initial_writer_snapshot)
                # Complete the allowlist, type, link-count, device and inode-alias
                # validation before the first ownership or mode mutation.
                _verify_snapshots(roots, initial_snapshots)

                initial_root_snapshot = initial_snapshots[0]
                missing_lock_names = _require_stable_lock_attributes(
                    initial_root_snapshot, shared_gid
                )
                existing_fence_directories = [
                    node
                    for name, node in initial_root_snapshot.children
                    if name in _FENCE_DIRECTORIES
                ]
                protected_directories.extend(
                    [fence_root, writer_root, *existing_fence_directories]
                )
                recovery_entries = _recovery_entries(initial_snapshots)
                if recovery_entries and not all(
                    _directory_is_frozen(directory)
                    for directory in protected_directories
                ):
                    raise PreparationError("unsafe_layout")
                present_fence_names = frozenset(
                    name
                    for name, _ in initial_root_snapshot.children
                    if _private_temp_target(name) is None
                )
                initial_regular_entries = _collect_regular_entries(
                    initial_snapshots[:-1],
                    initial_writer_snapshot,
                    operator_uid,
                    shared_gid,
                )
                _validate_regular_budgets(
                    initial_regular_entries, recovery_entries
                )
                initial_lock_identities = {
                    (entry.node.device, entry.node.inode)
                    for entry in initial_regular_entries
                    if entry.name in _LOCK_NAMES and entry.node is not None
                }

                initial_locks = _acquire_locks(
                    initial_root_snapshot,
                    require_all=False,
                    failure_code="unsafe_layout",
                )
                # This is the last zero-mutation gate.  It catches namespace,
                # hard-link and metadata changes that happened while acquiring
                # the two existing lock inodes.  A race after this point is
                # contained by copy-up: no old regular inode is ever chowned or
                # chmodded.
                _verify_snapshots(roots, initial_snapshots)
                remaining_bytes = _validate_regular_budgets(
                    initial_regular_entries, recovery_entries
                )
                copied_contents = [
                    (
                        entry,
                        b""
                        if entry.node is None
                        else _read_bounded_regular(entry.node, remaining_bytes),
                    )
                    for entry in initial_regular_entries
                ]
                _verify_snapshots(roots, initial_snapshots)
                _validate_regular_budgets(
                    initial_regular_entries, recovery_entries
                )
                phase = "mutation"
                # The fence root is the durable lifecycle gate. Freeze it
                # before any child namespace mutation, then freeze the other
                # validated directories while the gate remains closed.
                mutation_started = True
                _freeze_directories(protected_directories)

                phase = "validation"
                # A pre-held descriptor can gain another link outside these
                # frozen trees.  The old inode is copied but never chowned or
                # chmodded, so link-count growth is safe here.
                _verify_snapshots(
                    roots, initial_snapshots, allow_linked_regulars=True
                )
                initial_linked_identities = {
                    (entry.node.device, entry.node.inode)
                    for entry in initial_regular_entries
                    if entry.node is not None
                    and os.fstat(entry.node.descriptor).st_nlink != 1
                }
                if initial_lock_identities & initial_linked_identities:
                    raise PreparationError("unsafe_layout")
                post_freeze_remaining = [MAX_TREE_BYTES]
                for entry, captured_content in copied_contents:
                    if entry.node is None:
                        continue
                    observed_content = _read_bounded_regular(
                        entry.node,
                        post_freeze_remaining,
                        require_snapshot_metadata=False,
                    )
                    if observed_content != captured_content:
                        raise PreparationError("unsafe_layout")
                # The second byte-for-byte read catches same-size writes even
                # on filesystems whose timestamps are too coarse. A write after
                # this point can affect only an old inode that copy-up replaces.
                _verify_snapshots(
                    roots, initial_snapshots, allow_linked_regulars=True
                )

                phase = "mutation"
                _remove_recovery_entries(recovery_entries)
                _create_missing_fence_directories(
                    fence_root, present_fence_names
                )

                phase = "validation"
                file_budget = [MAX_REGULAR_FILES]
                fence_nodes, snapshots = _validate_fence(
                    stack,
                    fence_root,
                    allow_linked_regulars=True,
                    budget=file_budget,
                )
                writer_nodes, writer_snapshot = _validate_identifier_directory(
                    stack,
                    writer_root,
                    allow_linked_regulars=True,
                    budget=file_budget,
                )
                snapshots.append(writer_snapshot)
                _verify_snapshots(
                    roots, snapshots, allow_linked_regulars=True
                )
                fence_directories = [
                    node for node in fence_nodes if node.directory
                ]
                protected_identities = {
                    (node.device, node.inode) for node in protected_directories
                }
                for directory in fence_directories:
                    identity = (directory.device, directory.inode)
                    if identity not in protected_identities:
                        protected_directories.append(directory)
                        protected_identities.add(identity)

                phase = "mutation"
                for entry, content in copied_contents:
                    # Existing lock inodes define flock's logical domain and
                    # therefore must remain stable. A linked lock is unsafe to
                    # metadata-converge; a non-lock linked inode is isolated by
                    # copy-up.
                    if entry.name in _LOCK_NAMES and entry.node is not None:
                        continue
                    _create_private_regular(
                        entry.parent,
                        entry.name,
                        content,
                        entry.owner_uid,
                        entry.group_gid,
                        entry.node,
                    )

                phase = "validation"
                final_stack = contextlib.ExitStack()
                stack.enter_context(final_stack)
                file_budget = [MAX_REGULAR_FILES]
                final_fence_nodes, final_snapshots = _validate_fence(
                    final_stack, fence_root, budget=file_budget
                )
                final_writer_nodes, final_writer_snapshot = (
                    _validate_identifier_directory(
                        final_stack, writer_root, budget=file_budget
                    )
                )
                final_snapshots.append(final_writer_snapshot)
                _verify_snapshots(roots, final_snapshots)
                if missing_lock_names:
                    replacement_locks = _acquire_locks(
                        final_snapshots[0],
                        require_all=True,
                        failure_code="mutation_failed",
                        names=missing_lock_names,
                    )
                fence_directories = [
                    node for node in final_fence_nodes if node.directory
                ]

                phase = "mutation"
                _converge_directories(
                    fence_directories, LIGHTRAG_UID, shared_gid
                )
                _verify_final_attributes(
                    fence_directories, LIGHTRAG_UID, shared_gid
                )

                phase = "validation"
                _verify_snapshots(roots, final_snapshots)

                # Every fallible topology/content/attribute check completes
                # while the fence root is still root-owned and 0700.
                for _ in range(2):
                    _verify_snapshots(roots, final_snapshots)
                    _verify_final_attributes(
                        final_fence_nodes, LIGHTRAG_UID, shared_gid
                    )
                    _verify_final_attributes(
                        final_writer_nodes, operator_uid, LIGHTRAG_GID
                    )

                phase = "mutation"
                # Persist both roots' final identities in a non-traversable
                # state. Publish writer evidence first; the still-sealed fence
                # root keeps every lifecycle consumer fail-closed. The fence
                # chmod is the last fallible operation and the single publish
                # gate: before it the fence is sealed, after it the complete
                # tree already satisfies the final contract.
                _stage_root_for_publish(
                    fence_root, LIGHTRAG_UID, shared_gid
                )
                _stage_root_for_publish(
                    writer_root, operator_uid, LIGHTRAG_GID
                )
                _publish_root(writer_root)
                # Make the prerequisite root publication durable while the
                # fence gate is still sealed. A failure here cannot expose the
                # lifecycle namespace. The final fence chmod intentionally has
                # no fallible operation after it.
                os.fsync(writer_root.descriptor)
                final_gate_commit_started = True
                _publish_root(fence_root)
            except BaseException:
                # Before the final gate commit begins, the fence root is still
                # frozen or sealed. Recovery is useful hygiene but is not the
                # security boundary: even if every recovery syscall fails, an
                # unprivileged lifecycle consumer cannot traverse the fence.
                # Once commit begins, failure means either the fence remains
                # sealed or its single atomic chmod published a fully prepared
                # tree, so rollback is neither required nor safe to assume.
                if mutation_started and not final_gate_commit_started:
                    _attempt_reseal_directories(protected_directories)
                raise
            # Closing every registered descriptor releases its flock and cannot
            # raise through _close_descriptor. Avoid explicit LOCK_UN calls
            # after the fence commit point: successful commit must remain the
            # last observable/fallible operation.
    except PreparationError:
        raise
    except OSError:
        code = "unsafe_layout" if phase == "validation" else "mutation_failed"
        raise PreparationError(code) from None


def verify_shared_fence(
    operator_uid: int,
    shared_gid: int,
    expected_attempt: str | None = None,
) -> None:
    """Verify the complete on-disk contract without mutating either tree."""

    _require_operator_uid(operator_uid)
    _require_shared_gid(shared_gid)
    if expected_attempt is not None:
        parse_expected_attempt(expected_attempt)

    try:
        with contextlib.ExitStack() as stack:
            fence_root = _open_root(
                stack, FENCE_ROOT, SHARED_DIRECTORY_MODE
            )
            writer_root = _open_root(
                stack, WRITER_EVIDENCE_ROOT, SHARED_DIRECTORY_MODE
            )
            fence_nodes, snapshots = _validate_fence(stack, fence_root)
            writer_nodes, writer_snapshot = _validate_identifier_directory(
                stack, writer_root
            )
            snapshots.append(writer_snapshot)
            roots = (
                (FENCE_ROOT, fence_root),
                (WRITER_EVIDENCE_ROOT, writer_root),
            )

            _require_complete_verify_layout(
                snapshots, writer_snapshot, expected_attempt
            )
            regular_entries = _collect_regular_entries(
                snapshots[:-1], writer_snapshot, operator_uid, shared_gid
            )
            _validate_regular_budgets(regular_entries)
            _verify_snapshots(roots, snapshots)
            _verify_exact_attributes(
                [fence_root, *fence_nodes],
                LIGHTRAG_UID,
                shared_gid,
                failure_code="verification_failed",
            )
            _verify_exact_attributes(
                [writer_root, *writer_nodes],
                operator_uid,
                LIGHTRAG_GID,
                failure_code="verification_failed",
            )
            # Attribute reads are followed by a second namespace/inode check,
            # so a replacement during verification cannot be reported valid.
            _verify_snapshots(roots, snapshots)
            _validate_regular_budgets(regular_entries)
            _verify_exact_attributes(
                [fence_root, *fence_nodes],
                LIGHTRAG_UID,
                shared_gid,
                failure_code="verification_failed",
            )
            _verify_exact_attributes(
                [writer_root, *writer_nodes],
                operator_uid,
                LIGHTRAG_GID,
                failure_code="verification_failed",
            )
    except PreparationError as error:
        if error.code in {"invalid_uid", "invalid_gid", "invalid_attempt"}:
            raise
        raise PreparationError("verification_failed") from None
    except OSError:
        raise PreparationError("verification_failed") from None


def main(argv: Sequence[str] | None = None) -> int:
    arguments = list(sys.argv[1:] if argv is None else argv)
    try:
        if arguments and arguments[0] == "verify":
            if len(arguments) not in (3, 4):
                raise PreparationError("invalid_arguments")
            operator_uid = parse_operator_uid(arguments[1])
            shared_gid = parse_shared_gid(arguments[2])
            expected_attempt = (
                parse_expected_attempt(arguments[3])
                if len(arguments) == 4
                else None
            )
            verify_shared_fence(operator_uid, shared_gid, expected_attempt)
        else:
            if len(arguments) != 2:
                raise PreparationError("invalid_arguments")
            operator_uid = parse_operator_uid(arguments[0])
            shared_gid = parse_shared_gid(arguments[1])
            prepare_shared_fence(operator_uid, shared_gid)
    except PreparationError as error:
        print(_ERROR_MESSAGES[error.code], file=sys.stderr)
        return 1
    except Exception:
        # Never expose a path, file name, file content, or raw OS exception.
        print(_ERROR_MESSAGES["internal_error"], file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
