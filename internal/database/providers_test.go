package database

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tusk/internal/db"
)

type testProvider struct {
	open func(context.Context, db.ConnectionConfig) (db.Store, db.ConnectionInfo, error)
}

func (p testProvider) Open(ctx context.Context, config db.ConnectionConfig) (db.Store, db.ConnectionInfo, error) {
	return p.open(ctx, config)
}

type testStore struct {
	closed atomic.Bool
	query  func(context.Context, string, int) (db.QueryResult, error)
}

func (s *testStore) ListSchemas(context.Context) ([]string, error)           { return []string{"main"}, nil }
func (s *testStore) ListTables(context.Context) ([]db.TableInfo, error)      { return nil, nil }
func (s *testStore) ListColumns(context.Context) ([]db.CatalogColumn, error) { return nil, nil }
func (s *testStore) Close() error                                            { s.closed.Store(true); return nil }
func (s *testStore) Query(ctx context.Context, query string, limit int) (db.QueryResult, error) {
	if s.query != nil {
		return s.query(ctx, query, limit)
	}
	return db.QueryResult{CommandTag: query}, nil
}

func TestProviderSwitchPreservesOldSessionUntilSuccess(t *testing.T) {
	first, second := &testStore{}, &testStore{}
	s := NewService(map[string]db.Provider{
		"postgres": testProvider{func(context.Context, db.ConnectionConfig) (db.Store, db.ConnectionInfo, error) {
			return first, db.ConnectionInfo{Provider: "postgres"}, nil
		}},
		"sqlite": testProvider{func(_ context.Context, config db.ConnectionConfig) (db.Store, db.ConnectionInfo, error) {
			if config.Options.FilePath == "bad" {
				return nil, db.ConnectionInfo{}, errors.New("cannot open")
			}
			return second, db.ConnectionInfo{Provider: "sqlite"}, nil
		}},
	})
	t.Cleanup(s.Shutdown)
	if _, err := s.Connect("postgres://example"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConnectWithOptions(db.ConnectionOptions{Provider: "sqlite", FilePath: "bad"}); err == nil {
		t.Fatal("bad connection accepted")
	}
	if first.closed.Load() {
		t.Fatal("failed switch closed old session")
	}
	if _, err := s.RunQuery("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConnectWithOptions(db.ConnectionOptions{Provider: "sqlite", FilePath: "good"}); err != nil {
		t.Fatal(err)
	}
	if !first.closed.Load() || second.closed.Load() {
		t.Fatal("successful switch did not replace session")
	}
	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if !second.closed.Load() {
		t.Fatal("disconnect did not close active provider")
	}
	if _, err := s.RunQuery("SELECT 1"); err == nil {
		t.Fatal("query accepted without connection")
	}
}

func TestDisconnectCancelsAndWaitsForProviderWork(t *testing.T) {
	started := make(chan struct{})
	active := &testStore{query: func(ctx context.Context, _ string, _ int) (db.QueryResult, error) {
		close(started)
		<-ctx.Done()
		return db.QueryResult{}, ctx.Err()
	}}
	s := NewService(map[string]db.Provider{"sqlite": testProvider{func(context.Context, db.ConnectionConfig) (db.Store, db.ConnectionInfo, error) {
		return active, db.ConnectionInfo{Provider: "sqlite"}, nil
	}}})
	t.Cleanup(s.Shutdown)
	if _, err := s.ConnectWithOptions(db.ConnectionOptions{Provider: "sqlite"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.RunQuery("long query"); done <- err }()
	<-started
	if _, err := s.ConnectWithOptions(db.ConnectionOptions{Provider: "sqlite"}); err == nil {
		t.Fatal("switch allowed during query")
	}
	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("query not canceled: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect left query running")
	}
	if !active.closed.Load() {
		t.Fatal("disconnect did not close provider")
	}
}

func TestSharedBrowsingQuotesIdentifiersAndBoundsResults(t *testing.T) {
	var statement string
	var rowLimit int
	active := &testStore{query: func(_ context.Context, query string, limit int) (db.QueryResult, error) {
		statement, rowLimit = query, limit
		return db.QueryResult{}, nil
	}}
	s := NewService(map[string]db.Provider{"sqlite": testProvider{func(context.Context, db.ConnectionConfig) (db.Store, db.ConnectionInfo, error) {
		return active, db.ConnectionInfo{}, nil
	}}})
	t.Cleanup(s.Shutdown)
	if _, err := s.ConnectWithOptions(db.ConnectionOptions{Provider: "sqlite"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BrowseTableSorted(`main`, `odd "table`, `a"b`, true); err != nil {
		t.Fatal(err)
	}
	if statement != `SELECT * FROM "main"."odd ""table" ORDER BY "a""b" DESC NULLS LAST LIMIT 100` || rowLimit != 100 {
		t.Fatalf("unsafe or unbounded browse: %s (%d)", statement, rowLimit)
	}
	if _, err := s.RunQuery("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if rowLimit != db.QueryRowLimit {
		t.Fatal("SQL result row limit changed")
	}
	if _, err := s.RunQuery(strings.Repeat("x", 1<<20+1)); err == nil {
		t.Fatal("oversized SQL accepted")
	}
}
