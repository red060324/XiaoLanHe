# LightRAG Milvus Operations

The normative fence contract is
`specs/20260907-mysql-milvus-migration/rebuild-fence.md`. These commands fail
closed and do not replace deployment, destructive-operation or paid-provider approval.

Run the no-network checks with:

```bash
make lightrag-static
```

`bash deploy/lightrag-contract-hash.sh` prints the deterministic contract hash without
network access, storage initialization or an embedding call. CI can export its output as
`XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256`.

For CI or a fresh local stack, set a unique generation and attempt ID, dedicated absolute
host paths for fence and writer evidence, all three API keys, explicit MinIO credentials,
and distinct Milvus bootstrap/runtime credentials. An external operator or lifecycle system must
also establish that no process outside this Compose project can write the selected
working directory or Milvus namespace, then explicitly set
`XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION=no_uncontrolled_writers`. The wrapper
independently verifies that the managed LightRAG service is absent before it creates
checksum-bound, attempt-scoped writer evidence and invokes the controller:

```bash
export XLH_LIGHTRAG_DEPLOYMENT_GENERATION=local-empty-v1
export XLH_LIGHTRAG_ATTEMPT_ID=replace-with-a-unique-attempt-id
export XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR=/absolute/path/to/lightrag-fence
export XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR=/absolute/path/to/writer-evidence
# Non-root only: optional; bootstrap defaults to this nonzero primary GID, and any
# explicit value must occur in `id -G`.
# export XLH_LIGHTRAG_SHARED_GID=$(id -g)
# Root only: replace this placeholder with a dedicated nonzero numeric GID.
# export XLH_LIGHTRAG_SHARED_GID=replace-with-dedicated-nonzero-gid
export XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION=no_uncontrolled_writers
export XLH_LIGHTRAG_API_KEY=replace-with-a-private-key-of-at-least-32-characters
export XLH_LIGHTRAG_LLM_API_KEY=replace-with-a-provider-or-stub-key
export XLH_LIGHTRAG_EMBEDDING_API_KEY=replace-with-a-provider-or-stub-key
export XLH_MILVUS_MINIO_USER=replace-with-an-isolated-minio-user
export XLH_MILVUS_MINIO_PASSWORD=replace-with-an-isolated-minio-password
export XLH_MILVUS_ROOT_PASSWORD=replace-with-a-distinct-bootstrap-root-password
export XLH_MILVUS_TOKEN=xlh_lightrag:replace-with-a-distinct-runtime-password
# For the CI host stub reachable from both LightRAG containers:
export XLH_LIGHTRAG_LLM_BASE_URL=http://host.docker.internal:18089/v1
export XLH_LIGHTRAG_EMBEDDING_BASE_URL=http://host.docker.internal:18089/v1
bootstrap_output=$(make --no-print-directory lightrag-bootstrap-empty)
printf '%s\n' "$bootstrap_output"
export XLH_LIGHTRAG_DEPLOYMENT_GENERATION=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_DEPLOYMENT_GENERATION=//p' | tail -n 1
)
export XLH_LIGHTRAG_ATTEMPT_ID=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_ATTEMPT_ID=//p' | tail -n 1
)
export XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=//p' | tail -n 1
)
export XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=//p' | tail -n 1
)
export XLH_LIGHTRAG_SHARED_GID=$(
  printf '%s\n' "$bootstrap_output" | sed -n 's/^XLH_LIGHTRAG_SHARED_GID=//p' | tail -n 1
)
test -n "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION"
test -n "$XLH_LIGHTRAG_ATTEMPT_ID"
test -n "$XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256"
test -n "$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256"
[[ "$XLH_LIGHTRAG_SHARED_GID" =~ ^[1-9][0-9]{0,9}$ ]]
(( XLH_LIGHTRAG_SHARED_GID <= 2147483647 ))
make lightrag-up
```

The bootstrap target starts only Milvus dependencies, provisions the runtime role with
Milvus root, runs the non-paid empty bootstrap, and prints four exact evidence values plus
one runtime setting, `XLH_LIGHTRAG_SHARED_GID`. The example feeds all five back into the
current shell. Persist all five with the deployment record and inject the same numeric GID
on every restart; do not recompute it from a different operator account. It refuses to
certify nonempty authoritative sources or orphaned targets. The Milvus
server uses the root password as its initialization setting and only the init job receives a
root token; steady-state LightRAG receives only the distinct runtime token, which cannot
create databases or manage users.

