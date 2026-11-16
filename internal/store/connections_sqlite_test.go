package store

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"tusk/internal/db"
)

func sqliteConnectionFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	location := url.URL{Scheme: "file", Path: path}
	fixture, err := sql.Open("sqlite3", location.String())
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.Exec(`CREATE TABLE records (id INTEGER, note TEXT); INSERT INTO records VALUES (9223372036854775807, 'saved sqlite')`); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSavedSQLiteConnectionPersistenceEditAndReconnect(t *testing.T) {
	file := sqliteConnectionFixture(t, "local ? # 雪.sqlite3")
	otherFile := sqliteConnectionFixture(t, "other.sqlite3")
	path := filepath.Join(t.TempDir(), "connections.sqlite3")
	key := connectionTestKey(t)
	keys := func(bool) ([]byte, error) { return key, nil }
	database := connectionTestDatabase()
	defer database.Shutdown()
	store := newConnectionsService(database, path, keys)
	input := SaveConnectionInput{Name: "Local SQLite", Mode: "file", Options: db.ConnectionOptions{
		Provider: "sqlite", FilePath: file, Password: "unused-password-must-not-be-saved",
	}}
	saved, err := store.SaveConnection(input)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Provider != "sqlite" || saved.FilePath != file || saved.Database != filepath.Base(file) || saved.Mode != "file" || saved.Host != "" {
		t.Fatalf("incorrect SQLite metadata: %+v", saved)
	}
	storage := securitySQLite(t, path)
	payload, err := decryptConnection(key, saved, securityPayload(t, storage, saved.ID))
	if err != nil || payload.Options.Password != "" || payload.Options.Provider != "sqlite" || payload.Options.FilePath != file {
		t.Fatalf("incorrect SQLite payload: %v", err)
	}
	store.Shutdown()
	store = newConnectionsService(database, path, keys)
	defer store.Shutdown()
	listed, err := store.ListConnections()
	if err != nil || len(listed) != 1 || listed[0] != saved {
		t.Fatalf("SQLite metadata did not survive restart: %v", err)
	}
	info, err := store.ConnectSaved(saved.ID)
	if err != nil || info.Provider != "sqlite" || info.FilePath != file {
		t.Fatalf("saved SQLite could not connect: %v", err)
	}
	result, err := database.RunQuery("SELECT id, note FROM records")
	if err != nil || len(result.Rows) != 1 || *result.Rows[0][0] != "9223372036854775807" || *result.Rows[0][1] != "saved sqlite" {
		t.Fatalf("saved SQLite query failed: %v", err)
	}
	update := UpdateConnectionInput{Name: "Edited SQLite", Color: "#356cca", Options: db.ConnectionOptions{Provider: "sqlite", FilePath: otherFile}}
	edited, err := store.UpdateConnection(saved.ID, update)
	if err != nil || edited.ID != saved.ID || edited.FilePath != otherFile {
		t.Fatalf("SQLite edit failed: %v", err)
	}
	if _, err := database.RunQuery("CREATE TEMP TABLE session_kept (id INTEGER)"); err != nil {
		t.Fatalf("editing interrupted the active session: %v", err)
	}
	if info, err := store.ConnectSaved(saved.ID); err != nil || info.FilePath != otherFile {
		t.Fatalf("edited SQLite did not reconnect to its new file: %v", err)
	}
	update.Options.Provider = "postgres"
	if _, err := store.UpdateConnection(saved.ID, update); err == nil {
		t.Fatal("edit changed the saved provider")
	}
}

func TestSavedSQLiteProviderAndFilePathAreAuthenticated(t *testing.T) {
	for _, column := range []string{"provider", "file_path"} {
		t.Run(column, func(t *testing.T) {
			file := sqliteConnectionFixture(t, "fixture.sqlite3")
			path := filepath.Join(t.TempDir(), "connections.sqlite3")
			store := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
			saved, err := store.SaveConnection(SaveConnectionInput{Name: "SQLite", Mode: "file", Options: db.ConnectionOptions{Provider: "sqlite", FilePath: file}})
			if err != nil {
				t.Fatal(err)
			}
			storage := securitySQLite(t, path)
			if _, err := storage.Exec("UPDATE saved_connections SET "+column+"='changed' WHERE id=?", saved.ID); err != nil {
				t.Fatal(err)
			}
			_, err = store.ConnectSaved(saved.ID)
			if err == nil || !strings.Contains(err.Error(), "could not be decrypted") {
				t.Fatalf("tampered %s was accepted: %v", column, err)
			}
		})
	}
}
