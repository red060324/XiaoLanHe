package postgrestomysql

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	manifestFile        = "manifest.json"
	artifactLockFile    = "cutover.lock"
	temporaryFilePrefix = ".xlh-cutover-"
)

var ErrCheckpointLocked = errors.New("checkpoint directory is locked by another process")

type artifactStore struct {
	directory string
	handle    *sharedDirectoryHandle
}

// artifactStore is copied into Runner and several test harnesses, so ownership
// of the pinned descriptor is shared and Close must be idempotent across copies.
type sharedDirectoryHandle struct {
	file   *os.File
	mu     sync.RWMutex
	err    error
	closed bool
}

// artifactLock holds a process-exclusive advisory lock on the checkpoint
// directory. Call release (or Close) on every successful acquisition. The
// stable lock file is deliberately retained because flock locks its inode.
type artifactLock struct{ file *os.File }

func newArtifactStore(directory string) (artifactStore, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return artifactStore{}, errors.New("checkpoint directory must be an absolute normalized path")
	}
	directoryFile, err := openSecureArtifactDirectory(directory)
	if err != nil {
		return artifactStore{}, err
	}
	return artifactStore{directory: directory, handle: &sharedDirectoryHandle{file: directoryFile}}, nil
}

func (s artifactStore) Close() error {
	if s.handle == nil {
		return errors.New("artifact store is not initialized")
	}
	s.handle.mu.Lock()
	defer s.handle.mu.Unlock()
	if !s.handle.closed {
		s.handle.closed = true
		s.handle.err = s.handle.file.Close()
	}
	return s.handle.err
}

func (s artifactStore) withDirectoryFD(operation func(int) error) error {
	if s.handle == nil || s.handle.file == nil {
		return errors.New("artifact store is not initialized")
	}
	s.handle.mu.RLock()
	defer s.handle.mu.RUnlock()
	if s.handle.closed {
		return errors.New("artifact store is closed")
	}
	fd := int(s.handle.file.Fd())
	if fd < 0 {
		return errors.New("artifact store is closed")
	}
	if err := validateDirectoryFD(fd); err != nil {
		return err
	}
	return operation(fd)
}

