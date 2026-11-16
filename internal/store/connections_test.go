package store

import (
	"crypto/rand"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"tusk/internal/database"
	"tusk/internal/db"
	"tusk/internal/postgres"
	"tusk/internal/sqlite"
)

func connectionTestDatabase() *database.Service {
	return database.NewService(map[string]db.Provider{"postgres": postgres.NewProvider(), "sqlite": sqlite.NewProvider()})
}

func connectionTestKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func connectionTestInput() SaveConnectionInput {
	return SaveConnectionInput{
		Name: " Local database ", Mode: "fields",
		Options: db.ConnectionOptions{Host: "localhost", Port: 5432, Database: "test", User: "user", Password: " keep @:/? this exact \t", SSLMode: "disable"},
	}
}

func TestConnectionStorePersistenceAndCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "connections.sqlite3")
	key := connectionTestKey(t)
	calls := 0
	provider := func(create bool) ([]byte, error) {
		calls++
		if calls == 1 && !create {
			t.Error("first save did not allow a new key")
		}
		return key, nil
	}
	store := newConnectionsService(connectionTestDatabase(), path, provider)
	defer store.Shutdown()
	if connections, err := store.ListConnections(); err != nil || len(connections) != 0 || calls != 0 {
		t.Fatalf("empty listing attempted key access: %v", err)
	}
	saved, err := store.SaveConnection(connectionTestInput())
	if err != nil || !validSavedConnectionID(saved.ID) || saved.Name != "Local database" || saved.Host != "localhost" {
		t.Fatalf("save metadata incorrect: %+v %v", saved, err)
	}
	store.Shutdown()
	reopened := newConnectionsService(connectionTestDatabase(), path, provider)
	defer reopened.Shutdown()
	connections, err := reopened.ListConnections()
	if err != nil || len(connections) != 1 || connections[0] != saved || calls != 1 {
		t.Fatalf("restart/listing changed metadata or touched key: %v", err)
	}
	if err := reopened.DeleteConnection(saved.ID); err != nil || calls != 1 {
		t.Fatalf("delete required a key or failed: %v", err)
	}
	if err := reopened.DeleteConnection(saved.ID); err == nil {
		t.Fatal("missing connection deletion succeeded")
	}
	connections, err = reopened.ListConnections()
	if err != nil || len(connections) != 0 {
		t.Fatalf("deleted connection still listed: %v", err)
	}
}

func TestSavedPostgreSQLProviderIsExplicit(t *testing.T) {
	for _, mode := range []string{"fields", "url"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "connections.sqlite3")
			keys := func(bool) ([]byte, error) { return storageSecurityKey, nil }
			store := securityConnectionStore(t, path, keys)
			input := connectionTestInput()
			if mode == "url" {
				input = securityConnectionInput()
			}
			saved, err := store.SaveConnection(input)
			if err != nil || saved.Provider != "postgres" {
				t.Fatalf("PostgreSQL provider missing from saved metadata: %+v %v", saved, err)
			}
			storage := securitySQLite(t, path)
			var provider string
			if err := storage.QueryRow("SELECT provider FROM saved_connections WHERE id=?", saved.ID).Scan(&provider); err != nil || provider != "postgres" {
				t.Fatalf("PostgreSQL provider missing from stored row: provider=%q error=%v", provider, err)
			}
			if mode == "fields" {
				payload, err := decryptConnection(storageSecurityKey, saved, securityPayload(t, storage, saved.ID))
				if err != nil || payload.Options.Provider != "postgres" {
					t.Fatalf("PostgreSQL provider missing from saved options: %v", err)
				}
			}
			store.Shutdown()
			reopened := securityConnectionStore(t, path, keys)
			listed, err := reopened.ListConnections()
			if err != nil || len(listed) != 1 || listed[0] != saved {
				t.Fatalf("saved provider did not survive restart: %v", err)
			}
			if _, err := reopened.ConnectSaved(saved.ID); err == nil || !strings.Contains(err.Error(), "shutting down") {
				t.Fatalf("explicit provider record failed authentication: %v", err)
			}
		})
	}
}

