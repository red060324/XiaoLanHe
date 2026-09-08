#!/usr/bin/env python3
"""Validate dedicated host roots before exposing them to a root helper.

The caller supplies two lexically canonical absolute paths.  This program walks
each component relative to an already-open directory, never follows symlinks,
and creates at most a missing final directory.  Existing metadata is never
changed.
"""

from __future__ import annotations

import contextlib
import errno
import os
import re
import stat
import sys
from typing import NamedTuple, Sequence


LIGHTRAG_UID = 1000
LIGHTRAG_GID = 1000
MAX_ID = 2_147_483_647
MAX_PATH_BYTES = 4096
MAX_COMPONENT_BYTES = 255
MAX_DIRECTORY_ENTRIES = 4096
FRESH_ROOT_MODE = 0o0700
SEALED_ROOT_MODE = 0o0000
SHARED_DIRECTORY_MODE = 0o2750
REGULAR_MODE = 0o0640

_IDENTIFIER_JSON = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.json$")
_FENCE_REGULARS = frozenset({"serving.lock", "rebuild.lock", "current.json"})
_FENCE_DIRECTORIES = frozenset({"reports", "attempts"})
_ERROR_MESSAGES = {
    "invalid_arguments": "validate_shared_fence_host: invalid_arguments",
    "invalid_path": "validate_shared_fence_host: invalid_path",
    "invalid_uid": "validate_shared_fence_host: invalid_uid",
    "invalid_gid": "validate_shared_fence_host: invalid_gid",
    "unsafe_ancestor": "validate_shared_fence_host: unsafe_ancestor",
    "unsafe_layout": "validate_shared_fence_host: unsafe_layout",
    "privileged_recovery_required": (
        "validate_shared_fence_host: privileged_recovery_required"
    ),
    "create_failed": "validate_shared_fence_host: create_failed",
    "internal_error": "validate_shared_fence_host: internal_error",
}

_CLOSE_ON_EXEC = os.O_CLOEXEC
_NO_FOLLOW = os.O_NOFOLLOW
_DIRECTORY_FLAGS = os.O_RDONLY | os.O_DIRECTORY | _NO_FOLLOW | _CLOSE_ON_EXEC
_REGULAR_FLAGS = os.O_RDONLY | os.O_NONBLOCK | _NO_FOLLOW | _CLOSE_ON_EXEC


class HostValidationError(Exception):
    """A fixed-code failure that is safe to expose to an operator."""

    def __init__(self, code: str):
        if code not in _ERROR_MESSAGES:
            raise ValueError("invalid host validation error code")
        super().__init__(code)
        self.code = code


class _PathSpec(NamedTuple):
    components: tuple[str, ...]


class _OpenedNode(NamedTuple):
    descriptor: int
    device: int
    inode: int
    node_type: int
    link_count: int
    owner_uid: int
    group_gid: int
    mode: int
    directory: bool
    logical_name: tuple[str, ...]


class _Binding(NamedTuple):
    parent: _OpenedNode
    name: str
    child: _OpenedNode


class _DirectorySnapshot(NamedTuple):
    directory: _OpenedNode
    names: tuple[str, ...]
    children: tuple[_Binding, ...]


class _Candidate(NamedTuple):
    spec: _PathSpec
    parent: _OpenedNode
    ancestors: tuple[_OpenedNode, ...]
    ancestor_bindings: tuple[_Binding, ...]
    root: _OpenedNode | None
    root_binding: _Binding | None


class _Inspection(NamedTuple):
    candidates: tuple[_Candidate, _Candidate]
    snapshots: tuple[_DirectorySnapshot, ...]


class _Topology:
    """Reject one logical directory/file resolving to multiple identities or aliases."""

    def __init__(self) -> None:
        self._identity_to_name: dict[tuple[int, int], tuple[str, ...]] = {}
        self._name_to_identity: dict[tuple[str, ...], tuple[int, int]] = {}

    def add(self, node: _OpenedNode) -> None:
        identity = (node.device, node.inode)
        prior_name = self._identity_to_name.get(identity)
        if prior_name is not None and prior_name != node.logical_name:
            raise HostValidationError("unsafe_layout")
        prior_identity = self._name_to_identity.get(node.logical_name)
        if prior_identity is not None and prior_identity != identity:
            raise HostValidationError("unsafe_layout")
        self._identity_to_name[identity] = node.logical_name
        self._name_to_identity[node.logical_name] = identity


