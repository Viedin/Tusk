package db

import "testing"

func TestParseSelectSource(t *testing.T) {
	for _, statement := range []string{
		`SELECT id, note AS label FROM main.items WHERE note='JOIN other UNION SELECT' ORDER BY id LIMIT 20`,
		`-- SELECT unrelated` + "\n" + `SELECT i.note label, i.id FROM "odd schema"."odd""table" AS i WHERE id IN (1,2)`,
		`SELECT t.* FROM items t /* JOIN fake */ WHERE note=$tag$SELECT FROM fake$tag$;`,
		`SELECT "DISTINCT", "ISNULL" FROM items`,
		`SELECT [id], ` + "`note`" + ` FROM [items]`,
	} {
		if _, ok := ParseSelectSource(statement); !ok {
			t.Errorf("rejected safe source: %s", statement)
		}
	}
	for _, statement := range []string{
		`SELECT DISTINCT id FROM items`, `SELECT ALL id FROM items`, `SELECT NOT id FROM items`,
		`SELECT CURRENT_TIMESTAMP FROM items`, `SELECT NULL FROM items`, `SELECT TRUE FROM items`,
		`SELECT id ISNULL FROM items`, `SELECT id NOTNULL FROM items`,
		`SELECT id, FROM items`, `SELECT id,,note FROM items`, `SELECT id AS FROM items`,
		`SELECT id || note FROM items`, `SELECT count(*) FROM items`, `SELECT (id) FROM items`,
		`SELECT i.id FROM items i JOIN other o ON i.id=o.id`,
		`SELECT id FROM items,other`, `SELECT id FROM items UNION SELECT id FROM other`,
		`WITH x AS (SELECT * FROM items) SELECT * FROM x`,
		`SELECT id FROM items WHERE EXISTS (SELECT 1 FROM other)`,
		`SELECT id FROM items GROUP BY id`, `SELECT id INTO backup FROM items`,
		`SELECT id FROM items /* /* */ UNION SELECT id FROM other -- */`,
		`SELECT id FROM items WHERE note='back\slash'`,
		`SELECT id FROM items; DELETE FROM items`, `SELECT id FROM items /* unclosed`,
	} {
		if _, ok := ParseSelectSource(statement); ok {
			t.Errorf("accepted ambiguous source: %s", statement)
		}
	}
	source, ok := ParseSelectSource(`SELECT t.note AS label,t.id AS row_key FROM "odd"."items" t`)
	if !ok || source.Schema.Text != "odd" || !source.Schema.Quoted || source.Table.Text != "items" || source.Alias.Text != "t" || len(source.Projections) != 2 || source.Projections[0].Name.Text != "note" || source.Projections[1].Name.Text != "id" {
		t.Fatalf("lost underlying names: %#v", source)
	}
}
