// Package postgrestomysql implements the operator-only, one-way business-data
// cutover from a frozen PostgreSQL source to a migrated MySQL target. It is
// deliberately isolated from runtime repositories.
package postgrestomysql

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	ManifestSchemaVersion = "xiaolanhe-postgres-mysql-cutover-manifest/v1"
	ReportSchemaVersion   = "xiaolanhe-postgres-mysql-reconciliation/v1"
	ToolVersion           = "postgres-to-mysql-cutover/v1"
)

var (
	ErrIdentityMismatch       = errors.New("cutover identity mismatch")
	ErrCheckpointMismatch     = errors.New("cutover checkpoint mismatch")
	ErrReconciliationMismatch = errors.New("cutover reconciliation mismatch")
	ErrTargetLockUnavailable  = errors.New("target cutover lock unavailable")
)

type Mode string

const (
	ModeInspect Mode = "inspect"
	ModeCopy    Mode = "copy"
	ModeResume  Mode = "resume"
	ModeVerify  Mode = "verify"
)

func (m Mode) Valid() bool {
	switch m {
	case ModeInspect, ModeCopy, ModeResume, ModeVerify:
		return true
	default:
		return false
	}
}

type Options struct {
	Mode                       Mode
	Execute                    bool
	BatchSize                  int
	MaxSourceRows              int64
	MaxSourceBytes             int64
	FreezeAttestationPath      string
	FreezePublicKey            []byte
	FreezeKeyID                string
	TargetWriterFencePath      string
	TargetWriterFencePublicKey []byte
	TargetWriterFenceKeyID     string
	TargetDeploymentGeneration string
}

// SourceFreezeAttestation is signed out-of-band evidence from the deployment
// controller that all business writers and consumers are stopped. The
// migration verifies it against its own independently observed source identity
// and full canonical row digest; a human acknowledgement alone is insufficient.
type SourceFreezeAttestation struct {
	SchemaVersion string              `json:"schemaVersion"`
	AttestationID string              `json:"attestationId"`
	IssuedAt      time.Time           `json:"issuedAt"`
	ExpiresAt     time.Time           `json:"expiresAt"`
	Source        SourceFreezeBinding `json:"source"`
	Fence         SourceFreezeFence   `json:"fence"`
	AuthorizedBy  string              `json:"authorizedBy"`
	KeyID         string              `json:"keyId"`
	Signature     string              `json:"signature"`
}

type SourceFreezeBinding struct {
	OperatorClusterID string `json:"operatorClusterId"`
	DatabaseID        string `json:"databaseId"`
	SchemaSHA256      string `json:"schemaSha256"`
	SnapshotID        string `json:"snapshotId"`
}

type SourceFreezeFence struct {
	ApplicationWritesStopped bool  `json:"applicationWritesStopped"`
	BackgroundWorkersStopped bool  `json:"backgroundWorkersStopped"`
	CDCOrOutboxDrained       bool  `json:"cdcOrOutboxDrained"`
	ActiveBusinessWriters    int64 `json:"activeBusinessWriters"`
}

type SourceIdentity struct {
	ClusterID           string `json:"clusterId"`
	DatabaseID          string `json:"databaseId"`
	SnapshotID          string `json:"snapshotId"`
	ExportedSnapshot    string `json:"exportedSnapshot"`
	TransactionSnapshot string `json:"transactionSnapshot"`
	WALLSN              string `json:"walLsn"`
	SchemaSHA256        string `json:"schemaSha256"`
}

type TargetIdentity struct {
	InstanceID              string `json:"instanceId"`
	DatabaseID              string `json:"databaseId"`
	MigrationVersion        string `json:"migrationVersion"`
	MigrationChecksumSHA256 string `json:"migrationChecksumSha256"`
	SchemaSHA256            string `json:"schemaSha256"`
}

// TargetWriterFenceAttestation is issued independently of the source freeze.
// It proves that application-controlled MySQL writers cannot race the cutover;
// GET_LOCK alone only serializes instances of this migration tool.
type TargetWriterFenceAttestation struct {
	SchemaVersion        string            `json:"schemaVersion"`
	AttestationID        string            `json:"attestationId"`
	IssuedAt             time.Time         `json:"issuedAt"`
	ExpiresAt            time.Time         `json:"expiresAt"`
	DeploymentGeneration string            `json:"deploymentGeneration"`
	Target               TargetIdentity    `json:"target"`
	TargetIdentitySHA256 string            `json:"targetIdentitySha256"`
	Fence                TargetWriterFence `json:"fence"`
	AuthorizedBy         string            `json:"authorizedBy"`
	KeyID                string            `json:"keyId"`
	Signature            string            `json:"signature"`
}

type TargetWriterFence struct {
	ActiveApplicationWriters int64 `json:"activeApplicationWriters"`
	ActiveBackgroundWriters  int64 `json:"activeBackgroundWriters"`
	AutomaticRestartDisabled bool  `json:"automaticRestartDisabled"`
	WriteTrafficDisabled     bool  `json:"writeTrafficDisabled"`
}

