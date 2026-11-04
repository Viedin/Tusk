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

func TestPostgreSQLCatalogAndSorting(t *testing.T) {
	dsn := os.Getenv("TUSK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TUSK_TEST_DATABASE_URL to run catalog and sorting integration checks")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("cannot connect to integration database")
	}
	defer admin.Close(ctx)
	suffix := time.Now().UnixNano()
	schema := fmt.Sprintf("Tusk Catalog %d", suffix)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	table := `Order "Rows"`
	qualified := pgx.Identifier{schema, table}.Sanitize()
	sortColumn := `Case "Sort"`
	quotedSort := pgx.Identifier{sortColumn}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
	if _, err := admin.Exec(ctx, "CREATE TABLE "+qualified+` (id integer, "BigInt" bigint, `+quotedSort+` numeric, dropped text, note text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "ALTER TABLE "+qualified+" DROP COLUMN dropped"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "INSERT INTO "+qualified+` SELECT i, 9223372036854775807::bigint - i,
		12345678901234567890.00000000000000000000::numeric + i * 0.00000000000000000001::numeric,
		'row ' || i FROM generate_series(250, 1, -1) i`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "INSERT INTO "+qualified+" VALUES (251, NULL, NULL, ''), (252, NULL, NULL, NULL)"); err != nil {
		t.Fatal(err)
	}
	view := `Small "View"`
	viewQualified := pgx.Identifier{schema, view}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE VIEW "+viewQualified+" AS SELECT * FROM "+qualified+" WHERE id <= 2 OR id >= 251"); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	defer s.Shutdown()
	if _, err := s.Connect(dsn); err != nil {
		t.Fatal(err)
	}

	t.Run("sort entire table before limit with exact precision", func(t *testing.T) {
		for _, descending := range []bool{false, true} {
			result, err := s.BrowseTableSorted(schema, table, sortColumn, descending)
			if err != nil {
				t.Fatal(err)
			}
			if result.RowCount != 100 || len(result.Rows) != 100 {
				t.Fatalf("expected 100 sorted rows, got %d", result.RowCount)
			}
			first, last, precise := "1", "100", "12345678901234567890.00000000000000000001"
			if descending {
				first, last, precise = "250", "151", "12345678901234567890.00000000000000000250"
			}
			if result.Rows[0][0] == nil || *result.Rows[0][0] != first || *result.Rows[99][0] != last || *result.Rows[0][2] != precise {
				t.Fatalf("sort returned wrong range or rounded numeric values (descending=%v)", descending)
			}
			result, err = s.BrowseTableSorted(schema, table, "BigInt", descending)
			if err != nil {
				t.Fatal(err)
			}
			first, precise = "250", "9223372036854775557"
			if descending {
				first, precise = "1", "9223372036854775806"
			}
			if *result.Rows[0][0] != first || *result.Rows[0][1] != precise {
				t.Fatalf("bigint sort changed precision (descending=%v)", descending)
			}
		}
	})

	t.Run("NULLs stay last in both sort directions", func(t *testing.T) {
		for _, descending := range []bool{false, true} {
			result, err := s.BrowseTableSorted(schema, view, sortColumn, descending)
			if err != nil || result.RowCount != 4 {
				t.Fatalf("small view query failed: %v", err)
			}
			if result.Rows[0][2] == nil || result.Rows[1][2] == nil || result.Rows[2][2] != nil || result.Rows[3][2] != nil {
				t.Fatalf("NULLs were not last (descending=%v)", descending)
			}
		}
	})

	t.Run("sort column cannot inject SQL", func(t *testing.T) {
		attack := sortColumn + `"; DROP TABLE ` + qualified + "; --"
		if _, err := s.BrowseTableSorted(schema, table, attack, false); err == nil || !strings.Contains(err.Error(), "42703") {
			t.Fatalf("malicious sort did not remain a single quoted identifier: %v", err)
		}
		if result, err := s.BrowseTable(schema, table); err != nil || result.RowCount != 100 {
			t.Fatalf("table changed after invalid sort: %v", err)
		}
	})

	t.Run("catalog preserves names types and physical column order", func(t *testing.T) {
		columns, err := s.ListColumns()
		if err != nil {
			t.Fatal(err)
		}
		var names, types []string
		viewFound := false
		for _, column := range columns {
			if column.Schema == schema && column.Table == table {
				names = append(names, column.Name)
				types = append(types, column.DataType)
			}
			viewFound = viewFound || (column.Schema == schema && column.Table == view && column.Name == sortColumn)
			if strings.HasPrefix(column.Schema, "pg_") || column.Schema == "information_schema" {
				t.Fatal("system columns leaked into autocomplete catalog")
			}
		}
		if strings.Join(names, "|") != "id|BigInt|"+sortColumn+"|note" || strings.Join(types, "|") != "integer|bigint|numeric|text" || !viewFound {
			t.Fatalf("catalog names, types, dropped-column filtering, or view metadata incorrect: %v %v", names, types)
		}
	})

	t.Run("catalog hides inaccessible relations and columns", func(t *testing.T) {
		var canCreateRole bool
		if err := admin.QueryRow(ctx, "SELECT rolsuper OR rolcreaterole FROM pg_roles WHERE rolname = current_user").Scan(&canCreateRole); err != nil {
			t.Fatal(err)
		}
		if !canCreateRole {
			t.Skip("restricted catalog checks require CREATEROLE on the integration account")
		}
		role := pgx.Identifier{fmt.Sprintf("tusk_catalog_role_%d", suffix)}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN"); err != nil {
			t.Fatal(err)
		}
		defer func() {
			admin.Exec(ctx, "DROP OWNED BY "+role)
			admin.Exec(ctx, "DROP ROLE "+role)
		}()
		privateSchema := schema + " Private"
		privateQualified := pgx.Identifier{privateSchema}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+privateQualified); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(ctx, "DROP SCHEMA "+privateQualified+" CASCADE")
		hidden := pgx.Identifier{schema, "Hidden"}.Sanitize()
		partial := pgx.Identifier{schema, "Partial"}.Sanitize()
		private := pgx.Identifier{privateSchema, "PrivateTable"}.Sanitize()
		for _, statement := range []string{
			"CREATE TABLE " + hidden + " (secret text)",
			"CREATE TABLE " + partial + " (visible text, secret text)",
			"CREATE TABLE " + private + " (secret text)",
			"GRANT USAGE ON SCHEMA " + quotedSchema + " TO " + role,
			"GRANT SELECT ON " + qualified + " TO " + role,
			"GRANT SELECT (visible) ON " + partial + " TO " + role,
			"GRANT SELECT ON " + private + " TO " + role,
		} {
			if _, err := admin.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.RunQuery("SET ROLE " + role); err != nil {
			t.Fatal(err)
		}
		defer s.RunQuery("RESET ROLE")
		columns, err := s.ListColumns()
		if err != nil {
			t.Fatal(err)
		}
		visibleCount := 0
		for _, column := range columns {
			if column.Schema == privateSchema || (column.Schema == schema && column.Table != table) {
				t.Fatalf("inaccessible catalog entry exposed: %s.%s.%s", column.Schema, column.Table, column.Name)
			}
			if column.Schema == schema && column.Table == table {
				visibleCount++
			}
		}
		if visibleCount != 4 {
			t.Fatalf("accessible columns missing: got %d, want 4", visibleCount)
		}
	})
}

func TestSortedTableValidation(t *testing.T) {
	for _, fields := range [][3]string{{"", "table", "column"}, {"schema", "", "column"}, {"schema", "table", ""}, {"schema\x00", "table", "column"}, {"schema", "table\x00", "column"}, {"schema", "table", "column\x00"}} {
		if _, err := NewService().BrowseTableSorted(fields[0], fields[1], fields[2], false); err == nil || !strings.Contains(err.Error(), "valid schema, table, and sort column") {
			t.Fatalf("invalid identifier accepted: %v", err)
		}
	}
}
