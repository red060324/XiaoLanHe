package postgrestomysql

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestArtifactStoreRejectsUnsafeCheckpointDirectories(t *testing.T) {
	t.Run("secure directory", func(t *testing.T) {
		directory := secureArtifactTestDirectory(t)
		store, err := newArtifactStore(directory)
		if err != nil {
			t.Fatalf("newArtifactStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
	})

	t.Run("symlink", func(t *testing.T) {
		target := secureArtifactTestDirectory(t)
		link := filepath.Join(t.TempDir(), "checkpoint-link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := newArtifactStore(link); err == nil {
			t.Fatal("symlink checkpoint directory was accepted")
		}
	})

	t.Run("regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkpoint-file")
		if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newArtifactStore(path); err == nil {
			t.Fatal("regular file checkpoint path was accepted")
		}
	})

	t.Run("broad permissions", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := newArtifactStore(directory); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("newArtifactStore error = %v", err)
		}
	})

	t.Run("wrong owner metadata", func(t *testing.T) {
		stat := unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: uint32(os.Geteuid()) + 1}
		if err := validateOwnedMode("checkpoint directory", &stat, unix.S_IFDIR, 0o700); err == nil || !strings.Contains(err.Error(), "owned") {
			t.Fatalf("validateOwnedMode error = %v", err)
		}
	})
}

func TestArtifactStorePinsCheckpointDirectoryInode(t *testing.T) {
	parent := secureArtifactTestDirectory(t)
	directory := filepath.Join(parent, "checkpoint")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := newArtifactStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	moved := filepath.Join(parent, "checkpoint-original")
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := newManifest(testIdentity(), "sha256:identity", map[string]TableInventory{}, tables())
	if err := store.saveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(moved, manifestFile)); err != nil {
		t.Fatalf("pinned directory did not receive manifest: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, manifestFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("replacement directory received manifest: %v", err)
	}
}

func TestArtifactStoreCloseIsSharedAndIdempotent(t *testing.T) {
	store := newSecurityArtifactStore(t)
	copyOfStore := store
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copyOfStore.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := copyOfStore.manifestExists(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("operation after close error = %v", err)
	}
}

