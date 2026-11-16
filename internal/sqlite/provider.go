// Package sqlite implements the database store contract for local SQLite files.
package sqlite

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"tusk/internal/db"
)

type Provider struct{}

func NewProvider() *Provider { return &Provider{} }

// ValidatePath never creates a database when the path is misspelled.
func ValidatePath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, '\x00') || !filepath.IsAbs(path) || len(path) > 1<<20 {
		return "", errors.New("Enter an absolute path to an existing SQLite database file")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("SQLite database file was not found. Check its path and file permissions")
	}
	return path, nil
}

func (*Provider) Open(ctx context.Context, config db.ConnectionConfig) (db.Store, db.ConnectionInfo, error) {
	path, err := ValidatePath(config.Options.FilePath)
	if err != nil {
		return nil, db.ConnectionInfo{}, err
	}
	location := url.URL{Scheme: "file", Path: path}
	location.RawQuery = url.Values{"mode": {"rw"}, "_busy_timeout": {"1000"}}.Encode()
	connection, err := (&sqlite3.SQLiteDriver{}).Open(location.String())
	if err != nil {
		return nil, db.ConnectionInfo{}, errors.New("Could not open SQLite database. Check file permissions and that the file is a SQLite database")
	}
	store := &Store{conn: connection.(*sqlite3.SQLiteConn)}
	store.conn.RegisterAuthorizer(func(action int, table, _, _ string) int {
		if store.captureCommand && store.withCommand == "" && !strings.HasPrefix(table, "sqlite_") {
			switch action {
			case sqlite3.SQLITE_INSERT:
				store.withCommand = "INSERT"
			case sqlite3.SQLITE_UPDATE:
				store.withCommand = "UPDATE"
			case sqlite3.SQLITE_DELETE:
				store.withCommand = "DELETE"
			}
		}
		return sqlite3.SQLITE_OK
	})
	if err := store.each(ctx, "SELECT count(*) FROM sqlite_schema", nil, func([]driver.Value) error { return nil }); err != nil {
		_ = store.Close()
		if ctx.Err() != nil {
			return nil, db.ConnectionInfo{}, errors.New("Connection attempt canceled or timed out")
		}
		return nil, db.ConnectionInfo{}, errors.New("Could not read SQLite database. Check file permissions and that the file is a SQLite database")
	}
	return store, db.ConnectionInfo{Provider: "sqlite", FilePath: path, Database: filepath.Base(path)}, nil
}

type Store struct {
	conn           *sqlite3.SQLiteConn
	captureCommand bool
	withCommand    string
}

func (s *Store) Close() error { return s.conn.Close() }

func (s *Store) each(ctx context.Context, query string, args []driver.NamedValue, visit func([]driver.Value) error) error {
	rows, err := s.conn.QueryContext(ctx, query, args)
	if err != nil {
		return queryError(ctx, err)
	}
	defer rows.Close()
	values := make([]driver.Value, len(rows.Columns()))
	for {
		err := rows.Next(values)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return queryError(ctx, err)
		}
		if err := visit(values); err != nil {
			return err
		}
	}
}

func (s *Store) ListSchemas(ctx context.Context) ([]string, error) {
	schemas := []string{}
	err := s.each(ctx, "PRAGMA database_list", nil, func(values []driver.Value) error {
		schemas = append(schemas, values[1].(string))
		return nil
	})
	sort.Strings(schemas)
	return schemas, err
}

