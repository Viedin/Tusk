package sqlite

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mattn/go-sqlite3"
	"tusk/internal/db"
)

type editColumn struct {
	name     string
	dataType string
	key      bool
	readOnly bool
}

func (s *Store) editColumns(ctx context.Context, schema, table string) ([]editColumn, db.TableEditInfo, error) {
	info := db.TableEditInfo{KeyColumns: []string{}, ReadOnlyColumns: []string{}}
	if schema == "" || table == "" || strings.ContainsRune(schema+table, '\x00') {
		return nil, info, errors.New("Choose a valid schema and table")
	}
	var kind string
	err := s.each(ctx, "SELECT type FROM pragma_table_list WHERE schema = ? AND name = ?", []driver.NamedValue{{Ordinal: 1, Value: schema}, {Ordinal: 2, Value: table}}, func(values []driver.Value) error {
		kind = values[0].(string)
		return nil
	})
	if err != nil {
		return nil, info, err
	}
	if kind != "table" || strings.HasPrefix(strings.ToLower(table), "sqlite_") {
		info.Reason = "Only regular tables with a primary key can be edited."
		return nil, info, nil
	}
	columns := []editColumn{}
	err = s.each(ctx, "SELECT name, type, pk, hidden FROM pragma_table_xinfo(?, ?) WHERE hidden <> 1 ORDER BY cid", []driver.NamedValue{{Ordinal: 1, Value: table}, {Ordinal: 2, Value: schema}}, func(values []driver.Value) error {
		column := editColumn{name: values[0].(string), dataType: values[1].(string), key: values[2].(int64) > 0, readOnly: values[3].(int64) != 0}
		columns = append(columns, column)
		if column.key {
			info.KeyColumns = append(info.KeyColumns, column.name)
		}
		if column.readOnly {
			info.ReadOnlyColumns = append(info.ReadOnlyColumns, column.name)
		}
		return nil
	})
	if err == nil && len(info.KeyColumns) == 0 {
		info.Reason = "This table has no primary key. Add one before editing or removing rows."
	}
	return columns, info, err
}

func (s *Store) TableEditInfo(ctx context.Context, schema, table string) (db.TableEditInfo, error) {
	_, info, err := s.editColumns(ctx, schema, table)
	return info, err
}

func (s *Store) SaveTableChanges(ctx context.Context, schema, table string, columnNames []string, changes []db.RowChange) (int, error) {
	if err := db.ValidateChanges(columnNames, changes); err != nil {
		return 0, err
	}
	if _, err := s.conn.ExecContext(ctx, "BEGIN IMMEDIATE", nil); err != nil {
		return 0, queryError(ctx, err)
	}
	defer s.conn.ExecContext(context.Background(), "ROLLBACK", nil)
	columns, info, err := s.editColumns(ctx, schema, table)
	if err != nil {
		return 0, err
	}
	if info.Reason != "" {
		return 0, errors.New(info.Reason)
	}
	actualColumns := make(map[string]editColumn, len(columns))
	for _, column := range columns {
		actualColumns[column.name] = column
	}
	selectedColumns := make([]editColumn, len(columnNames))
	columnIndex := make(map[string]int, len(columns))
	quotedColumns := make([]string, len(columnNames))
	for index, name := range columnNames {
		column, exists := actualColumns[name]
		if !exists {
			return 0, errors.New("The table columns changed. Undo your changes and refresh the rows")
		}
		selectedColumns[index] = column
		columnIndex[column.name] = index
		quotedColumns[index] = db.QuoteIdentifier(column.name)
	}
	for _, key := range info.KeyColumns {
		if _, exists := columnIndex[key]; !exists {
			return 0, errors.New("Include every primary key column before editing or removing rows")
		}
	}
	columns = selectedColumns
	qualified := db.QuoteIdentifier(schema) + "." + db.QuoteIdentifier(table)
	seenKeys := make(map[string]bool, len(changes))
	for _, change := range changes {
		keys := []*string{}
		for index, column := range columns {
			if column.key {
				if change.Original[index] == nil {
					return 0, errors.New("Rows with a NULL primary key cannot be edited safely")
				}
				keys = append(keys, change.Original[index])
			}
		}
		encodedKeys, _ := json.Marshal(keys)
		if seenKeys[string(encodedKeys)] {
			return 0, errors.New("A row can only appear once in a save")
		}
		seenKeys[string(encodedKeys)] = true
		for name := range change.Values {
			index, exists := columnIndex[name]
			if !exists || columns[index].readOnly {
				return 0, fmt.Errorf("Column %q cannot be edited. Undo your changes and refresh the rows", name)
			}
		}
		current, err := s.originalRow(ctx, qualified, quotedColumns, columns, change.Original)
		if err != nil {
			return 0, err
		}
		args := []driver.NamedValue{}
		addArgument := func(value driver.Value) string {
			args = append(args, driver.NamedValue{Ordinal: len(args) + 1, Value: value})
			return "?"
		}
		statement := "DELETE FROM " + qualified
		if !change.Delete {
			assignments := []string{}
			for index, column := range columns {
				value, changed := change.Values[column.name]
				if !changed {
					continue
				}
				bound, err := editedValue(value, column.dataType, current[index])
				if err != nil {
					return 0, fmt.Errorf("Column %q: %w", column.name, err)
				}
				assignments = append(assignments, quotedColumns[index]+" = "+addArgument(bound))
			}
			statement = "UPDATE " + qualified + " SET " + strings.Join(assignments, ", ")
		}
		where := []string{}
		for index, column := range columns {
			if column.key {
				where = append(where, quotedColumns[index]+" IS "+addArgument(current[index]))
			}
		}
		result, err := s.conn.ExecContext(ctx, statement+" WHERE "+strings.Join(where, " AND "), args)
		if err != nil {
			return 0, queryError(ctx, err)
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return 0, errors.New("A row changed or was removed. Nothing was saved. Undo your changes and refresh the rows")
		}
	}
	if _, err := s.conn.ExecContext(ctx, "COMMIT", nil); err != nil {
		return 0, queryError(ctx, err)
	}
	return len(changes), nil
}

