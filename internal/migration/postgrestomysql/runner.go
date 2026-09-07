package postgrestomysql

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

type Config struct {
	Source              *pgxpool.Pool
	Target              *sql.DB
	CheckpointDirectory string
	ToolCommit          string
	Metrics             *platformmetrics.Registry
}

type Runner struct {
	sourcePool *pgxpool.Pool
	openSource func(context.Context) (*sourceSnapshot, error)
	target     *targetDatabase
	artifacts  artifactStore
	toolCommit string
	specs      []tableSpec
	metrics    *platformmetrics.Registry
}

type preparedSource struct {
	snapshot   *sourceSnapshot
	rows       map[string][]Row
	inventory  map[string]TableInventory
	totalRows  int64
	totalBytes int64
}

func New(config Config) (*Runner, error) {
	if config.Source == nil || config.Target == nil {
		return nil, errors.New("PostgreSQL source and MySQL target are required")
	}
	if strings.TrimSpace(config.ToolCommit) == "" {
		return nil, errors.New("tool commit is required")
	}
	artifacts, err := newArtifactStore(config.CheckpointDirectory)
	if err != nil {
		return nil, err
	}
	registry := config.Metrics
	if registry == nil {
		registry = platformmetrics.Default()
	}
	runner := &Runner{sourcePool: config.Source, target: &targetDatabase{db: config.Target}, artifacts: artifacts, toolCommit: config.ToolCommit, specs: tables(), metrics: registry}
	runner.openSource = func(ctx context.Context) (*sourceSnapshot, error) {
		return beginSourceSnapshot(ctx, runner.sourcePool)
	}
	return runner, nil
}

// Close releases the pinned checkpoint-directory descriptor owned by Runner.
// It is safe to call more than once.
func (r *Runner) Close() error {
	if r == nil {
		return nil
	}
	return r.artifacts.Close()
}

