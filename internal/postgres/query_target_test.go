package postgres

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"tusk/internal/db"
)

func TestPostgreSQLQueryDeletionTargets(t *testing.T) {
	dsn := os.Getenv("TUSK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TUSK_TEST_DATABASE_URL to run PostgreSQL SELECT deletion checks")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("cannot connect to integration database")
	}
	defer admin.Close(ctx)
	schema := fmt.Sprintf("tusk query edit %d", time.Now().UnixNano())
	table := `odd "table`
	qualified := pgx.Identifier{schema, table}.Sanitize()
	exec := func(statement string) {
		t.Helper()
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + pgx.Identifier{schema}.Sanitize())
	defer admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	exec("CREATE TABLE " + qualified + " (id bigint PRIMARY KEY,name text,nullable text)")
	exec("INSERT INTO " + qualified + " VALUES (1,'first',NULL),(2,'second',''),(9223372036854775807,NULL,NULL)")
	view := pgx.Identifier{schema, "view"}.Sanitize()
	noKey := pgx.Identifier{schema, "no key"}.Sanitize()
	exec("CREATE VIEW " + view + " AS SELECT * FROM " + qualified)
	exec("CREATE TABLE " + noKey + " (id bigint,name text)")
	s := NewService()
	defer s.Shutdown()
	if _, err := s.Connect(dsn); err != nil {
		t.Fatal(err)
	}
	query := func(statement string) db.QueryResult {
		t.Helper()
		result, err := s.RunQuery(statement)
		if err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
		return result
	}
	for name, statement := range map[string]string{
		"all columns":      "SELECT * FROM " + qualified + " ORDER BY id",
		"partial aliases":  "SELECT name AS label,id AS row_key FROM " + qualified + " ORDER BY id",
		"qualified":        "SELECT t.id,t.name FROM " + qualified + " AS t WHERE id<10 LIMIT 10",
		"qualified star":   "SELECT t.* FROM " + qualified + " t",
		"literal keywords": "SELECT id,name FROM " + qualified + " WHERE name='JOIN other UNION SELECT' /* FROM fake */",
	} {
		t.Run(name, func(t *testing.T) {
			result := query(statement)
			if result.DeletionTarget == nil || result.RemovalUnavailableReason != "" || result.DeletionTarget.Schema != schema || result.DeletionTarget.Table != table {
				t.Fatalf("safe SELECT unavailable: %#v %s", result.DeletionTarget, result.RemovalUnavailableReason)
			}
		})
	}
	for name, statement := range map[string]string{
		"missing key":           "SELECT name FROM " + qualified,
		"computed":              "SELECT id,upper(name) FROM " + qualified,
		"constant":              "SELECT id,'value' FROM " + qualified,
		"duplicate":             "SELECT id,id FROM " + qualified,
		"view":                  "SELECT * FROM " + view,
		"no primary key":        "SELECT * FROM " + noKey,
		"join only one origin":  "SELECT a.id,a.name FROM " + qualified + " a JOIN " + qualified + " b ON a.id=b.id",
		"join multiple origins": "SELECT a.id,b.name FROM " + qualified + " a JOIN " + qualified + " b ON a.id=b.id",
		"aggregate":             "SELECT min(id),count(*) FROM " + qualified,
		"group":                 "SELECT id FROM " + qualified + " GROUP BY id",
		"distinct":              "SELECT DISTINCT id FROM " + qualified,
		"cte":                   "WITH x AS (SELECT * FROM " + qualified + ") SELECT * FROM x",
		"union":                 "SELECT id FROM " + qualified + " UNION SELECT id FROM " + qualified,
		"write returning":       "INSERT INTO " + qualified + " VALUES (3,'third',NULL) RETURNING *",
	} {
		t.Run(name, func(t *testing.T) {
			result := query(statement)
			if result.DeletionTarget != nil || result.RemovalUnavailableReason == "" {
				t.Fatalf("ambiguous result removable: %#v", result.DeletionTarget)
			}
		})
	}
	result := query("SELECT name AS label,id AS row_key FROM " + qualified + " WHERE id IN (1,2) ORDER BY id")
	if !slices.Equal(result.DeletionTarget.Columns, []string{"name", "id"}) {
		t.Fatalf("aliases changed original columns: %#v", result.DeletionTarget)
	}
	exec("UPDATE " + qualified + " SET name='external' WHERE id=2")
	changes := []db.RowChange{{Original: result.Rows[0], Delete: true}, {Original: result.Rows[1], Delete: true}}
	if _, err := s.SaveTableChanges(schema, table, result.DeletionTarget.Columns, changes); err == nil || !strings.Contains(err.Error(), "changed or was removed") {
		t.Fatalf("stale partial snapshot accepted: %v", err)
	}
	if fresh := query("SELECT id FROM " + qualified + " WHERE id=1"); fresh.RowCount != 1 {
		t.Fatal("stale batch partially deleted rows")
	}
	value := "changed"
	if _, err := s.SaveTableChanges(schema, table, result.DeletionTarget.Columns, []db.RowChange{{Original: result.Rows[1], Values: map[string]*string{"name": &value}}}); err == nil || !strings.Contains(err.Error(), "changed or was removed") {
		t.Fatalf("stale partial update accepted: %v", err)
	}
	if _, err := s.SaveTableChanges(schema, table, []string{"name"}, []db.RowChange{{Original: []*string{&value}, Delete: true}}); err == nil {
		t.Fatal("deletion accepted without key")
	}
	big := query("SELECT name AS label,id AS row_key FROM " + qualified + " WHERE id=9223372036854775807")
	if count, err := s.SaveTableChanges(schema, table, big.DeletionTarget.Columns, []db.RowChange{{Original: big.Rows[0], Delete: true}}); err != nil || count != 1 {
		t.Fatalf("bigint NULL partial delete: %d %v", count, err)
	}
	if count, err := s.SaveTableChanges(schema, table, result.DeletionTarget.Columns, []db.RowChange{{Original: result.Rows[0], Delete: true}}); err != nil || count != 1 {
		t.Fatalf("reordered partial delete: %d %v", count, err)
	}
}