func TestConnectionStoreUpgradesEmptySchemas(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		t.Run(version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "connections.sqlite3")
			location := url.URL{Scheme: "file", Path: path}
			storage, err := sql.Open("sqlite3", location.String())
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			colorColumn := ""
			if version == "2" {
				colorColumn = ", color TEXT NOT NULL DEFAULT ''"
			}
			if _, err := storage.Exec(`CREATE TABLE saved_connections (
				id TEXT PRIMARY KEY, name TEXT NOT NULL, mode TEXT NOT NULL,
				host TEXT NOT NULL, port INTEGER NOT NULL, database_name TEXT NOT NULL,
				user_name TEXT NOT NULL, ssl_mode TEXT NOT NULL, encrypted_payload BLOB NOT NULL
			` + colorColumn + "); PRAGMA user_version=" + version); err != nil {
				t.Fatal(err)
			}
			store := securityConnectionStore(t, path, func(bool) ([]byte, error) { return storageSecurityKey, nil })
			listed, err := store.ListConnections()
			if err != nil || len(listed) != 0 {
				t.Fatalf("empty schema upgrade failed: %v", err)
			}
			var upgraded int
			if err := storage.QueryRow("PRAGMA user_version").Scan(&upgraded); err != nil || upgraded != 3 {
				t.Fatal("schema version was not upgraded")
			}
			saved, err := store.SaveConnection(connectionTestInput())
			if err != nil || saved.Provider != "postgres" {
				t.Fatalf("upgraded schema could not save an explicit PostgreSQL provider: %v", err)
			}
		})
	}
}

func TestConnectionStoreParallelCRUD(t *testing.T) {
	key := connectionTestKey(t)
	store := newConnectionsService(connectionTestDatabase(), filepath.Join(t.TempDir(), "connections.sqlite3"), func(bool) ([]byte, error) { return key, nil })
	defer store.Shutdown()
	var work sync.WaitGroup
	for range 12 {
		work.Go(func() {
			saved, err := store.SaveConnection(connectionTestInput())
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := store.ListConnections(); err != nil {
				t.Error(err)
			}
			if err := store.DeleteConnection(saved.ID); err != nil {
				t.Error(err)
			}
		})
	}
	work.Wait()
	if connections, err := store.ListConnections(); err != nil || len(connections) != 0 {
		t.Fatalf("parallel create/delete left records behind: %v", err)
	}
}

func TestConnectionStoreRejectsInvalidSettingsBeforeOpening(t *testing.T) {
	for _, change := range []func(*SaveConnectionInput){
		func(input *SaveConnectionInput) { input.Name = " " },
		func(input *SaveConnectionInput) { input.Name = strings.Repeat("x", 101) },
		func(input *SaveConnectionInput) { input.Name = "bad\x00name" },
		func(input *SaveConnectionInput) { input.Mode = "unknown" },
		func(input *SaveConnectionInput) { input.Color = "not-a-color" },
		func(input *SaveConnectionInput) { input.Options.Port = 0 },
		func(input *SaveConnectionInput) {
			input.Mode, input.URL = "url", "postgres://user:DO-NOT-ECHO@localhost:invalid/test"
		},
	} {
		input := connectionTestInput()
		change(&input)
		path := filepath.Join(t.TempDir(), "not-created", "connections.sqlite3")
		store := newConnectionsService(connectionTestDatabase(), path, func(bool) ([]byte, error) { t.Fatal("invalid settings touched Keychain"); return nil, nil })
		if _, err := store.SaveConnection(input); err == nil || strings.Contains(err.Error(), "DO-NOT-ECHO") {
			t.Fatal("invalid settings accepted or exposed credentials")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid settings created a local database")
		}
		store.Shutdown()
	}
}