def _parse_numeric_id(value: str, *, allow_zero: bool, code: str) -> int:
    if (
        not isinstance(value, str)
        or not value
        or len(value) > 10
        or not value.isascii()
        or not value.isdecimal()
    ):
        raise HostValidationError(code)
    numeric_id = int(value, 10)
    if numeric_id > MAX_ID or (numeric_id == 0 and not allow_zero):
        raise HostValidationError(code)
    return numeric_id


def parse_operator_uid(value: str) -> int:
    return _parse_numeric_id(value, allow_zero=True, code="invalid_uid")


def parse_shared_gid(value: str) -> int:
    return _parse_numeric_id(value, allow_zero=False, code="invalid_gid")


def _require_operator_uid(value: int) -> None:
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or value < 0
        or value > MAX_ID
    ):
        raise HostValidationError("invalid_uid")


def _require_shared_gid(value: int) -> None:
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or value <= 0
        or value > MAX_ID
    ):
        raise HostValidationError("invalid_gid")


def _parse_path(value: str) -> _PathSpec:
    if not isinstance(value, str) or not value or "\x00" in value:
        raise HostValidationError("invalid_path")
    try:
        encoded = os.fsencode(value)
    except (TypeError, UnicodeError):
        raise HostValidationError("invalid_path") from None
    if (
        len(encoded) > MAX_PATH_BYTES
        or value == "/"
        or not value.startswith("/")
        or value.startswith("//")
        or value.endswith("/")
    ):
        raise HostValidationError("invalid_path")
    components = tuple(value[1:].split("/"))
    if (
        not components
        or any(component in {"", ".", ".."} for component in components)
        or any(len(os.fsencode(component)) > MAX_COMPONENT_BYTES for component in components)
        or "/" + "/".join(components) != value
    ):
        raise HostValidationError("invalid_path")
    return _PathSpec(components=components)


def _require_distinct_paths(fence: _PathSpec, writer: _PathSpec) -> None:
    if fence.components == writer.components:
        raise HostValidationError("unsafe_layout")
    shorter, longer = sorted((fence.components, writer.components), key=len)
    if longer[: len(shorter)] == shorter:
        raise HostValidationError("unsafe_layout")


def _node_from_status(
    stack: contextlib.ExitStack,
    descriptor: int,
    node_status: os.stat_result,
    *,
    directory: bool,
    logical_name: tuple[str, ...],
) -> _OpenedNode:
    expected_type = stat.S_ISDIR if directory else stat.S_ISREG
    if not expected_type(node_status.st_mode):
        os.close(descriptor)
        raise HostValidationError("unsafe_layout")
    if not directory and node_status.st_nlink != 1:
        os.close(descriptor)
        raise HostValidationError("unsafe_layout")
    stack.callback(os.close, descriptor)
    return _OpenedNode(
        descriptor=descriptor,
        device=node_status.st_dev,
        inode=node_status.st_ino,
        node_type=stat.S_IFMT(node_status.st_mode),
        link_count=node_status.st_nlink,
        owner_uid=node_status.st_uid,
        group_gid=node_status.st_gid,
        mode=stat.S_IMODE(node_status.st_mode),
        directory=directory,
        logical_name=logical_name,
    )


def _open_node(
    stack: contextlib.ExitStack,
    name: str,
    *,
    parent_fd: int | None,
    directory: bool,
    logical_name: tuple[str, ...],
    failure_code: str,
) -> _OpenedNode:
    flags = _DIRECTORY_FLAGS if directory else _REGULAR_FLAGS
    try:
        if parent_fd is None:
            descriptor = os.open(name, flags)
        else:
            descriptor = os.open(name, flags, dir_fd=parent_fd)
        node_status = os.fstat(descriptor)
    except OSError:
        try:
            os.close(descriptor)
        except (OSError, UnboundLocalError):
            pass
        raise HostValidationError(failure_code) from None
    try:
        return _node_from_status(
            stack,
            descriptor,
            node_status,
            directory=directory,
            logical_name=logical_name,
        )
    except HostValidationError:
        if failure_code != "unsafe_layout":
            raise HostValidationError(failure_code) from None
        raise


