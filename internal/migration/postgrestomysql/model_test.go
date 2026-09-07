package postgrestomysql

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTablesHaveExplicitOrderAndDeferredLinks(t *testing.T) {
	specs := tables()
	want := []string{
		"user_account", "user_session", "player_profile", "conversation_session", "conversation_message",
		"game", "game_edition", "game_price", "coupon_campaign", "coupon_definition", "coupon_claim",
		"purchase_order", "purchase_order_item", "payment_record", "game_entitlement", "community_post",
		"community_comment", "community_reaction", "flash_sale_scope_lock", "flash_sale_activity",
		"flash_sale_reservation", "flash_sale_release_job",
	}
	if strings.Join(tableNames(specs), ",") != strings.Join(want, ",") {
		t.Fatalf("table order=%v", tableNames(specs))
	}
	deferred := map[string][]string{}
	for _, table := range specs {
		for _, index := range table.deferredColumns() {
			deferred[table.name] = append(deferred[table.name], table.columns[index].name)
		}
	}
	for table, wantColumns := range map[string][]string{
		"conversation_session": {"summary_through_message_id"},
		"coupon_claim":         {"status", "redeemed_order_id"},
		"purchase_order":       {"coupon_claim_id"},
		"game_entitlement":     {"source_order_id"},
	} {
		if strings.Join(deferred[table], ",") != strings.Join(wantColumns, ",") {
			t.Errorf("deferred %s=%v", table, deferred[table])
		}
	}
	if got := specs[18].sourceFrom; !strings.Contains(got, "SELECT edition_id,region_code,currency") {
		t.Fatalf("scope lock is not derived from activities: %s", got)
	}
}

func TestConversationMessageProjectsLegacyRowsWithNullMessageKey(t *testing.T) {
	var messages tableSpec
	for _, spec := range tables() {
		if spec.name == "conversation_message" {
			messages = spec
			break
		}
	}
	if len(messages.columns) != 8 {
		t.Fatalf("conversation_message columns = %d, want 8", len(messages.columns))
	}
	messageKey := messages.columns[2]
	if messageKey.name != "message_key" || messageKey.sourceExpr != "NULL::text" || len(messageKey.sourceDependencies) != 0 || messageKey.kind != kindString {
		t.Fatalf("message_key projection = %+v", messageKey)
	}
	if !strings.Contains(messages.targetProjection(), "`session_id`,`message_key`,`role`") {
		t.Fatalf("target projection does not preserve message_key position: %s", messages.targetProjection())
	}
	required := expectedSourceColumns(tables())["conversation_message"]
	if required["message_key"] || !required["session_id"] || !required["role"] {
		t.Fatalf("legacy source dependencies = %+v", required)
	}
}

func TestNormalizeCellRejectsMalformedSource(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 456789000, time.FixedZone("east", 8*60*60))
	cell, err := normalizeCell(kindTime, now)
	if err != nil || cell.Value != "2026-09-06T17:02:03.456789Z" {
		t.Fatalf("UTC cell=%+v err=%v", cell, err)
	}
	if _, err := normalizeCell(kindTime, now.Add(time.Nanosecond)); err == nil {
		t.Fatal("expected sub-microsecond timestamp rejection")
	}
	if _, err := normalizeCell(kindBinary, make([]byte, 31)); err == nil {
		t.Fatal("expected malformed digest rejection")
	}
	jsonCell, err := normalizeCell(kindJSON, []byte(`{"b":2,"a":1}`))
	if err != nil || jsonCell.Value != `{"a":1,"b":2}` {
		t.Fatalf("JSON cell=%+v err=%v", jsonCell, err)
	}
	if _, err := normalizeCell(kindJSON, []byte(`{} null`)); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}

func TestInitialRowsStageCouponClaimRedemptionLegally(t *testing.T) {
	var claim tableSpec
	for _, spec := range tables() {
		if spec.name == "coupon_claim" {
			claim = spec
		}
	}
	row := make(Row, len(claim.columns))
	for i, column := range claim.columns {
		row[i] = Cell{Kind: column.kind, Value: "7"}
	}
	row[3] = Cell{Kind: kindString, Value: "redeemed"}
	initial := claim.initialRows([]Row{row})[0]
	if initial[3].Null || initial[3].Value != "claimed" || !initial[6].Null || initial[0].Null {
		t.Fatalf("source=%+v initial=%+v", row, initial)
	}
	if row[3].Value != "redeemed" || row[6].Null {
		t.Fatalf("initial staging mutated source row: %+v", row)
	}
	status := claim.columns[3]
	if status.sourceExpr != "status" || strings.Join(status.sourceDependencies, ",") != "status" {
		t.Fatalf("status source contract=%+v", status)
	}
}

