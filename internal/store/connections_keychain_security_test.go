//go:build darwin && cgo

package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keybase/go-keychain"
)

func TestSavedConnectionNativeKeychainSecurity(t *testing.T) {
	if os.Getenv("TUSK_TEST_KEYCHAIN") != "1" {
		t.Skip("set TUSK_TEST_KEYCHAIN=1 to test a temporary, isolated macOS Keychain item")
	}
	service := fmt.Sprintf("io.tusk.app.test.storage.%d", time.Now().UnixNano())
	account := "synthetic-encryption-key"
	t.Cleanup(func() {
		if err := keychain.DeleteGenericPasswordItem(service, account); err != nil && !errors.Is(err, keychain.ErrorItemNotFound) {
			t.Error("could not remove the isolated Keychain test item")
		}
		remaining, err := keychain.GetGenericPassword(service, account, "", "")
		if err != nil || len(remaining) != 0 {
			t.Error("isolated Keychain test item remains after cleanup")
		}
	})
	if _, err := loadKeychainConnectionKey(service, account, false); err == nil {
		t.Fatal("missing native key was silently created during read")
	}

	var writers sync.WaitGroup
	keys := make(chan []byte, 3)
	errorsSeen := make(chan error, 3)
	for range 3 {
		writers.Go(func() {
			key, err := loadKeychainConnectionKey(service, account, true)
			keys <- key
			errorsSeen <- err
		})
	}
	writers.Wait()
	close(keys)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal("could not create/read an isolated native Keychain encryption key")
		}
	}
	var original []byte
	for key := range keys {
		if len(key) != 32 {
			t.Fatal("native encryption key is not 256 bits")
		}
		if original == nil {
			original = bytes.Clone(key)
		}
		if !bytes.Equal(original, key) {
			t.Fatal("concurrent creators persisted different encryption keys")
		}
	}
	provider := func(create bool) ([]byte, error) { return loadKeychainConnectionKey(service, account, create) }
	path := filepath.Join(t.TempDir(), "connections.sqlite3")
	store := securityConnectionStore(t, path, provider)
	saved, err := store.SaveConnection(securityConnectionInput())
	if err != nil {
		t.Fatal(err)
	}
	store.Shutdown()
	reopened := securityConnectionStore(t, path, provider)
	if _, err := reopened.ConnectSaved(saved.ID); err == nil || !bytes.Contains([]byte(err.Error()), []byte("shutting down")) {
		t.Fatalf("native Keychain key did not decrypt the same SQLite file after restart: %v", err)
	}
	if err := keychain.DeleteGenericPasswordItem(service, account); err != nil {
		t.Fatal("could not remove the isolated native test key")
	}
	_, err = reopened.ConnectSaved(saved.ID)
	requireSafeStorageError(t, err)
	if bytes.Contains([]byte(err.Error()), []byte("shutting down")) {
		t.Fatal("missing native key reached the database connector")
	}
}
