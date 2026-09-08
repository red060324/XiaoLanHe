# Research Notes

- Status: `IMPLEMENTED — LOCAL CI PASS; MYSQL 8.4 REMOTE REVERIFY PENDING`
- Authoritative spec: `./spec.md`
- Research date: 2026-09-07

## Repository Audit

The current application is deeply coupled to PostgreSQL rather than merely using a
portable DSN. The composition root, seed/import commands, migration runner and eight
business repository areas use pgx. Runtime SQL includes PostgreSQL placeholders,
`RETURNING`, `ON CONFLICT`, partial/expression indexes, JSONB operators, `LATERAL`,
`ANY`, aggregate filters, modifying CTEs, `UPDATE ... FROM`, advisory locks, pgvector
casts and PostgreSQL time functions.

The existing core database integration suite also encodes PostgreSQL schemas, pgvector,
advisory-lock observation and default Read Committed behavior. Therefore a driver-only
swap would compile poorly and, more importantly, silently weaken concurrency and
uniqueness behavior.

## MySQL 8.4 Findings Applied To The Design

- InnoDB defaults normally differ from PostgreSQL's transaction isolation. Critical
  repository transactions must request Read Committed explicitly and be retested after
  waiting on locks.
- MySQL named locks are connection-scoped and do not release at transaction commit.
  They are suitable only for the migration runner on a fixed connection with explicit
  release, not pooled business request locks.
- MySQL DDL implicitly commits. A migration runner must expose dirty state and operator
  repair instead of claiming whole-file transactional rollback.
- MySQL lacks PostgreSQL partial unique indexes. Nullable generated discriminator
  columns or ordinary nullable unique constraints preserve the business predicates.
- `LastInsertId` must come from the executing result or a same-transaction lookup; a
  separate pooled `SELECT LAST_INSERT_ID()` is unsafe.
- Default MySQL collations may collapse more values than application lowercase/uppercase
  ASCII normalization. Machine identifiers require explicit binary semantics.
- `SKIP LOCKED` exists, but the old modifying CTE does not. Workers need locked ID
  selection followed by update/read in the same transaction and new contention tests.
- `TEXT` stores at most 65,535 bytes, so a 20,000-rune utf8mb4 description can exceed
  it; unconstrained conversation/summary and catalog description fields use
  `MEDIUMTEXT` and receive maximum-input round-trip tests.
- Driver `clientFoundRows` changes `RowsAffected` from changed rows to matched rows. The
  target DSN fixes it false and repository branches are tested against that semantic.
- Connection session initialization is per physical connection, not per pool. A wrapped
  connector applies/verifies UTC and strict SQL mode whenever the pool opens or replaces
  a connection; production additionally requires certificate/hostname-verified TLS.
- MySQL 8.4 serializes charset-introduced string literals read from
  `information_schema.CHECK_CONSTRAINTS.CHECK_CLAUSE` in an escaped-delimiter form such
  as `_utf8mb4\'^[a-z0-9_]{3,32}$\'`, even though the migration source uses ordinary
  quoted literals. GitHub Actions run `34241868418` observed this behavior against the
  pinned server. Migration postcondition comparison therefore needs a narrowly scoped
  canonicalization rule: accept the exact `_utf8mb4` introducer case-insensitively in
  ordinary or escaped-delimiter form. The latter is decoded according to the two
  `String::print` layers: three slashes plus quote is a value apostrophe, four slashes
  is a value backslash, and doubled slash plus `0/n/r/Z` is the corresponding control
  byte. Unknown charsets, lookalike prefixes, whitespace-separated or unterminated
  forms, impossible slash runs and unknown outer escapes are rejected. A global
  backslash/quote replacement would hide malformed metadata or alter regular-expression
  contents and is not allowed.

## Official LightRAG 1.5.7 Findings

The official tag is fixed to commit
`28ff1b05f2ac3f3e6fa14dd2cd33656579bd0c9c`. It natively includes
`MilvusVectorDBStorage` and reads these server variables. The bare URI below is the
server-level value used by database initialization, not the selected LightRAG runtime
value:

```text
LIGHTRAG_VECTOR_STORAGE=MilvusVectorDBStorage
MILVUS_URI=http://milvus:19530
MILVUS_DB_NAME=lightrag
MILVUS_USER / MILVUS_PASSWORD / MILVUS_TOKEN (optional)
MILVUS_WORKSPACE (optional override; deliberately unset here)
```

For this deployment, `milvus-init` retains the bare URI so root can list/create
databases. `lightrag-bootstrap` and steady-state `lightrag` instead use
`MILVUS_URI=http://milvus:19530/lightrag` together with
`MILVUS_DB_NAME=lightrag`.

Milvus implements only LightRAG's vector port. KV, graph and document status must each
have their own selected implementation. This spec keeps JsonKV, NetworkX and
JsonDocStatus, so a single LightRAG replica and persistent workspace are still required.

The v1.5.7 package declares `pymilvus>=2.6.2,<4.0.0`; the selected immutable image
resolves PyMilvus 3.0.0. Its official CPU standalone template selects Milvus 2.6.11,
etcd 3.5.25 and MinIO
`RELEASE.2025-09-07T16-13-09Z`. Milvus standalone depends on both etcd and MinIO; all
three stores require durable volumes.