Before the controller starts, the wrapper uses the same pinned LightRAG image with an
overridden entry point to run one bounded root ownership initializer. It touches only the
canonical fence and writer-evidence bind mounts. It assigns the fence tree to
`1000:<shared-gid>` and the writer-evidence tree to `<operator-uid>:1000`, then converges
their modes. The wrapper first creates the new evidence file as the host operator with
mode `0600`; the initializer changes its final mode to `0640` before the controller reads
it. Before any privileged metadata change, the initializer opens both complete trees
through no-follow descriptors and rejects unexpected nodes, aliases and hard links. It
also excludes a competing initializer by locking both stable root-directory inodes before
any child mutation. Only after that read-only validation and bounded file/count accounting
succeeds does it freeze every validated directory as `root:root`/`0700`. Existing mutable
regular files are copied into private new inodes; only the new inodes are
chowned/chmodded, fsynced and atomically replaced, so a pre-held descriptor or late hard
link never receives privileged metadata changes. The two lock files are created privately
only when absent. Once present they must already have the exact owner, group and `0640`
mode and their inodes are never replaced, preserving one `flock` domain for pre-opened and
path-opened descriptors. It validates and fsyncs the child directories, stages both roots
with final ownership and mode `0000`, publishes and fsyncs the writer root, then uses a
single final `fchmod` of the fence root as the commit point while initialization leases
remain held through commit. The fence
is non-traversable before that syscall and the full tree is already prepared after it, so
safety does not depend on a fallible post-publish check or rollback. An interrupted frozen or sealed tree is never
auto-repaired by an unprivileged retry: bootstrap fails closed until an operator performs
an explicit privileged inspection and recovery. The normal LightRAG services deliberately
do not set Compose `user:`:
the official entry point must start as root, initialize and chown
`/app/data/rag_storage`, and then drop to UID/GID `1000`. Do not replace this sequence
with a blanket recursive chmod or a world-readable/world-writable mode.

`make lightrag-up` is the only supported steady-state Compose entry point. It validates
the canonical absolute fence path and exact generation/contract before starting only etcd,
MinIO, Milvus and LightRAG. The container takes a shared lease on the pre-created
`serving.lock`, repeats the fence check, and supervises the Gunicorn process group while
retaining that lease. A controller takes `serving.lock` exclusively before
`rebuild.lock`, so any mutation fails closed while steady state is serving.
Do not use bare `docker compose up`. The one-shot `milvus-init` and `lightrag-bootstrap`
services are behind the `bootstrap` profile and are not dependencies of steady state, so a
normal restart cannot rerun either bootstrap.

For a running isolated stack, validate the Milvus server and all three live collection
schemas/indexes with `bash deploy/check-lightrag-milvus.sh` and run the authenticated
API and deployment-fence check with
`bash deploy/check-lightrag-live.sh "$XLH_LIGHTRAG_BASE_URL" "$XLH_LIGHTRAG_API_KEY"
"$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR" "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION"
"$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256" "$XLH_LIGHTRAG_SHARED_GID"`. All six
arguments are mandatory so an API-only probe cannot be mistaken for complete deployment
readiness.

The controller consumes the service's LightRAG, Milvus and embedding variables plus:

```text
XLH_LIGHTRAG_IMAGE_DIGEST=sha256:...
XLH_LIGHTRAG_WORKING_DIR_ID=replace-with-stable-volume-identity
XLH_EMBEDDING_ENDPOINT_PROFILE=replace-with-versioned-non-secret-identifier
XLH_LIGHTRAG_FIXTURE_SUITE_VERSION=replace-with-version
XLH_LEGACY_KNOWLEDGE=required|absent
XLH_LEGACY_MANIFEST_SHA256=sha256:... # only when legacy import applies
```

No environment variable proves quiescence by itself. Every operation takes an
externally generated canonical `xlh.lightrag_writer_fence.v2` file plus the independently
supplied SHA-256 of its canonical JSON bytes. The bootstrap wrapper's explicit
`XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION` input records the operator's independent
namespace/volume observation; the wrapper additionally checks the managed Compose
service. The exact evidence fields bind the attempt ID, generation, fixed command
operation, contract, expiry, observed idle pipeline, zero server replicas, disabled
automatic restart, regex-safe observer and the `no_uncontrolled_writers` attestation.

Set `XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR` to a dedicated host directory outside the
LightRAG/Milvus consistency unit. The bounded ownership initializer and
bootstrap/controller bind-mount it read-write; steady LightRAG and all readiness readers
mount it read-only.
A host-run Go application sets `XLH_LIGHTRAG_REBUILD_FENCE_DIR` to that same absolute
host path and must run as the bootstrap operator or another member of the selected
shared group. A containerized Go application keeps its image's normal UID/GID, adds the
persisted GID as a supplementary group, and uses a read-only bind mount; for example:

```bash
test -n "$XLH_LIGHTRAG_SHARED_GID"
test -n "$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR"
test -n "$XLH_GO_IMAGE"
docker run --rm \
  --group-add "$XLH_LIGHTRAG_SHARED_GID" \
  --mount "type=bind,src=$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR,dst=/rebuild-fence,readonly" \
  --env XLH_LIGHTRAG_REBUILD_FENCE_DIR=/rebuild-fence \
  --env XLH_LIGHTRAG_DEPLOYMENT_GENERATION \
  --env XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256 \
  "$XLH_GO_IMAGE"
```

Do not use `--user` merely to match LightRAG UID `1000`, do not omit `readonly`, and do
not use a world-readable fence to work around a missing group. The fence root is exactly
mode `02750`; `reports/` and `attempts/` are `02750`; the writer-evidence directory is
`02750`; and `serving.lock`, `rebuild.lock`, `current.json`, writer evidence, reports and
attempt journals are `0640`. Fence paths are owned by `1000:<shared-gid>`, while
writer-evidence paths are owned by `<operator-uid>:1000`. Their setgid directories keep
new artifacts in the corresponding reader group.

Fixture and legacy evidence are independently produced canonical v2 JSON bound to the
exact attempt, generation, operation, contract and source snapshot.
The controller also creates an immutable `attempts/<attempt-id>.json` journal containing
the exact pre-operation contract context. It is written mode `0640`, exclusively and
durably before the fence enters `rebuilding`; operators and callers must never author or
replace these journal files.

The remaining command blocks show the controller CLI inside a one-shot container from
the pinned `lightrag-bootstrap` service. They assume the named environment variables,
mounts and external evidence have already been prepared by reviewed lifecycle automation,
including the bounded ownership initializer. Compose passes
`python /opt/xlh/rebuild_fence.py ...` as command arguments to the image's official entry
point. The entry point therefore performs its root data-volume initialization, drops to
UID/GID `1000`, and only then executes the controller. Do not run `rebuild_fence.py`
directly on the host, add `--entrypoint`, or add a Compose `user:` override.

For restore, create new empty volumes and use a target generation different from the
manifest's `source_generation`. While LightRAG remains stopped, `prepare-restore`
validates the complete backup envelope and every archive/member and publishes the new
attempt's mandatory stale marker. Revalidate immediately before extraction:

```bash
docker compose --profile bootstrap -f deploy/docker-compose.lightrag.yml \
  run --rm --no-deps lightrag-bootstrap \
  python /opt/xlh/rebuild_fence.py prepare-restore \
  --fence-dir /rebuild-fence \
  --expected-uid 1000 \
  --expected-gid "$XLH_LIGHTRAG_SHARED_GID" \
  --generation restore-20260907-01 \
  --attempt-id 019... \
  --writer-evidence /writer-evidence/019....writer.json \
  --writer-evidence-sha256 sha256:... \
  --backup-report /backup-evidence/knowledge-backup.json \
  --backup-report-sha256 sha256:... \
  --backup-writer-evidence /writer-evidence/backup-attempt.writer.json \
  --backup-writer-evidence-sha256 sha256:... \
  --expected-source-generation deploy-01
```

After `prepare-restore`, independently re-open the canonical report, require exactly
the four declared components, and revalidate every archive size/digest and sorted
regular-member path/size/digest immediately before extraction. Resolve exactly one new
Compose volume for each component, require each volume to be empty, then extract the
validated `working_directory`, `milvus`, `etcd`, and `minio` archives into their
respective volumes. Reject absolute/parent paths, links, devices, unsupported members,
duplicate normalized paths, or checksum drift. `deploy/check-lightrag-lifecycle.sh` is
the executable reference for the validation, empty-volume check, and extraction loop.

Then start etcd, MinIO and Milvus only, and verify the same stale marker and evidence.
The command fixes the operation to `restore_verify`; callers cannot relabel it. The
controller invokes the fixture command with its freshly enumerated source digest; the
fixture requires the restored document, source-bound chunks/entities/relationships,
managed citations in all four modes, and its declared semantic-signal quality threshold.
LightRAG may start only after this succeeds and fence readiness passes:

```bash
docker compose --profile bootstrap -f deploy/docker-compose.lightrag.yml \
  run --rm --no-deps lightrag-bootstrap \
  python /opt/xlh/rebuild_fence.py restore-verify \
  --fence-dir /rebuild-fence \
  --expected-uid 1000 \
  --expected-gid "$XLH_LIGHTRAG_SHARED_GID" \
  --generation restore-20260907-01 \
  --attempt-id 019... \
  --writer-evidence /writer-evidence/019....writer.json \
  --writer-evidence-sha256 sha256:... \
  --backup-report /backup-evidence/knowledge-backup.json \
  --backup-report-sha256 sha256:... \
  --backup-writer-evidence /writer-evidence/backup-attempt.writer.json \
  --backup-writer-evidence-sha256 sha256:... \
  --expected-source-generation deploy-01 \
  --fixture-report /tmp/xlh-restore-fixtures.json \
  --fixture-command /backup-evidence/run-approved-fixtures
```

Fresh-empty bootstrap performs no embedding call but requires external writer evidence
and empty authoritative sources and targets:

```bash
docker compose --profile bootstrap -f deploy/docker-compose.lightrag.yml \
  run --rm --no-deps lightrag-bootstrap \
  python /opt/xlh/rebuild_fence.py bootstrap \
  --fence-dir /rebuild-fence \
  --expected-uid 1000 \
  --expected-gid "$XLH_LIGHTRAG_SHARED_GID" \
  --generation deploy-01 \
  --attempt-id 019... \
  --writer-evidence /writer-evidence/019....writer.json \
  --writer-evidence-sha256 sha256:...
```

A rebuild drops all three vector targets and invokes paid embedding. Run it only after
the complete backup and explicit authorization:

```bash
docker compose --profile bootstrap -f deploy/docker-compose.lightrag.yml \
  run --rm --no-deps lightrag-bootstrap \
  python /opt/xlh/rebuild_fence.py rebuild \
  --fence-dir /rebuild-fence \
  --expected-uid 1000 \
  --expected-gid "$XLH_LIGHTRAG_SHARED_GID" \
  --generation deploy-01 \
  --attempt-id 019... \
  --writer-evidence /writer-evidence/019....writer.json \
  --writer-evidence-sha256 sha256:... \
  --approval approved-destructive-paid-rebuild \
  --backup-report /backup-evidence/knowledge-backup.json \
  --backup-report-sha256 sha256:... \
  --backup-writer-evidence /writer-evidence/backup-attempt.writer.json \
  --backup-writer-evidence-sha256 sha256:... \
  --expected-source-generation deploy-01 \
  --fixture-report /tmp/xlh-rebuild-fixtures.json \
  --legacy-report /backup-evidence/legacy-import.json \
  --legacy-report-sha256 sha256:... \
  --fixture-command /backup-evidence/run-approved-fixtures
```

The controller runs the fixture command after fresh collection verification and passes
`XLH_REBUILD_ATTEMPT_ID`, `XLH_REBUILD_GENERATION`, `XLH_REBUILD_OPERATION`,
`XLH_REBUILD_CONTRACT_SHA256` and `XLH_REBUILD_SOURCE_SHA256`. The resulting fixture
report must echo all five values, preventing reuse of evidence from an older target.

`restore-verify` and `revalidate` do not call a rebuild function, but still
perform fresh source, collection schema, exact ID-set, graph consistency and fixture
checks. A required legacy report must have a matching manifest digest, equal
`source_rows`, `accepted_rows`, `continuous_success_watermark` and `terminal_rows`,
and `reconciled: true`.

The canonical backup envelope has exact top-level keys `schema_version`,
`evidence_type=xlh.lightrag_backup.v2`, `manifest`, and `manifest_sha256`. Its manifest
binds `source_generation`, `contract_sha256`, `configuration_sha256`, and
`writer_evidence_sha256`, and contains exactly `working_directory`, `milvus`, `etcd`,
and `minio`. Each component repeats all four bindings and records the relative archive
path, byte size, `sha256:...` digest, regular-member count, canonical member-list
digest, and sorted `{path,size_bytes,sha256}` records. The report SHA passed on the CLI
is over canonical parsed envelope JSON, excluding its trailing newline. Restore rejects
absolute or parent paths, links, devices, unsupported members, duplicate normalized
paths, truncated archives, checksum drift, missing components and mixed bindings before
extraction.

`--backup-writer-evidence` and `--backup-writer-evidence-sha256` identify the distinct
canonical evidence captured when that backup was made. It uses operation `backup` and
binds the source generation and contract; restore-time writer evidence remains a
separate artifact bound to the target attempt and `restore_verify` operation.

Do not start LightRAG until the controller publishes `verified`. Raw volume backup
requires stopping LightRAG and the complete Milvus, etcd and MinIO stack.