def _require_safe_ancestor(node: _OpenedNode, operator_uid: int) -> None:
    if not node.directory or node.owner_uid not in {0, operator_uid}:
        raise HostValidationError("unsafe_ancestor")
    if node.mode & 0o022:
        root_owned_sticky = node.owner_uid == 0 and bool(node.mode & stat.S_ISVTX)
        if not root_owned_sticky:
            raise HostValidationError("unsafe_ancestor")


def _open_candidate(
    stack: contextlib.ExitStack,
    spec: _PathSpec,
    operator_uid: int,
    topology: _Topology,
) -> _Candidate:
    root = _open_node(
        stack,
        "/",
        parent_fd=None,
        directory=True,
        logical_name=(),
        failure_code="unsafe_ancestor",
    )
    _require_safe_ancestor(root, operator_uid)
    topology.add(root)
    ancestors = [root]
    bindings: list[_Binding] = []
    parent = root
    logical: tuple[str, ...] = ()
    for component in spec.components[:-1]:
        logical += (component,)
        child = _open_node(
            stack,
            component,
            parent_fd=parent.descriptor,
            directory=True,
            logical_name=logical,
            failure_code="unsafe_ancestor",
        )
        _require_safe_ancestor(child, operator_uid)
        topology.add(child)
        bindings.append(_Binding(parent=parent, name=component, child=child))
        ancestors.append(child)
        parent = child

    leaf = spec.components[-1]
    logical = spec.components
    try:
        descriptor = os.open(leaf, _DIRECTORY_FLAGS, dir_fd=parent.descriptor)
    except FileNotFoundError:
        return _Candidate(
            spec=spec,
            parent=parent,
            ancestors=tuple(ancestors),
            ancestor_bindings=tuple(bindings),
            root=None,
            root_binding=None,
        )
    except OSError as error:
        if error.errno in {errno.EACCES, errno.EPERM}:
            try:
                node_status = os.stat(
                    leaf, dir_fd=parent.descriptor, follow_symlinks=False
                )
            except OSError:
                raise HostValidationError("unsafe_layout") from None
            if (
                stat.S_ISDIR(node_status.st_mode)
                and stat.S_IMODE(node_status.st_mode)
                in {FRESH_ROOT_MODE, SEALED_ROOT_MODE}
            ):
                # Scheme A: an unprivileged process cannot validate the contents
                # of a genuinely frozen root.  Fail closed and require an
                # explicit privileged inspection/recovery; never claim that the
                # ordinary bootstrap retry has validated this namespace.
                raise HostValidationError("privileged_recovery_required") from None
        raise HostValidationError("unsafe_layout") from None
    try:
        node_status = os.fstat(descriptor)
        opened_root = _node_from_status(
            stack,
            descriptor,
            node_status,
            directory=True,
            logical_name=logical,
        )
    except OSError:
        try:
            os.close(descriptor)
        except OSError:
            pass
        raise HostValidationError("unsafe_layout") from None
    topology.add(opened_root)
    return _Candidate(
        spec=spec,
        parent=parent,
        ancestors=tuple(ancestors),
        ancestor_bindings=tuple(bindings),
        root=opened_root,
        root_binding=_Binding(parent=parent, name=leaf, child=opened_root),
    )


def _list_directory(
    directory: _OpenedNode, *, frozen: bool = False
) -> tuple[str, ...]:
    try:
        names = os.listdir(directory.descriptor)
    except OSError as error:
        if frozen and error.errno in {errno.EACCES, errno.EPERM}:
            raise HostValidationError("privileged_recovery_required") from None
        raise HostValidationError("unsafe_layout") from None
    if (
        len(names) > MAX_DIRECTORY_ENTRIES
        or any(not isinstance(name, str) for name in names)
    ):
        raise HostValidationError("unsafe_layout")
    return tuple(sorted(names))


