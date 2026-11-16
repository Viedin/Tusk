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

func TestPostgreSQLTableChanges(t *testing.T) {
	dsn := os.Getenv("TUSK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TUSK_TEST_DATABASE_URL to run PostgreSQL table-edit checks")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("cannot connect to integration database")
	}
	defer admin.Close(ctx)
	schema := fmt.Sprintf("tusk edit %d", time.Now().UnixNano())
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
	exec("CREATE TABLE " + qualified + ` (id bigint PRIMARY KEY, note text, nullable text, padded char(4), payload json, generated text GENERATED ALWAYS AS (note || '!') STORED, identity_value bigint GENERATED ALWAYS AS IDENTITY)`)
	exec("INSERT INTO " + qualified + ` (id,note,nullable,padded,payload) VALUES (9223372036854775807, '', NULL, 'x', '{"ok":true}'), (2,'second','nullable','y','{}')`)
	s := NewService()
	defer s.Shutdown()
	if _, err := s.Connect(dsn); err != nil {
		t.Fatal(err)
	}
	info, err := s.GetTableEditInfo(schema, table)
	if err != nil || info.Reason != "" || !slices.Equal(info.KeyColumns, []string{"id"}) || !slices.Equal(info.ReadOnlyColumns, []string{"generated", "identity_value"}) {
		t.Fatalf("metadata: %#v, %v", info, err)
	}
	load := func() (db.QueryResult, []string) {
		t.Helper()
		result, err := s.BrowseTableSorted(schema, table, "id", true)
		if err != nil {
			t.Fatal(err)
		}
		columns := []string{}
		for _, column := range result.Columns {
			columns = append(columns, column.Name)
		}
		return result, columns
	}
	text := func(value string) *string { return &value }
	result, columns := load()
	if _, err := s.SaveTableChanges(schema, table, columns, []db.RowChange{{Original: result.Rows[0], Values: map[string]*string{"id": text("9223372036854775806"), "note": text("Robert'); DROP TABLE nowhere; --"), "nullable": text("")}}}); err != nil {
		t.Fatal(err)
	}
	result, columns = load()
	if *result.Rows[0][0] != "9223372036854775806" || *result.Rows[0][1] != "Robert'); DROP TABLE nowhere; --" || result.Rows[0][2] == nil || *result.Rows[0][2] != "" || *result.Rows[0][3] != "x   " {
		t.Fatalf("update changed exact values: %#v", result.Rows[0])
	}
	t.Run("stale second row rolls back first edit", func(t *testing.T) {
		exec("UPDATE " + qualified + " SET note='external' WHERE id=2")
		_, err := s.SaveTableChanges(schema, table, columns, []db.RowChange{
			{Original: result.Rows[0], Values: map[string]*string{"note": text("must roll back")}},
			{Original: result.Rows[1], Delete: true},
		})
		if err == nil || !strings.Contains(err.Error(), "changed or was removed") {
			t.Fatalf("accepted stale snapshot: %v", err)
		}
		fresh, _ := load()
		if *fresh.Rows[0][1] != *result.Rows[0][1] || len(fresh.Rows) != 2 {
			t.Fatal("failed batch was partially committed")
		}
	})
	result, columns = load()
	for _, name := range []string{"generated", "identity_value"} {
		if _, err := s.SaveTableChanges(schema, table, columns, []db.RowChange{{Original: result.Rows[0], Values: map[string]*string{name: text("123")}}}); err == nil {
			t.Fatalf("accepted generated column %s", name)
		}
	}
	if _, err := s.SaveTableChanges(schema, table, columns, []db.RowChange{{Original: result.Rows[0], Delete: true}, {Original: result.Rows[0], Delete: true}}); err == nil {
		t.Fatal("accepted duplicate row changes")
	}
	if _, err := s.SaveTableChanges(schema, table, []string{"wrong"}, []db.RowChange{{Original: []*string{text("1")}, Delete: true}}); err == nil {
		t.Fatal("accepted wrong table columns")
	}
	if _, err := s.RunQuery("BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTableChanges(schema, table, columns, []db.RowChange{{Original: result.Rows[0], Delete: true}}); err == nil {
		t.Fatal("accepted saving inside SQL editor transaction")
	}
	if _, err := s.RunQuery("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	t.Run("cancellation rolls back the whole batch", func(t *testing.T) {
		function := pgx.Identifier{schema, "delay edit"}.Sanitize()
		exec("CREATE FUNCTION " + function + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.note='pause' THEN PERFORM pg_sleep(10); END IF; RETURN NEW; END $$`)
		exec("CREATE TRIGGER delay_edit BEFORE UPDATE ON " + qualified + " FOR EACH ROW EXECUTE FUNCTION " + function + "()")
		defer exec("DROP TRIGGER delay_edit ON " + qualified)
		pid := backendPID(t, s)
		done := make(chan error, 1)
		go func() {
			_, err := s.SaveTableChanges(schema, table, columns, []db.RowChange{
				{Original: result.Rows[0], Values: map[string]*string{"nullable": text("must roll back")}},
				{Original: result.Rows[1], Values: map[string]*string{"note": text("pause")}},
			})
			done <- err
		}()
		deadline := time.Now().Add(3 * time.Second)
		waiting := false
		for time.Now().Before(deadline) {
			if err := admin.QueryRow(ctx, "SELECT COALESCE(wait_event='PgSleep', false) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := s.CancelQuery(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "canceled") {
				t.Fatalf("save was not canceled: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("save did not finish after cancellation")
		}
		if !waiting {
			t.Fatal("save never entered the delay trigger")
		}
		fresh, _ := load()
		if *fresh.Rows[0][2] != *result.Rows[0][2] || *fresh.Rows[1][1] != *result.Rows[1][1] {
			t.Fatal("canceled save was partially committed")
		}
	})
	if count, err := s.SaveTableChanges(schema, table, columns, []db.RowChange{{Original: result.Rows[0], Values: map[string]*string{"nullable": nil}}, {Original: result.Rows[1], Delete: true}}); err != nil || count != 2 {
		t.Fatalf("save and remove: %d %v", count, err)
	}
	result, _ = load()
	if len(result.Rows) != 1 || result.Rows[0][2] != nil {
		t.Fatal("delete or SQL NULL update failed")
	}
	exec("CREATE TABLE " + pgx.Identifier{schema, "no key"}.Sanitize() + " (note text)")
	info, err = s.GetTableEditInfo(schema, "no key")
	if err != nil || !strings.Contains(info.Reason, "primary key") {
		t.Fatalf("unkeyed table enabled: %#v %v", info, err)
	}
	exec("CREATE VIEW " + pgx.Identifier{schema, "view"}.Sanitize() + " AS SELECT * FROM " + qualified)
	info, err = s.GetTableEditInfo(schema, "view")
	if err != nil || !strings.Contains(info.Reason, "Views") {
		t.Fatalf("view enabled: %#v %v", info, err)
	}
	t.Run("projected query cell changes", func(t *testing.T) {
		projected := pgx.Identifier{schema, "query cells"}.Sanitize()
		exec("CREATE TABLE " + projected + " (id bigint PRIMARY KEY, note text, extra text, generated text GENERATED ALWAYS AS (note || '!') STORED)")
		exec("INSERT INTO " + projected + " (id,note,extra) VALUES (9223372036854775807,NULL,'keep'),(2,'second','other')")
		query := func() db.QueryResult {
			t.Helper()
			result, err := s.RunQuery("SELECT note AS label,id AS row_key FROM " + projected + " ORDER BY id DESC")
			if err != nil || result.DeletionTarget == nil {
				t.Fatalf("query target: %#v %v", result.DeletionTarget, err)
			}
			return result
		}
		result := query()
		if count, err := s.SaveTableChanges(schema, "query cells", result.DeletionTarget.Columns, []db.RowChange{{Original: result.Rows[0], Values: map[string]*string{"id": text("9223372036854775806"), "note": text("")}}}); err != nil || count != 1 {
			t.Fatalf("projected update: %d %v", count, err)
		}
		fresh, err := s.RunQuery("SELECT id,note,extra,generated FROM " + projected + " ORDER BY id DESC")
		if err != nil || fresh.RowCount != 2 || *fresh.Rows[0][0] != "9223372036854775806" || fresh.Rows[0][1] == nil || *fresh.Rows[0][1] != "" || *fresh.Rows[0][2] != "keep" || *fresh.Rows[0][3] != "!" {
			t.Fatalf("projected update changed exact values: %#v %v", fresh.Rows, err)
		}
		result = query()
		exec("UPDATE " + projected + " SET note='external' WHERE id=2")
		if _, err := s.SaveTableChanges(schema, "query cells", result.DeletionTarget.Columns, []db.RowChange{
			{Original: result.Rows[0], Values: map[string]*string{"note": text("must roll back")}},
			{Original: result.Rows[1], Values: map[string]*string{"note": nil}},
		}); err == nil || !strings.Contains(err.Error(), "changed or was removed") {
			t.Fatalf("accepted stale projected update: %v", err)
		}
		fresh = query()
		if *fresh.Rows[0][0] != "" || *fresh.Rows[1][0] != "external" {
			t.Fatal("projected update conflict did not roll back the batch")
		}
		generated, err := s.RunQuery("SELECT generated,id FROM " + projected + " ORDER BY id DESC")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveTableChanges(schema, "query cells", generated.DeletionTarget.Columns, []db.RowChange{{Original: generated.Rows[0], Values: map[string]*string{"generated": text("bad")}}}); err == nil || !strings.Contains(err.Error(), "generated") {
			t.Fatalf("accepted projected generated column: %v", err)
		}
		if _, err := s.SaveTableChanges(schema, "query cells", []string{"note"}, []db.RowChange{{Original: []*string{text("")}, Values: map[string]*string{"note": text("bad")}}}); err == nil || !strings.Contains(err.Error(), "primary-key") {
			t.Fatalf("accepted projected update without its key: %v", err)
		}
	})
}
