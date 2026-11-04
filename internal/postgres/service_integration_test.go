package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgreSQLWorkflow(t *testing.T) {
	connectionURL := os.Getenv("TUSK_TEST_DATABASE_URL")
	if connectionURL == "" {
		t.Skip("set TUSK_TEST_DATABASE_URL to run real PostgreSQL integration checks")
	}
	admin, err := pgx.Connect(context.Background(), connectionURL)
	if err != nil {
		t.Fatal("cannot connect to integration database")
	}
	defer admin.Close(context.Background())
	schema := fmt.Sprintf("tusk test %d", time.Now().UnixNano())
	table := `odd "table`
	qualified := pgx.Identifier{schema, table}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	emptySchema := schema + " empty"
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+pgx.Identifier{emptySchema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{emptySchema}.Sanitize())
	if _, err := admin.Exec(context.Background(), "CREATE TABLE "+qualified+` (id bigint PRIMARY KEY, amount numeric, nullable text, empty text, payload jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), "INSERT INTO "+qualified+` VALUES (9223372036854775807, 12345678901234567890.12345678901234567890, NULL, '', '{"ok":true}')`); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	defer s.Shutdown()
	info, err := s.Connect(connectionURL)
	if err != nil || info.Database == "" {
		t.Fatalf("connect: %+v %v", info, err)
	}
	t.Run("structured login and JDBC URL", func(t *testing.T) {
		config, err := pgx.ParseConfig(connectionURL)
		if err != nil {
			t.Fatal("integration database URL is invalid")
		}
		options := ConnectionOptions{Host: config.Host, Port: int(config.Port), Database: config.Database, User: config.User, Password: config.Password, SSLMode: "disable"}
		structured, err := s.ConnectWithOptions(options)
		if err != nil || structured.Database != info.Database || structured.User != info.User {
			t.Fatalf("structured login did not connect to expected database: %v", err)
		}
		jdbc, err := s.Connect("jdbc:" + connectionURL)
		if err != nil || jdbc.Database != info.Database {
			t.Fatalf("JDBC login did not connect: %v", err)
		}
	})

	t.Run("schema and quoted table", func(t *testing.T) {
		schemas, err := s.ListSchemas()
		if err != nil {
			t.Fatal(err)
		}
		foundEmpty := false
		for _, name := range schemas {
			foundEmpty = foundEmpty || name == emptySchema
		}
		if !foundEmpty {
			t.Fatal("empty schema missing from catalog")
		}
		tables, err := s.ListTables()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range tables {
			found = found || (item.Schema == schema && item.Name == table)
		}
		if !found {
			t.Fatal("quoted schema/table missing from catalog")
		}
		result, err := s.BrowseTable(schema, table)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Columns) != 5 || len(result.Rows) != 1 {
			t.Fatalf("unexpected table result: %+v", result)
		}
		row := result.Rows[0]
		if *row[0] != "9223372036854775807" || *row[1] != "12345678901234567890.12345678901234567890" || row[2] != nil || row[3] == nil || *row[3] != "" {
			t.Fatalf("values changed across bridge: %+v", row)
		}
	})

	t.Run("one statement and useful errors", func(t *testing.T) {
		if _, err := s.RunQuery("SELECT 1; SELECT 2"); err == nil || !strings.Contains(err.Error(), "42601") {
			t.Fatalf("multiple statements accepted: %v", err)
		}
		if _, err := s.RunQuery("SELECT * FROM no_such_tusk_table"); err == nil || !strings.Contains(err.Error(), "42P01") {
			t.Fatalf("missing useful SQLSTATE: %v", err)
		}
		if _, err := s.RunQuery("SELECT ';' AS semicolon"); err != nil {
			t.Fatalf("valid statement with semicolon rejected: %v", err)
		}
	})

	t.Run("row and byte limits", func(t *testing.T) {
		result, err := s.RunQuery("SELECT i FROM generate_series(1, 1200) AS i")
		if err != nil || result.RowCount != queryRowLimit || !result.Truncated {
			t.Fatalf("row limit: rows=%d truncated=%v err=%v", result.RowCount, result.Truncated, err)
		}
		result, err = s.RunQuery("SELECT repeat('x', 1048576) FROM generate_series(1, 8)")
		if err != nil || !result.Truncated || result.RowCount >= 8 {
			t.Fatalf("byte limit: rows=%d truncated=%v err=%v", result.RowCount, result.Truncated, err)
		}
		if _, err := s.RunQuery("SELECT 1"); err != nil {
			t.Fatalf("connection unusable after truncation: %v", err)
		}
	})

	t.Run("truncated returning commits write", func(t *testing.T) {
		if _, err := s.RunQuery("CREATE TEMP TABLE tusk_returning_test (id integer)"); err != nil {
			t.Fatal(err)
		}
		result, err := s.RunQuery("INSERT INTO tusk_returning_test SELECT i FROM generate_series(1, 1200) i RETURNING id")
		if err != nil || !result.Truncated || result.RowsAffected != "1200" {
			t.Fatalf("returning: affected=%s truncated=%v err=%v", result.RowsAffected, result.Truncated, err)
		}
		result, err = s.RunQuery("SELECT count(*) FROM tusk_returning_test")
		if err != nil || len(result.Rows) != 1 || *result.Rows[0][0] != "1200" {
			t.Fatalf("truncation did not commit full write: %v", err)
		}
	})

	t.Run("cancel then reuse", func(t *testing.T) {
		done := make(chan error, 1)
		go func() { _, err := s.RunQuery("SELECT pg_sleep(20)"); done <- err }()
		deadline := time.Now().Add(2 * time.Second)
		for {
			s.mu.Lock()
			active := s.cancel != nil
			s.mu.Unlock()
			if active {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("query never became active")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := s.RunQuery("SELECT 2"); err == nil {
			t.Fatal("concurrent query accepted")
		}
		if err := s.CancelQuery(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "canceled") {
				t.Fatalf("expected cancellation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancel did not stop database work")
		}
		if _, err := s.RunQuery("SELECT 42"); err != nil {
			t.Fatalf("connection unusable after cancellation: %v", err)
		}
	})

	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunQuery("SELECT 1"); err == nil {
		t.Fatal("query accepted after disconnect")
	}
}