def _open_child(
    stack: contextlib.ExitStack,
    parent: _OpenedNode,
    name: str,
    *,
    directory: bool,
    topology: _Topology,
) -> _Binding:
    child = _open_node(
        stack,
        name,
        parent_fd=parent.descriptor,
        directory=directory,
        logical_name=parent.logical_name + (name,),
        failure_code="unsafe_layout",
    )
    if child.device != parent.device:
        raise HostValidationError("unsafe_layout")
    topology.add(child)
    return _Binding(parent=parent, name=name, child=child)


def _require_attributes(
    node: _OpenedNode, owner_uid: int, group_gid: int, mode: int
) -> None:
    if (
        node.owner_uid != owner_uid
        or node.group_gid != group_gid
        or node.mode != mode
    ):
        raise HostValidationError("unsafe_layout")


def _root_state(
    root: _OpenedNode, *, role: str, operator_uid: int, shared_gid: int
) -> str:
    # Check the fail-closed recovery state first.  When the invoking operator is
    # root, root:root/0700 is otherwise indistinguishable from a fresh root and
    # a non-empty interrupted tree would be incorrectly rejected as fresh.
    if (root.owner_uid, root.group_gid, root.mode) in {
        (0, 0, FRESH_ROOT_MODE),
        (0, 0, SEALED_ROOT_MODE),
    }:
        return "frozen"
    if root.mode == SEALED_ROOT_MODE:
        # Final-owner/0000 is the helper's staged publish state. It can result
        # from interruption immediately before a root is atomically reopened,
        # and always requires privileged inspection/recovery.
        return "frozen"
    if role == "fence":
        prepared = (LIGHTRAG_UID, shared_gid, SHARED_DIRECTORY_MODE)
    else:
        prepared = (operator_uid, LIGHTRAG_GID, SHARED_DIRECTORY_MODE)
    if (root.owner_uid, root.group_gid, root.mode) == prepared:
        return "prepared"
    if root.owner_uid == operator_uid and root.mode == FRESH_ROOT_MODE:
        return "fresh"
    raise HostValidationError("unsafe_layout")


def _snapshot_identifier_directory(
    stack: contextlib.ExitStack,
    directory: _OpenedNode,
    *,
    topology: _Topology,
    strict_owner_uid: int | None,
    strict_group_gid: int | None,
) -> _DirectorySnapshot:
    names = _list_directory(directory)
    if any(_IDENTIFIER_JSON.fullmatch(name) is None for name in names):
        raise HostValidationError("unsafe_layout")
    children: list[_Binding] = []
    for name in names:
        binding = _open_child(
            stack, directory, name, directory=False, topology=topology
        )
        if strict_owner_uid is not None and strict_group_gid is not None:
            _require_attributes(
                binding.child, strict_owner_uid, strict_group_gid, REGULAR_MODE
            )
        children.append(binding)
    return _DirectorySnapshot(directory, names, tuple(children))


def _snapshot_existing_root(
    stack: contextlib.ExitStack,
    candidate: _Candidate,
    *,
    role: str,
    operator_uid: int,
    shared_gid: int,
    topology: _Topology,
) -> tuple[_DirectorySnapshot, ...]:
    root = candidate.root
    if root is None:
        return ()
    state = _root_state(
        root, role=role, operator_uid=operator_uid, shared_gid=shared_gid
    )
    names = _list_directory(root, frozen=state == "frozen")
    if state == "fresh":
        if names:
            raise HostValidationError("unsafe_layout")
        return (_DirectorySnapshot(root, names, ()),)

    strict = state == "prepared"
    # Frozen contents can reflect any interruption point in the root helper's
    # ordered fchown/fchmod sequence.  The host check therefore validates their
    # bounded names, types, link counts, device and inode topology, but does not
    # authorize their transitional metadata.  The root helper independently
    # repeats those structural checks through retained descriptors and either
    # converges exact metadata or keeps the roots frozen.
    if role == "fence":
        if any(
            name not in _FENCE_REGULARS and name not in _FENCE_DIRECTORIES
            for name in names
        ):
            raise HostValidationError("unsafe_layout")
        owner_uid = LIGHTRAG_UID if strict else None
        group_gid = shared_gid if strict else None
        children: list[_Binding] = []
        nested: list[_DirectorySnapshot] = []
        for name in names:
            directory = name in _FENCE_DIRECTORIES
            binding = _open_child(
                stack, root, name, directory=directory, topology=topology
            )
            if strict:
                _require_attributes(
                    binding.child,
                    owner_uid,
                    group_gid,
                    SHARED_DIRECTORY_MODE if directory else REGULAR_MODE,
                )
            children.append(binding)
            if directory:
                nested.append(
                    _snapshot_identifier_directory(
                        stack,
                        binding.child,
                        topology=topology,
                        strict_owner_uid=owner_uid,
                        strict_group_gid=group_gid,
                    )
                )
        return (_DirectorySnapshot(root, names, tuple(children)), *nested)

    owner_uid = operator_uid if strict else None
    group_gid = LIGHTRAG_GID if strict else None
    return (
        _snapshot_identifier_directory(
            stack,
            root,
            topology=topology,
            strict_owner_uid=owner_uid,
            strict_group_gid=group_gid,
        ),
    )