The adapter defaults to `AUTOINDEX` and `COSINE`, derives collection dimension from the
embedding function and fails on dimension mismatch. Embedding model/dimension therefore
belong to the persisted index contract.

LightRAG's Milvus adapter can create a missing configured database through
`list_databases/create_database`. This design instead uses a short-lived root deployment
init identity and the bare server URI to create `lightrag`, then connects LightRAG with
the separate runtime identity and the `/lightrag` URI path. In PyMilvus 3.0.0 that path
selects the named database for the initial client context; retaining
`MILVUS_DB_NAME=lightrag` keeps the LightRAG and deployment checks explicit but does not
justify an initial connection to `default`. The runtime role retains only
database-scoped `DatabaseAdmin`/`CollectionReadWrite` on `lightrag` and cluster-scoped
`ListDatabases`/`RenameCollection`. No grant is added in `default`, and no adapter fork
is needed. Because `describe_role()` defaults to an empty database scope and omits named
database grants, exact audit uses `db_name="*"` (or an equivalent all-scope query).
The pinned embedding contract is symmetric `text-embedding-v4`/1024 with
`EMBEDDING_SEND_DIM=false` and both prefixes unset. Model, dimension, provider behavior,
send-dimension, asymmetric mode or either prefix changes vector semantics and requires
all three targets to be rebuilt/re-ingested.

## Rebuild Evidence

Official `lightrag-rebuild-vdb` defines these authoritative sources:

| Target | Authoritative source |
|---|---|
| entity vectors | graph nodes |
| relationship vectors | graph edges |
| chunk vectors | `text_chunks` KV |

For a vector-backend or embedding change, the documented process is to stop every
writer, preserve workspace/working directory/graph/KV and embedding configuration,
select the target vector backend, and rebuild all vector storages before restarting.
The operation drops and re-embeds targets, may incur real provider cost, is rerunnable
because sources are retained, and does not persist its own readiness marker.

Automation therefore calls the pinned `rebuild_entities_vdb`,
`rebuild_relationships_vdb` and `rebuild_chunks_vdb` library functions and validates
their structured fields (`source_total`, `prepared`, `rebuilt`, `staged`, `skipped`,
`duplicates`, `failed_batches`, `errors`). It does not trust the interactive command's
exit status. A deployment-owned five-state fence and independent collection schema/ID
set checks close the readiness gap.

## Deployment Limitations

- Milvus HTTP health at port 9091 proves more than the upstream template's TCP-listener
  check on 19530, so the selected manifest uses `/healthz`.
- Multiple Gunicorn workers inside one LightRAG service use upstream shared process
  coordination, but local JsonKV/NetworkX/JsonDocStatus are not a multi-container shared
  backend. Multiple LightRAG replicas remain unsupported.
- Milvus standalone is not Milvus distributed/HA. External managed Milvus may later
  satisfy the same LightRAG port but needs separate TLS/auth/backup validation.
- Render Blueprint provides a PostgreSQL resource but not an equivalent managed MySQL
  resource, and is not a stateful Milvus orchestrator. The repository can describe an
  external DSN; resource purchase/provisioning stays rollout work.

## Sources

- [MySQL 8.4.6 CHECK expression printing](https://github.com/mysql/mysql-server/blob/mysql-8.4.6/sql/sql_check_constraint.cc#L86-L91)
- [MySQL 8.4.6 introduced string printing](https://github.com/mysql/mysql-server/blob/mysql-8.4.6/sql/item.cc#L3584-L3637)
- [MySQL 8.4.6 `String::print` escaping](https://github.com/mysql/mysql-server/blob/mysql-8.4.6/sql-common/sql_string.cc#L949-L979)
- [MySQL 8.4.6 CHECK metadata conversion](https://github.com/mysql/mysql-server/blob/mysql-8.4.6/sql/dd/dd_table.cc#L10872-L10883)
- [LightRAG v1.5.7 release](https://github.com/HKUDS/LightRAG/releases/tag/v1.5.7)
- [LightRAG v1.5.7 source](https://github.com/HKUDS/LightRAG/tree/v1.5.7)
- [LightRAG API server storage documentation](https://github.com/HKUDS/LightRAG/blob/v1.5.7/docs/LightRAG-API-Server.md)
- [LightRAG Milvus configuration guide](https://github.com/HKUDS/LightRAG/blob/v1.5.7/docs/MilvusConfigurationGuide.md)
- [LightRAG v1.5.7 environment example](https://github.com/HKUDS/LightRAG/blob/v1.5.7/env.example)
- [LightRAG official Milvus CPU template](https://github.com/HKUDS/LightRAG/blob/v1.5.7/scripts/setup/templates/milvus.yml)
- [Milvus 2.6.11 standalone Compose](https://github.com/milvus-io/milvus/blob/v2.6.11/deployments/docker/standalone/docker-compose.yml)
- LightRAG 1.5.7 `lightrag/tools/README_REBUILD_VDB.md`, inspected from the pinned tag

The MySQL rewrite findings are based on the repository's current SQL and Go behavior.
Implementation will validate every relied-upon MySQL behavior against the pinned 8.4
container rather than treating dialect documentation as execution evidence.
