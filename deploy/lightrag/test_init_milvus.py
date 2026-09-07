import importlib.util
import os
import pathlib
import sys
import types
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("init_milvus.py")


def load_module(client_type, exception_type=RuntimeError):
    adapter = types.ModuleType("lightrag.kg.milvus_impl")
    adapter.MilvusClient = client_type
    pymilvus = types.ModuleType("pymilvus")
    pymilvus.MilvusException = exception_type
    modules = {
        "lightrag": types.ModuleType("lightrag"),
        "lightrag.kg": types.ModuleType("lightrag.kg"),
        "lightrag.kg.milvus_impl": adapter,
        "pymilvus": pymilvus,
    }
    spec = importlib.util.spec_from_file_location("init_milvus_under_test", MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    with mock.patch.dict(sys.modules, modules):
        assert spec.loader is not None
        spec.loader.exec_module(module)
    return module


class FakePermissionDenied(Exception):
    pass


class FakeClient:
    calls = []
    databases = {"default"}
    users = {"root"}
    roles = set()
    user_roles = {}
    grants = set()

    def __init__(self, **kwargs):
        self.kwargs = kwargs
        self.runtime = "db_name" in kwargs
        self.calls.append(("connect", kwargs))

    def close(self):
        self.calls.append(("close", self.runtime))

    def list_databases(self):
        return sorted(self.databases)

    def create_database(self, name):
        if self.runtime:
            self.calls.append(("runtime_create_database", name))
            raise FakePermissionDenied("permission deny")
        self.databases.add(name)
        self.calls.append(("create_database", name))

    def list_users(self):
        return sorted(self.users)

    def create_user(self, **kwargs):
        if self.runtime:
            self.calls.append(("runtime_create_user", kwargs))
            raise FakePermissionDenied("permission deny")
        self.users.add(kwargs["user_name"])
        self.calls.append(("create_user", kwargs))

    def list_roles(self):
        return sorted(self.roles)

    def create_role(self, **kwargs):
        self.roles.add(kwargs["role_name"])
        self.calls.append(("create_role", kwargs))

    def describe_user(self, **kwargs):
        return {"roles": tuple(self.user_roles.get(kwargs["user_name"], set()))}

    def grant_role(self, **kwargs):
        self.user_roles.setdefault(kwargs["user_name"], set()).add(kwargs["role_name"])
        self.calls.append(("grant_role", kwargs))

    def describe_role(self, **kwargs):
        return {
            "privileges": [
                {"privilege": privilege, "collection_name": collection, "db_name": database}
                for privilege, collection, database in sorted(self.grants)
            ]
        }

    def grant_privilege_v2(self, **kwargs):
        self.grants.add((kwargs["privilege"], kwargs["collection_name"], kwargs["db_name"]))
        self.calls.append(("grant", kwargs))

    def get_server_version(self):
        return "2.6.11"

    def list_collections(self):
        return []


class InitMilvusTest(unittest.TestCase):
    def setUp(self):
        FakeClient.calls = []
        FakeClient.databases = {"default"}
        FakeClient.users = {"root"}
        FakeClient.roles = set()
        FakeClient.user_roles = {}
        FakeClient.grants = set()
        self.module = load_module(FakeClient, FakePermissionDenied)
        self.environment = {
            "MILVUS_URI": "http://milvus:19530",
            "MILVUS_DB_NAME": "lightrag",
            "MILVUS_BOOTSTRAP_TOKEN": "root:bootstrap-secret",
            "MILVUS_RUNTIME_TOKEN": "xlh_lightrag:runtime-secret",
        }

    def test_bootstrap_and_runtime_identities_are_separate_and_least_privilege(self):
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.module.main()
        connections = [item[1] for item in FakeClient.calls if item[0] == "connect"]
        self.assertEqual(connections[0], {"uri": "http://milvus:19530", "token": "root:bootstrap-secret"})
        self.assertEqual(
            connections[1],
            {"uri": "http://milvus:19530", "db_name": "lightrag", "token": "xlh_lightrag:runtime-secret"},
        )
        grants = {
            (call[1]["privilege"], call[1]["collection_name"], call[1]["db_name"])
            for call in FakeClient.calls
            if call[0] == "grant"
        }
        self.assertEqual(
            grants,
            {
                ("ListDatabases", "*", "*"),
                ("RenameCollection", "*", "*"),
                ("DatabaseAdmin", "*", "lightrag"),
                ("CollectionReadWrite", "*", "lightrag"),
            },
        )
        granted_privileges = {privilege for privilege, _, _ in grants}
        self.assertTrue(
            {"ClusterReadOnly", "CollectionAdmin", "ClusterAdmin"}.isdisjoint(granted_privileges)
        )
        self.assertIn(
            ("runtime_create_database", "xlh_runtime_must_not_create_database"),
            FakeClient.calls,
        )
        self.assertTrue(any(call[0] == "runtime_create_user" for call in FakeClient.calls))

    def test_rerun_is_idempotent(self):
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.module.main()
            FakeClient.calls = []
            self.module.main()
        mutations = {"create_database", "create_user", "create_role", "grant_role", "grant"}
        self.assertFalse(any(call[0] in mutations for call in FakeClient.calls), FakeClient.calls)

    def test_rejects_empty_or_shared_identity(self):
        for bootstrap, runtime in (
            ("", "xlh:secret1"),
            ("operator:secret1", "xlh:secret2"),
            ("root:same-secret", "root:same-secret"),
            ("root:secret1", "root:secret2"),
        ):
            environment = dict(self.environment, MILVUS_BOOTSTRAP_TOKEN=bootstrap, MILVUS_RUNTIME_TOKEN=runtime)
            with self.subTest(bootstrap=bootstrap, runtime=runtime), mock.patch.dict(os.environ, environment, clear=True):
                with self.assertRaises(ValueError):
                    self.module.main()


if __name__ == "__main__":
    unittest.main()