func (s *Store) ListTables(ctx context.Context) ([]db.TableInfo, error) {
	schemas, err := s.ListSchemas(ctx)
	if err != nil {
		return nil, err
	}
	tables := []db.TableInfo{}
	for _, schema := range schemas {
		query := "SELECT name, type FROM " + db.QuoteIdentifier(schema) + ".sqlite_schema WHERE type IN ('table','view') AND substr(name, 1, 7) <> 'sqlite_' ORDER BY name"
		if err := s.each(ctx, query, nil, func(values []driver.Value) error {
			tables = append(tables, db.TableInfo{Schema: schema, Name: values[0].(string), Type: values[1].(string)})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return tables, nil
}

func (s *Store) ListColumns(ctx context.Context) ([]db.CatalogColumn, error) {
	tables, err := s.ListTables(ctx)
	if err != nil {
		return nil, err
	}
	columns := []db.CatalogColumn{}
	for _, table := range tables {
		query := "SELECT name, type FROM pragma_table_xinfo(?, ?) WHERE hidden <> 1 ORDER BY cid"
		args := []driver.NamedValue{{Ordinal: 1, Value: table.Name}, {Ordinal: 2, Value: table.Schema}}
		if err := s.each(ctx, query, args, func(values []driver.Value) error {
			columns = append(columns, db.CatalogColumn{Schema: table.Schema, Table: table.Name, Name: values[0].(string), DataType: values[1].(string)})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return columns, nil
}

func (s *Store) Query(ctx context.Context, statement string, rowLimit int) (db.QueryResult, error) {
	result := db.QueryResult{Columns: []db.ColumnInfo{}, Rows: [][]*string{}, RowsAffected: "0"}
	statement, err := singleStatement(statement)
	if err != nil {
		return result, err
	}
	started := time.Now()
	command := firstWord(statement)
	s.captureCommand, s.withCommand = command == "WITH", ""
	prepared, err := s.conn.PrepareContext(ctx, statement)
	s.captureCommand = false
	if command == "WITH" {
		command = s.withCommand
	}
	if err != nil {
		return db.QueryResult{}, queryError(ctx, err)
	}
	defer prepared.Close()
	stmt := prepared.(*sqlite3.SQLiteStmt)
	if stmt.NumInput() != 0 {
		return db.QueryResult{}, errors.New("Provide literal SQL values; query parameters are not supported")
	}
	rows, err := stmt.QueryContext(ctx, nil)
	if err != nil {
		return db.QueryResult{}, queryError(ctx, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = rows.Close()
		}
	}()
	types := rows.(*sqlite3.SQLiteRows).DeclTypes()
	for index, name := range rows.Columns() {
		dataType := types[index]
		if dataType == "" {
			dataType = "unknown"
		}
		result.Columns = append(result.Columns, db.ColumnInfo{Name: name, DataType: dataType})
	}
	// The pinned driver uses this mutable slice to coerce dates/booleans; preserve stored values instead.
	clear(types)
	encoded, _ := json.Marshal(result.Columns)
	byteCount := len(encoded)
	values := make([]driver.Value, len(result.Columns))
	for {
		err := rows.Next(values)
		if err == io.EOF {
			break
		}
		if err != nil {
			return db.QueryResult{}, queryError(ctx, err)
		}
		if result.Truncated || len(result.Rows) >= rowLimit {
			result.Truncated = true
			continue
		}
		texts := make([][]byte, len(values))
		for index, value := range values {
			if value == nil {
				continue
			}
			text, valueType := formatValue(value)
			texts[index] = []byte(text)
			if result.Columns[index].DataType == "unknown" {
				result.Columns[index].DataType = valueType
			}
		}
		if !db.AppendResultRow(&result, texts, &byteCount, db.ResultByteLimit) {
			result.Truncated = true
		}
	}
	if err := rows.Close(); err != nil {
		return db.QueryResult{}, queryError(ctx, err)
	}
	closed = true
	if command == "REPLACE" {
		command = "INSERT"
	}
	if command == "INSERT" || command == "UPDATE" || command == "DELETE" {
		// Read changes only after draining RETURNING so the write has completed.
		if err := s.each(ctx, "SELECT changes()", nil, func(values []driver.Value) error {
			result.RowsAffected = strconv.FormatInt(values[0].(int64), 10)
			return nil
		}); err != nil {
			return db.QueryResult{}, err
		}
	}
	if command == "" {
		command = "SELECT"
	}
	result.CommandTag = command
	result.RowCount = len(result.Rows)
	result.DurationMs = time.Since(started).Milliseconds()
	s.deletionTarget(ctx, statement, &result)
	return result, nil
}

func firstWord(statement string) string {
	index := skipTrivia(statement, 0)
	end := index
	for end < len(statement) && (statement[end] >= 'a' && statement[end] <= 'z' || statement[end] >= 'A' && statement[end] <= 'Z') {
		end++
	}
	return strings.ToUpper(statement[index:end])
}

func formatValue(value driver.Value) (string, string) {
	switch value := value.(type) {
	case string:
		return value, "text"
	case []byte:
		return "X'" + hex.EncodeToString(value) + "'", "blob"
	case int64:
		return strconv.FormatInt(value, 10), "integer"
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), "real"
	case bool:
		return strconv.FormatBool(value), "boolean"
	case time.Time:
		return value.Format(time.RFC3339Nano), "timestamp"
	default:
		return fmt.Sprint(value), "text"
	}
}

func queryError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("Query timed out after five minutes. The statement may have been rolled back")
	}
	if ctx.Err() != nil {
		return errors.New("Operation canceled. An interrupted write may have been rolled back")
	}
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) {
		return fmt.Errorf("SQLite: %s", sqliteError.Error())
	}
	return errors.New("Could not run SQLite statement. Check the database file and try again")
}
