package postgrestomysql

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreezeAttestationStrictSignatureBindingAndExpiry(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	source := testIdentity().Source
	attestation := SourceFreezeAttestation{
		SchemaVersion: freezeAttestationSchemaVersion, AttestationID: "freeze-123", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Source:       SourceFreezeBinding{OperatorClusterID: "operator-cluster-prod", DatabaseID: source.DatabaseID, SchemaSHA256: source.SchemaSHA256, SnapshotID: source.SnapshotID},
		Fence:        SourceFreezeFence{ApplicationWritesStopped: true, BackgroundWorkersStopped: true, CDCOrOutboxDrained: true},
		AuthorizedBy: "deployment-freeze-controller", KeyID: "freeze-key-2026-09",
	}
	payload, err := canonicalJSONBytes(attestation)
	if err != nil {
		t.Fatal(err)
	}
	attestation.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "freeze.json")
	if err := writeAtomicJSON(path, attestation); err != nil {
		t.Fatal(err)
	}
	verified, err := loadAndVerifyFreezeAttestation(path, publicKey, attestation.KeyID, source, now)
	if err != nil || verified.Source.OperatorClusterID != "operator-cluster-prod" {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}

	for _, test := range []struct {
		name  string
		keyID string
		now   time.Time
		edit  func(*SourceFreezeAttestation)
	}{
		{name: "wrong trusted key id", keyID: "other", now: now},
		{name: "expired", keyID: attestation.KeyID, now: attestation.ExpiresAt},
		{name: "writer active", keyID: attestation.KeyID, now: now, edit: func(value *SourceFreezeAttestation) { value.Fence.ActiveBusinessWriters = 1 }},
		{name: "source drift", keyID: attestation.KeyID, now: now, edit: func(value *SourceFreezeAttestation) { value.Source.SnapshotID = "sha256:changed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := attestation
			if test.edit != nil {
				test.edit(&candidate)
			}
			if err := writeAtomicJSON(path, candidate); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAndVerifyFreezeAttestation(path, publicKey, test.keyID, source, test.now); err == nil {
				t.Fatal("invalid freeze evidence was accepted")
			}
		})
	}
}

func TestFreezeAttestationRejectsUnknownFieldsAndSymlinks(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "freeze.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":"xlh.postgres_write_freeze.v1","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAndVerifyFreezeAttestation(path, make([]byte, ed25519.PublicKeySize), "key", testIdentity().Source, nowUTC()); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field err=%v", err)
	}
	symlink := filepath.Join(directory, "freeze-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if _, err := loadAndVerifyFreezeAttestation(symlink, make([]byte, ed25519.PublicKeySize), "key", testIdentity().Source, nowUTC()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink err=%v", err)
	}
}
