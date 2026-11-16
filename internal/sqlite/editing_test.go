package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"tusk/internal/db"
)

func text(value string) *string { return &value }

func columnNames(result db.QueryResult) []string {
	columns := make([]string, len(result.Columns))
	for index, column := range result.Columns {
		columns[index] = column.Name
	}
	return columns
}

func TestSQLiteSaveEditsAndDeletes(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE "odd""table" (id INTEGER PRIMARY KEY, note TEXT, payload BLOB, generated TEXT AS (note || '!'))`)
	run(t, s, `INSERT INTO "odd""table" (id, note, payload) VALUES (9223372036854775807, NULL, X'00ff'), (2, 'delete me', X'')`)
	info, err := s.TableEditInfo(context.Background(), "main", `odd"table`)
	if err != nil || info.Reason != "" || len(info.KeyColumns) != 1 || info.KeyColumns[0] != "id" || len(info.ReadOnlyColumns) != 1 || info.ReadOnlyColumns[0] != "generated" {
		t.Fatalf("metadata: %#v %v", info, err)
	}
	original := run(t, s, `SELECT * FROM "odd""table" ORDER BY id DESC`)
	count, err := s.SaveTableChanges(context.Background(), "main", `odd"table`, columnNames(original), []db.RowChange{
		{Original: original.Rows[0], Values: map[string]*string{"id": text("9223372036854775806"), "note": text(""), "payload": text("X'0102'")}},
		{Original: original.Rows[1], Delete: true},
	})
	if err != nil || count != 2 {
		t.Fatalf("save: %d %v", count, err)
	}
	result := run(t, s, `SELECT * FROM "odd""table"`)
	if result.RowCount != 1 || *result.Rows[0][0] != "9223372036854775806" || result.Rows[0][1] == nil || *result.Rows[0][1] != "" || *result.Rows[0][2] != "X'0102'" || *result.Rows[0][3] != "!" {
		t.Fatalf("saved row: %#v", result.Rows)
	}
	_, err = s.SaveTableChanges(context.Background(), "main", `odd"table`, columnNames(result), []db.RowChange{{Original: result.Rows[0], Values: map[string]*string{"note": nil, "payload": nil}}})
	if err != nil {
		t.Fatal(err)
	}
	result = run(t, s, `SELECT * FROM "odd""table"`)
	if result.Rows[0][1] != nil || result.Rows[0][2] != nil || result.Rows[0][3] != nil {
		t.Fatal("NULL edit was not preserved")
	}
}

func TestSQLiteSaveConflictRollsBackBatch(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT)`)
	run(t, s, `INSERT INTO items VALUES (1, 'first'), (2, 'second')`)
	original := run(t, s, `SELECT * FROM items ORDER BY id`)
	run(t, s, `UPDATE items SET note='changed elsewhere' WHERE id=2`)
	_, err := s.SaveTableChanges(context.Background(), "main", "items", columnNames(original), []db.RowChange{
		{Original: original.Rows[0], Values: map[string]*string{"note": text("must roll back")}},
		{Original: original.Rows[1], Delete: true},
	})
	if err == nil || !strings.Contains(err.Error(), "Nothing was saved") {
		t.Fatalf("accepted stale row: %v", err)
	}
	result := run(t, s, `SELECT * FROM items ORDER BY id`)
	if result.RowCount != 2 || *result.Rows[0][1] != "first" || *result.Rows[1][1] != "changed elsewhere" {
		t.Fatal("failed save partially committed")
	}
	_, err = s.SaveTableChanges(context.Background(), "main", "items", columnNames(result), []db.RowChange{
		{Original: result.Rows[0], Values: map[string]*string{"note": text("must also roll back")}},
		{Original: result.Rows[1], Values: map[string]*string{"id": text("1")}},
	})
	if err == nil {
		t.Fatal("accepted duplicate primary key")
	}
	result = run(t, s, `SELECT * FROM items ORDER BY id`)
	if *result.Rows[0][1] != "first" {
		t.Fatal("constraint failure partially committed")
	}
}

func TestSQLiteProjectedQueryCellChanges(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT, extra TEXT, generated TEXT AS (note || '!'))`)
	run(t, s, `INSERT INTO items(id,note,extra) VALUES (9223372036854775807,NULL,'keep'),(2,'second','other')`)
	query := func() db.QueryResult {
		return run(t, s, `SELECT note AS label,id AS row_key FROM items ORDER BY id DESC`)
	}
	original := query()
	if original.DeletionTarget == nil {
		t.Fatal("missing projected query target")
	}
	columns := original.DeletionTarget.Columns
	count, err := s.SaveTableChanges(context.Background(), "main", "items", columns, []db.RowChange{{Original: original.Rows[0], Values: map[string]*string{"id": text("9223372036854775806"), "note": text("")}}})
	if err != nil || count != 1 {
		t.Fatalf("projected update: %d %v", count, err)
	}
	fresh := run(t, s, `SELECT id,note,extra,generated FROM items ORDER BY id DESC`)
	if fresh.RowCount != 2 || *fresh.Rows[0][0] != "9223372036854775806" || fresh.Rows[0][1] == nil || *fresh.Rows[0][1] != "" || *fresh.Rows[0][2] != "keep" || *fresh.Rows[0][3] != "!" {
		t.Fatalf("projected update changed exact values: %#v", fresh.Rows)
	}
	original = query()
	run(t, s, `UPDATE items SET note='external' WHERE id=2`)
	_, err = s.SaveTableChanges(context.Background(), "main", "items", columns, []db.RowChange{
		{Original: original.Rows[0], Values: map[string]*string{"note": text("must roll back")}},
		{Original: original.Rows[1], Values: map[string]*string{"note": nil}},
	})
	if err == nil || !strings.Contains(err.Error(), "Nothing was saved") {
		t.Fatalf("accepted stale projected update: %v", err)
	}
	fresh = query()
	if *fresh.Rows[0][0] != "" || *fresh.Rows[1][0] != "external" {
		t.Fatal("projected update conflict did not roll back the batch")
	}
	generated := run(t, s, `SELECT generated,id FROM items ORDER BY id DESC`)
	if _, err := s.SaveTableChanges(context.Background(), "main", "items", generated.DeletionTarget.Columns, []db.RowChange{{Original: generated.Rows[0], Values: map[string]*string{"generated": text("bad")}}}); err == nil || !strings.Contains(err.Error(), "cannot be edited") {
		t.Fatalf("accepted projected generated column: %v", err)
	}
	if _, err := s.SaveTableChanges(context.Background(), "main", "items", []string{"note"}, []db.RowChange{{Original: []*string{text("")}, Values: map[string]*string{"note": text("bad")}}}); err == nil || !strings.Contains(err.Error(), "primary key") {
		t.Fatalf("accepted projected update without its key: %v", err)
	}
}

