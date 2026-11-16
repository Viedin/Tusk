package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"tusk/internal/db"
)

const queryTimeout = 5 * time.Minute

// Service owns a single session; every database operation shares its cancellation gate.
type Service struct {
	mu             sync.Mutex
	conn           db.Store
	providers      map[string]db.Provider
	cancel         context.CancelFunc
	done           chan struct{}
	disconnecting  bool
	disconnectDone chan struct{}
	closed         bool
}

func NewService(providers map[string]db.Provider) *Service {
	registered := make(map[string]db.Provider, len(providers))
	for name, provider := range providers {
		registered[name] = provider
	}
	return &Service{providers: registered}
}

func (s *Service) begin(requireConnection bool, timeout time.Duration) (context.Context, db.Store, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, nil, errors.New("Tusk is shutting down")
	}
	if s.disconnecting || s.cancel != nil {
		return nil, nil, nil, errors.New("Another operation is running. Cancel it or wait for it to finish")
	}
	if requireConnection && s.conn == nil {
		return nil, nil, nil, errors.New("Not connected. Connect to a database first")
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

func (s *Service) Connect(connectionURL string) (db.ConnectionInfo, error) {
	return s.connect("postgres", db.ConnectionConfig{URL: connectionURL})
}

// ConnectSavedURL keeps saved credentials inside Go rather than exposing another binding.
func ConnectSavedURL(s *Service, connectionURL, password string) (db.ConnectionInfo, error) {
	return s.connect("postgres", db.ConnectionConfig{URL: connectionURL, Password: &password})
}

func (s *Service) ConnectWithOptions(options db.ConnectionOptions) (db.ConnectionInfo, error) {
	provider := options.Provider
	if provider == "" {
		provider = "postgres"
	}
	return s.connect(provider, db.ConnectionConfig{Options: options})
}

func (s *Service) connect(name string, config db.ConnectionConfig) (db.ConnectionInfo, error) {
	provider := s.providers[name]
	if provider == nil {
		return db.ConnectionInfo{}, errors.New("Choose a supported database provider")
	}
	ctx, old, finish, err := s.begin(false, 15*time.Second)
	if err != nil {
		return db.ConnectionInfo{}, err
	}
	defer finish()
	conn, info, err := provider.Open(ctx, config)
	if err != nil {
		return db.ConnectionInfo{}, err
	}
	if ctx.Err() != nil {
		_ = conn.Close()
		return db.ConnectionInfo{}, errors.New("Connection attempt canceled or timed out")
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return info, nil
}

func (s *Service) ListTables() ([]db.TableInfo, error) {
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return nil, err
	}
	defer finish()
	return conn.ListTables(ctx)
}

func (s *Service) ListSchemas() ([]string, error) {
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return nil, err
	}
	defer finish()
	return conn.ListSchemas(ctx)
}

func (s *Service) ListColumns() ([]db.CatalogColumn, error) {
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return nil, err
	}
	defer finish()
	return conn.ListColumns(ctx)
}

func (s *Service) BrowseTable(schema, table string) (db.QueryResult, error) {
	if schema == "" || table == "" || strings.ContainsRune(schema+table, '\x00') {
		return db.QueryResult{}, errors.New("Choose a valid schema and table")
	}
	return s.query("SELECT * FROM "+db.QuoteIdentifier(schema)+"."+db.QuoteIdentifier(table)+" LIMIT 100", 100)
}

func (s *Service) BrowseTableSorted(schema, table, sortColumn string, descending bool) (db.QueryResult, error) {
	if schema == "" || table == "" || sortColumn == "" || strings.ContainsRune(schema+table+sortColumn, '\x00') {
		return db.QueryResult{}, errors.New("Choose a valid schema, table, and sort column")
	}
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	// Keep NULL values last in both directions and sort before applying the row limit.
	sql := "SELECT * FROM " + db.QuoteIdentifier(schema) + "." + db.QuoteIdentifier(table) +
		" ORDER BY " + db.QuoteIdentifier(sortColumn) + " " + direction + " NULLS LAST LIMIT 100"
	return s.query(sql, 100)
}

func (s *Service) RunQuery(sql string) (db.QueryResult, error) {
	if strings.TrimSpace(sql) == "" {
		return db.QueryResult{}, errors.New("Enter one SQL statement to run")
	}
	if len(sql) > 1<<20 {
		return db.QueryResult{}, errors.New("SQL statements must be smaller than 1 MiB")
	}
	return s.query(sql, db.QueryRowLimit)
}

func (s *Service) GetTableEditInfo(schema, table string) (db.TableEditInfo, error) {
	if schema == "" || table == "" || strings.ContainsRune(schema+table, '\x00') {
		return db.TableEditInfo{}, errors.New("Choose a valid schema and table")
	}
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return db.TableEditInfo{}, err
	}
	defer finish()
	editor, ok := conn.(db.TableEditor)
	if !ok {
		return db.TableEditInfo{KeyColumns: []string{}, ReadOnlyColumns: []string{}, Reason: "This database does not support table editing"}, nil
	}
	return editor.TableEditInfo(ctx, schema, table)
}

func (s *Service) SaveTableChanges(schema, table string, columns []string, changes []db.RowChange) (int, error) {
	if schema == "" || table == "" || strings.ContainsRune(schema+table, '\x00') {
		return 0, errors.New("Choose a valid schema and table")
	}
	if err := db.ValidateChanges(columns, changes); err != nil {
		return 0, err
	}
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return 0, err
	}
	defer finish()
	editor, ok := conn.(db.TableEditor)
	if !ok {
		return 0, errors.New("This database does not support table editing")
	}
	return editor.SaveTableChanges(ctx, schema, table, columns, changes)
}

func (s *Service) query(statement string, rowLimit int) (db.QueryResult, error) {
	ctx, conn, finish, err := s.begin(true, queryTimeout)
	if err != nil {
		return db.QueryResult{}, err
	}
	defer finish()
	return conn.Query(ctx, statement, rowLimit)
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
	if conn != nil {
		_ = conn.Close()
	}
	s.mu.Lock()
	s.disconnecting = false
	close(s.disconnectDone)
	s.disconnectDone = nil
	s.mu.Unlock()
	return nil
}
