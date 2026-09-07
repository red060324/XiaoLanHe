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

func TestTargetWriterFenceStrictSignatureBindingAndManifestEvidence(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	target := testIdentity().Target
	attestation := signedTargetWriterFence(t, privateKey, target, now)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "target-fence.json")
	if err := writeAtomicJSON(path, attestation); err != nil {
		t.Fatal(err)
	}
	evidence, err := loadAndVerifyTargetWriterFence(path, publicKey, attestation.KeyID, attestation.DeploymentGeneration, target, now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := canonicalTargetWriterFencePayload(attestation)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.CanonicalPayloadBase64 != base64.RawStdEncoding.EncodeToString(payload) || evidence.PayloadSHA256 != digestBytes(payload) || evidence.KeyID != attestation.KeyID {
		t.Fatalf("manifest evidence is not exact: %+v", evidence)
	}
	if err := verifyTargetWriterFenceEvidence(evidence, publicKey, attestation.KeyID, attestation.DeploymentGeneration, target, now); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		keyID      string
		generation string
		now        time.Time
		key        []byte
		edit       func(*TargetWriterFenceAttestation)
	}{
		{name: "wrong trusted key", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: now, key: make([]byte, ed25519.PublicKeySize)},
		{name: "wrong key id", keyID: "other", generation: attestation.DeploymentGeneration, now: now, key: publicKey},
		{name: "wrong deployment generation", keyID: attestation.KeyID, generation: "other", now: now, key: publicKey},
		{name: "expired", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: attestation.ExpiresAt, key: publicKey},
		{name: "issued in future", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: attestation.IssuedAt.Add(-time.Second), key: publicKey},
		{name: "wrong target identity", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: now, key: publicKey, edit: func(value *TargetWriterFenceAttestation) { value.Target.DatabaseID = "other" }},
		{name: "wrong target digest", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: now, key: publicKey, edit: func(value *TargetWriterFenceAttestation) { value.TargetIdentitySHA256 = "sha256:other" }},
		{name: "active application writer", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: now, key: publicKey, edit: func(value *TargetWriterFenceAttestation) { value.Fence.ActiveApplicationWriters = 1 }},
		{name: "active background writer", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: now, key: publicKey, edit: func(value *TargetWriterFenceAttestation) { value.Fence.ActiveBackgroundWriters = 1 }},
		{name: "automatic restart enabled", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: now, key: publicKey, edit: func(value *TargetWriterFenceAttestation) { value.Fence.AutomaticRestartDisabled = false }},
		{name: "write traffic enabled", keyID: attestation.KeyID, generation: attestation.DeploymentGeneration, now: now, key: publicKey, edit: func(value *TargetWriterFenceAttestation) { value.Fence.WriteTrafficDisabled = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := attestation
			if test.edit != nil {
				test.edit(&candidate)
				candidate = resignTargetWriterFence(t, privateKey, candidate)
			}
			candidateEvidence, err := newTargetWriterFenceEvidence(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyTargetWriterFenceEvidence(candidateEvidence, test.key, test.keyID, test.generation, target, test.now); err == nil {
				t.Fatal("invalid target-writer fence was accepted")
			}
		})
	}
}

func TestTargetWriterFenceRejectsManifestTamperingUnknownFieldsAndSymlinks(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	target := testIdentity().Target
	attestation := signedTargetWriterFence(t, privateKey, target, now)
	evidence, err := newTargetWriterFenceEvidence(attestation)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*TargetWriterFenceEvidence){
		func(value *TargetWriterFenceEvidence) {
			value.CanonicalPayloadBase64 = base64.RawStdEncoding.EncodeToString([]byte("{}"))
		},
		func(value *TargetWriterFenceEvidence) { value.PayloadSHA256 = "sha256:tampered" },
		func(value *TargetWriterFenceEvidence) { value.AttestationSHA256 = "sha256:tampered" },
		func(value *TargetWriterFenceEvidence) { value.KeyID = "other" },
	} {
		candidate := evidence
		mutate(&candidate)
		if err := verifyTargetWriterFenceEvidence(candidate, publicKey, attestation.KeyID, attestation.DeploymentGeneration, target, now); err == nil {
			t.Fatal("tampered manifest evidence was accepted")
		}
	}

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "target-fence.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":"xlh.mysql_target_writer_fence.v1","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAndVerifyTargetWriterFence(path, publicKey, attestation.KeyID, attestation.DeploymentGeneration, target, now); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field err=%v", err)
	}
	link := filepath.Join(directory, "target-fence-link.json")
	if err := os.Symlink(path, link); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if _, err := loadAndVerifyTargetWriterFence(link, publicKey, attestation.KeyID, attestation.DeploymentGeneration, target, now); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink err=%v", err)
	}
}

func signedTargetWriterFence(t *testing.T, privateKey ed25519.PrivateKey, target TargetIdentity, now time.Time) TargetWriterFenceAttestation {
	t.Helper()
	targetDigest, err := targetIdentityDigest(target)
	if err != nil {
		t.Fatal(err)
	}
	return resignTargetWriterFence(t, privateKey, TargetWriterFenceAttestation{
		SchemaVersion: targetWriterFenceSchemaVersion, AttestationID: "target-fence-123",
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		DeploymentGeneration: "target-generation-2026-09", Target: target, TargetIdentitySHA256: targetDigest,
		Fence:        TargetWriterFence{AutomaticRestartDisabled: true, WriteTrafficDisabled: true},
		AuthorizedBy: "deployment-fence-controller", KeyID: "target-fence-key-2026-09",
	})
}

func resignTargetWriterFence(t *testing.T, privateKey ed25519.PrivateKey, attestation TargetWriterFenceAttestation) TargetWriterFenceAttestation {
	t.Helper()
	attestation.Signature = ""
	payload, err := canonicalTargetWriterFencePayload(attestation)
	if err != nil {
		t.Fatal(err)
	}
	attestation.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return attestation
}