func (r *Runner) Run(ctx context.Context, options Options) (report RunReport, retErr error) {
	started := time.Now()
	report = RunReport{SchemaVersion: ReportSchemaVersion, Mode: options.Mode, DryRun: !options.Execute, CopiedRows: map[string]int64{}}
	defer func() {
		registry := r.metrics
		if registry == nil {
			registry = platformmetrics.Default()
		}
		registry.ObserveMySQLCutover(mysqlCutoverObservation(report, retErr, time.Since(started)))
	}()
	if !options.Mode.Valid() {
		return report, fmt.Errorf("unsupported mode %q", options.Mode)
	}
	if options.BatchSize < 1 || options.BatchSize > 10_000 {
		return report, errors.New("batch size must be between 1 and 10000")
	}
	if options.MaxSourceRows < 1 || options.MaxSourceBytes < 1 {
		return report, errors.New("positive max source rows and bytes are required")
	}
	report.MaxSourceRows, report.MaxSourceBytes = options.MaxSourceRows, options.MaxSourceBytes
	if (options.Mode == ModeInspect || options.Mode == ModeVerify) && options.Execute {
		return report, fmt.Errorf("mode %s is read-only and does not accept execute", options.Mode)
	}
	if options.Execute && options.Mode != ModeCopy && options.Mode != ModeResume {
		return report, errors.New("only copy and resume accept execute")
	}
	if options.Mode == ModeVerify && strings.TrimSpace(options.FreezeAttestationPath) != "" {
		return report, errors.New("verify accepts only the manifest-embedded source-freeze attestation")
	}
	if options.Mode == ModeVerify && strings.TrimSpace(options.TargetWriterFencePath) != "" {
		return report, errors.New("verify accepts only the manifest-embedded target-writer-fence attestation")
	}
	if options.Execute || options.Mode == ModeVerify {
		if strings.TrimSpace(options.FreezeKeyID) == "" || strings.TrimSpace(options.TargetWriterFenceKeyID) == "" ||
			options.FreezeKeyID == options.TargetWriterFenceKeyID || bytes.Equal(options.FreezePublicKey, options.TargetWriterFencePublicKey) {
			return report, errors.New("source-freeze and target-writer-fence trust inputs must be present and independent")
		}
		if strings.TrimSpace(options.TargetDeploymentGeneration) == "" {
			return report, errors.New("target deployment generation is required")
		}
	}

	if r.openSource == nil {
		return report, errors.New("PostgreSQL source snapshot opener is not configured")
	}

	// Mutating runs create (once) and hold the checkpoint lock. Verify and
	// resume dry-runs may only open an existing lock file, preserving their
	// read-only filesystem contract while excluding a concurrent writer.
	var checkpointLock *artifactLock
	var err error
	switch {
	case options.Execute:
		checkpointLock, err = r.artifacts.acquireExclusiveLock()
	case options.Mode == ModeVerify || options.Mode == ModeResume:
		checkpointLock, err = r.artifacts.acquireExistingExclusiveLock()
	}
	if err != nil {
		return report, err
	}
	if checkpointLock != nil {
		defer func() { retErr = errors.Join(retErr, checkpointLock.Close()) }()
	}

	// Execute holds one target-wide named lock on a dedicated connection from
	// before identity inspection until every artifact has been published.
	var coordinates targetCoordinates
	if options.Execute {
		targetLock, lockedCoordinates, lockErr := acquireTargetRunLock(ctx, r.target.db)
		if lockErr != nil {
			return report, lockErr
		}
		coordinates = lockedCoordinates
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			retErr = errors.Join(retErr, targetLock.Release(releaseCtx))
		}()
	}

	// Every non-mutating mode uses one target transaction. In verify this same
	// snapshot covers identity, checkpoint validation and reconciliation.
	var targetReader sqlReader = r.target
	var readOnlyTarget *targetSnapshot
	if !options.Execute {
		readOnlyTarget, err = beginTargetSnapshot(ctx, r.target.db)
		if err != nil {
			return report, err
		}
		targetReader = readOnlyTarget
		defer func() { retErr = errors.Join(retErr, readOnlyTarget.Close()) }()
	}

	source, err := r.openSource(ctx)
	if err != nil {
		return report, err
	}
	defer source.Close(context.WithoutCancel(ctx))
	targetIdentity, err := inspectTargetIdentityForTables(ctx, targetReader, coordinates, tables())
	if err != nil {
		return report, err
	}
	allRows, inventory, totalRows, totalBytes, err := inspectSource(ctx, source, r.specs, options.MaxSourceRows, options.MaxSourceBytes)
	if err != nil {
		return report, err
	}
	report.SourceInventory = inventory
	report.SourceRows, report.SourceCanonicalBytes = totalRows, totalBytes
	report.SourceChecks, err = sourceChecks(ctx, source)
	if err != nil {
		return report, err
	}
	var freezeAttestation SourceFreezeAttestation
	var targetWriterFence TargetWriterFenceEvidence
	var verifiedManifest *Manifest
	if options.Execute {
		freezeAttestation, err = loadAndVerifyFreezeAttestation(options.FreezeAttestationPath, options.FreezePublicKey, options.FreezeKeyID, source.identity, nowUTC())
		if err != nil {
			report.Message = "source write-freeze attestation failed; no target data was changed"
			return report, err
		}
		source.identity.ClusterID = freezeAttestation.Source.OperatorClusterID
		targetWriterFence, err = loadAndVerifyTargetWriterFence(options.TargetWriterFencePath, options.TargetWriterFencePublicKey, options.TargetWriterFenceKeyID, options.TargetDeploymentGeneration, targetIdentity, nowUTC())
		if err != nil {
			report.Message = "target writer fence failed; no target data was changed"
			return report, err
		}
	} else if options.Mode == ModeVerify {
		manifest, loadErr := r.artifacts.loadManifest()
		if loadErr != nil {
			return report, loadErr
		}
		freezeSHA, digestErr := freezeAttestationDigest(manifest.SourceFreezeAttestation)
		if digestErr != nil || manifest.SourceFreezeSHA256 == "" || manifest.SourceFreezeSHA256 != freezeSHA {
			return report, fmt.Errorf("manifest source-freeze evidence digest mismatch: %w", ErrIdentityMismatch)
		}
		if verifyErr := verifyFreezeAttestation(manifest.SourceFreezeAttestation, options.FreezePublicKey, options.FreezeKeyID, source.identity, nowUTC()); verifyErr != nil {
			return report, fmt.Errorf("verify manifest source-freeze evidence: %w", verifyErr)
		}
		freezeAttestation = manifest.SourceFreezeAttestation
		source.identity.ClusterID = freezeAttestation.Source.OperatorClusterID
		if verifyErr := verifyTargetWriterFenceEvidence(manifest.TargetWriterFence, options.TargetWriterFencePublicKey, options.TargetWriterFenceKeyID, options.TargetDeploymentGeneration, targetIdentity, nowUTC()); verifyErr != nil {
			return report, fmt.Errorf("verify manifest target-writer-fence evidence: %w", verifyErr)
		}
		targetWriterFence = manifest.TargetWriterFence
		verifiedManifest = &manifest
	}
	identity := CutoverIdentity{SchemaVersion: ManifestSchemaVersion, ToolVersion: ToolVersion, ToolCommit: r.toolCommit, Source: source.identity, Target: targetIdentity}
	// A resume dry-run may recover the cluster label from its manifest for
	// identity comparison; execute and verify authenticate it first.
	if !options.Execute && options.Mode == ModeResume {
		existing, loadErr := r.artifacts.loadManifest()
		if loadErr != nil {
			return report, loadErr
		}
		identity.Source.ClusterID = existing.Identity.Source.ClusterID
	}
	if options.Execute || options.Mode == ModeResume || options.Mode == ModeVerify {
		if err := identity.Validate(); err != nil {
			return report, err
		}
	}
	identitySHA, err := identityDigest(identity)
	if err != nil {
		return report, err
	}
	report.Identity = identity
	if invalidChecks(report.SourceChecks) > 0 {
		report.Message = "source preflight failed; no target data was changed"
		if options.Execute {
			preflight := reconciliationFromSourceChecks(identitySHA, report.SourceChecks)
			if err := r.artifacts.saveReport(preflight); err != nil {
				return report, err
			}
			report.Reconciliation = &preflight
		}
		return report, ErrReconciliationMismatch
	}
	switch options.Mode {
	case ModeInspect:
		report.Message = "inspection complete; no target data was changed"
		return report, nil
	case ModeCopy:
		exists, err := r.artifacts.manifestExists()
		if err != nil {
			return report, err
		}
		if exists {
			return report, errors.New("cutover manifest already exists; use resume or a new empty checkpoint directory")
		}
		if err := r.requireEmptyTarget(ctx); err != nil {
			return report, err
		}
		if !options.Execute {
			report.Message = "copy dry run complete; rerun with --execute to create the manifest and copy"
			return report, nil
		}
		manifest := newManifest(identity, identitySHA, inventory, r.specs)
		manifest.SourceFreezeAttestation = freezeAttestation
		manifest.SourceFreezeSHA256, err = freezeAttestationDigest(freezeAttestation)
		if err != nil {
			return report, err
		}
		manifest.TargetWriterFence = targetWriterFence
		if err := r.artifacts.saveManifest(manifest); err != nil {
			return report, err
		}
		return r.executeCopy(ctx, report, source, allRows, manifest, options)
	case ModeResume:
		manifest, err := r.loadBoundManifest(identity, inventory)
		if err != nil {
			return report, err
		}
		if err := r.validateCheckpoints(ctx, allRows, manifest); err != nil {
			return report, err
		}
		if options.Execute {
			freezeSHA, digestErr := freezeAttestationDigest(freezeAttestation)
			if digestErr != nil || manifest.SourceFreezeSHA256 != freezeSHA || manifest.SourceFreezeAttestation.AttestationID != freezeAttestation.AttestationID {
				return report, fmt.Errorf("resume source-freeze evidence changed: %w", ErrIdentityMismatch)
			}
			if err := verifyTargetWriterFenceEvidenceBinding(manifest.TargetWriterFence, options.TargetWriterFencePublicKey, options.TargetWriterFenceKeyID, options.TargetDeploymentGeneration, targetIdentity); err != nil {
				return report, fmt.Errorf("resume manifest target-writer-fence evidence: %w", err)
			}
			// A newly issued target fence is allowed for resume, but it must be
			// authenticated for the same target identity and generation before
			// replacing the manifest binding and before any target mutation.
			manifest.TargetWriterFence = targetWriterFence
			if err := r.artifacts.saveManifest(manifest); err != nil {
				return report, err
			}
		}
		if !options.Execute {
			report.Message = "resume dry run complete; checkpoint and committed target batches match"
			return report, nil
		}
		return r.executeCopy(ctx, report, source, allRows, manifest, options)
	case ModeVerify:
		if verifiedManifest == nil {
			return report, errors.New("verify has no authenticated manifest")
		}
		manifest, err := r.bindLoadedManifest(*verifiedManifest, identity, inventory)
		if err != nil {
			return report, err
		}
		if err := r.validateCompleteManifest(ctx, targetReader, allRows, manifest); err != nil {
			return report, err
		}
		storedReport, err := r.artifacts.loadReport(manifest.ReconciliationSHA256)
		if err != nil {
			return report, err
		}
		if err := r.validateStoredReconciliation(storedReport, manifest); err != nil {
			return report, err
		}
		return r.verifyReadOnly(ctx, report, source, targetReader.(targetReconciliationReader), manifest, options.MaxSourceBytes)
	default:
		return report, fmt.Errorf("unsupported mode %q", options.Mode)
	}
}

