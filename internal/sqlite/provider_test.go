package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tusk/internal/db"
)

func fixture(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test ?# 雪.sqlite3")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	opened, info, err := NewProvider().Open(context.Background(), db.ConnectionConfig{Options: db.ConnectionOptions{Provider: "sqlite", FilePath: path}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Provider != "sqlite" || info.FilePath != path || info.Database != filepath.Base(path) {
		t.Fatalf("wrong info: %#v", info)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return opened.(*Store), path
}

func run(t *testing.T, store *Store, statement string) db.QueryResult {
	t.Helper()
	result, err := store.Query(context.Background(), statement, db.QueryRowLimit)
	if err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
	return result
}

func TestSQLiteWorkflow(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE "odd""table" ("Id" INTEGER, note TEXT, payload BLOB, generated TEXT AS (note || '!'))`)
	run(t, s, `CREATE VIEW "a.view" AS SELECT * FROM "odd""table"`)
	result := run(t, s, `INSERT INTO "odd""table" ("Id", note, payload) VALUES (9223372036854775807, '', X'00ff'), (NULL, NULL, X'') RETURNING *`)
	if result.CommandTag != "INSERT" || result.RowsAffected != "2" || result.RowCount != 2 {
		t.Fatalf("bad insert: %#v", result)
	}
	if got := *result.Rows[0][0]; got != "9223372036854775807" {
		t.Fatalf("lost integer precision: %s", got)
	}
	if result.Rows[0][1] == nil || *result.Rows[0][1] != "" || result.Rows[1][1] != nil {
		t.Fatal("NULL and empty string changed")
	}
	if *result.Rows[0][2] != "X'00ff'" || *result.Rows[1][2] != "X''" {
		t.Fatal("blob changed")
	}
	result = run(t, s, `SELECT * FROM "main"."odd""table" ORDER BY "Id" DESC NULLS LAST LIMIT 100`)
	if result.CommandTag != "SELECT" || result.RowsAffected != "0" || result.RowCount != 2 {
		t.Fatalf("bad select: %#v", result)
	}
	schemas, err := s.ListSchemas(context.Background())
	if err != nil || len(schemas) != 1 || schemas[0] != "main" {
		t.Fatalf("schemas: %v %v", schemas, err)
	}
	tables, err := s.ListTables(context.Background())
	if err != nil || len(tables) != 2 || tables[0].Name != "a.view" || tables[0].Type != "view" || tables[1].Name != `odd"table` {
		t.Fatalf("tables: %v %v", tables, err)
	}
	columns, err := s.ListColumns(context.Background())
	if err != nil || len(columns) != 8 || columns[3].Name != "generated" {
		t.Fatalf("columns: %v %v", columns, err)
	}
	result = run(t, s, `WITH numbers(id) AS (SELECT 2) UPDATE "odd""table" SET "Id"=3 WHERE "Id" IS NULL`)
	if result.CommandTag != "UPDATE" || result.RowsAffected != "1" {
		t.Fatalf("CTE write: %#v", result)
	}
	result = run(t, s, `UPDATE "odd""table" SET note='changed' WHERE 0`)
	if result.RowsAffected != "0" || result.CommandTag != "UPDATE" {
		t.Fatalf("zero affected: %#v", result)
	}
	result = run(t, s, `SELECT 7, 1.5, 'text', NULL`)
	if result.Columns[0].DataType != "integer" || result.Columns[1].DataType != "real" || result.Columns[2].DataType != "text" {
		t.Fatalf("expression types: %#v", result.Columns)
	}
}