func (s artifactStore) loadManifest() (Manifest, error) {
	var manifest Manifest
	if err := s.withDirectoryFD(func(directoryFD int) error {
		return readStrictJSONAt(directoryFD, manifestFile, &manifest)
	}); err != nil {
		return Manifest{}, err
	}
	want := manifest.DigestSHA256
	manifest.DigestSHA256 = ""
	got, err := digestJSON(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.DigestSHA256 = want
	if want == "" || want != got {
		return Manifest{}, fmt.Errorf("manifest digest mismatch: %w", ErrCheckpointMismatch)
	}
	// deferredCheckpoints was omitted from early/empty v1 manifests. Restore an
	// assignable map only after verifying the serialized representation.
	if manifest.DeferredCheckpoints == nil {
		manifest.DeferredCheckpoints = map[string][]DeferredCheckpoint{}
	}
	return manifest, nil
}

func (s artifactStore) saveManifest(manifest Manifest) error {
	manifest.UpdatedAt = time.Now().UTC()
	manifest.DigestSHA256 = ""
	digest, err := digestJSON(manifest)
	if err != nil {
		return err
	}
	manifest.DigestSHA256 = digest

	return s.withDirectoryFD(func(directoryFD int) error {
		return writeAtomicJSONAt(directoryFD, manifestFile, manifest)
	})
}

func (s artifactStore) saveReport(report ReconciliationReport) error {
	if err := validateReportDigest(report); err != nil {
		return err
	}
	name, err := reconciliationReportName(report.DigestSHA256)
	if err != nil {
		return err
	}
	return s.withDirectoryFD(func(directoryFD int) error {
		return saveReportAt(directoryFD, name, report)
	})
}

func saveReportAt(directoryFD int, name string, report ReconciliationReport) error {
	existing, exists, err := loadExistingReportAt(directoryFD, name)
	if err != nil {
		return fmt.Errorf("immutable reconciliation report conflicts with existing file: %w", err)
	}
	if exists {
		return compareReconciliationReports(existing, report)
	}

	temporary, temporaryName, err := createTemporaryArtifact(directoryFD)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = unix.Unlinkat(directoryFD, temporaryName, 0)
		}
	}()
	if err := encodeAndSyncJSON(temporary, report); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary reconciliation report: %w", err)
	}

	// linkat publishes the report without replacement. If another process won
	// the race, validate and compare the winner instead of overwriting it.
	if err := unix.Linkat(directoryFD, temporaryName, directoryFD, name, 0); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("publish immutable reconciliation report: %w", err)
		}
		existing, exists, loadErr := loadExistingReportAt(directoryFD, name)
		if loadErr != nil {
			return fmt.Errorf("immutable reconciliation report conflicts with concurrently published file: %w", loadErr)
		}
		if !exists {
			return errors.New("concurrently published reconciliation report disappeared")
		}
		return compareReconciliationReports(existing, report)
	}
	if err := unix.Unlinkat(directoryFD, temporaryName, 0); err != nil {
		return fmt.Errorf("remove temporary reconciliation report: %w", err)
	}
	removeTemporary = false
	if err := unix.Fsync(directoryFD); err != nil {
		return fmt.Errorf("sync artifact directory: %w", err)
	}

	// Success is reported only after the bytes actually published under the
	// content-addressed name have been securely reopened and rehashed.
	persisted, exists, err := loadExistingReportAt(directoryFD, name)
	if err != nil {
		return fmt.Errorf("verify published reconciliation report: %w", err)
	}
	if !exists {
		return errors.New("published reconciliation report disappeared")
	}
	return compareReconciliationReports(persisted, report)
}

// loadReport securely loads the immutable content-addressed report selected by
// expectedDigest. It validates the expected digest's syntax, the filename, and
// a fresh digest of the persisted body before returning any data.
func (s artifactStore) loadReport(expectedDigest string) (ReconciliationReport, error) {
	name, err := reconciliationReportName(expectedDigest)
	if err != nil {
		return ReconciliationReport{}, err
	}
	var report ReconciliationReport
	err = s.withDirectoryFD(func(directoryFD int) error {
		loaded, exists, loadErr := loadExistingReportAt(directoryFD, name)
		if loadErr != nil {
			return fmt.Errorf("load reconciliation report: %w", loadErr)
		}
		if !exists {
			return fmt.Errorf("load reconciliation report: %w", fs.ErrNotExist)
		}
		if loaded.DigestSHA256 != expectedDigest {
			return errors.New("reconciliation report digest does not match expected digest")
		}
		report = loaded
		return nil
	})
	return report, err
}

func loadExistingReportAt(directoryFD int, name string) (ReconciliationReport, bool, error) {
	var report ReconciliationReport
	err := readStrictJSONAt(directoryFD, name, &report)
	if errors.Is(err, fs.ErrNotExist) {
		return ReconciliationReport{}, false, nil
	}
	if err != nil {
		return ReconciliationReport{}, false, err
	}
	// Always recompute the digest from the persisted body with its digest field
	// blanked. Trusting the requested report's digest would allow a tampered
	// pre-existing artifact to masquerade as immutable.
	if err := validateReportDigest(report); err != nil {
		return ReconciliationReport{}, false, err
	}
	wantName, err := reconciliationReportName(report.DigestSHA256)
	if err != nil {
		return ReconciliationReport{}, false, err
	}
	if name != wantName {
		return ReconciliationReport{}, false, errors.New("reconciliation report filename does not match its content digest")
	}
	return report, true, nil
}