func mysqlCutoverObservation(report RunReport, err error, duration time.Duration) platformmetrics.MySQLCutoverObservation {
	observation := platformmetrics.MySQLCutoverObservation{
		Operation: string(report.Mode), Outcome: mysqlCutoverOutcome(err), Duration: duration,
	}
	// A source-only preflight report intentionally uses DeferredPending and is
	// not a complete source/target reconciliation snapshot.
	if report.Reconciliation == nil || report.Reconciliation.DeferredForeignKeys != DeferredComplete {
		return observation
	}
	observation.HasReconciliation = true
	observation.Ready = report.Reconciliation.CutoverReady
	observation.SourceRows = 0
	for _, table := range report.Reconciliation.Tables {
		observation.SourceRows = addTelemetryCount(observation.SourceRows, table.SourceRows)
		observation.TargetRows = addTelemetryCount(observation.TargetRows, table.TargetRows)
	}
	observation.MismatchCount = int64(max(0, report.Reconciliation.MismatchCount))
	return observation
}

func mysqlCutoverOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, ErrIdentityMismatch), errors.Is(err, ErrCheckpointMismatch), errors.Is(err, ErrReconciliationMismatch), errors.Is(err, ErrTargetLockUnavailable), errors.Is(err, ErrCheckpointLocked):
		return "rejected"
	default:
		return "error"
	}
}