def _current_status(node: _OpenedNode) -> os.stat_result:
    try:
        node_status = os.fstat(node.descriptor)
    except OSError:
        raise HostValidationError("unsafe_layout") from None
    expected_type = stat.S_ISDIR if node.directory else stat.S_ISREG
    if (
        not expected_type(node_status.st_mode)
        or (node_status.st_dev, node_status.st_ino) != (node.device, node.inode)
        or node_status.st_uid != node.owner_uid
        or node_status.st_gid != node.group_gid
        or stat.S_IMODE(node_status.st_mode) != node.mode
        or (not node.directory and node_status.st_nlink != 1)
    ):
        raise HostValidationError("unsafe_layout")
    return node_status


def _verify_binding(binding: _Binding) -> None:
    flags = _DIRECTORY_FLAGS if binding.child.directory else _REGULAR_FLAGS
    try:
        descriptor = os.open(binding.name, flags, dir_fd=binding.parent.descriptor)
        node_status = os.fstat(descriptor)
    except OSError:
        try:
            os.close(descriptor)
        except (OSError, UnboundLocalError):
            pass
        raise HostValidationError("unsafe_layout") from None
    try:
        expected_type = stat.S_ISDIR if binding.child.directory else stat.S_ISREG
        if (
            not expected_type(node_status.st_mode)
            or (node_status.st_dev, node_status.st_ino)
            != (binding.child.device, binding.child.inode)
            or (not binding.child.directory and node_status.st_nlink != 1)
        ):
            raise HostValidationError("unsafe_layout")
    finally:
        os.close(descriptor)


def _verify_inspection(inspection: _Inspection, operator_uid: int) -> None:
    for candidate in inspection.candidates:
        for ancestor in candidate.ancestors:
            current = _current_status(ancestor)
            current_node = _OpenedNode(
                ancestor.descriptor,
                current.st_dev,
                current.st_ino,
                stat.S_IFMT(current.st_mode),
                current.st_nlink,
                current.st_uid,
                current.st_gid,
                stat.S_IMODE(current.st_mode),
                True,
                ancestor.logical_name,
            )
            _require_safe_ancestor(current_node, operator_uid)
        for binding in candidate.ancestor_bindings:
            _verify_binding(binding)
        if candidate.root is not None:
            _current_status(candidate.root)
            if candidate.root_binding is None:
                raise HostValidationError("unsafe_layout")
            _verify_binding(candidate.root_binding)

    for snapshot in inspection.snapshots:
        _current_status(snapshot.directory)
        if _list_directory(snapshot.directory) != snapshot.names:
            raise HostValidationError("unsafe_layout")
        for binding in snapshot.children:
            _current_status(binding.child)
            _verify_binding(binding)


