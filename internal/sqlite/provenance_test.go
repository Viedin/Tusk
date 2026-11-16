package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"tusk/internal/db"
)

func TestSQLiteSelectDeletionTargets(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE "odd""table" (id INTEGER PRIMARY KEY, note TEXT, extra TEXT)`)
	run(t, s, `INSERT INTO "odd""table" VALUES (1, 'JOIN SELECT', 'x'), (2, 'second', 'y')`)
	for _, test := range []struct {
		query   string
		columns []string
	}{
		{`SELECT * FROM "odd""table"`, []string{"id", "note", "extra"}},
		{`SELECT t.* FROM main."odd""table" AS t WHERE note = 'JOIN SELECT' ORDER BY id LIMIT 1`, []string{"id", "note", "extra"}},
		{`SELECT note AS renamed, id AS "primary key" FROM "odd""table"`, []string{"note", "id"}},
		{`SELECT t.ID, t.Note FROM main."odd""table" t ORDER BY id DESC`, []string{"id", "note"}},
		{"/* SELECT FROM ignored */ SELECT id, note FROM \"odd\"\"table\" -- JOIN ignored\n WHERE note = 'JOIN SELECT'", []string{"id", "note"}},
	} {
		t.Run(test.query, func(t *testing.T) {
			result := run(t, s, test.query)
			if target := result.DeletionTarget; target == nil || target.Schema != "main" || target.Table != `odd"table` || !reflect.DeepEqual(target.Columns, test.columns) || result.RemovalUnavailableReason != "" {
				t.Fatalf("target: %#v, reason: %s", target, result.RemovalUnavailableReason)
			}
		})
	}

	run(t, s, `CREATE VIEW item_view AS SELECT * FROM "odd""table"`)
	run(t, s, `CREATE TABLE no_key(note TEXT)`)
	for _, query := range []string{
		`SELECT note FROM "odd""table"`,
		`SELECT * FROM item_view`,
		`SELECT * FROM no_key`,
		`SELECT a.id, a.note FROM "odd""table" a JOIN "odd""table" b ON a.id=b.id`,
		`SELECT id, upper(note) AS note FROM "odd""table"`,
		`SELECT id, 'note' AS note FROM "odd""table"`,
		`SELECT count(*) FROM "odd""table"`,
		`SELECT DISTINCT id, note FROM "odd""table"`,
		`SELECT id, note FROM "odd""table" UNION SELECT id, note FROM "odd""table"`,
		`SELECT id, note FROM "odd""table" WHERE id IN (SELECT id FROM "odd""table")`,
		`WITH source AS (SELECT * FROM "odd""table") SELECT * FROM source`,
		`SELECT id, id AS same_key FROM "odd""table"`,
		`UPDATE "odd""table" SET note=note WHERE id=1 RETURNING *`,
	} {
		t.Run(query, func(t *testing.T) {
			result := run(t, s, query)
			if result.DeletionTarget != nil || result.RemovalUnavailableReason == "" {
				t.Fatalf("unsafe target: %#v, reason: %q", result.DeletionTarget, result.RemovalUnavailableReason)
			}
		})
	}
}

