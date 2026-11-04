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
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
)

const (
	queryRowLimit   = 1000
	resultByteLimit = 5 << 20
	queryTimeout    = 5 * time.Minute
)

type ConnectionInfo struct {
	Database string `json:"database"`
	User     string `json:"user"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
}

type ConnectionOptions struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
	SSLMode  string `json:"sslMode"`
}

type TableInfo struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

type ColumnInfo struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
}

type CatalogColumn struct {
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Name     string `json:"name"`
	DataType string `json:"dataType"`
}

type QueryResult struct {
	Columns      []ColumnInfo `json:"columns"`
	Rows         [][]*string  `json:"rows"`
	RowCount     int          `json:"rowCount"`
	RowsAffected string       `json:"rowsAffected"`
	DurationMs   int64        `json:"durationMs"`
	Truncated    bool         `json:"truncated"`
	CommandTag   string       `json:"commandTag"`
}

// Service owns a single session; every database operation shares its cancellation gate.
type Service struct {
	mu             sync.Mutex
	conn           *pgx.Conn
	cancel         context.CancelFunc
	done           chan struct{}
	disconnecting  bool
	disconnectDone chan struct{}
	closed         bool
}

func NewService() *Service { return &Service{} }

func (s *Service) begin(requireConnection bool, timeout time.Duration) (context.Context, *pgx.Conn, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, nil, errors.New("Tusk is shutting down")
	}
	if s.disconnecting || s.cancel != nil {
		return nil, nil, nil, errors.New("Another operation is running. Cancel it or wait for it to finish")
	}
	if requireConnection && (s.conn == nil || s.conn.IsClosed()) {
		return nil, nil, nil, errors.New("Not connected. Connect to a PostgreSQL database first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	return ctx, s.conn, func() {
		cancel()
		s.mu.Lock()
		s.cancel, s.done = nil, nil
		close(done)
		s.mu.Unlock()
	}, nil
}

func (s *Service) Connect(connectionURL string) (ConnectionInfo, error) {
	return s.connect(connectionURL, nil)
}

// ConnectSavedURL connects with the password captured when the URL was saved.
// Kept off Service's method set to avoid adding a Wails binding.
func ConnectSavedURL(s *Service, connectionURL, password string) (ConnectionInfo, error) {
	return s.connect(connectionURL, &password)
}

func (s *Service) connect(connectionURL string, password *string) (ConnectionInfo, error) {
	var info ConnectionInfo
	connectionURL = strings.TrimSpace(connectionURL)
	if strings.HasPrefix(connectionURL, "jdbc:postgresql://") || strings.HasPrefix(connectionURL, "jdbc:postgres://") {
		connectionURL = strings.TrimPrefix(connectionURL, "jdbc:")
	}
	parsed, err := url.Parse(connectionURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return info, errors.New("Enter a PostgreSQL URL beginning with postgres:// or postgresql://")
	}
	config, err := pgx.ParseConfig(connectionURL)
	if err != nil {
		return info, errors.New("Invalid PostgreSQL connection URL. Check its host, port, credentials, and TLS options")
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
	ctx, old, finish, err := s.begin(false, 15*time.Second)
	if err != nil {
		return info, err
	}
	defer finish()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return info, connectionError(ctx, err)
	}
	if err := conn.QueryRow(ctx, "SELECT current_database(), current_user").Scan(&info.Database, &info.User); err != nil {
		closeConnection(conn)
		return ConnectionInfo{}, connectionError(ctx, err)
	}
	info.Host, info.Port = config.Host, config.Port
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	closeConnection(old)
	return info, nil
}

func (s *Service) ConnectWithOptions(options ConnectionOptions) (ConnectionInfo, error) {
	connectionURL, err := ConnectionURLForOptions(options)
	if err != nil {
		return ConnectionInfo{}, err
	}
	return s.connect(connectionURL, &options.Password)
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

func (s *Service) ListTables() ([]TableInfo, error) {
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return nil, err
	}
	defer finish()
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

func (s *Service) ListSchemas() ([]string, error) {
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return nil, err
	}
	defer finish()
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

func (s *Service) ListColumns() ([]CatalogColumn, error) {
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return nil, err
	}
	defer finish()
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

func (s *Service) BrowseTable(schema, table string) (QueryResult, error) {
	if schema == "" || table == "" || strings.ContainsRune(schema+table, '\x00') {
		return QueryResult{}, errors.New("Choose a valid schema and table")
	}
	return s.query("SELECT * FROM "+pgx.Identifier{schema, table}.Sanitize()+" LIMIT 100", 100)
}

func (s *Service) BrowseTableSorted(schema, table, sortColumn string, descending bool) (QueryResult, error) {
	if schema == "" || table == "" || sortColumn == "" || strings.ContainsRune(schema+table+sortColumn, '\x00') {
		return QueryResult{}, errors.New("Choose a valid schema, table, and sort column")
	}
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	// Keep NULL values last in both directions; PostgreSQL sorts before applying the row limit.
	sql := "SELECT * FROM " + pgx.Identifier{schema, table}.Sanitize() +
		" ORDER BY " + pgx.Identifier{sortColumn}.Sanitize() + " " + direction + " NULLS LAST LIMIT 100"
	return s.query(sql, 100)
}

func (s *Service) RunQuery(sql string) (QueryResult, error) {
	if strings.TrimSpace(sql) == "" {
		return QueryResult{}, errors.New("Enter one SQL statement to run")
	}
	if len(sql) > 1<<20 {
		return QueryResult{}, errors.New("SQL statements must be smaller than 1 MiB")
	}
	return s.query(sql, queryRowLimit)
}

func (s *Service) query(sql string, rowLimit int) (QueryResult, error) {
	result := QueryResult{Columns: []ColumnInfo{}, Rows: [][]*string{}, RowsAffected: "0"}
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return result, err
	}
	defer finish()
	started := time.Now()
	// Extended protocol rejects multiple statements and text format preserves numeric precision.
	reader := conn.PgConn().ExecParams(ctx, sql, nil, nil, nil, nil)
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
		if !appendResultRow(&result, reader.Values(), &byteCount, resultByteLimit) {
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
	return result, nil
}

func appendResultRow(result *QueryResult, values [][]byte, byteCount *int, byteLimit int) bool {
	row := make([]*string, len(values))
	for i, value := range values {
		if value != nil {
			text := string(value)
			row[i] = &text
		}
	}
	encoded, _ := json.Marshal(row)
	if *byteCount+len(encoded) > byteLimit {
		return false
	}
	*byteCount += len(encoded)
	result.Rows = append(result.Rows, row)
	return true
}

func (s *Service) CancelQuery() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (s *Service) Disconnect() error { return s.disconnect(false) }

func (s *Service) Shutdown() { _ = s.disconnect(true) }

func (s *Service) disconnect(shutdown bool) error {
	s.mu.Lock()
	if shutdown {
		s.closed = true
	}
	if s.disconnecting {
		done := s.disconnectDone
		s.mu.Unlock()
		<-done
		return nil
	}
	s.disconnecting = true
	s.disconnectDone = make(chan struct{})
	done := s.done
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	closeConnection(conn)
	s.mu.Lock()
	s.disconnecting = false
	close(s.disconnectDone)
	s.disconnectDone = nil
	s.mu.Unlock()
	return nil
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