func TestIdentityBindingRejectsEveryRequiredDrift(t *testing.T) {
	base := testIdentity()
	cases := map[string]func(*CutoverIdentity){
		"source cluster":            func(v *CutoverIdentity) { v.Source.ClusterID = "changed" },
		"source database":           func(v *CutoverIdentity) { v.Source.DatabaseID = "changed" },
		"source snapshot":           func(v *CutoverIdentity) { v.Source.SnapshotID = "changed" },
		"source schema":             func(v *CutoverIdentity) { v.Source.SchemaSHA256 = "changed" },
		"tool commit":               func(v *CutoverIdentity) { v.ToolCommit = "changed" },
		"target instance":           func(v *CutoverIdentity) { v.Target.InstanceID = "changed" },
		"target database":           func(v *CutoverIdentity) { v.Target.DatabaseID = "changed" },
		"target migration version":  func(v *CutoverIdentity) { v.Target.MigrationVersion = "changed" },
		"target migration checksum": func(v *CutoverIdentity) { v.Target.MigrationChecksumSHA256 = "changed" },
		"target schema":             func(v *CutoverIdentity) { v.Target.SchemaSHA256 = "changed" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if base.BoundEqual(changed) {
				t.Fatal("identity drift was accepted")
			}
		})
	}
}

func TestIdentityBindingTreatsTransactionTokensAsAuditOnly(t *testing.T) {
	base := testIdentity()
	changed := base
	changed.Source.ExportedSnapshot = "0002-9"
	changed.Source.TransactionSnapshot = "10:20:"
	changed.Source.WALLSN = "0/FFFFFF"
	if !base.BoundEqual(changed) {
		t.Fatal("new read-only transaction tokens must not make cross-process resume impossible when the full source snapshot digest is unchanged")
	}
}

func TestSourceIdentityQueryRequiresNoControlSystemPrivilege(t *testing.T) {
	if strings.Contains(sourceIdentitySQL, "pg_control_system") {
		t.Fatal("source identity must be available to an ordinary read-only database user")
	}
	for _, fragment := range []string{"current_database()", "pg_database", "server_version_num", "inet_server_addr", "inet_server_port", "pg_export_snapshot", "pg_current_wal_lsn"} {
		if !strings.Contains(sourceIdentitySQL, fragment) {
			t.Errorf("source identity query missing %s", fragment)
		}
	}
}

func TestCheckpointDigestSeparatesDeferredSourceAndCommittedTarget(t *testing.T) {
	var order tableSpec
	for _, spec := range tables() {
		if spec.name == "purchase_order" {
			order = spec
		}
	}
	source := make(Row, len(order.columns))
	for i, column := range order.columns {
		source[i] = Cell{Kind: column.kind, Value: "1"}
	}
	target := order.initialRows([]Row{source})
	checkpoint, err := makeTableCheckpoint("sha256:id", order, []Row{source}, target, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.BatchSourceSHA256 == checkpoint.TargetCommittedSHA256 {
		t.Fatal("source digest must retain deferred values while committed digest records NULL links")
	}
	if checkpoint.DeferredForeignKeyState != DeferredPending {
		t.Fatalf("state=%s", checkpoint.DeferredForeignKeyState)
	}
}

func TestArtifactManifestIsAtomicDigestBoundAndTamperEvident(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := newArtifactStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manifest := newManifest(testIdentity(), "sha256:identity", map[string]TableInventory{}, tables())
	if err := store.saveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.loadManifest()
	if err != nil || loaded.DigestSHA256 == "" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	path := filepath.Join(directory, manifestFile)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), "pending", "complete", 1))
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.loadManifest(); !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("tamper err=%v", err)
	}
}

func TestArtifactRejectsExposedDirectoryPermissions(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := newArtifactStore(directory)
	if err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("err=%v", err)
	}
	_ = store
}

