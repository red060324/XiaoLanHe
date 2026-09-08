#!/usr/bin/env python3
"""Provision the LightRAG database and its least-privilege runtime identity."""

from __future__ import annotations

import os
import re

# Import through the pinned adapter so its declared pymilvus dependency bootstrap is
# identical to the LightRAG process rather than assuming a global package layout.
from lightrag.kg.milvus_impl import MilvusClient
from pymilvus import MilvusException


DATABASE = "lightrag"
RUNTIME_ROLE = "xlh_lightrag_runtime"
# Pinned Milvus 2.6.11 built-in groups needed by LightRAG 1.5.7. Both groups are
# scoped to lightrag. ListDatabases/RenameCollection are standalone cluster-scope
# privileges; none of these grants database creation or RBAC user management.
RUNTIME_GRANTS = (
    ("ListDatabases", "*", "*"),
    ("RenameCollection", "*", "*"),
    ("DatabaseAdmin", "*", DATABASE),
    ("CollectionReadWrite", "*", DATABASE),
)
IDENTITY = re.compile(r"^[A-Za-z][A-Za-z0-9_]{0,31}$")


def required(name: str) -> str:
    value = os.environ.get(name, "")
    if not value or value != value.strip() or "\n" in value or "\r" in value:
        raise ValueError(f"{name} must be a nonempty single-line value without surrounding whitespace")
    return value


def split_token(name: str) -> tuple[str, str]:
    token = required(name)
    user, separator, password = token.partition(":")
    if separator != ":" or not IDENTITY.fullmatch(user):
        raise ValueError(f"{name} must be username:password with a valid Milvus username")
    if len(password) < 6 or len(password) > 256:
        raise ValueError(f"{name} password must contain 6-256 characters")
    return user, password


def already_exists(error: BaseException) -> bool:
    return "already exist" in str(error).lower()


def grant_key(value: object) -> tuple[str, str, str] | None:
    if not isinstance(value, dict):
        return None
    privilege = str(value.get("privilege", ""))
    collection = str(value.get("collection_name", value.get("object_name", "")))
    database = str(value.get("db_name", ""))
    return privilege, collection, database


def main() -> None:
    database = required("MILVUS_DB_NAME")
    if database != DATABASE:
        raise SystemExit("MILVUS_DB_NAME must be lightrag")
    uri = required("MILVUS_URI")
    bootstrap_user, bootstrap_password = split_token("MILVUS_BOOTSTRAP_TOKEN")
    if bootstrap_user != "root":
        raise ValueError("MILVUS_BOOTSTRAP_TOKEN must use the Milvus root identity")
    runtime_user, runtime_password = split_token("MILVUS_RUNTIME_TOKEN")
    if bootstrap_user == runtime_user:
        raise ValueError("bootstrap and runtime Milvus identities must be distinct")
    bootstrap_token = f"{bootstrap_user}:{bootstrap_password}"

    client = MilvusClient(uri=uri, token=bootstrap_token)
    try:
        databases = set(client.list_databases())
        if database not in databases:
            client.create_database(database)
        if database not in set(client.list_databases()):
            raise RuntimeError("Milvus database creation was not observable")

        if runtime_user not in set(client.list_users()):
            client.create_user(user_name=runtime_user, password=runtime_password)
        roles = set(client.list_roles())
        if RUNTIME_ROLE not in roles:
            client.create_role(role_name=RUNTIME_ROLE)
        user = client.describe_user(user_name=runtime_user)
        if RUNTIME_ROLE not in set(user.get("roles", ())):
            client.grant_role(user_name=runtime_user, role_name=RUNTIME_ROLE)
        # PyMilvus defaults describe_role() to db_name="", which omits grants
        # scoped to a named database. The Milvus wildcard requests every scope,
        # allowing the exact least-privilege contract to be verified atomically.
        existing_grants = {
            key
            for item in client.describe_role(
                role_name=RUNTIME_ROLE, db_name="*"
            ).get("privileges", ())
            if (key := grant_key(item)) is not None
        }
        for privilege, collection, db_name in RUNTIME_GRANTS:
            if (privilege, collection, db_name) in existing_grants:
                continue
            try:
                client.grant_privilege_v2(
                    role_name=RUNTIME_ROLE,
                    privilege=privilege,
                    collection_name=collection,
                    db_name=db_name,
                )
            except MilvusException as error:
                if not already_exists(error):
                    raise
        actual_grants = {
            key
            for item in client.describe_role(
                role_name=RUNTIME_ROLE, db_name="*"
            ).get("privileges", ())
            if (key := grant_key(item)) is not None
        }
        if actual_grants != set(RUNTIME_GRANTS):
            raise RuntimeError("Milvus runtime role grants differ from the pinned least-privilege contract")
    finally:
        client.close()

    runtime = MilvusClient(uri=uri, db_name=database, token=f"{runtime_user}:{runtime_password}")
    try:
        if runtime.get_server_version() != "2.6.11":
            raise RuntimeError("unexpected Milvus server version")
        runtime.list_collections()
        denied_database = "xlh_runtime_must_not_create_database"
        try:
            runtime.create_database(denied_database)
        except Exception as error:
            if "permission deny" not in str(error).lower():
                raise RuntimeError("runtime CreateDatabase failed for an unexpected reason") from error
        else:
            raise RuntimeError("runtime identity unexpectedly has CreateDatabase privilege")
        denied_user = "xlh_runtime_must_not_create_user"
        try:
            runtime.create_user(user_name=denied_user, password="denied-password")
        except Exception as error:
            if "permission deny" not in str(error).lower():
                raise RuntimeError("runtime CreateUser failed for an unexpected reason") from error
        else:
            raise RuntimeError("runtime identity unexpectedly has user-management privilege")
    finally:
        runtime.close()


if __name__ == "__main__":
    main()