def _inspect(
    stack: contextlib.ExitStack,
    fence: _PathSpec,
    writer: _PathSpec,
    operator_uid: int,
    shared_gid: int,
    *,
    require_existing: bool,
) -> _Inspection:
    topology = _Topology()
    fence_candidate = _open_candidate(stack, fence, operator_uid, topology)
    writer_candidate = _open_candidate(stack, writer, operator_uid, topology)
    if require_existing and (
        fence_candidate.root is None or writer_candidate.root is None
    ):
        raise HostValidationError("unsafe_layout")
    if fence_candidate.root is not None and writer_candidate.root is not None:
        if (fence_candidate.root.device, fence_candidate.root.inode) == (
            writer_candidate.root.device,
            writer_candidate.root.inode,
        ):
            raise HostValidationError("unsafe_layout")
    snapshots = (
        *_snapshot_existing_root(
            stack,
            fence_candidate,
            role="fence",
            operator_uid=operator_uid,
            shared_gid=shared_gid,
            topology=topology,
        ),
        *_snapshot_existing_root(
            stack,
            writer_candidate,
            role="writer",
            operator_uid=operator_uid,
            shared_gid=shared_gid,
            topology=topology,
        ),
    )
    inspection = _Inspection(
        candidates=(fence_candidate, writer_candidate),
        snapshots=tuple(snapshots),
    )
    _verify_inspection(inspection, operator_uid)
    return inspection


def _create_missing_root(candidate: _Candidate, operator_uid: int) -> None:
    if candidate.root is not None:
        return
    leaf = candidate.spec.components[-1]
    try:
        previous_umask = os.umask(0o077)
        try:
            os.mkdir(leaf, FRESH_ROOT_MODE, dir_fd=candidate.parent.descriptor)
        finally:
            os.umask(previous_umask)
        os.fsync(candidate.parent.descriptor)
        descriptor = os.open(leaf, _DIRECTORY_FLAGS, dir_fd=candidate.parent.descriptor)
        try:
            node_status = os.fstat(descriptor)
            names = os.listdir(descriptor)
            if (
                not stat.S_ISDIR(node_status.st_mode)
                or node_status.st_uid != operator_uid
                or stat.S_IMODE(node_status.st_mode) != FRESH_ROOT_MODE
                or names
            ):
                raise HostValidationError("create_failed")
            rebound = os.open(leaf, _DIRECTORY_FLAGS, dir_fd=candidate.parent.descriptor)
            try:
                rebound_status = os.fstat(rebound)
                if (rebound_status.st_dev, rebound_status.st_ino) != (
                    node_status.st_dev,
                    node_status.st_ino,
                ):
                    raise HostValidationError("create_failed")
            finally:
                os.close(rebound)
        finally:
            os.close(descriptor)
    except HostValidationError:
        raise
    except OSError:
        raise HostValidationError("create_failed") from None


def validate_shared_fence_host(
    fence_path: str,
    writer_path: str,
    operator_uid: int,
    shared_gid: int,
) -> None:
    """Validate two dedicated roots and create only absent final leaves."""

    _require_operator_uid(operator_uid)
    _require_shared_gid(shared_gid)
    fence = _parse_path(fence_path)
    writer = _parse_path(writer_path)
    _require_distinct_paths(fence, writer)

    with contextlib.ExitStack() as stack:
        inspection = _inspect(
            stack,
            fence,
            writer,
            operator_uid,
            shared_gid,
            require_existing=False,
        )
        # Both existing trees and both parent chains have passed complete,
        # read-only validation before the first filesystem mutation.
        for candidate in inspection.candidates:
            _create_missing_root(candidate, operator_uid)
            _verify_inspection(inspection, operator_uid)

    # Reopen by name and repeat the complete snapshot.  A successful result is
    # tied to the final namespace bindings, not merely the descriptors retained
    # before mkdir.
    with contextlib.ExitStack() as stack:
        final_inspection = _inspect(
            stack,
            fence,
            writer,
            operator_uid,
            shared_gid,
            require_existing=True,
        )
        _verify_inspection(final_inspection, operator_uid)


def main(argv: Sequence[str] | None = None) -> int:
    arguments = list(sys.argv[1:] if argv is None else argv)
    try:
        if len(arguments) != 4:
            raise HostValidationError("invalid_arguments")
        operator_uid = parse_operator_uid(arguments[2])
        shared_gid = parse_shared_gid(arguments[3])
        validate_shared_fence_host(
            arguments[0], arguments[1], operator_uid, shared_gid
        )
    except HostValidationError as error:
        print(_ERROR_MESSAGES[error.code], file=sys.stderr)
        return 1
    except BaseException:
        # Never expose a caller-controlled path, node name, or raw OS error.
        print(_ERROR_MESSAGES["internal_error"], file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