func compareReconciliationReports(existing, requested ReconciliationReport) error {
	existingJSON, err := json.Marshal(existing)
	if err != nil {
		return err
	}
	requestedJSON, err := json.Marshal(requested)
	if err != nil {
		return err
	}
	if !bytes.Equal(existingJSON, requestedJSON) {
		return errors.New("immutable reconciliation report conflicts with existing file")
	}
	return nil
}

func validateReportDigest(report ReconciliationReport) error {
	want := report.DigestSHA256
	if _, err := reconciliationReportName(want); err != nil {
		return err
	}
	report.DigestSHA256 = ""
	got, err := digestJSON(report)
	if err != nil {
		return err
	}
	if want != got {
		return errors.New("reconciliation report has invalid digest")
	}
	return nil
}

func reconciliationReportName(digest string) (string, error) {
	const prefix = "sha256:"
	if len(digest) != len(prefix)+64 || !strings.HasPrefix(digest, prefix) {
		return "", errors.New("reconciliation report has invalid digest")
	}
	encoded := strings.TrimPrefix(digest, prefix)
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != 32 || encoded != strings.ToLower(encoded) {
		return "", errors.New("reconciliation report has invalid digest")
	}
	return "reconciliation-" + encoded + ".json", nil
}

func readStrictJSON(path string, destination any) error {
	// This defends the final path component against symlink/type substitution by
	// validating the descriptor returned by O_NOFOLLOW. Callers of arbitrary
	// paths (such as freeze attestations outside the checkpoint directory) must
	// place the file below trusted, non-replaceable ancestor directories. Store
	// artifacts use readStrictJSONAt with the store's pinned directory instead.
	file, err := openSecureArtifact(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return decodeStrictJSON(file, filepath.Base(path), destination)
}

func readStrictJSONAt(directoryFD int, name string, destination any) error {
	file, err := openSecureArtifactAt(directoryFD, name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer file.Close()
	return decodeStrictJSON(file, name, destination)
}

func decodeStrictJSON(reader io.Reader, name string, destination any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(name), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: trailing value", filepath.Base(name))
		}
		return fmt.Errorf("decode %s trailer: %w", filepath.Base(name), err)
	}
	return nil
}

func writeAtomicJSON(path string, value any) error {
	directoryFile, err := openSecureArtifactDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return writeAtomicJSONAt(int(directoryFile.Fd()), filepath.Base(path), value)
}

func writeAtomicJSONAt(directoryFD int, name string, value any) error {
	if err := validateArtifactName(name); err != nil {
		return err
	}
	if _, err := artifactExistsAt(directoryFD, name); err != nil {
		return fmt.Errorf("inspect existing artifact %s: %w", name, err)
	}
	temporary, temporaryName, err := createTemporaryArtifact(directoryFD)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Unlinkat(directoryFD, temporaryName, 0) }()
	if err := encodeAndSyncJSON(temporary, value); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary artifact: %w", err)
	}
	// Recheck immediately before replacement so an unsafe destination is never
	// knowingly overwritten. The validated 0700 directory prevents another UID
	// from changing entries between this check and renameat.
	if _, err := artifactExistsAt(directoryFD, name); err != nil {
		return fmt.Errorf("inspect artifact before replacement %s: %w", name, err)
	}
	if err := unix.Renameat(directoryFD, temporaryName, directoryFD, name); err != nil {
		return fmt.Errorf("replace artifact %s: %w", name, err)
	}
	if _, err := artifactExistsAt(directoryFD, name); err != nil {
		return fmt.Errorf("verify replaced artifact %s: %w", name, err)
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return fmt.Errorf("sync artifact directory: %w", err)
	}
	return nil
}

func encodeAndSyncJSON(file *os.File, value any) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return nil
}