func addTelemetryCount(total, value int64) int64 {
	if value <= 0 {
		return total
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if total > maxInt64-value {
		return maxInt64
	}
	return total + value
}

func reconciliationFromSourceChecks(identitySHA string, checks []CheckResult) ReconciliationReport {
	report := ReconciliationReport{
		SchemaVersion: ReportSchemaVersion, IdentitySHA256: identitySHA, GeneratedAt: nowUTC(),
		DeferredForeignKeys: DeferredPending, Checks: append([]CheckResult(nil), checks...),
		MismatchCount: invalidChecks(checks), CutoverReady: false,
	}
	// This source-only preflight artifact is deliberately not finalized as a
	// full reconciliation. Pending deferred foreign keys block readiness but do
	// not count as a failed check because that phase has not run.
	digest, err := digestJSON(report)
	if err == nil {
		report.DigestSHA256 = digest
	}
	return report
}

// revalidateFrozenSource acquires a fresh transaction and re-enumerates every
// canonical row immediately before an execute run can be finalized. Both the
// signed freeze evidence and the independently observed source identity must
// still match the original run.
func (r *Runner) revalidateFrozenSource(ctx context.Context, original SourceIdentity, options Options) error {
	fresh, err := r.openSource(ctx)
	if err != nil {
		return err
	}
	defer fresh.Close(context.WithoutCancel(ctx))
	_, _, _, _, err = inspectSource(ctx, fresh, r.specs, options.MaxSourceRows, options.MaxSourceBytes)
	if err != nil {
		return err
	}
	attestation, err := loadAndVerifyFreezeAttestation(options.FreezeAttestationPath, options.FreezePublicKey, options.FreezeKeyID, fresh.identity, nowUTC())
	if err != nil {
		return err
	}
	fresh.identity.ClusterID = attestation.Source.OperatorClusterID
	if original.ClusterID != fresh.identity.ClusterID || original.DatabaseID != fresh.identity.DatabaseID || original.SchemaSHA256 != fresh.identity.SchemaSHA256 || original.SnapshotID != fresh.identity.SnapshotID {
		return fmt.Errorf("source changed after write-freeze attestation: %w", ErrIdentityMismatch)
	}
	return nil
}

// revalidateTargetWriterFence reloads the externally controlled artifact and
// re-inspects the live target. The exact evidence must remain the version bound
// into the manifest for this run; only resume's pre-write setup may replace it.
func (r *Runner) revalidateTargetWriterFence(ctx context.Context, original TargetIdentity, expected TargetWriterFenceEvidence, options Options) error {
	freshTarget, err := inspectTargetIdentityForTables(ctx, r.target, targetCoordinates{InstanceID: original.InstanceID, DatabaseID: original.DatabaseID}, tables())
	if err != nil {
		return err
	}
	if freshTarget != original {
		return fmt.Errorf("target identity changed after target-writer fence: %w", ErrIdentityMismatch)
	}
	freshEvidence, err := loadAndVerifyTargetWriterFence(options.TargetWriterFencePath, options.TargetWriterFencePublicKey, options.TargetWriterFenceKeyID, options.TargetDeploymentGeneration, freshTarget, nowUTC())
	if err != nil {
		return err
	}
	if freshEvidence != expected {
		return fmt.Errorf("target-writer-fence evidence changed during execution: %w", ErrIdentityMismatch)
	}
	return nil
}

func newManifest(identity CutoverIdentity, identitySHA string, inventory map[string]TableInventory, specs []tableSpec) Manifest {
	now := nowUTC()
	return Manifest{
		SchemaVersion: ManifestSchemaVersion, Identity: identity, IdentitySHA256: identitySHA, CreatedAt: now, UpdatedAt: now,
		TableOrder: tableNames(specs), SourceInventory: inventory, Checkpoints: map[string][]TableCheckpoint{},
		DeferredCheckpoints: map[string][]DeferredCheckpoint{}, DeferredForeignKeys: DeferredPending, AutoIncrementCompleted: map[string]bool{},
	}
}

func (r *Runner) loadBoundManifest(identity CutoverIdentity, inventory map[string]TableInventory) (Manifest, error) {
	manifest, err := r.artifacts.loadManifest()
	if err != nil {
		return Manifest{}, err
	}
	return r.bindLoadedManifest(manifest, identity, inventory)
}

func (r *Runner) bindLoadedManifest(manifest Manifest, identity CutoverIdentity, inventory map[string]TableInventory) (Manifest, error) {
	if manifest.SchemaVersion != ManifestSchemaVersion || !manifest.Identity.BoundEqual(identity) {
		return Manifest{}, ErrIdentityMismatch
	}
	digest, err := identityDigest(identity)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.IdentitySHA256 != digest || !equalInventory(manifest.SourceInventory, inventory) {
		return Manifest{}, ErrIdentityMismatch
	}
	if strings.Join(manifest.TableOrder, "\x00") != strings.Join(tableNames(r.specs), "\x00") {
		return Manifest{}, ErrIdentityMismatch
	}
	return manifest, nil
}

func (r *Runner) validateStoredReconciliation(report ReconciliationReport, manifest Manifest) error {
	if report.SchemaVersion != ReportSchemaVersion || report.IdentitySHA256 != manifest.IdentitySHA256 ||
		report.DeferredForeignKeys != DeferredComplete || !report.CutoverReady || report.MismatchCount != 0 || report.GeneratedAt.IsZero() {
		return fmt.Errorf("stored reconciliation report is not successful or identity-bound: %w", ErrCheckpointMismatch)
	}
	if len(report.Tables) != len(r.specs) {
		return fmt.Errorf("stored reconciliation table coverage differs: %w", ErrCheckpointMismatch)
	}
	for index, table := range r.specs {
		item := report.Tables[index]
		source := manifest.SourceInventory[table.name]
		if item.Table != table.name || !item.Match || item.SourceRows != item.TargetRows || item.SourceDigest != item.TargetDigest ||
			item.SourceRows != source.Rows || item.SourceDigest != source.DigestSHA256 {
			return fmt.Errorf("stored reconciliation table %s is inconsistent: %w", table.name, ErrCheckpointMismatch)
		}
	}
	expectedChecks := invariants()
	autoCount := 0
	for _, table := range r.specs {
		if table.autoIncrement {
			autoCount++
		}
	}
	if len(report.Checks) != len(expectedChecks)+autoCount {
		return fmt.Errorf("stored reconciliation check coverage differs: %w", ErrCheckpointMismatch)
	}
	for index, expected := range expectedChecks {
		item := report.Checks[index]
		if item.Category != expected.category || item.Name != expected.name || !item.Match || item.SourceMismatches != 0 || item.TargetMismatches != 0 {
			return fmt.Errorf("stored reconciliation check %s is inconsistent: %w", expected.name, ErrCheckpointMismatch)
		}
	}
	checkIndex := len(expectedChecks)
	for _, table := range r.specs {
		if !table.autoIncrement {
			continue
		}
		item := report.Checks[checkIndex]
		checkIndex++
		if item.Category != "auto_increment" || item.Name != table.name || !item.Match || item.SourceMismatches != 0 || item.TargetMismatches != 0 {
			return fmt.Errorf("stored AUTO_INCREMENT check %s is inconsistent: %w", table.name, ErrCheckpointMismatch)
		}
	}
	expectedMetrics := metrics()
	if len(report.Metrics) != len(expectedMetrics) {
		return fmt.Errorf("stored reconciliation metric coverage differs: %w", ErrCheckpointMismatch)
	}
	for index, expected := range expectedMetrics {
		item := report.Metrics[index]
		if item.Category != expected.category || item.Name != expected.name || !item.Match || item.Source != item.Target {
			return fmt.Errorf("stored reconciliation metric %s is inconsistent: %w", expected.name, ErrCheckpointMismatch)
		}
	}
	recomputed := report
	finalizeReport(&recomputed)
	if !reflect.DeepEqual(recomputed, report) {
		return fmt.Errorf("stored reconciliation summary is inconsistent: %w", ErrCheckpointMismatch)
	}
	return nil
}

func equalInventory(left, right map[string]TableInventory) bool {
	leftDigest, _ := digestJSON(left)
	rightDigest, _ := digestJSON(right)
	return leftDigest == rightDigest
}

func identityDigest(identity CutoverIdentity) (string, error) {
	return digestJSON(struct {
		SchemaVersion string `json:"schemaVersion"`
		ToolVersion   string `json:"toolVersion"`
		ToolCommit    string `json:"toolCommit"`
		Source        struct {
			ClusterID, DatabaseID, SnapshotID, SchemaSHA256 string
		} `json:"source"`
		Target TargetIdentity `json:"target"`
	}{
		SchemaVersion: identity.SchemaVersion, ToolVersion: identity.ToolVersion, ToolCommit: identity.ToolCommit,
		Source: struct{ ClusterID, DatabaseID, SnapshotID, SchemaSHA256 string }{identity.Source.ClusterID, identity.Source.DatabaseID, identity.Source.SnapshotID, identity.Source.SchemaSHA256},
		Target: identity.Target,
	})
}

func (r *Runner) requireEmptyTarget(ctx context.Context) error {
	for _, table := range r.specs {
		var count int64
		if err := r.target.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table.name+"`").Scan(&count); err != nil {
			return fmt.Errorf("inspect MySQL %s: %w", table.name, err)
		}
		if count != 0 {
			return fmt.Errorf("MySQL target table %s is not empty; copy requires a fresh target", table.name)
		}
	}
	return nil
}

