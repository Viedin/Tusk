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

func TestPostgreSQLFieldLoginSpecialCredentials(t *testing.T) {
	dsn := os.Getenv("TUSK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TUSK_TEST_DATABASE_URL to run real PostgreSQL field-login checks")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid integration connection configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to integration database")
	}
	defer admin.Close(context.Background())
	var canCreateRole bool
	if err := admin.QueryRow(ctx, "SELECT rolcreaterole OR rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&canCreateRole); err != nil {
		t.Fatal(err)
	}
	if !canCreateRole {
		t.Skip("special-credential authentication check requires CREATEROLE on a disposable PostgreSQL server")
	}

	role := fmt.Sprintf(" tusk login %d@ ' ", time.Now().UnixNano())
	password := "  p@ss:/?#%&+=\\'\"secret  "
	var quotedPassword string
	if err := admin.QueryRow(ctx, "SELECT quote_literal($1::text)", password).Scan(&quotedPassword); err != nil {
		t.Fatal("could not quote synthetic test password")
	}
	if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD "+quotedPassword); err != nil {
		t.Fatal("could not create isolated authentication test role")
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			t.Error("could not remove isolated authentication test role")
		}
	}()

	s := NewService()
	defer s.Shutdown()
	options := ConnectionOptions{
		Host: config.Host, Port: int(config.Port), Database: config.Database,
		User: role, Password: password, SSLMode: "disable",
	}
	info, err := s.ConnectWithOptions(options)
	if err != nil {
		t.Fatalf("field login changed special characters or surrounding whitespace: %v", err)
	}
	if info.User != role || info.Database != config.Database {
		t.Fatal("field login did not preserve account and database identity")
	}
	if _, err := s.RunQuery("SELECT 42"); err != nil {
		t.Fatal("field-login session is unusable")
	}

	options.Password = strings.TrimSpace(password)
	if _, err := s.ConnectWithOptions(options); err == nil {
		t.Fatal("trimmed password unexpectedly authenticated")
	} else if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), role) || strings.Contains(err.Error(), "postgresql://") {
		t.Fatal("authentication error exposed connection credentials")
	}
	if _, err := s.RunQuery("SELECT 43"); err != nil {
		t.Fatal("failed field login discarded the working session")
	}

	t.Setenv("PGPASSWORD", password)
	options.Password = ""
	if _, err := s.ConnectWithOptions(options); err == nil {
		t.Fatal("blank field password silently used PGPASSWORD")
	}
}
