package store

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedConnectionUpdatePreservesIDPasswordAndOtherRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.sqlite3")
	store := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
	input := connectionTestInput()
	first, err := store.SaveConnection(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SaveConnection(input)
	if err != nil {
		t.Fatal(err)
	}
	update := UpdateConnectionInput{Name: " Edited database ", Color: "#356CCA", Options: input.Options}
	update.Options.Host, update.Options.Password = "other.local", "not-the-saved-password"
	edited, err := store.UpdateConnection(first.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	if edited.ID != first.ID || edited.Name != "Edited database" || edited.Color != "#356cca" || edited.Host != "other.local" {
		t.Fatalf("updated metadata incorrect: %+v", edited)
	}
	db := securitySQLite(t, path)
	payload, err := decryptConnection(storageSecurityKey, edited, securityPayload(t, db, edited.ID))
	if err != nil || payload.Options.Password != input.Options.Password {
		t.Fatal("editing replaced the saved password without explicit intent")
	}
	update.ReplacePassword, update.Options.Password = true, ""
	edited, err = store.UpdateConnection(first.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	payload, err = decryptConnection(storageSecurityKey, edited, securityPayload(t, db, edited.ID))
	if err != nil || payload.Options.Password != "" {
		t.Fatal("explicit password clearing failed")
	}
	update.Options.Port = 0
	if _, err := store.UpdateConnection(first.ID, update); err == nil {
		t.Fatal("invalid edit was accepted")
	}
	store.Shutdown()
	reopened := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
	listed, err := reopened.ListConnections()
	if err != nil || len(listed) != 2 || listed[0] != edited || listed[1] != second {
		t.Fatalf("updates changed another record or failed to persist: %v", err)
	}
	if _, err := reopened.ConnectSaved(edited.ID); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("edited credentials no longer authenticate: %v", err)
	}
}

func TestSavedURLIdentityEditRetainsResolvedCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.sqlite3")
	store := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
	t.Setenv("PGPASSWORD", "original-environment-secret")
	input := SaveConnectionInput{Name: "URL", Mode: "url", URL: "postgres://fixture@localhost/fixture?sslmode=disable&application_name=tusk"}
	saved, err := store.SaveConnection(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSWORD", "changed-environment-secret")
	edited, err := store.UpdateConnection(saved.ID, UpdateConnectionInput{Name: "Renamed URL", Color: "#287c58"})
	if err != nil {
		t.Fatal(err)
	}
	db := securitySQLite(t, path)
	payload, err := decryptConnection(storageSecurityKey, edited, securityPayload(t, db, edited.ID))
	if err != nil || payload.URL != input.URL || payload.Password != "original-environment-secret" {
		t.Fatal("name/color edit altered stored URL settings or resolved credentials")
	}
	replacement := "postgres://replacement:replacement-secret@other.local/other?sslmode=require"
	edited, err = store.UpdateConnection(saved.ID, UpdateConnectionInput{Name: "Replacement URL", URL: replacement})
	if err != nil {
		t.Fatal(err)
	}
	payload, err = decryptConnection(storageSecurityKey, edited, securityPayload(t, db, edited.ID))
	if err != nil || payload.URL != replacement || payload.Password != "replacement-secret" || edited.Host != "other.local" {
		t.Fatal("explicit URL replacement failed")
	}
	encoded, _ := json.Marshal(edited)
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatal("update response exposed credentials")
	}
}

func TestSavedConnectionColorIsAuthenticatedAndInvalidEditsAreAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.sqlite3")
	store := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
	input := connectionTestInput()
	input.Color = "#356cca"
	saved, err := store.SaveConnection(input)
	if err != nil {
		t.Fatal(err)
	}
	db := securitySQLite(t, path)
	original := securityPayload(t, db, saved.ID)
	if _, err := store.UpdateConnection(saved.ID, UpdateConnectionInput{Name: "Rename", Color: "red"}); err == nil {
		t.Fatal("invalid color was accepted")
	}
	if !bytes.Equal(original, securityPayload(t, db, saved.ID)) {
		t.Fatal("failed edit changed the ciphertext")
	}
	if _, err := db.Exec("UPDATE saved_connections SET color='#b94b51' WHERE id=?", saved.ID); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateConnection(saved.ID, UpdateConnectionInput{Name: "Rename", Options: input.Options})
	requireSafeStorageError(t, err)
	if !bytes.Equal(original, securityPayload(t, db, saved.ID)) {
		t.Fatal("tampered record was overwritten")
	}
	_, err = store.ConnectSaved(saved.ID)
	requireSafeStorageError(t, err)
	if strings.Contains(err.Error(), "shutting down") {
		t.Fatal("tampered color reached the connector")
	}
}

func TestVersionOneConnectionsMigrateWithoutCredentialChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.sqlite3")
	store := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
	saved, err := store.SaveConnection(connectionTestInput())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(saved)
	if bytes.Contains(encoded, []byte("color")) {
		t.Fatal("empty color changed legacy authenticated metadata")
	}
	store.Shutdown()
	db := securitySQLite(t, path)
	original := securityPayload(t, db, saved.ID)
	if _, err := db.Exec("ALTER TABLE saved_connections DROP COLUMN color; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	reopened := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
	listed, err := reopened.ListConnections()
	if err != nil || len(listed) != 1 || listed[0] != saved {
		t.Fatalf("legacy metadata changed: %v", err)
	}
	if !bytes.Equal(original, securityPayload(t, db, saved.ID)) {
		t.Fatal("migration rewrote encrypted credentials")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("migration version incorrect")
	}
	if _, err := reopened.ConnectSaved(saved.ID); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("legacy credentials failed to decrypt after migration: %v", err)
	}
}