func TestSQLiteSingleStatementBeforeWrites(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE items(value TEXT)`)
	for _, statement := range []string{
		`INSERT INTO items VALUES ('never'); SELECT 1`,
		`INSERT INTO items VALUES ('never'); /* comment */ INSERT INTO items VALUES ('also never')`,
		`SELECT ';'; SELECT 2`,
		"SELECT 1; -- next\nSELECT 2",
		"SELECT 1\x00; INSERT INTO items VALUES ('never')",
	} {
		if _, err := s.Query(context.Background(), statement, 100); err == nil {
			t.Fatalf("accepted script: %s", statement)
		}
	}
	result := run(t, s, `SELECT count(*) FROM items`)
	if *result.Rows[0][0] != "0" {
		t.Fatal("rejected script performed a write")
	}
	for _, statement := range []string{
		`SELECT ';' AS ";"; /* ignored ; SELECT */ -- more`,
		`SELECT 'a'';b', "semi;colon" FROM (SELECT 1 AS "semi;colon");`,
		`;; /* before */ SELECT 3; ; -- trailing`,
		`CREATE TRIGGER add_item AFTER INSERT ON items BEGIN UPDATE items SET value=value || ';'; SELECT 1; END; -- trailing`,
	} {
		run(t, s, statement)
	}
	for _, statement := range []string{" ", "-- comment", ";; /* only */"} {
		if _, err := s.Query(context.Background(), statement, 100); err == nil {
			t.Fatal("accepted empty statement")
		}
	}
	if _, err := s.Query(context.Background(), "SELECT ?", 100); err == nil {
		t.Fatal("accepted unbound parameter")
	}
}

func TestSQLitePreservesValuesRegardlessOfDeclaredType(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE values_test(date_value DATE, datetime_value DATETIME, timestamp_value TIMESTAMP, bool_value BOOLEAN)`)
	run(t, s, `INSERT INTO values_test VALUES ('not a date', 123, '2024-01-02 03:04:05.123', -1), (123, 'unchanged', 'not a timestamp', 2)`)
	result := run(t, s, `SELECT * FROM values_test`)
	want := [][]string{{"not a date", "123", "2024-01-02 03:04:05.123", "-1"}, {"123", "unchanged", "not a timestamp", "2"}}
	for row := range want {
		for column, value := range want[row] {
			if result.Rows[row][column] == nil || *result.Rows[row][column] != value {
				t.Fatalf("value [%d,%d] changed: %#v", row, column, result.Rows[row][column])
			}
		}
	}
	if result.Columns[0].DataType != "date" || result.Columns[3].DataType != "boolean" {
		t.Fatal("declared column types were lost")
	}
}

func TestSQLiteLimitsDrainWritesAndPersist(t *testing.T) {
	s, path := fixture(t)
	run(t, s, `CREATE TABLE items(id INTEGER)`)
	result, err := s.Query(context.Background(), `WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers WHERE n<1200) INSERT INTO items SELECT n FROM numbers RETURNING id`, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.RowCount != 100 || result.RowsAffected != "1200" {
		t.Fatalf("truncated write: %#v", result)
	}
	location := url.URL{Scheme: "file", Path: path}
	other, err := sql.Open("sqlite3", location.String())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var count int
	if err := other.QueryRow("SELECT count(*) FROM items").Scan(&count); err != nil || count != 1200 {
		t.Fatalf("write not committed: %d %v", count, err)
	}
	result = run(t, s, "SELECT printf('%.*c', 6000000, 'a')")
	if !result.Truncated || result.RowCount != 0 {
		t.Fatal("byte limit was not enforced")
	}
}

func TestSQLiteCancelAndReuse(t *testing.T) {
	s, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := s.Query(ctx, `WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers) SELECT n FROM numbers`, 100)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("did not cancel: %v", err)
	}
	if result := run(t, s, `SELECT 42`); *result.Rows[0][0] != "42" {
		t.Fatal("connection unusable after cancel")
	}
}

func TestSQLiteAttachedAndTempCatalog(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `ATTACH DATABASE ':memory:' AS "extra""db"`)
	run(t, s, `CREATE TABLE "extra""db".attached(id INTEGER)`)
	run(t, s, `CREATE TEMP TABLE temporary(value TEXT)`)
	tables, err := s.ListTables(context.Background())
	if err != nil || len(tables) != 2 || tables[0].Schema != `extra"db` || tables[1].Schema != "temp" {
		t.Fatalf("attached tables: %v %v", tables, err)
	}
	columns, err := s.ListColumns(context.Background())
	if err != nil || len(columns) != 2 {
		t.Fatalf("attached columns: %v %v", columns, err)
	}
	run(t, s, `DETACH DATABASE "extra""db"`)
	tables, err = s.ListTables(context.Background())
	if err != nil || len(tables) != 1 || tables[0].Schema != "temp" {
		t.Fatalf("detached tables: %v %v", tables, err)
	}
}

func TestSQLiteInvalidPathsDoNotCreateFiles(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.sqlite3")
	for _, path := range []string{"", ":memory:", "relative.db", root, missing, missing + "\x00"} {
		if _, _, err := NewProvider().Open(context.Background(), db.ConnectionConfig{Options: db.ConnectionOptions{FilePath: path}}); err == nil {
			t.Fatalf("accepted path: %q", path)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing path created a file")
	}
	invalid := filepath.Join(root, "not-a-db")
	if err := os.WriteFile(invalid, []byte("This is not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewProvider().Open(context.Background(), db.ConnectionConfig{Options: db.ConnectionOptions{FilePath: invalid}}); err == nil {
		t.Fatal("opened invalid database")
	}
}