func TestConnectionStoreRejectsUnknownDatabaseFormats(t *testing.T) {
	for _, setup := range []string{"PRAGMA user_version=4", "CREATE TABLE unrelated (value TEXT)"} {
		path := filepath.Join(t.TempDir(), "connections.sqlite3")
		location := url.URL{Scheme: "file", Path: path}
		db, err := sql.Open("sqlite3", location.String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(setup); err != nil {
			t.Fatal(err)
		}
		db.Close()
		store := newConnectionsService(connectionTestDatabase(), path, func(bool) ([]byte, error) { t.Fatal("unknown database touched Keychain"); return nil, nil })
		if _, err := store.ListConnections(); err == nil {
			t.Fatal("unknown database format was overwritten")
		}
		store.Shutdown()
	}
}

func TestSavedURLRequiresExplicitTLSMode(t *testing.T) {
	for _, inherited := range []string{"require", "disable"} {
		t.Setenv("PGSSLMODE", inherited)
		_, _, err := validateSavedConnection(SaveConnectionInput{Name: "URL fixture", Mode: "url", URL: "postgresql://user:PRIVATE-PASSWORD@localhost/database"})
		if err == nil || !strings.Contains(err.Error(), "explicit TLS mode") || strings.Contains(err.Error(), "PRIVATE-PASSWORD") {
			t.Fatalf("environment TLS mode bypassed explicit saved URL validation: %v", err)
		}
	}
}

func TestSavedURLExplicitTLSIgnoresEnvironment(t *testing.T) {
	for _, suffix := range []string{"sslmode=require", "ssl=true"} {
		t.Setenv("PGSSLMODE", "require")
		key := connectionTestKey(t)
		database := connectionTestDatabase()
		database.Shutdown()
		store := newConnectionsService(database, filepath.Join(t.TempDir(), "connections.sqlite3"), func(bool) ([]byte, error) { return key, nil })
		saved, err := store.SaveConnection(SaveConnectionInput{Name: "Explicit TLS fixture", Mode: "url", URL: "postgresql://user:PRIVATE-PASSWORD@localhost/database?" + suffix})
		if err != nil || saved.SSLMode != "require" {
			t.Fatalf("explicit TLS setting not saved correctly: %v", err)
		}
		t.Setenv("PGSSLMODE", "disable")
		if _, err := store.ConnectSaved(saved.ID); err == nil || !strings.Contains(err.Error(), "shutting down") {
			t.Fatalf("explicit saved TLS changed with environment: %v", err)
		}
		store.Shutdown()
	}
}

func TestSavedURLTLSMetadataMatchesOrderedParameters(t *testing.T) {
	for query, expected := range map[string]string{
		"sslmode=require&sslmode=disable":  "disable",
		"ssl=true&sslmode=disable":         "disable",
		"sslmode=disable&ssl=true":         "require",
		"sslmode=disable&%73slmode=prefer": "prefer",
	} {
		saved, _, err := validateSavedConnection(SaveConnectionInput{Name: "TLS metadata fixture", Mode: "url", URL: "postgresql://user@localhost/database?" + query})
		if err != nil || saved.SSLMode != expected {
			t.Fatalf("TLS metadata differs from final URL setting: got %s, want %s, error=%v", saved.SSLMode, expected, err)
		}
	}
}

func TestPostgreSQLSavedConnections(t *testing.T) {
	dsn := os.Getenv("TUSK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TUSK_TEST_DATABASE_URL to run saved connection integration checks")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("integration URL is invalid")
	}
	key := connectionTestKey(t)
	path := filepath.Join(t.TempDir(), "connections.sqlite3")
	database := connectionTestDatabase()
	defer database.Shutdown()
	provider := func(bool) ([]byte, error) { return key, nil }
	store := newConnectionsService(database, path, provider)
	input := SaveConnectionInput{Name: "Fields fixture", Mode: "fields", Options: db.ConnectionOptions{Host: config.Host, Port: int(config.Port), Database: config.Database, User: config.User, Password: config.Password, SSLMode: "disable"}}
	saved, err := store.SaveConnection(input)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := store.ConnectSaved(saved.ID); err != nil || info.Database != config.Database {
		t.Fatalf("saved fields failed to connect: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("integration URL must use PostgreSQL URL syntax")
	}
	parsed.User = url.User(config.User)
	query := parsed.Query()
	query.Del("password")
	parsed.RawQuery = query.Encode()
	t.Setenv("PGPASSWORD", config.Password)
	savedURL, err := store.SaveConnection(SaveConnectionInput{Name: "URL fixture", Mode: "url", URL: "jdbc:" + parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	store.Shutdown()
	store = newConnectionsService(database, path, provider)
	defer store.Shutdown()
	t.Setenv("PGPASSWORD", "different-password-after-saving")
	if info, err := store.ConnectSaved(savedURL.ID); err != nil || info.Database != config.Database {
		t.Fatalf("saved URL password changed after restart/environment change: %v", err)
	}
	parsed.Path = ""
	t.Setenv("PGDATABASE", config.Database)
	defaultDatabase, err := store.SaveConnection(SaveConnectionInput{Name: "Environment target fixture", Mode: "url", URL: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGDATABASE", "changed-database-after-saving")
	if _, err := store.ConnectSaved(defaultDatabase.ID); err == nil || !strings.Contains(err.Error(), "resolves to different connection settings") {
		t.Fatalf("saved URL silently changed its resolved endpoint: %v", err)
	}
}
