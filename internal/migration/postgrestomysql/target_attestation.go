package postgrestomysql

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const targetWriterFenceSchemaVersion = "xlh.mysql_target_writer_fence.v1"

func loadAndVerifyTargetWriterFence(path string, publicKey []byte, expectedKeyID, expectedGeneration string, target TargetIdentity, now time.Time) (TargetWriterFenceEvidence, error) {
	if strings.TrimSpace(path) == "" {
		return TargetWriterFenceEvidence{}, errors.New("target-writer-fence attestation is required")
	}
	if info, err := os.Lstat(path); err != nil {
		return TargetWriterFenceEvidence{}, fmt.Errorf("inspect target-writer-fence attestation: %w", err)
	} else if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return TargetWriterFenceEvidence{}, errors.New("target-writer-fence attestation must be a private regular file")
	}
	var attestation TargetWriterFenceAttestation
	if err := readStrictJSON(path, &attestation); err != nil {
		return TargetWriterFenceEvidence{}, err
	}
	evidence, err := newTargetWriterFenceEvidence(attestation)
	if err != nil {
		return TargetWriterFenceEvidence{}, err
	}
	if err := verifyTargetWriterFenceEvidence(evidence, publicKey, expectedKeyID, expectedGeneration, target, now); err != nil {
		return TargetWriterFenceEvidence{}, err
	}
	return evidence, nil
}

func newTargetWriterFenceEvidence(attestation TargetWriterFenceAttestation) (TargetWriterFenceEvidence, error) {
	payload, err := canonicalTargetWriterFencePayload(attestation)
	if err != nil {
		return TargetWriterFenceEvidence{}, err
	}
	attestationDigest, err := digestJSON(attestation)
	if err != nil {
		return TargetWriterFenceEvidence{}, err
	}
	return TargetWriterFenceEvidence{
		Attestation:            attestation,
		CanonicalPayloadBase64: base64.RawStdEncoding.EncodeToString(payload),
		PayloadSHA256:          digestBytes(payload),
		AttestationSHA256:      attestationDigest,
		KeyID:                  attestation.KeyID,
	}, nil
}

func verifyTargetWriterFenceEvidence(evidence TargetWriterFenceEvidence, publicKey []byte, expectedKeyID, expectedGeneration string, target TargetIdentity, now time.Time) error {
	return verifyTargetWriterFenceEvidenceWindow(evidence, publicKey, expectedKeyID, expectedGeneration, target, now, true)
}

// verifyTargetWriterFenceEvidenceBinding authenticates an already embedded
// fence while allowing its old validity window to have elapsed. Resume uses it
// only before replacing that evidence with a freshly current signed fence.
func verifyTargetWriterFenceEvidenceBinding(evidence TargetWriterFenceEvidence, publicKey []byte, expectedKeyID, expectedGeneration string, target TargetIdentity) error {
	return verifyTargetWriterFenceEvidenceWindow(evidence, publicKey, expectedKeyID, expectedGeneration, target, time.Time{}, false)
}

func verifyTargetWriterFenceEvidenceWindow(evidence TargetWriterFenceEvidence, publicKey []byte, expectedKeyID, expectedGeneration string, target TargetIdentity, now time.Time, requireCurrent bool) error {
	expected, err := newTargetWriterFenceEvidence(evidence.Attestation)
	if err != nil {
		return err
	}
	payload, err := base64.RawStdEncoding.DecodeString(evidence.CanonicalPayloadBase64)
	if err != nil || evidence.CanonicalPayloadBase64 == "" {
		return errors.New("target-writer-fence canonical payload is invalid")
	}
	if evidence.CanonicalPayloadBase64 != expected.CanonicalPayloadBase64 ||
		evidence.PayloadSHA256 != expected.PayloadSHA256 || evidence.PayloadSHA256 != digestBytes(payload) ||
		evidence.AttestationSHA256 != expected.AttestationSHA256 || evidence.KeyID != expected.KeyID {
		return errors.New("target-writer-fence manifest evidence digest or canonical payload mismatch")
	}
	return verifyTargetWriterFenceAttestation(evidence.Attestation, publicKey, expectedKeyID, expectedGeneration, target, now, requireCurrent)
}