func (r *Runner) executeCopy(ctx context.Context, report RunReport, source *sourceSnapshot, allRows map[string][]Row, manifest Manifest, options Options) (RunReport, error) {
	batchSize, maxBytes := options.BatchSize, options.MaxSourceBytes
	for _, table := range r.specs {
		sourceRows := allRows[table.name]
		rows := table.initialRows(sourceRows)
		completed := completedRows(manifest.Checkpoints[table.name])
		for completed < len(rows) {
			end := completed + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			batch := rows[completed:end]
			if err := r.revalidateTargetWriterFence(ctx, manifest.Identity.Target, manifest.TargetWriterFence, options); err != nil {
				report.Message = "target writer fence changed before copied batch; cutover remains blocked"
				return report, err
			}
			if err := r.target.writeBatch(ctx, table, batch); err != nil {
				return report, err
			}
			if err := r.target.verifyBatch(ctx, table, batch); err != nil {
				return report, err
			}
			checkpoint, err := makeTableCheckpoint(manifest.IdentitySHA256, table, sourceRows, rows, completed, end)
			if err != nil {
				return report, err
			}
			manifest.Checkpoints[table.name] = append(manifest.Checkpoints[table.name], checkpoint)
			if err := r.artifacts.saveManifest(manifest); err != nil {
				return report, err
			}
			completed = end
		}
		report.CopiedRows[table.name] = int64(completed)
	}
	for _, table := range r.specs {
		if len(table.deferredColumns()) == 0 {
			continue
		}
		rows := allRows[table.name]
		completed := completedDeferredRows(manifest.DeferredCheckpoints[table.name])
		for completed < len(rows) {
			end := completed + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			batch := rows[completed:end]
			if err := r.revalidateTargetWriterFence(ctx, manifest.Identity.Target, manifest.TargetWriterFence, options); err != nil {
				report.Message = "target writer fence changed before deferred batch; cutover remains blocked"
				return report, err
			}
			if err := r.target.writeDeferredBatch(ctx, table, batch); err != nil {
				return report, err
			}
			if err := r.target.verifyBatch(ctx, table, batch); err != nil {
				return report, err
			}
			checkpoint, err := makeDeferredCheckpoint(table, rows, completed, end)
			if err != nil {
				return report, err
			}
			manifest.DeferredCheckpoints[table.name] = append(manifest.DeferredCheckpoints[table.name], checkpoint)
			if err := r.artifacts.saveManifest(manifest); err != nil {
				return report, err
			}
			completed = end
		}
	}
	manifest.DeferredForeignKeys = DeferredComplete
	for name, checkpoints := range manifest.Checkpoints {
		for i := range checkpoints {
			checkpoints[i].DeferredForeignKeyState = DeferredComplete
		}
		manifest.Checkpoints[name] = checkpoints
	}
	if err := r.artifacts.saveManifest(manifest); err != nil {
		return report, err
	}
	preflight, err := r.buildConsistentReconciliation(ctx, source, manifest, maxBytes, false)
	if err != nil {
		return report, err
	}
	if !preflight.CutoverReady {
		if err := r.artifacts.saveReport(preflight); err != nil {
			return report, err
		}
		report.Reconciliation = &preflight
		report.Message = "copy committed but reconciliation failed; cutover remains blocked"
		return report, ErrReconciliationMismatch
	}
	for _, table := range r.specs {
		if manifest.AutoIncrementCompleted[table.name] || !table.autoIncrement {
			continue
		}
		if err := r.revalidateTargetWriterFence(ctx, manifest.Identity.Target, manifest.TargetWriterFence, options); err != nil {
			report.Message = "target writer fence changed before auto-increment update; cutover remains blocked"
			return report, err
		}
		if err := r.target.advanceAutoIncrement(ctx, table, allRows[table.name]); err != nil {
			return report, err
		}
		manifest.AutoIncrementCompleted[table.name] = true
		if err := r.artifacts.saveManifest(manifest); err != nil {
			return report, err
		}
	}
	for _, table := range r.specs {
		if !table.autoIncrement {
			manifest.AutoIncrementCompleted[table.name] = true
		}
	}
	if err := r.revalidateFrozenSource(ctx, source.identity, options); err != nil {
		report.Message = "source write-freeze changed during copy; cutover remains blocked"
		return report, err
	}
	return r.reconcile(ctx, report, source, manifest, options)
}

