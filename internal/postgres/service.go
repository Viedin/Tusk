package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"tusk/internal/db"
)

type ConnectionInfo = db.ConnectionInfo
type ConnectionOptions = db.ConnectionOptions
type TableInfo = db.TableInfo
type ColumnInfo = db.ColumnInfo
type CatalogColumn = db.CatalogColumn
type QueryResult = db.QueryResult

type Provider struct{}

func NewProvider() *Provider { return &Provider{} }

type session struct{ conn *pgx.Conn }

var _ db.Provider = (*Provider)(nil)
var _ db.Store = (*session)(nil)

func (p *Provider) Open(ctx context.Context, request db.ConnectionConfig) (db.Store, ConnectionInfo, error) {
	connectionURL, password := request.URL, request.Password
	if connectionURL == "" {
		var err error
		connectionURL, err = ConnectionURLForOptions(request.Options)
		if err != nil {
			return nil, ConnectionInfo{}, err
		}
		password = &request.Options.Password
	}
	var info ConnectionInfo
	connectionURL = strings.TrimSpace(connectionURL)
	if strings.HasPrefix(connectionURL, "jdbc:postgresql://") || strings.HasPrefix(connectionURL, "jdbc:postgres://") {
		connectionURL = strings.TrimPrefix(connectionURL, "jdbc:")
	}
	parsed, err := url.Parse(connectionURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, info, errors.New("Enter a PostgreSQL URL beginning with postgres:// or postgresql://")
	}
	config, err := pgx.ParseConfig(connectionURL)
	if err != nil {
		return nil, info, errors.New("Invalid PostgreSQL connection URL. Check its host, port, credentials, and TLS options")
	}
	if password != nil {
		// A blank password in the form must not silently use PGPASSWORD or a passfile.
		config.Password = *password
	}
	config.ConnectTimeout = 10 * time.Second
	config.RuntimeParams["application_name"] = "Tusk"
	config.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: conn, DeadlineDelay: 3 * time.Second}
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, info, connectionError(ctx, err)
	}
	if err := conn.QueryRow(ctx, "SELECT current_database(), current_user").Scan(&info.Database, &info.User); err != nil {
		closeConnection(conn)
		return nil, ConnectionInfo{}, connectionError(ctx, err)
	}
	info.Host, info.Port = config.Host, config.Port
	info.Provider = "postgres"
	return &session{conn: conn}, info, nil
}

// ConnectionURLForOptions validates form fields and preserves exact credential values.
func ConnectionURLForOptions(options ConnectionOptions) (string, error) {
	for _, value := range []string{options.Host, options.Database, options.User, options.Password, options.SSLMode} {
		if strings.ContainsRune(value, '\x00') {
			return "", errors.New("Connection fields cannot contain NUL characters")
		}
	}
	host := strings.TrimSpace(options.Host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return "", errors.New("Host is required")
	}
	if strings.ContainsAny(host, "[]/?#@,\\") || strings.IndexFunc(host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", errors.New("Enter a hostname or IP address without a URL or port")
	}
	if strings.Contains(host, ":") {
		if _, err := netip.ParseAddr(host); err != nil {
			return "", errors.New("Enter a hostname or IPv6 address; specify the port separately")
		}
	}
	if options.Port < 1 || options.Port > 65535 {
		return "", errors.New("Port must be between 1 and 65535")
	}
	if strings.TrimSpace(options.User) == "" {
		return "", errors.New("Username is required")
	}
	if strings.TrimSpace(options.Database) == "" {
		return "", errors.New("Database is required")
	}
	sslMode := strings.ToLower(strings.TrimSpace(options.SSLMode))
	if sslMode == "" {
		sslMode = "prefer"
	}
	switch sslMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return "", errors.New("Choose a supported PostgreSQL TLS mode")
	}
	connectionURL := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(options.User, options.Password),
		Host:   net.JoinHostPort(host, strconv.Itoa(options.Port)),
		Path:   "/" + options.Database,
	}
	query := url.Values{"sslmode": {sslMode}}
	connectionURL.RawQuery = query.Encode()
	return connectionURL.String(), nil
}