// TargetWriterFenceEvidence makes the exact signed payload independently
// auditable. All fields are covered by the outer manifest digest, while the
// detached key and expected generation remain out-of-band trust inputs.
type TargetWriterFenceEvidence struct {
	Attestation            TargetWriterFenceAttestation `json:"attestation"`
	CanonicalPayloadBase64 string                       `json:"canonicalPayloadBase64"`
	PayloadSHA256          string                       `json:"payloadSha256"`
	AttestationSHA256      string                       `json:"attestationSha256"`
	KeyID                  string                       `json:"keyId"`
}

type CutoverIdentity struct {
	SchemaVersion string         `json:"schemaVersion"`
	ToolVersion   string         `json:"toolVersion"`
	ToolCommit    string         `json:"toolCommit"`
	Source        SourceIdentity `json:"source"`
	Target        TargetIdentity `json:"target"`
}

// BoundEqual compares the immutable identities that make a resume safe. The
// exported snapshot and observed WAL/transaction positions remain in every
// signed artifact as audit evidence. SnapshotID is a digest of the complete
// canonical source inventory and is the cross-process snapshot binding: a
// changed source row cannot be hidden by a newly acquired transaction token.
func (i CutoverIdentity) BoundEqual(other CutoverIdentity) bool {
	return i.SchemaVersion == other.SchemaVersion &&
		i.ToolVersion == other.ToolVersion &&
		i.ToolCommit == other.ToolCommit &&
		i.Source.ClusterID == other.Source.ClusterID &&
		i.Source.DatabaseID == other.Source.DatabaseID &&
		i.Source.SnapshotID == other.Source.SnapshotID &&
		i.Source.SchemaSHA256 == other.Source.SchemaSHA256 &&
		i.Target == other.Target
}

func (i CutoverIdentity) Validate() error {
	values := map[string]string{
		"schema version": i.SchemaVersion, "tool version": i.ToolVersion,
		"tool commit": i.ToolCommit, "source cluster": i.Source.ClusterID,
		"source database": i.Source.DatabaseID, "source snapshot": i.Source.SnapshotID,
		"source exported snapshot": i.Source.ExportedSnapshot, "source transaction snapshot": i.Source.TransactionSnapshot,
		"source WAL LSN": i.Source.WALLSN,
		"source schema":  i.Source.SchemaSHA256, "target instance": i.Target.InstanceID,
		"target database": i.Target.DatabaseID, "target migration version": i.Target.MigrationVersion,
		"target migration checksum": i.Target.MigrationChecksumSHA256, "target schema": i.Target.SchemaSHA256,
	}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is empty", name)
		}
	}
	return nil
}

type TableInventory struct {
	Rows           int64  `json:"rows"`
	CanonicalBytes int64  `json:"canonicalBytes"`
	DigestSHA256   string `json:"digestSha256"`
	LastKey        Cursor `json:"lastPrimaryKey,omitempty"`
}

type DeferredStatus string

const (
	DeferredPending  DeferredStatus = "pending"
	DeferredComplete DeferredStatus = "complete"
)

type TableCheckpoint struct {
	IdentitySHA256          string         `json:"identitySha256"`
	Table                   string         `json:"table"`
	BatchStartPrimaryKey    Cursor         `json:"batchStartPrimaryKey,omitempty"`
	LastSourcePrimaryKey    Cursor         `json:"lastSourcePrimaryKey,omitempty"`
	SourceRows              int64          `json:"sourceRows"`
	BatchSourceRows         int64          `json:"batchSourceRows"`
	BatchSourceSHA256       string         `json:"batchSourceSha256"`
	TargetCommittedRows     int64          `json:"targetCommittedRows"`
	TargetCommittedSHA256   string         `json:"targetCommittedSha256"`
	DeferredForeignKeyState DeferredStatus `json:"deferredForeignKeyState"`
	CommittedAt             time.Time      `json:"committedAt"`
}

type DeferredCheckpoint struct {
	Table                string    `json:"table"`
	BatchStartPrimaryKey Cursor    `json:"batchStartPrimaryKey,omitempty"`
	LastSourcePrimaryKey Cursor    `json:"lastSourcePrimaryKey,omitempty"`
	CommittedRows        int64     `json:"committedRows"`
	BatchSHA256          string    `json:"batchSha256"`
	CommittedAt          time.Time `json:"committedAt"`
}

type Manifest struct {
	SchemaVersion           string                          `json:"schemaVersion"`
	Identity                CutoverIdentity                 `json:"identity"`
	IdentitySHA256          string                          `json:"identitySha256"`
	CreatedAt               time.Time                       `json:"createdAt"`
	UpdatedAt               time.Time                       `json:"updatedAt"`
	TableOrder              []string                        `json:"tableOrder"`
	SourceInventory         map[string]TableInventory       `json:"sourceInventory"`
	Checkpoints             map[string][]TableCheckpoint    `json:"checkpoints"`
	DeferredCheckpoints     map[string][]DeferredCheckpoint `json:"deferredCheckpoints,omitempty"`
	DeferredForeignKeys     DeferredStatus                  `json:"deferredForeignKeys"`
	AutoIncrementCompleted  map[string]bool                 `json:"autoIncrementCompleted"`
	SourceFreezeAttestation SourceFreezeAttestation         `json:"sourceFreezeAttestation"`
	SourceFreezeSHA256      string                          `json:"sourceFreezeSha256"`
	TargetWriterFence       TargetWriterFenceEvidence       `json:"targetWriterFence"`
	ReconciliationSHA256    string                          `json:"reconciliationSha256,omitempty"`
	Completed               bool                            `json:"completed"`
	DigestSHA256            string                          `json:"digestSha256"`
}