func (r *Runner) buildConsistentReconciliation(ctx context.Context, source *sourceSnapshot, manifest Manifest, maxBytes int64, includeAutoIncrement bool) (ReconciliationReport, error) {
	snapshot, err := beginTargetSnapshot(ctx, r.target.db)
	if err != nil {
		return ReconciliationReport{}, err
	}
	defer snapshot.Close()
	reconciliation, err := buildReconciliation(ctx, source, snapshot, r.specs, manifest.SourceInventory, maxBytes, manifest.IdentitySHA256, manifest.DeferredForeignKeys)
	if err != nil {
		return ReconciliationReport{}, err
	}
	if includeAutoIncrement {
		autoChecks, autoErr := verifyAutoIncrements(ctx, snapshot, r.specs)
		if autoErr != nil {
			return ReconciliationReport{}, autoErr
		}
		reconciliation.Checks = append(reconciliation.Checks, autoChecks...)
	}
	finalizeReport(&reconciliation)
	return reconciliation, nil
}

func (r *Runner) reconcile(ctx context.Context, report RunReport, source *sourceSnapshot, manifest Manifest, options Options) (RunReport, error) {
	reconciliation, err := r.buildConsistentReconciliation(ctx, source, manifest, options.MaxSourceBytes, true)
	if err != nil {
		return report, err
	}
	if !reconciliation.CutoverReady {
		if err := r.artifacts.saveReport(reconciliation); err != nil {
			return report, err
		}
		report.Reconciliation = &reconciliation
		report.Message = "reconciliation mismatch; cutover remains blocked"
		return report, ErrReconciliationMismatch
	}
	if err := r.revalidateTargetWriterFence(ctx, manifest.Identity.Target, manifest.TargetWriterFence, options); err != nil {
		report.Message = "target writer fence changed before successful reconciliation publication; cutover remains blocked"
		return report, err
	}
	if err := r.artifacts.saveReport(reconciliation); err != nil {
		return report, err
	}
	manifest.ReconciliationSHA256 = reconciliation.DigestSHA256
	manifest.Completed = true
	if err := r.revalidateTargetWriterFence(ctx, manifest.Identity.Target, manifest.TargetWriterFence, options); err != nil {
		report.Message = "target writer fence changed before Completed/CutoverReady publication; cutover remains blocked"
		return report, err
	}
	if err := r.artifacts.saveManifest(manifest); err != nil {
		return report, err
	}
	report.Reconciliation = &reconciliation
	report.CutoverReady = true
	report.Message = "copy and reconciliation complete; this tool did not switch production, enable writes, or delete PostgreSQL data"
	return report, nil
}