func TestSecureArtifactReadRejectsSymlinkTypeOwnerAndMode(t *testing.T) {
	directory := secureArtifactTestDirectory(t)
	validPath := filepath.Join(directory, "valid.json")
	if err := os.WriteFile(validPath, []byte(`{"value":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Value string `json:"value"`
	}
	if err := readStrictJSON(validPath, &decoded); err != nil || decoded.Value != "ok" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if err := os.Chmod(validPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := readStrictJSON(validPath, &decoded); err != nil {
		t.Fatalf("owner-read-only artifact rejected: %v", err)
	}

	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(directory, "link.json")
		if err := os.Symlink(validPath, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := readStrictJSON(link, &decoded); err == nil {
			t.Fatal("symlink artifact was read")
		}
	})

	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(directory, "directory.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := readStrictJSON(path, &decoded); err == nil || !strings.Contains(err.Error(), "real non-symlink file") {
			t.Fatalf("directory artifact error = %v", err)
		}
	})

	t.Run("fifo", func(t *testing.T) {
		path := filepath.Join(directory, "fifo.json")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Skipf("fifo unavailable: %v", err)
		}
		if err := readStrictJSON(path, &decoded); err == nil || !strings.Contains(err.Error(), "real non-symlink file") {
			t.Fatalf("fifo artifact error = %v", err)
		}
	})

	t.Run("broad permissions", func(t *testing.T) {
		path := filepath.Join(directory, "broad.json")
		if err := os.WriteFile(path, []byte(`{"value":"private"}`), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := readStrictJSON(path, &decoded); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("broad artifact error = %v", err)
		}
	})

	t.Run("wrong owner metadata", func(t *testing.T) {
		stat := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: uint32(os.Geteuid()) + 1}
		if err := validateOwnedMode("artifact", &stat, unix.S_IFREG, 0o600); err == nil || !strings.Contains(err.Error(), "owned") {
			t.Fatalf("validateOwnedMode error = %v", err)
		}
	})
}

func TestSaveManifestRejectsUnsafeExistingTargets(t *testing.T) {
	manifest := newManifest(testIdentity(), "sha256:identity", map[string]TableInventory{}, tables())

	t.Run("symlink", func(t *testing.T) {
		store := newSecurityArtifactStore(t)
		victim := filepath.Join(store.directory, "victim.json")
		if err := os.WriteFile(victim, []byte("do-not-replace"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(store.directory, manifestFile)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := store.saveManifest(manifest); err == nil {
			t.Fatal("manifest symlink was overwritten")
		}
		contents, err := os.ReadFile(victim)
		if err != nil || string(contents) != "do-not-replace" {
			t.Fatalf("victim contents=%q err=%v", contents, err)
		}
	})

	t.Run("broad permissions", func(t *testing.T) {
		store := newSecurityArtifactStore(t)
		path := filepath.Join(store.directory, manifestFile)
		if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := store.saveManifest(manifest); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("saveManifest error = %v", err)
		}
	})
}

func TestManifestExistsRejectsSymlinkAndTypeAttacks(t *testing.T) {
	store := newSecurityArtifactStore(t)
	victim := filepath.Join(store.directory, "victim")
	if err := os.WriteFile(victim, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(store.directory, manifestFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if exists, err := store.manifestExists(); err == nil || exists {
		t.Fatalf("manifestExists=%t err=%v", exists, err)
	}
}

func TestSaveReportRejectsUnsafeTargetAndPublishesOnce(t *testing.T) {
	report := testReconciliationReport()

	t.Run("symlink", func(t *testing.T) {
		store := newSecurityArtifactStore(t)
		path := reconciliationReportPath(store, report)
		victim := filepath.Join(store.directory, "victim.json")
		if err := os.WriteFile(victim, []byte("do-not-replace"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := store.saveReport(report); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("saveReport error = %v", err)
		}
		contents, err := os.ReadFile(victim)
		if err != nil || string(contents) != "do-not-replace" {
			t.Fatalf("victim contents=%q err=%v", contents, err)
		}
	})

	t.Run("concurrent identical publication", func(t *testing.T) {
		store := newSecurityArtifactStore(t)
		const writers = 8
		errorsByWriter := make([]error, writers)
		start := make(chan struct{})
		var group sync.WaitGroup
		for writer := range errorsByWriter {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				errorsByWriter[index] = store.saveReport(report)
			}(writer)
		}
		close(start)
		group.Wait()
		for writer, err := range errorsByWriter {
			if err != nil {
				t.Fatalf("writer %d: %v", writer, err)
			}
		}
		matches, err := filepath.Glob(filepath.Join(store.directory, "reconciliation-*.json"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("published reports=%v err=%v", matches, err)
		}
		temporary, err := filepath.Glob(filepath.Join(store.directory, temporaryFilePrefix+"*.tmp"))
		if err != nil || len(temporary) != 0 {
			t.Fatalf("temporary artifacts=%v err=%v", temporary, err)
		}
		assertSecureRegularArtifact(t, matches[0])
	})
}

func TestLoadReportValidatesExpectedDigestAndPersistedContent(t *testing.T) {
	store := newSecurityArtifactStore(t)
	report := testReconciliationReport()
	if err := store.saveReport(report); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.loadReport(report.DigestSHA256)
	if err != nil || loaded.DigestSHA256 != report.DigestSHA256 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err := store.loadReport("sha256:../../manifest.json"); err == nil {
		t.Fatal("unsafe expected digest was accepted")
	}

	path := reconciliationReportPath(store, report)
	var tampered ReconciliationReport
	if err := readStrictJSON(path, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.IdentitySHA256 = "sha256:tampered"
	if err := writeAtomicJSON(path, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := store.loadReport(report.DigestSHA256); err == nil || !strings.Contains(err.Error(), "invalid digest") {
		t.Fatalf("load tampered report error = %v", err)
	}
}

func TestArtifactExclusiveLockFailsFastAndCanBeReacquired(t *testing.T) {
	store := newSecurityArtifactStore(t)
	first, err := store.acquireExclusiveLock()
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	second, err := store.acquireExclusiveLock()
	if !errors.Is(err, ErrCheckpointLocked) {
		_ = first.Close()
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second acquisition error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("second release should be idempotent: %v", err)
	}
	reacquired, err := store.acquireExclusiveLock()
	if err != nil {
		t.Fatalf("reacquire lock: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("close reacquired lock: %v", err)
	}
	assertSecureRegularArtifact(t, filepath.Join(store.directory, artifactLockFile))
}

func TestArtifactExistingExclusiveLockNeverCreates(t *testing.T) {
	store := newSecurityArtifactStore(t)
	if lock, err := store.acquireExistingExclusiveLock(); !errors.Is(err, fs.ErrNotExist) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("missing existing lock error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(store.directory, artifactLockFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read-only acquisition created lock: %v", err)
	}
	created, err := store.acquireExclusiveLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	existing, err := store.acquireExistingExclusiveLock()
	if err != nil {
		t.Fatalf("acquire existing lock: %v", err)
	}
	if err := existing.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactExclusiveLockRejectsSymlink(t *testing.T) {
	store := newSecurityArtifactStore(t)
	victim := filepath.Join(store.directory, "victim.lock")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(store.directory, artifactLockFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if lock, err := store.acquireExclusiveLock(); err == nil {
		_ = lock.Close()
		t.Fatal("symlink lock file was accepted")
	}
}

func newSecurityArtifactStore(t *testing.T) artifactStore {
	t.Helper()
	directory := secureArtifactTestDirectory(t)
	store, err := newArtifactStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func secureArtifactTestDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func assertSecureRegularArtifact(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("artifact mode = %v, want regular non-symlink", info.Mode())
	}
	if info.Mode().Perm()&^os.FileMode(0o600) != 0 {
		t.Fatalf("artifact permissions = %o, want no broader than 0600", info.Mode().Perm())
	}
	file, err := openSecureArtifact(path)
	if err != nil {
		t.Fatalf("secure artifact validation: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
