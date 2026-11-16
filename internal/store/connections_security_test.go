package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var storageSecurityKey = bytes.Repeat([]byte{0x41}, 32)

func securityConnectionInput() SaveConnectionInput {
	return SaveConnectionInput{
		Name: "Security fixture", Mode: "url",
		URL: "postgresql://fixture:URL-USERINFO-SECRET@127.0.0.1:1/fixture?sslmode=disable&password=URL-QUERY-SECRET&sslpassword=TLS-KEY-SECRET",
	}
}

func securityConnectionStore(t *testing.T, path string, keys connectionKeyProvider) *ConnectionsService {
	t.Helper()
	database := connectionTestDatabase()
	database.Shutdown() // Successful decryption can be checked without network access.
	store := newConnectionsService(database, path, keys)
	t.Cleanup(store.Shutdown)
	return store
}

func securitySQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	location := url.URL{Scheme: "file", Path: path}
	location.RawQuery = "mode=rw&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func securityPayload(t *testing.T, db *sql.DB, id string) []byte {
	t.Helper()
	var payload []byte
	if err := db.QueryRow("SELECT encrypted_payload FROM saved_connections WHERE id=?", id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func requireSafeStorageError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a storage error")
	}
	for _, secret := range []string{"URL-USERINFO-SECRET", "URL-QUERY-SECRET", "TLS-KEY-SECRET", "PROVIDER-ERROR-SECRET"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("storage error exposed credentials")
		}
	}
}

func TestSavedConnectionCiphertextOnlyOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private ?# directory", "connections ?#%.sqlite3")
	keys := func(bool) ([]byte, error) { return storageSecurityKey, nil }
	store := securityConnectionStore(t, path, keys)
	first, err := store.SaveConnection(securityConnectionInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SaveConnection(securityConnectionInput())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.ListConnections()
	if err != nil || len(metadata) != 2 {
		t.Fatalf("list after save: count=%d error=%v", len(metadata), err)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	store.Shutdown()
	db := securitySQLite(t, path)
	one, two := securityPayload(t, db, first.ID), securityPayload(t, db, second.ID)
	if len(one) < 29 || len(two) < 29 || bytes.Equal(one[1:13], two[1:13]) {
		t.Fatal("saved credentials did not receive independent random GCM nonces")
	}
	for _, filePath := range []string{filepath.Dir(path), path} {
		info, err := os.Stat(filePath)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("private storage permissions: got %o, want %o", info.Mode().Perm(), want)
		}
	}
	if err := filepath.WalkDir(filepath.Dir(path), func(filePath string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		contents, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		for _, secret := range [][]byte{[]byte("URL-USERINFO-SECRET"), []byte("URL-QUERY-SECRET"), []byte("TLS-KEY-SECRET"), storageSecurityKey} {
			if bytes.Contains(contents, secret) || bytes.Contains(encoded, secret) {
				t.Fatal("plaintext credential or encryption key found in SQLite files or frontend metadata")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	reopened := securityConnectionStore(t, path, keys)
	if _, err := reopened.ConnectSaved(first.ID); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("same-disk restart did not decrypt the original record: %v", err)
	}
}

func TestSavedConnectionRejectsChangedDataAndKeys(t *testing.T) {
	for _, change := range []string{"ciphertext", "record-id", "empty-record-id", "metadata", "provider", "wrong-key", "missing-key", "invalid-key"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "connections.sqlite3")
			store := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
			saved, err := store.SaveConnection(securityConnectionInput())
			if err != nil {
				t.Fatal(err)
			}
			store.Shutdown()
			db := securitySQLite(t, path)
			original := securityPayload(t, db, saved.ID)
			id := saved.ID
			keys := connectionKeyProvider(func(bool) ([]byte, error) { return storageSecurityKey, nil })
			switch change {
			case "ciphertext":
				corrupt := bytes.Clone(original)
				corrupt[len(corrupt)/2] ^= 0x80
				_, err = db.Exec("UPDATE saved_connections SET encrypted_payload=? WHERE id=?", corrupt, id)
			case "record-id":
				id = strings.Repeat("b", 32)
				_, err = db.Exec("UPDATE saved_connections SET id=? WHERE id=?", id, saved.ID)
			case "empty-record-id":
				id = ""
				_, err = db.Exec("UPDATE saved_connections SET id='' WHERE id=?", saved.ID)
			case "metadata":
				_, err = db.Exec("UPDATE saved_connections SET host=? WHERE id=?", "changed.invalid", id)
			case "provider":
				_, err = db.Exec("UPDATE saved_connections SET provider='sqlite' WHERE id=?", id)
			case "wrong-key":
				keys = func(create bool) ([]byte, error) {
					if create {
						t.Error("existing records requested key replacement")
					}
					return bytes.Repeat([]byte{0x42}, 32), nil
				}
			case "missing-key":
				keys = func(create bool) ([]byte, error) {
					if create {
						t.Error("missing key was silently recreated")
					}
					return nil, errors.New("PROVIDER-ERROR-SECRET")
				}
			case "invalid-key":
				keys = func(bool) ([]byte, error) { return []byte("short"), nil }
			}
			if err != nil {
				t.Fatal(err)
			}
			changed := securityConnectionStore(t, path, keys)
			_, err = changed.ConnectSaved(id)
			requireSafeStorageError(t, err)
			if strings.Contains(err.Error(), "shutting down") {
				t.Fatal("changed credentials or metadata reached the database connector")
			}
			_, err = changed.SaveConnection(securityConnectionInput())
			requireSafeStorageError(t, err)
			_, err = changed.UpdateConnection(id, UpdateConnectionInput{Name: "Changed name", Color: "#356cca", Options: connectionTestInput().Options})
			requireSafeStorageError(t, err)
			var count int
			if err := db.QueryRow("SELECT count(*) FROM saved_connections").Scan(&count); err != nil || count != 1 {
				t.Fatalf("failed closed save changed record count: count=%d error=%v", count, err)
			}
		})
	}
}

