package postgrestomysql

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const freezeAttestationSchemaVersion = "xlh.postgres_write_freeze.v1"

func loadAndVerifyFreezeAttestation(path string, publicKey []byte, expectedKeyID string, source SourceIdentity, now time.Time) (SourceFreezeAttestation, error) {
	if strings.TrimSpace(path) == "" {
		return SourceFreezeAttestation{}, errors.New("source-freeze attestation is required")
	}
	if info, err := os.Lstat(path); err != nil {
		return SourceFreezeAttestation{}, fmt.Errorf("inspect source-freeze attestation: %w", err)
	} else if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return SourceFreezeAttestation{}, errors.New("source-freeze attestation must be a private regular file")
	}
	var attestation SourceFreezeAttestation
	if err := readStrictJSON(path, &attestation); err != nil {
		return SourceFreezeAttestation{}, err
	}
	if err := verifyFreezeAttestation(attestation, publicKey, expectedKeyID, source, now); err != nil {
		return SourceFreezeAttestation{}, err
	}
	return attestation, nil
}

// verifyFreezeAttestation authenticates either freshly supplied external
// evidence or the exact evidence persisted in a manifest. It never trusts the
// manifest's source-cluster label until the signature and observed source
// bindings have been verified with an independently configured key.
func verifyFreezeAttestation(attestation SourceFreezeAttestation, publicKey []byte, expectedKeyID string, source SourceIdentity, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("a valid Ed25519 source-freeze public key is required")
	}
	signature, err := base64.RawStdEncoding.DecodeString(attestation.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("source-freeze attestation signature is invalid")
	}
	attestation.Signature = ""
	payload, err := canonicalJSONBytes(attestation)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return errors.New("source-freeze attestation signature verification failed")
	}
	if attestation.SchemaVersion != freezeAttestationSchemaVersion || attestation.AttestationID == "" || attestation.AuthorizedBy == "" ||
		attestation.KeyID == "" || attestation.KeyID != expectedKeyID ||
		!attestation.Fence.ApplicationWritesStopped || !attestation.Fence.BackgroundWorkersStopped || !attestation.Fence.CDCOrOutboxDrained || attestation.Fence.ActiveBusinessWriters != 0 ||
		strings.ContainsAny(attestation.AttestationID+attestation.AuthorizedBy+attestation.KeyID, "\r\n\t") || len(attestation.AttestationID) > 256 || len(attestation.AuthorizedBy) > 256 || len(attestation.KeyID) > 128 ||
		attestation.IssuedAt.IsZero() || attestation.ExpiresAt.IsZero() || attestation.IssuedAt.After(now) || !now.Before(attestation.ExpiresAt) ||
		attestation.Source.OperatorClusterID == "" || attestation.Source.DatabaseID != source.DatabaseID ||
		attestation.Source.SchemaSHA256 != source.SchemaSHA256 || attestation.Source.SnapshotID != source.SnapshotID {
		return errors.New("source-freeze attestation is stale, incomplete, or bound to another source")
	}
	return nil
}

func canonicalJSONBytes(attestation SourceFreezeAttestation) ([]byte, error) {
	// RFC 8785 sorts object member names lexicographically. This closed schema
	// deliberately contains no floating-point numbers, so constructing the
	// sorted object form explicitly avoids depending on Go struct field order.
	quote := strconv.Quote
	issued, err := attestation.IssuedAt.MarshalText()
	if err != nil {
		return nil, err
	}
	expires, err := attestation.ExpiresAt.MarshalText()
	if err != nil {
		return nil, err
	}
	payload := "{" +
		quote("attestationId") + ":" + quote(attestation.AttestationID) + "," +
		quote("authorizedBy") + ":" + quote(attestation.AuthorizedBy) + "," +
		quote("expiresAt") + ":" + quote(string(expires)) + "," +
		quote("fence") + ":{" +
		quote("activeBusinessWriters") + ":" + strconv.FormatInt(attestation.Fence.ActiveBusinessWriters, 10) + "," +
		quote("applicationWritesStopped") + ":" + strconv.FormatBool(attestation.Fence.ApplicationWritesStopped) + "," +
		quote("backgroundWorkersStopped") + ":" + strconv.FormatBool(attestation.Fence.BackgroundWorkersStopped) + "," +
		quote("cdcOrOutboxDrained") + ":" + strconv.FormatBool(attestation.Fence.CDCOrOutboxDrained) + "}," +
		quote("issuedAt") + ":" + quote(string(issued)) + "," +
		quote("keyId") + ":" + quote(attestation.KeyID) + "," +
		quote("schemaVersion") + ":" + quote(attestation.SchemaVersion) + "," +
		quote("source") + ":{" +
		quote("databaseId") + ":" + quote(attestation.Source.DatabaseID) + "," +
		quote("operatorClusterId") + ":" + quote(attestation.Source.OperatorClusterID) + "," +
		quote("schemaSha256") + ":" + quote(attestation.Source.SchemaSHA256) + "," +
		quote("snapshotId") + ":" + quote(attestation.Source.SnapshotID) + "}}"
	return []byte(payload), nil
}

func freezeAttestationDigest(attestation SourceFreezeAttestation) (string, error) {
	return digestJSON(attestation)
}