// Resolve textual keys back to stored values before writing; SQLite permits mixed storage types.
func (s *Store) originalRow(ctx context.Context, qualified string, quotedColumns []string, columns []editColumn, original []*string) ([]driver.Value, error) {
	args := []driver.NamedValue{}
	where := []string{}
	for index, column := range columns {
		if !column.key {
			continue
		}
		text := *original[index]
		candidates := []driver.Value{text}
		if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
			candidates = append(candidates, integer)
		} else if real, err := strconv.ParseFloat(text, 64); err == nil {
			candidates = append(candidates, real)
		}
		if blob, ok := parseBlob(text); ok {
			candidates = append(candidates, blob)
		}
		alternatives := []string{}
		for _, candidate := range candidates {
			args = append(args, driver.NamedValue{Ordinal: len(args) + 1, Value: candidate})
			alternatives = append(alternatives, quotedColumns[index]+" IS ?")
		}
		where = append(where, "("+strings.Join(alternatives, " OR ")+")")
	}
	rows, err := s.conn.QueryContext(ctx, "SELECT "+strings.Join(quotedColumns, ", ")+" FROM "+qualified+" WHERE "+strings.Join(where, " AND "), args)
	if err != nil {
		return nil, queryError(ctx, err)
	}
	defer rows.Close()
	clear(rows.(*sqlite3.SQLiteRows).DeclTypes())
	values := make([]driver.Value, len(columns))
	var matched []driver.Value
	for {
		if err := rows.Next(values); err == io.EOF {
			break
		} else if err != nil {
			return nil, queryError(ctx, err)
		}
		matches := true
		for index, value := range values {
			if value == nil || original[index] == nil {
				matches = matches && value == nil && original[index] == nil
			} else {
				text, _ := formatValue(value)
				matches = matches && text == *original[index]
			}
		}
		if matches {
			if matched != nil {
				return nil, errors.New("This row cannot be identified uniquely. Nothing was saved")
			}
			matched = append([]driver.Value{}, values...)
			for index, value := range matched {
				if blob, ok := value.([]byte); ok {
					matched[index] = append([]byte{}, blob...)
				}
			}
		}
	}
	if matched == nil {
		return nil, errors.New("A row changed or was removed. Nothing was saved. Undo your changes and refresh the rows")
	}
	return matched, nil
}

func parseBlob(text string) ([]byte, bool) {
	if len(text) < 3 || (text[0] != 'X' && text[0] != 'x') || text[1] != '\'' || text[len(text)-1] != '\'' {
		return nil, false
	}
	value, err := hex.DecodeString(text[2 : len(text)-1])
	return value, err == nil
}

func editedValue(value *string, declaredType string, current driver.Value) (driver.Value, error) {
	if value == nil {
		return nil, nil
	}
	_, wasBlob := current.([]byte)
	if wasBlob || strings.Contains(strings.ToUpper(declaredType), "BLOB") {
		blob, ok := parseBlob(*value)
		if !ok {
			return nil, errors.New("Enter a blob as X'hexadecimal digits', or set it to NULL")
		}
		return blob, nil
	}
	// Binding text lets the column's SQLite affinity perform conversion without a float round trip.
	return *value, nil
}