func (r *Runner) verifyReadOnly(ctx context.Context, report RunReport, source *sourceSnapshot, target targetReconciliationReader, manifest Manifest, maxBytes int64) (RunReport, error) {
	reconciliation, err := buildReconciliation(ctx, source, target, r.specs, manifest.SourceInventory, maxBytes, manifest.IdentitySHA256, manifest.DeferredForeignKeys)
	if err != nil {
		return report, err
	}
	autoChecks, err := verifyAutoIncrements(ctx, target, r.specs)
	if err != nil {
		return report, err
	}
	reconciliation.Checks = append(reconciliation.Checks, autoChecks...)
	finalizeReport(&reconciliation)
	report.Reconciliation = &reconciliation
	report.CutoverReady = reconciliation.CutoverReady
	if !reconciliation.CutoverReady {
		report.Message = "read-only verification found reconciliation mismatch; cutover remains blocked"
		return report, ErrReconciliationMismatch
	}
	report.Message = "read-only verification complete; no manifest or report was written"
	return report, nil
}

func invalidChecks(checks []CheckResult) int {
	count := 0
	for _, check := range checks {
		if !check.Match {
			count++
		}
	}
	return count
}

func completedRows(checkpoints []TableCheckpoint) int {
	if len(checkpoints) == 0 {
		return 0
	}
	return int(checkpoints[len(checkpoints)-1].SourceRows)
}

func completedDeferredRows(checkpoints []DeferredCheckpoint) int {
	if len(checkpoints) == 0 {
		return 0
	}
	return int(checkpoints[len(checkpoints)-1].CommittedRows)
}

func makeTableCheckpoint(identitySHA string, table tableSpec, sourceRows, targetRows []Row, start, end int) (TableCheckpoint, error) {
	checkpoint := TableCheckpoint{IdentitySHA256: identitySHA, Table: table.name, SourceRows: int64(end), BatchSourceRows: int64(end - start), TargetCommittedRows: int64(end), DeferredForeignKeyState: DeferredPending, CommittedAt: nowUTC()}
	if start > 0 {
		checkpoint.BatchStartPrimaryKey, _ = keyOf(table, sourceRows[start-1])
	}
	if end > start {
		checkpoint.LastSourcePrimaryKey, _ = keyOf(table, sourceRows[end-1])
	}
	checkpoint.BatchSourceSHA256 = canonicalDigest(sourceRows[start:end])
	checkpoint.TargetCommittedSHA256 = canonicalDigest(targetRows[start:end])
	return checkpoint, nil
}

func makeDeferredCheckpoint(table tableSpec, rows []Row, start, end int) (DeferredCheckpoint, error) {
	checkpoint := DeferredCheckpoint{Table: table.name, CommittedRows: int64(end), BatchSHA256: canonicalDigest(rows[start:end]), CommittedAt: nowUTC()}
	if start > 0 {
		checkpoint.BatchStartPrimaryKey, _ = keyOf(table, rows[start-1])
	}
	if end > start {
		checkpoint.LastSourcePrimaryKey, _ = keyOf(table, rows[end-1])
	}
	return checkpoint, nil
}