func TestSQLiteSaveRejectsUnsafeChanges(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT, generated TEXT AS (note || '!'))`)
	run(t, s, `INSERT INTO items(id, note) VALUES (1, 'original')`)
	run(t, s, `CREATE VIEW items_view AS SELECT * FROM items`)
	run(t, s, `CREATE TABLE no_key(note TEXT)`)
	for _, table := range []string{"items_view", "no_key", "missing", "sqlite_schema"} {
		info, err := s.TableEditInfo(context.Background(), "main", table)
		if err != nil || info.Reason == "" {
			t.Fatalf("unsafe metadata for %s: %#v %v", table, info, err)
		}
	}
	original := run(t, s, `SELECT * FROM items`)
	cases := []struct {
		name    string
		columns []string
		changes []db.RowChange
	}{
		{"generated", columnNames(original), []db.RowChange{{Original: original.Rows[0], Values: map[string]*string{"generated": text("bad")}}}},
		{"unknown column", columnNames(original), []db.RowChange{{Original: original.Rows[0], Values: map[string]*string{"unknown": text("bad")}}}},
		{"schema mismatch", []string{"note", "id", "generated"}, []db.RowChange{{Original: original.Rows[0], Delete: true}}},
		{"duplicate row", columnNames(original), []db.RowChange{{Original: original.Rows[0], Delete: true}, {Original: original.Rows[0], Delete: true}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.SaveTableChanges(context.Background(), "main", "items", test.columns, test.changes); err == nil {
				t.Fatal("accepted unsafe save")
			}
			result := run(t, s, `SELECT * FROM items`)
			if result.RowCount != 1 || *result.Rows[0][1] != "original" {
				t.Fatal("rejected save changed a row")
			}
		})
	}
	run(t, s, `CREATE TABLE nullable_key(id TEXT PRIMARY KEY, note TEXT)`)
	run(t, s, `INSERT INTO nullable_key VALUES(NULL, 'ambiguous'), (NULL, 'ambiguous')`)
	nullRows := run(t, s, `SELECT * FROM nullable_key`)
	if _, err := s.SaveTableChanges(context.Background(), "main", "nullable_key", columnNames(nullRows), []db.RowChange{{Original: nullRows.Rows[0], Delete: true}}); err == nil {
		t.Fatal("accepted a nullable primary key")
	}
}

func TestSQLiteMixedStoragePrimaryKeys(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE mixed(id PRIMARY KEY, note TEXT)`)
	run(t, s, `INSERT INTO mixed VALUES (1, 'integer'), ('1', 'text'), (X'00ff', 'blob'), (1.5, 'real')`)
	for _, note := range []string{"integer", "text", "blob", "real"} {
		original := run(t, s, "SELECT * FROM mixed WHERE note = '"+note+"'")
		_, err := s.SaveTableChanges(context.Background(), "main", "mixed", columnNames(original), []db.RowChange{{Original: original.Rows[0], Values: map[string]*string{"note": text(note + " edited")}}})
		if err != nil {
			t.Fatalf("%s key: %v", note, err)
		}
	}
	result := run(t, s, `SELECT typeof(id), note FROM mixed ORDER BY note`)
	for _, row := range result.Rows {
		if !strings.HasSuffix(*row[1], " edited") {
			t.Fatal("mixed primary key row was not edited")
		}
	}
	run(t, s, `UPDATE mixed SET note='same' WHERE id IS 1 OR id IS '1'`)
	original := run(t, s, `SELECT * FROM mixed WHERE note='same'`)
	if _, err := s.SaveTableChanges(context.Background(), "main", "mixed", columnNames(original), []db.RowChange{{Original: original.Rows[0], Delete: true}}); err == nil {
		t.Fatal("accepted indistinguishable keys")
	}
}

func TestSQLiteSaveCancellationRollsBackAndReusesConnection(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT)`)
	run(t, s, `INSERT INTO items VALUES (1, 'original')`)
	run(t, s, `CREATE TRIGGER slow_update BEFORE UPDATE ON items BEGIN SELECT (WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n); END`)
	original := run(t, s, `SELECT * FROM items`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := s.SaveTableChanges(ctx, "main", "items", columnNames(original), []db.RowChange{{Original: original.Rows[0], Values: map[string]*string{"note": text("must roll back")}}})
	if err == nil {
		t.Fatal("save was not canceled")
	}
	result := run(t, s, `SELECT * FROM items`)
	if result.RowCount != 1 || *result.Rows[0][1] != "original" {
		t.Fatal("canceled save committed")
	}
}