func TestSavedConnectionFilesystemFailuresDoNotFallback(t *testing.T) {
	for _, failure := range []string{"file-symlink", "directory-symlink", "unwritable-parent"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(root, "existing-file")
			if err := os.WriteFile(outside, []byte("existing user data"), 0644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "private", "connections.sqlite3")
			switch failure {
			case "file-symlink":
				if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				if err := os.Symlink(root, filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			case "unwritable-parent":
				if os.Geteuid() == 0 {
					t.Skip("permission check requires a non-root account")
				}
				if err := os.Chmod(root, 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(root, 0700) })
			}
			keyCalled := false
			store := securityConnectionStore(t, path, func(bool) ([]byte, error) { keyCalled = true; return storageSecurityKey, nil })
			_, err := store.SaveConnection(securityConnectionInput())
			requireSafeStorageError(t, err)
			if keyCalled {
				t.Fatal("storage failure attempted a Keychain operation or alternate storage")
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "existing user data" {
				t.Fatal("storage failure changed an unrelated user file")
			}
		})
	}
}

func TestSavedConnectionShutdownConcurrentCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.sqlite3")
	keys := func(bool) ([]byte, error) { return storageSecurityKey, nil }
	store := securityConnectionStore(t, path, keys)
	if _, err := store.SaveConnection(securityConnectionInput()); err != nil {
		t.Fatal(err)
	}
	var work sync.WaitGroup
	start := make(chan struct{})
	for range 12 {
		work.Go(func() {
			<-start
			_, _ = store.ListConnections()
			_, _ = store.SaveConnection(securityConnectionInput())
		})
	}
	close(start)
	store.Shutdown()
	work.Wait()
	if _, err := store.ListConnections(); err == nil {
		t.Fatal("closed storage silently reopened")
	}
	if _, err := store.SaveConnection(securityConnectionInput()); err == nil {
		t.Fatal("closed storage accepted a save")
	}
	store.Shutdown()
	reopened := securityConnectionStore(t, path, keys)
	if saved, err := reopened.ListConnections(); err != nil || len(saved) < 1 {
		t.Fatalf("concurrent shutdown corrupted SQLite: %v", err)
	}
}

func TestSavedConnectionShutdownWaitsForActiveSave(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishProvider := func() { releaseOnce.Do(func() { close(release) }) }
	defer finishProvider()
	store := securityConnectionStore(t, filepath.Join(t.TempDir(), "connections.sqlite3"), func(bool) ([]byte, error) {
		close(entered)
		<-release
		return storageSecurityKey, nil
	})
	saved := make(chan error, 1)
	go func() { _, err := store.SaveConnection(securityConnectionInput()); saved <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("save never reached the key provider")
	}
	closed := make(chan struct{})
	go func() { store.Shutdown(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("shutdown returned before the active save finished")
	case <-time.After(20 * time.Millisecond):
	}
	finishProvider()
	if err := <-saved; err != nil {
		t.Fatalf("shutdown interrupted an atomic save: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after save")
	}
}

func TestSavedURLTLSAliasMustRemainEffective(t *testing.T) {
	saved, _, err := validateSavedConnection(SaveConnectionInput{
		Name: "Overridden TLS alias", Mode: "url",
		URL: "postgresql://fixture@localhost/fixture?sslmode=disable&ssl=true&ssl=false",
	})
	if err != nil || saved.SSLMode != "disable" {
		t.Fatalf("overridden ssl=true alias changed explicit TLS mode: mode=%s error=%v", saved.SSLMode, err)
	}
	_, _, err = validateSavedConnection(SaveConnectionInput{
		Name: "Ineffective TLS alias", Mode: "url",
		URL: "postgresql://fixture@localhost/fixture?ssl=true&ssl=false",
	})
	if err == nil {
		t.Fatal("overridden ssl=true alias bypassed the explicit saved TLS mode requirement")
	}
}