func verifyTargetWriterFenceAttestation(attestation TargetWriterFenceAttestation, publicKey []byte, expectedKeyID, expectedGeneration string, target TargetIdentity, now time.Time, requireCurrent bool) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("a valid Ed25519 target-writer-fence public key is required")
	}
	signature, err := base64.RawStdEncoding.DecodeString(attestation.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("target-writer-fence attestation signature is invalid")
	}
	payload, err := canonicalTargetWriterFencePayload(attestation)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return errors.New("target-writer-fence attestation signature verification failed")
	}
	targetDigest, err := targetIdentityDigest(target)
	if err != nil {
		return err
	}
	windowInvalid := attestation.IssuedAt.IsZero() || attestation.ExpiresAt.IsZero() || !attestation.IssuedAt.Before(attestation.ExpiresAt)
	if requireCurrent {
		windowInvalid = windowInvalid || now.IsZero() || attestation.IssuedAt.After(now) || !now.Before(attestation.ExpiresAt)
	}
	if attestation.SchemaVersion != targetWriterFenceSchemaVersion || attestation.AttestationID == "" || attestation.AuthorizedBy == "" ||
		attestation.KeyID == "" || attestation.KeyID != expectedKeyID ||
		attestation.DeploymentGeneration == "" || attestation.DeploymentGeneration != expectedGeneration ||
		attestation.Target != target || attestation.TargetIdentitySHA256 == "" || attestation.TargetIdentitySHA256 != targetDigest ||
		attestation.Fence.ActiveApplicationWriters != 0 || attestation.Fence.ActiveBackgroundWriters != 0 ||
		!attestation.Fence.AutomaticRestartDisabled || !attestation.Fence.WriteTrafficDisabled || windowInvalid ||
		containsUnsafeAttestationText(attestation.AttestationID, attestation.AuthorizedBy, attestation.KeyID, attestation.DeploymentGeneration) ||
		len(attestation.AttestationID) > 256 || len(attestation.AuthorizedBy) > 256 || len(attestation.KeyID) > 128 || len(attestation.DeploymentGeneration) > 256 {
		return errors.New("target-writer-fence attestation is stale, incomplete, active, or bound to another target or generation")
	}
	return nil
}

func containsUnsafeAttestationText(values ...string) bool {
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n\t") {
			return true
		}
	}
	return false
}

func targetIdentityDigest(target TargetIdentity) (string, error) {
	bytes, err := canonicalTargetIdentityBytes(target)
	if err != nil {
		return "", err
	}
	return digestBytes(bytes), nil
}

func canonicalTargetIdentityBytes(target TargetIdentity) ([]byte, error) {
	quote := strconv.Quote
	payload := "{" +
		quote("databaseId") + ":" + quote(target.DatabaseID) + "," +
		quote("instanceId") + ":" + quote(target.InstanceID) + "," +
		quote("migrationChecksumSha256") + ":" + quote(target.MigrationChecksumSHA256) + "," +
		quote("migrationVersion") + ":" + quote(target.MigrationVersion) + "," +
		quote("schemaSha256") + ":" + quote(target.SchemaSHA256) + "}"
	return []byte(payload), nil
}

func canonicalTargetWriterFencePayload(attestation TargetWriterFenceAttestation) ([]byte, error) {
	quote := strconv.Quote
	issued, err := attestation.IssuedAt.MarshalText()
	if err != nil {
		return nil, err
	}
	expires, err := attestation.ExpiresAt.MarshalText()
	if err != nil {
		return nil, err
	}
	target, err := canonicalTargetIdentityBytes(attestation.Target)
	if err != nil {
		return nil, err
	}
	payload := "{" +
		quote("attestationId") + ":" + quote(attestation.AttestationID) + "," +
		quote("authorizedBy") + ":" + quote(attestation.AuthorizedBy) + "," +
		quote("deploymentGeneration") + ":" + quote(attestation.DeploymentGeneration) + "," +
		quote("expiresAt") + ":" + quote(string(expires)) + "," +
		quote("fence") + ":{" +
		quote("activeApplicationWriters") + ":" + strconv.FormatInt(attestation.Fence.ActiveApplicationWriters, 10) + "," +
		quote("activeBackgroundWriters") + ":" + strconv.FormatInt(attestation.Fence.ActiveBackgroundWriters, 10) + "," +
		quote("automaticRestartDisabled") + ":" + strconv.FormatBool(attestation.Fence.AutomaticRestartDisabled) + "," +
		quote("writeTrafficDisabled") + ":" + strconv.FormatBool(attestation.Fence.WriteTrafficDisabled) + "}," +
		quote("issuedAt") + ":" + quote(string(issued)) + "," +
		quote("keyId") + ":" + quote(attestation.KeyID) + "," +
		quote("schemaVersion") + ":" + quote(attestation.SchemaVersion) + "," +
		quote("target") + ":" + string(target) + "," +
		quote("targetIdentitySha256") + ":" + quote(attestation.TargetIdentitySHA256) + "}"
	return []byte(payload), nil
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