func createTemporaryArtifact(directoryFD int) (*os.File, string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return nil, "", fmt.Errorf("generate temporary artifact name: %w", err)
		}
		name := temporaryFilePrefix + hex.EncodeToString(random) + ".tmp"
		fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("create temporary artifact: %w", err)
		}
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(directoryFD, name, 0)
			return nil, "", fmt.Errorf("set temporary artifact permissions: %w", err)
		}
		if err := validateOpenArtifactFD(fd, name); err != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(directoryFD, name, 0)
			return nil, "", err
		}
		return os.NewFile(uintptr(fd), name), name, nil
	}
	return nil, "", errors.New("could not allocate a unique temporary artifact")
}

func openSecureArtifactDirectory(directory string) (*os.File, error) {
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open checkpoint directory: %w", err)
	}
	if err := validateDirectoryFD(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), directory), nil
}

func validateDirectoryFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect checkpoint directory: %w", err)
	}
	return validateOwnedMode("checkpoint directory", &stat, unix.S_IFDIR, 0o700)
}

func openSecureArtifact(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := validateOpenArtifactFD(fd, filepath.Base(path)); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openSecureArtifactAt(directoryFD int, name string, access int) (*os.File, error) {
	if err := validateArtifactName(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(directoryFD, name, access|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := validateOpenArtifactFD(fd, name); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func validateOpenArtifactFD(fd int, name string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect artifact %s: %w", name, err)
	}
	return validateOwnedMode("artifact "+name, &stat, unix.S_IFREG, 0o600)
}

func validateOwnedMode(label string, stat *unix.Stat_t, requiredType uint32, maximumMode uint32) error {
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != requiredType {
		kind := "file"
		if requiredType == unix.S_IFDIR {
			kind = "directory"
		}
		return fmt.Errorf("%s must be a real non-symlink %s", label, kind)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s must be owned by effective uid %d", label, os.Geteuid())
	}
	permissions := mode & 0o7777
	if permissions&^maximumMode != 0 {
		return fmt.Errorf("%s permissions %o are broader than %04o", label, permissions, maximumMode)
	}
	return nil
}

func artifactExistsAt(directoryFD int, name string) (bool, error) {
	file, err := openSecureArtifactAt(directoryFD, name, unix.O_RDONLY)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	return true, nil
}

func validateArtifactName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, filepath.Separator) {
		return errors.New("artifact name must be a single path component")
	}
	return nil
}

func (s artifactStore) manifestExists() (bool, error) {
	var exists bool
	err := s.withDirectoryFD(func(directoryFD int) error {
		var err error
		exists, err = artifactExistsAt(directoryFD, manifestFile)
		return err
	})
	return exists, err
}

func (s artifactStore) acquireExclusiveLock() (*artifactLock, error) {
	return s.acquireExclusiveLockWithCreate(true)
}

// acquireExistingExclusiveLock is the read-only counterpart used by verify:
// it never creates cutover.lock and fails when the lock artifact is absent.
func (s artifactStore) acquireExistingExclusiveLock() (*artifactLock, error) {
	return s.acquireExclusiveLockWithCreate(false)
}

func (s artifactStore) acquireExclusiveLockWithCreate(create bool) (*artifactLock, error) {
	var lock *artifactLock
	err := s.withDirectoryFD(func(directoryFD int) error {
		var err error
		lock, err = acquireExclusiveLockAt(directoryFD, create)
		return err
	})
	return lock, err
}

func acquireExclusiveLockAt(directoryFD int, create bool) (*artifactLock, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Openat(directoryFD, artifactLockFile, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open checkpoint lock: %w", err)
	}
	if err := validateOpenArtifactFD(fd, artifactLockFile); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrCheckpointLocked
		}
		return nil, fmt.Errorf("lock checkpoint directory: %w", err)
	}
	return &artifactLock{file: os.NewFile(uintptr(fd), artifactLockFile)}, nil
}

func (lock *artifactLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	file := lock.file
	lock.file = nil
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock checkpoint directory: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close checkpoint lock: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}

func (lock *artifactLock) Release() error { return lock.release() }

func (lock *artifactLock) Close() error { return lock.release() }
