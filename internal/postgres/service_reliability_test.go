package postgres

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgreSQLReliability(t *testing.T) {
	dsn := os.Getenv("TUSK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TUSK_TEST_DATABASE_URL to run real PostgreSQL reliability checks")
	}
	admin, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal("cannot connect to integration database")
	}
	defer admin.Close(context.Background())

	connect := func(t *testing.T) *Service {
		t.Helper()
		s := NewService()
		t.Cleanup(s.Shutdown)
		if _, err := s.Connect(dsn); err != nil {
			t.Fatal(err)
		}
		return s
	}
	waitActive := func(t *testing.T, pid uint32) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for {
			var active bool
			err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND state='active')", pid).Scan(&active)
			if err != nil {
				t.Fatal("query did not become active on PostgreSQL")
			}
			if active {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	t.Run("failed reconnect keeps current session", func(t *testing.T) {
		s := connect(t)
		bad, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("integration URL must use PostgreSQL URL syntax")
		}
		bad.Path = "/tusk_nonexistent_database_for_reconnect_check"
		if _, err := s.Connect(bad.String()); err == nil {
			t.Fatal("connected to nonexistent database")
		}
		if _, err := s.RunQuery("SELECT 42"); err != nil {
			t.Fatalf("failed reconnect discarded working session: %v", err)
		}
	})

	t.Run("cancel reaches server during result drain", func(t *testing.T) {
		s := connect(t)
		pid := backendPID(t, s)
		done := make(chan error, 1)
		go func() {
			_, err := s.RunQuery("SELECT repeat('x', 1048576), pg_sleep(0.01) FROM generate_series(1, 100000)")
			done <- err
		}()
		waitActive(t, pid)
		// More than the display byte limit is emitted before cancellation.
		time.Sleep(250 * time.Millisecond)
		if err := s.CancelQuery(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "canceled") {
				t.Fatalf("expected cancellation, got %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancel did not interrupt PostgreSQL result drain")
		}
		var stillActive bool
		if err := admin.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND state='active')", pid).Scan(&stillActive); err != nil || stillActive {
			t.Fatalf("canceled query still running on server: active=%v err=%v", stillActive, err)
		}
		if _, err := s.RunQuery("SELECT 42"); err != nil {
			t.Fatalf("session unusable after cancel: %v", err)
		}
	})

	for _, shutdown := range []bool{false, true} {
		name := "disconnect stops active server query"
		if shutdown {
			name = "shutdown stops active server query"
		}
		t.Run(name, func(t *testing.T) {
			s := connect(t)
			pid := backendPID(t, s)
			done := make(chan error, 1)
			go func() { _, err := s.RunQuery("SELECT pg_sleep(30)"); done <- err }()
			waitActive(t, pid)
			if shutdown {
				s.Shutdown()
			} else if err := s.Disconnect(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("active query was not interrupted")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("disconnect did not finish active query")
			}
			if _, err := s.RunQuery("SELECT 1"); err == nil {
				t.Fatal("query accepted after disconnect")
			}
			if !shutdown {
				if _, err := s.Connect(dsn); err != nil {
					t.Fatalf("cannot reconnect after disconnect: %v", err)
				}
				if _, err := s.RunQuery("SELECT 42"); err != nil {
					t.Fatalf("reconnected session unusable: %v", err)
				}
			}
		})
	}
}