func (r *Runner) validateCheckpoints(ctx context.Context, allRows map[string][]Row, manifest Manifest) error {
	return r.validateCheckpointsWithReader(ctx, r.target, allRows, manifest)
}

func (r *Runner) validateCheckpointsWithReader(ctx context.Context, target sqlReader, allRows map[string][]Row, manifest Manifest) error {
	for _, table := range r.specs {
		sourceRows := allRows[table.name]
		rows := table.initialRows(sourceRows)
		deferredRows := completedDeferredRows(manifest.DeferredCheckpoints[table.name])
		if deferredRows < 0 || deferredRows > len(rows) {
			return fmt.Errorf("invalid %s deferred checkpoint bounds: %w", table.name, ErrCheckpointMismatch)
		}
		previous := 0
		for _, checkpoint := range manifest.Checkpoints[table.name] {
			end := int(checkpoint.SourceRows)
			if checkpoint.IdentitySHA256 != manifest.IdentitySHA256 || checkpoint.Table != table.name || end <= previous || end > len(rows) {
				return fmt.Errorf("invalid %s checkpoint bounds: %w", table.name, ErrCheckpointMismatch)
			}
			expected, _ := makeTableCheckpoint(manifest.IdentitySHA256, table, sourceRows, rows, previous, end)
			wantDeferred := DeferredPending
			if manifest.DeferredForeignKeys == DeferredComplete {
				wantDeferred = DeferredComplete
			}
			if checkpoint.BatchSourceRows != expected.BatchSourceRows || checkpoint.BatchSourceSHA256 != expected.BatchSourceSHA256 || checkpoint.TargetCommittedRows != expected.TargetCommittedRows || checkpoint.TargetCommittedSHA256 != expected.TargetCommittedSHA256 || checkpoint.DeferredForeignKeyState != wantDeferred || !sameCursor(checkpoint.BatchStartPrimaryKey, expected.BatchStartPrimaryKey) || !sameCursor(checkpoint.LastSourcePrimaryKey, expected.LastSourcePrimaryKey) {
				return fmt.Errorf("invalid %s checkpoint digest: %w", table.name, ErrCheckpointMismatch)
			}
			finalizedInBatch := deferredRows - previous
			if finalizedInBatch < 0 {
				finalizedInBatch = 0
			}
			if finalizedInBatch > end-previous {
				finalizedInBatch = end - previous
			}
			if err := verifyTargetResumableRows(ctx, target, table, rows[previous:end], sourceRows[previous:end], finalizedInBatch); err != nil {
				return err
			}
			previous = end
		}
		deferredPrevious := 0
		for _, checkpoint := range manifest.DeferredCheckpoints[table.name] {
			end := int(checkpoint.CommittedRows)
			if checkpoint.Table != table.name || end <= deferredPrevious || end > len(sourceRows) {
				return fmt.Errorf("invalid %s deferred checkpoint bounds: %w", table.name, ErrCheckpointMismatch)
			}
			expected, _ := makeDeferredCheckpoint(table, sourceRows, deferredPrevious, end)
			if checkpoint.BatchSHA256 != expected.BatchSHA256 || !sameCursor(checkpoint.BatchStartPrimaryKey, expected.BatchStartPrimaryKey) || !sameCursor(checkpoint.LastSourcePrimaryKey, expected.LastSourcePrimaryKey) {
				return fmt.Errorf("invalid %s deferred checkpoint digest: %w", table.name, ErrCheckpointMismatch)
			}
			deferredPrevious = end
		}
	}
	return nil
}

func (r *Runner) validateCompleteManifest(ctx context.Context, target sqlReader, allRows map[string][]Row, manifest Manifest) error {
	if !manifest.Completed || strings.TrimSpace(manifest.ReconciliationSHA256) == "" {
		return fmt.Errorf("manifest has no completed reconciliation evidence: %w", ErrCheckpointMismatch)
	}
	if err := r.validateCheckpointsWithReader(ctx, target, allRows, manifest); err != nil {
		return err
	}
	for _, table := range r.specs {
		if completedRows(manifest.Checkpoints[table.name]) != len(allRows[table.name]) {
			return fmt.Errorf("table %s copy is incomplete: %w", table.name, ErrCheckpointMismatch)
		}
		if len(table.deferredColumns()) > 0 && completedDeferredRows(manifest.DeferredCheckpoints[table.name]) != len(allRows[table.name]) {
			return fmt.Errorf("table %s deferred links are incomplete: %w", table.name, ErrCheckpointMismatch)
		}
		if !manifest.AutoIncrementCompleted[table.name] {
			return fmt.Errorf("table %s AUTO_INCREMENT phase is incomplete: %w", table.name, ErrCheckpointMismatch)
		}
	}
	if manifest.DeferredForeignKeys != DeferredComplete {
		return fmt.Errorf("deferred foreign keys are not complete: %w", ErrCheckpointMismatch)
	}
	return nil
}

func sameCursor(left, right Cursor) bool {
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}