func TestManifestInventoryAndIdentityDriftRejected(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, _ := newArtifactStore(directory)
	t.Cleanup(func() { _ = store.Close() })
	identity := testIdentity()
	identitySHA, _ := identityDigest(identity)
	inventory := map[string]TableInventory{"user_account": {Rows: 1, CanonicalBytes: 7, DigestSHA256: "sha256:rows"}}
	if err := store.saveManifest(newManifest(identity, identitySHA, inventory, tables())); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{artifacts: store, specs: tables()}
	changedIdentity := identity
	changedIdentity.Source.SnapshotID = "sha256:changed"
	if _, err := runner.loadBoundManifest(changedIdentity, inventory); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity drift err=%v", err)
	}
	changedInventory := map[string]TableInventory{"user_account": {Rows: 2, CanonicalBytes: 14, DigestSHA256: "sha256:changed"}}
	if _, err := runner.loadBoundManifest(identity, changedInventory); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("inventory drift err=%v", err)
	}
}

func TestReconciliationMismatchAlwaysBlocks(t *testing.T) {
	cases := []struct {
		name string
		edit func(*ReconciliationReport)
	}{
		{"table count/digest", func(r *ReconciliationReport) { r.Tables = []TableResult{{Table: "user_account", Match: false}} }},
		{"orphan", func(r *ReconciliationReport) {
			r.Checks = []CheckResult{{Category: "orphan", Name: "user_session.user", TargetMismatches: 1}}
		}},
		{"unique", func(r *ReconciliationReport) {
			r.Checks = []CheckResult{{Category: "unique", Name: "open_price", TargetMismatches: 1}}
		}},
		{"status", func(r *ReconciliationReport) {
			r.Checks = []CheckResult{{Category: "status", Name: "commerce", TargetMismatches: 1}}
		}},
		{"inventory", func(r *ReconciliationReport) {
			r.Metrics = []MetricResult{{Category: "inventory", Name: "coupon.total_stock", Source: 2, Target: 1}}
		}},
		{"claims", func(r *ReconciliationReport) {
			r.Metrics = []MetricResult{{Category: "claims", Name: "claims.total", Source: 2, Target: 1}}
		}},
		{"orders", func(r *ReconciliationReport) {
			r.Metrics = []MetricResult{{Category: "orders", Name: "orders.total_minor", Source: 2, Target: 1}}
		}},
		{"payments", func(r *ReconciliationReport) {
			r.Metrics = []MetricResult{{Category: "payments", Name: "payments.paid_minor", Source: 2, Target: 1}}
		}},
		{"entitlements", func(r *ReconciliationReport) {
			r.Metrics = []MetricResult{{Category: "entitlements", Name: "entitlements.active", Source: 2, Target: 1}}
		}},
		{"flashsale", func(r *ReconciliationReport) {
			r.Metrics = []MetricResult{{Category: "flashsale", Name: "flashsale.reservations", Source: 2, Target: 1}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			report := ReconciliationReport{DeferredForeignKeys: DeferredComplete}
			test.edit(&report)
			finalizeReport(&report)
			if report.CutoverReady || report.MismatchCount == 0 || report.DigestSHA256 == "" {
				t.Fatalf("report=%+v", report)
			}
		})
	}
	report := ReconciliationReport{DeferredForeignKeys: DeferredPending}
	finalizeReport(&report)
	if report.CutoverReady {
		t.Fatal("pending deferred FK status must block cutover")
	}
}

func testIdentity() CutoverIdentity {
	return CutoverIdentity{
		SchemaVersion: ManifestSchemaVersion, ToolVersion: ToolVersion, ToolCommit: "tool-commit",
		Source: SourceIdentity{ClusterID: "cluster", DatabaseID: "source", SnapshotID: "sha256:snapshot", ExportedSnapshot: "0001-1", TransactionSnapshot: "1:2:", WALLSN: "0/16B6C50", SchemaSHA256: "sha256:schema"},
		Target: TargetIdentity{InstanceID: "instance", DatabaseID: "target", MigrationVersion: "025_create_flash_sale_release_job.sql", MigrationChecksumSHA256: "sha256:migrations", SchemaSHA256: "sha256:target-schema"},
	}
}