type CheckResult struct {
	Category         string `json:"category"`
	Name             string `json:"name"`
	SourceMismatches int64  `json:"sourceMismatches"`
	TargetMismatches int64  `json:"targetMismatches"`
	Match            bool   `json:"match"`
}

type MetricResult struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Source   int64  `json:"source"`
	Target   int64  `json:"target"`
	Match    bool   `json:"match"`
}

type TableResult struct {
	Table        string `json:"table"`
	SourceRows   int64  `json:"sourceRows"`
	TargetRows   int64  `json:"targetRows"`
	SourceDigest string `json:"sourceDigestSha256"`
	TargetDigest string `json:"targetDigestSha256"`
	Match        bool   `json:"match"`
}

type ReconciliationReport struct {
	SchemaVersion       string         `json:"schemaVersion"`
	IdentitySHA256      string         `json:"identitySha256"`
	GeneratedAt         time.Time      `json:"generatedAt"`
	DeferredForeignKeys DeferredStatus `json:"deferredForeignKeys"`
	Tables              []TableResult  `json:"tables"`
	Checks              []CheckResult  `json:"checks"`
	Metrics             []MetricResult `json:"metrics"`
	MismatchCount       int            `json:"mismatchCount"`
	CutoverReady        bool           `json:"cutoverReady"`
	DigestSHA256        string         `json:"digestSha256"`
}

type RunReport struct {
	SchemaVersion        string                    `json:"schemaVersion"`
	Mode                 Mode                      `json:"mode"`
	DryRun               bool                      `json:"dryRun"`
	Identity             CutoverIdentity           `json:"identity"`
	SourceInventory      map[string]TableInventory `json:"sourceInventory"`
	SourceRows           int64                     `json:"sourceRows"`
	SourceCanonicalBytes int64                     `json:"sourceCanonicalBytes"`
	MaxSourceRows        int64                     `json:"maxSourceRows"`
	MaxSourceBytes       int64                     `json:"maxSourceBytes"`
	SourceChecks         []CheckResult             `json:"sourceChecks,omitempty"`
	CopiedRows           map[string]int64          `json:"copiedRows,omitempty"`
	Reconciliation       *ReconciliationReport     `json:"reconciliation,omitempty"`
	CutoverReady         bool                      `json:"cutoverReady"`
	Message              string                    `json:"message"`
}

type Cell struct {
	Kind  cellKind `json:"kind"`
	Null  bool     `json:"null,omitempty"`
	Value string   `json:"value,omitempty"`
}

type Row []Cell
type Cursor []string

func (c Cell) driverValue() (any, error) {
	if c.Null {
		return nil, nil
	}
	switch c.Kind {
	case kindInt:
		return strconv.ParseInt(c.Value, 10, 64)
	case kindTime:
		return time.Parse(time.RFC3339Nano, c.Value)
	case kindDate:
		return time.Parse("2006-01-02", c.Value)
	case kindBinary:
		return hex.DecodeString(c.Value)
	case kindJSON:
		return []byte(c.Value), nil
	default:
		return c.Value, nil
	}
}

func canonicalDigest(rows []Row) string {
	h := sha256.New()
	for _, row := range rows {
		b, _ := json.Marshal(row)
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func digestJSON(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func nowUTC() time.Time { return time.Now().UTC() }

func keyOf(table tableSpec, row Row) (Cursor, error) {
	key := make(Cursor, len(table.keyColumns))
	for i, index := range table.keyColumns {
		if index < 0 || index >= len(row) || row[index].Null {
			return nil, fmt.Errorf("%s row has invalid primary key", table.name)
		}
		key[i] = row[index].Value
	}
	return key, nil
}

func compareCursor(table tableSpec, left, right Cursor) (int, error) {
	if len(left) != len(table.keyColumns) || len(right) != len(table.keyColumns) {
		return 0, errors.New("cursor width mismatch")
	}
	for i, columnIndex := range table.keyColumns {
		kind := table.columns[columnIndex].kind
		if kind == kindInt {
			a, err := strconv.ParseInt(left[i], 10, 64)
			if err != nil {
				return 0, err
			}
			b, err := strconv.ParseInt(right[i], 10, 64)
			if err != nil {
				return 0, err
			}
			if a < b {
				return -1, nil
			}
			if a > b {
				return 1, nil
			}
			continue
		}
		if left[i] < right[i] {
			return -1, nil
		}
		if left[i] > right[i] {
			return 1, nil
		}
	}
	return 0, nil
}
