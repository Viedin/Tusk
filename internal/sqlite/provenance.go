package sqlite

import (
	"context"
	"database/sql/driver"
	"strings"

	"tusk/internal/db"
)

// SQLite does not expose column-origin metadata through the driver, so only direct single-table SELECTs qualify.
func (s *Store) deletionTarget(ctx context.Context, statement string, result *db.QueryResult) {
	if len(result.Columns) == 0 {
		return
	}
	result.RemovalUnavailableReason = "Editing or removing rows requires a SELECT of one table's columns, including every primary key column."
	source, ok := db.ParseSelectSource(statement)
	if !ok || result.CommandTag != "SELECT" {
		return
	}
	schema, table, ok := s.resolveSelectTable(ctx, source)
	if !ok {
		return
	}
	columns, info, err := s.editColumns(ctx, schema, table)
	if err != nil {
		return
	}
	if info.Reason != "" {
		result.RemovalUnavailableReason = info.Reason
		return
	}
	resolved := []string{}
	seen := map[string]bool{}
	for _, projection := range source.Projections {
		if projection.Qualifier.Text != "" {
			qualifier := source.Table.Text
			if source.Alias.Text != "" {
				qualifier = source.Alias.Text
			}
			if sqliteName(projection.Qualifier.Text) != sqliteName(qualifier) {
				return
			}
		}
		found := false
		for _, column := range columns {
			if !projection.Star && sqliteName(projection.Name.Text) != sqliteName(column.name) {
				continue
			}
			if seen[column.name] {
				return
			}
			resolved = append(resolved, column.name)
			seen[column.name] = true
			found = true
		}
		if !found {
			return
		}
	}
	if len(resolved) != len(result.Columns) {
		return
	}
	for _, key := range info.KeyColumns {
		if !seen[key] {
			result.RemovalUnavailableReason = "Include every primary key column in the SELECT before editing or removing rows."
			return
		}
	}
	result.DeletionTarget = &db.RowTarget{Schema: schema, Table: table, Columns: resolved}
	result.RemovalUnavailableReason = ""
}

func sqliteName(name string) string {
	return strings.Map(func(character rune) rune {
		if character >= 'A' && character <= 'Z' {
			return character + ('a' - 'A')
		}
		return character
	}, name)
}

func (s *Store) resolveSelectTable(ctx context.Context, source db.SelectSource) (string, string, bool) {
	// SQLite resolves an unqualified name through temp, main, then attached databases in attachment order.
	priority := map[string]int{"temp": -2, "main": -1}
	if err := s.each(ctx, "PRAGMA database_list", nil, func(values []driver.Value) error {
		name := values[1].(string)
		if name != "temp" && name != "main" {
			priority[name] = int(values[0].(int64))
		}
		return nil
	}); err != nil {
		return "", "", false
	}
	var schema, table string
	best := int(^uint(0) >> 1)
	err := s.each(ctx, "SELECT schema, name FROM pragma_table_list WHERE name = ? COLLATE NOCASE", []driver.NamedValue{{Ordinal: 1, Value: source.Table.Text}}, func(values []driver.Value) error {
		candidate := values[0].(string)
		if source.Schema.Text != "" && sqliteName(candidate) != sqliteName(source.Schema.Text) {
			return nil
		}
		if rank, exists := priority[candidate]; exists && rank < best {
			schema, table, best = candidate, values[1].(string), rank
		}
		return nil
	})
	return schema, table, err == nil && table != ""
}