func (s *session) ListTables(ctx context.Context) ([]TableInfo, error) {
	conn := s.conn
	rows, err := conn.Query(ctx, `
		SELECT n.nspname, c.relname,
		       CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view'
		            WHEN 'f' THEN 'foreign table' ELSE 'table' END
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND n.nspname <> 'information_schema'
		  AND n.nspname !~ '^pg_'
		  AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
		  AND pg_catalog.has_table_privilege(c.oid, 'SELECT')
		ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, queryError(ctx, err)
	}
	defer rows.Close()
	tables := make([]TableInfo, 0)
	for rows.Next() {
		var table TableInfo
		if err := rows.Scan(&table.Schema, &table.Name, &table.Type); err != nil {
			return nil, queryError(ctx, err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, queryError(ctx, err)
	}
	return tables, nil
}

func (s *session) ListSchemas(ctx context.Context) ([]string, error) {
	conn := s.conn
	rows, err := conn.Query(ctx, `
		SELECT nspname FROM pg_catalog.pg_namespace
		WHERE nspname <> 'information_schema' AND nspname !~ '^pg_'
		  AND pg_catalog.has_schema_privilege(oid, 'USAGE')
		ORDER BY nspname`)
	if err != nil {
		return nil, queryError(ctx, err)
	}
	defer rows.Close()
	schemas := make([]string, 0)
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			return nil, queryError(ctx, err)
		}
		schemas = append(schemas, schema)
	}
	if err := rows.Err(); err != nil {
		return nil, queryError(ctx, err)
	}
	return schemas, nil
}

func (s *session) ListColumns(ctx context.Context) ([]CatalogColumn, error) {
	conn := s.conn
	rows, err := conn.Query(ctx, `
		SELECT n.nspname, c.relname, a.attname,
		       pg_catalog.format_type(a.atttypid, a.atttypmod)
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND a.attnum > 0 AND NOT a.attisdropped
		  AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'
		  AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
		  AND pg_catalog.has_table_privilege(c.oid, 'SELECT')
		  AND pg_catalog.has_column_privilege(c.oid, a.attnum, 'SELECT')
		ORDER BY n.nspname, c.relname, a.attnum`)
	if err != nil {
		return nil, queryError(ctx, err)
	}
	defer rows.Close()
	columns := make([]CatalogColumn, 0)
	for rows.Next() {
		var column CatalogColumn
		if err := rows.Scan(&column.Schema, &column.Table, &column.Name, &column.DataType); err != nil {
			return nil, queryError(ctx, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, queryError(ctx, err)
	}
	return columns, nil
}

func (s *session) Query(ctx context.Context, sql string, rowLimit int) (QueryResult, error) {
	result := QueryResult{Columns: []ColumnInfo{}, Rows: [][]*string{}, RowsAffected: "0"}
	conn := s.conn
	started := time.Now()
	// Extended protocol rejects multiple statements and text format preserves numeric precision.
	reader := conn.PgConn().ExecParams(ctx, sql, nil, nil, nil, nil)
	fields := append([]pgconn.FieldDescription(nil), reader.FieldDescriptions()...)
	for _, field := range reader.FieldDescriptions() {
		dataType := "oid:" + strconv.FormatUint(uint64(field.DataTypeOID), 10)
		if known, ok := conn.TypeMap().TypeForOID(field.DataTypeOID); ok {
			dataType = known.Name
		}
		result.Columns = append(result.Columns, ColumnInfo{Name: field.Name, DataType: dataType})
	}
	columnBytes, _ := json.Marshal(result.Columns)
	byteCount := len(columnBytes)
	for reader.NextRow() {
		if result.Truncated || len(result.Rows) >= rowLimit {
			result.Truncated = true
			continue
		}
		if !db.AppendResultRow(&result, reader.Values(), &byteCount, db.ResultByteLimit) {
			result.Truncated = true
		}
	}
	// Drain excess rows so a successful write with RETURNING still commits. Cancel remains active.
	tag, err := reader.Close()
	result.DurationMs = time.Since(started).Milliseconds()
	if err != nil {
		return QueryResult{}, queryError(ctx, err)
	}
	result.CommandTag = tag.String()
	result.RowsAffected = strconv.FormatInt(tag.RowsAffected(), 10)
	result.RowCount = len(result.Rows)
	s.identifyDeletionTarget(ctx, sql, fields, &result)
	return result, nil
}

func (s *session) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.conn.Close(ctx)
}

func closeConnection(conn *pgx.Conn) {
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = conn.Close(ctx)
	}
}

func connectionError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("Connection attempt timed out. Check the host, port, and network")
	}
	if ctx.Err() != nil {
		return errors.New("Connection attempt canceled")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01", "28000":
			return errors.New("Authentication failed. Check the username and password")
		case "3D000":
			return errors.New("Database does not exist. Check the database name in the URL")
		}
	}
	// Driver connection errors can embed the full URL; never send them across the bridge.
	return errors.New("Could not connect. Check the host, port, network, credentials, and TLS options")
}

func queryError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("Query timed out after five minutes. The statement may have been rolled back")
	}
	if ctx.Err() != nil {
		return errors.New("Operation canceled. An interrupted write may have been rolled back")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("%s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
	}
	return errors.New("Database connection was interrupted. Reconnect and try again")
}
