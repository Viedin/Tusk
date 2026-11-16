package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"tusk/internal/db"
)

func postgresName(name db.SelectName) string {
	if name.Quoted {
		return name.Text
	}
	return strings.ToLower(name.Text)
}

func (s *session) identifyDeletionTarget(ctx context.Context, statement string, fields []pgconn.FieldDescription, result *db.QueryResult) {
	result.RemovalUnavailableReason = "Use a SELECT of direct columns from one table, including every primary-key column, to edit or remove result rows"
	if !strings.HasPrefix(result.CommandTag, "SELECT") {
		result.RemovalUnavailableReason = "Only SELECT result rows can be edited or removed. Open a table in the sidebar to edit rows"
		return
	}
	source, ok := db.ParseSelectSource(statement)
	if !ok || len(fields) == 0 || fields[0].TableOID == 0 {
		return
	}
	oid := fields[0].TableOID
	for _, field := range fields {
		if field.TableOID != oid || field.TableAttributeNumber == 0 {
			return
		}
	}
	rows, err := s.conn.Query(ctx, `
		SELECT n.nspname,c.relname,c.relkind::text,a.attnum,a.attname,
		       EXISTS (SELECT 1 FROM pg_catalog.pg_index i WHERE i.indrelid=c.oid AND i.indisprimary AND a.attnum=ANY(i.indkey))
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid
		WHERE c.oid=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		result.RemovalUnavailableReason = "Could not identify the result table. Open the table browser to edit or remove rows"
		return
	}
	defer rows.Close()
	var schema, table, kind string
	names := []string{}
	byAttribute := map[uint16]string{}
	keys := []string{}
	for rows.Next() {
		var number uint16
		var name string
		var key bool
		if err := rows.Scan(&schema, &table, &kind, &number, &name, &key); err != nil {
			return
		}
		names = append(names, name)
		byAttribute[number] = name
		if key {
			keys = append(keys, name)
		}
	}
	if rows.Err() != nil || len(names) == 0 {
		return
	}
	if kind != "r" && kind != "p" {
		result.RemovalUnavailableReason = "Views and foreign tables are read-only. Open their underlying table to edit or remove rows"
		return
	}
	if postgresName(source.Table) != table || source.Schema.Text != "" && postgresName(source.Schema) != schema {
		return
	}
	if len(keys) == 0 {
		result.RemovalUnavailableReason = "This table needs a primary key before result rows can be edited or removed"
		return
	}
	expected := []string{}
	qualifier := source.Table
	if source.Alias.Text != "" {
		qualifier = source.Alias
	}
	for _, projection := range source.Projections {
		if projection.Qualifier.Text != "" && postgresName(projection.Qualifier) != postgresName(qualifier) {
			return
		}
		if projection.Star {
			expected = append(expected, names...)
		} else {
			expected = append(expected, postgresName(projection.Name))
		}
	}
	if len(expected) != len(fields) {
		return
	}
	target := &db.RowTarget{Schema: schema, Table: table, Columns: make([]string, len(fields))}
	seen := map[string]bool{}
	for i, field := range fields {
		name := byAttribute[field.TableAttributeNumber]
		if name == "" || name != expected[i] || seen[name] {
			return
		}
		target.Columns[i] = name
		seen[name] = true
	}
	for _, key := range keys {
		if !seen[key] {
			result.RemovalUnavailableReason = "Include every primary-key column in the SELECT result before editing or removing rows"
			return
		}
	}
	result.DeletionTarget = target
	result.RemovalUnavailableReason = ""
}