func TestSQLitePartialDeletionSnapshots(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT, extra TEXT)`)
	run(t, s, `INSERT INTO items VALUES (1, 'first', 'x'), (2, 'second', 'y')`)
	original := run(t, s, `SELECT note AS renamed, id AS key_name FROM items ORDER BY id`)
	if original.DeletionTarget == nil {
		t.Fatal("missing deletion target")
	}
	columns := original.DeletionTarget.Columns
	run(t, s, `UPDATE items SET note='changed elsewhere' WHERE id=2`)
	_, err := s.SaveTableChanges(context.Background(), "main", "items", columns, []db.RowChange{{Original: original.Rows[0], Delete: true}, {Original: original.Rows[1], Delete: true}})
	if err == nil || !strings.Contains(err.Error(), "Nothing was saved") {
		t.Fatalf("accepted stale projected snapshot: %v", err)
	}
	if result := run(t, s, `SELECT * FROM items`); result.RowCount != 2 {
		t.Fatal("projected conflict did not roll back earlier delete")
	}
	count, err := s.SaveTableChanges(context.Background(), "main", "items", columns, []db.RowChange{{Original: original.Rows[0], Delete: true}})
	if err != nil || count != 1 {
		t.Fatalf("projected delete: %d %v", count, err)
	}
	remaining := run(t, s, `SELECT * FROM items`)
	if remaining.RowCount != 1 || *remaining.Rows[0][0] != "2" {
		t.Fatal("deleted the wrong projected row")
	}
	if _, err := s.SaveTableChanges(context.Background(), "main", "items", []string{"note"}, []db.RowChange{{Original: []*string{text("changed elsewhere")}, Delete: true}}); err == nil {
		t.Fatal("accepted deletion without its primary key")
	}
	if _, err := s.SaveTableChanges(context.Background(), "main", "items", []string{"id", "note"}, []db.RowChange{{Original: []*string{text("2"), text("second")}, Values: map[string]*string{"note": text("stale partial edit")}}}); err == nil || !strings.Contains(err.Error(), "Nothing was saved") {
		t.Fatalf("accepted a stale partial update snapshot: %v", err)
	}
}

func TestSQLiteDeletionTargetResolution(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT)`)
	run(t, s, `INSERT INTO items VALUES (1, 'main')`)
	run(t, s, `ATTACH DATABASE ':memory:' AS "other db"`)
	run(t, s, `CREATE TABLE "other db".items(id INTEGER PRIMARY KEY, note TEXT)`)
	run(t, s, `INSERT INTO "other db".items VALUES (2, 'attached')`)
	run(t, s, `CREATE TEMP TABLE items(id INTEGER PRIMARY KEY, note TEXT)`)
	run(t, s, `INSERT INTO temp.items VALUES (3, 'temporary')`)
	for _, test := range []struct {
		query, schema string
	}{
		{`SELECT * FROM ITEMS`, "temp"},
		{`SELECT * FROM MAIN.ITEMS`, "main"},
		{`SELECT * FROM "other db".items`, "other db"},
	} {
		result := run(t, s, test.query)
		if target := result.DeletionTarget; target == nil || target.Schema != test.schema || target.Table != "items" {
			t.Fatalf("%s resolved to %#v", test.query, target)
		}
	}
	run(t, s, `DROP TABLE temp.items`)
	result := run(t, s, `SELECT * FROM items`)
	if result.DeletionTarget == nil || result.DeletionTarget.Schema != "main" {
		t.Fatal("main table was not resolved after temp table was dropped")
	}
	run(t, s, `DROP TABLE main.items`)
	result = run(t, s, `SELECT * FROM items`)
	if result.DeletionTarget == nil || result.DeletionTarget.Schema != "other db" {
		t.Fatal("attached table was not resolved after main table was dropped")
	}
}

func TestSQLiteExpressionKeywordsCannotMasqueradeAsColumns(t *testing.T) {
	s, _ := fixture(t)
	for _, keyword := range []string{"DISTINCT", "ALL", "NOT", "CURRENT_TIMESTAMP", "CURRENT_DATE", "CURRENT_TIME", "NULL"} {
		run(t, s, `CREATE TABLE keyword_collision("`+keyword+`" TEXT PRIMARY KEY, id INTEGER)`)
		run(t, s, `INSERT INTO keyword_collision VALUES ('primary', 7)`)
		projection := keyword
		if keyword == "DISTINCT" || keyword == "ALL" || keyword == "NOT" {
			projection += " id"
		}
		result := run(t, s, "SELECT "+projection+" FROM keyword_collision")
		if result.DeletionTarget != nil {
			t.Fatalf("expression %s was mapped to a same-named column: %#v", projection, result.DeletionTarget)
		}
		run(t, s, `DROP TABLE keyword_collision`)
	}
	run(t, s, `CREATE TABLE items(id INTEGER PRIMARY KEY)`)
	run(t, s, `INSERT INTO items VALUES (1)`)
	for _, projection := range []string{"id ISNULL", "id NOTNULL"} {
		result := run(t, s, "SELECT "+projection+" FROM items")
		if result.DeletionTarget != nil {
			t.Fatalf("postfix expression %s was mapped to id: %#v", projection, result.DeletionTarget)
		}
	}
	run(t, s, `CREATE TABLE other(id INTEGER PRIMARY KEY)`)
	run(t, s, `INSERT INTO other VALUES (2)`)
	result := run(t, s, `SELECT id FROM items /* /* */ UNION SELECT id FROM other -- */`)
	if result.DeletionTarget != nil {
		t.Fatal("a nested-looking comment hid a UNION from provenance validation")
	}
}

func TestSQLiteDeletionUsesASCIICaseFolding(t *testing.T) {
	s, _ := fixture(t)
	run(t, s, `CREATE TABLE unicode_columns(k INTEGER PRIMARY KEY, "K" INTEGER)`)
	run(t, s, `INSERT INTO unicode_columns VALUES (1, 7)`)
	result := run(t, s, `SELECT "K" FROM unicode_columns`)
	if result.DeletionTarget != nil {
		t.Fatal("a Unicode-equivalent name was mistaken for the primary key")
	}
	result = run(t, s, `SELECT K FROM unicode_columns`)
	if result.DeletionTarget == nil || !reflect.DeepEqual(result.DeletionTarget.Columns, []string{"k"}) {
		t.Fatal("ASCII case-insensitive column lookup did not preserve the actual name")
	}
	run(t, s, `CREATE TABLE k(id INTEGER PRIMARY KEY)`)
	run(t, s, `CREATE TABLE "K"(id INTEGER PRIMARY KEY)`)
	result = run(t, s, `SELECT * FROM "K"`)
	if result.DeletionTarget == nil || result.DeletionTarget.Table != "K" {
		t.Fatal("Unicode-distinct source tables were conflated")
	}
}
