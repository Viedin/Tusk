package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"tusk/internal/db"
)

func TestResultValuesPreserveNullEmptyAndPrecision(t *testing.T) {
	result := QueryResult{Rows: [][]*string{}}
	bytes := 0
	values := [][]byte{nil, {}, []byte("9223372036854775807"), []byte("12345678901234567890.12345678901234567890")}
	if !appendResultRow(&result, values, &bytes, 1024) {
		t.Fatal("row unexpectedly exceeds limit")
	}
	encoded, err := json.Marshal(result.Rows)
	if err != nil {
		t.Fatal(err)
	}
	want := `[[null,"","9223372036854775807","12345678901234567890.12345678901234567890"]]`
	if string(encoded) != want {
		t.Fatalf("got %s, want %s", encoded, want)
	}
}

func TestResultByteLimitIncludesJSONEscaping(t *testing.T) {
	result := QueryResult{Rows: [][]*string{}}
	bytes := 0
	if appendResultRow(&result, [][]byte{[]byte(strings.Repeat("\x00", 10))}, &bytes, 30) {
		t.Fatal("escaped JSON exceeds byte limit")
	}
	if bytes != 0 || len(result.Rows) != 0 {
		t.Fatal("rejected row changed result")
	}
}

func TestValidationDoesNotExposeCredentials(t *testing.T) {
	s := NewService()
	for _, raw := range []string{"password=secret", "postgres://user:secret@localhost:invalid/db"} {
		_, err := s.Connect(raw)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe URL error: %v", err)
		}
	}
	if _, err := s.RunQuery(" \n"); err == nil {
		t.Fatal("empty SQL accepted")
	}
	if _, err := s.BrowseTable("public", "bad\x00name"); err == nil {
		t.Fatal("NUL identifier accepted")
	}
}

func TestStructuredConnectionURLPreservesValues(t *testing.T) {
	options := ConnectionOptions{
		Host: "localhost", Port: 5432,
		Database: " sales/db?x=1#&%雪 ", User: " user@name:/?&#%雪 ",
		Password: " \tpass:@/?&#%+雪\n ", SSLMode: "disable",
	}
	connectionURL, err := ConnectionURLForOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(connectionURL)
	if err != nil {
		t.Fatal("constructed connection URL did not parse")
	}
	if config.Database != options.Database || config.User != options.User || config.Password != options.Password {
		t.Fatal("special characters or whitespace changed during URL encoding")
	}
	if config.Host != "localhost" || config.Port != 5432 || config.TLSConfig != nil {
		t.Fatal("host, port, or TLS mode changed during URL encoding")
	}
}

func TestStructuredConnectionIPv6AndTLS(t *testing.T) {
	for _, host := range []string{"::1", "[::1]", "fe80::1%en0", "[fe80::1%en0]"} {
		connectionURL, err := ConnectionURLForOptions(ConnectionOptions{Host: host, Port: 5432, Database: "db", User: "user", SSLMode: "disable"})
		if err != nil {
			t.Fatalf("valid IPv6 host rejected: %v", err)
		}
		config, err := pgx.ParseConfig(connectionURL)
		if err != nil || config.Host != strings.Trim(host, "[]") {
			t.Fatal("IPv6 host changed during URL encoding")
		}
	}
	for _, mode := range []string{"", "disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		connectionURL, err := ConnectionURLForOptions(ConnectionOptions{Host: "localhost", Port: 5432, Database: "db", User: "user", SSLMode: mode})
		if err != nil {
			t.Fatalf("valid TLS mode rejected: %v", err)
		}
		if mode == "" && !strings.Contains(connectionURL, "sslmode=prefer") {
			t.Fatal("default TLS mode is not prefer")
		}
	}
}

func TestStructuredConnectionValidation(t *testing.T) {
	valid := ConnectionOptions{Host: "localhost", Port: 5432, Database: "db", User: "user", Password: "never-print-this-password"}
	cases := map[string]func(*ConnectionOptions){
		"missing host":     func(o *ConnectionOptions) { o.Host = " " },
		"missing user":     func(o *ConnectionOptions) { o.User = "\t" },
		"missing database": func(o *ConnectionOptions) { o.Database = "\n" },
		"zero port":        func(o *ConnectionOptions) { o.Port = 0 },
		"negative port":    func(o *ConnectionOptions) { o.Port = -1 },
		"large port":       func(o *ConnectionOptions) { o.Port = 65536 },
		"invalid TLS":      func(o *ConnectionOptions) { o.SSLMode = "invented" },
		"URL host":         func(o *ConnectionOptions) { o.Host = "postgres://localhost" },
		"host with port":   func(o *ConnectionOptions) { o.Host = "localhost:5432" },
		"host authority":   func(o *ConnectionOptions) { o.Host = "localhost@elsewhere" },
		"host query":       func(o *ConnectionOptions) { o.Host = "localhost?sslmode=disable" },
		"host comma":       func(o *ConnectionOptions) { o.Host = "localhost,elsewhere" },
		"host control":     func(o *ConnectionOptions) { o.Host = "local\bhost" },
		"unclosed IPv6":    func(o *ConnectionOptions) { o.Host = "[::1" },
		"host NUL":         func(o *ConnectionOptions) { o.Host += "\x00" },
		"user NUL":         func(o *ConnectionOptions) { o.User += "\x00" },
		"database NUL":     func(o *ConnectionOptions) { o.Database += "\x00" },
		"password NUL":     func(o *ConnectionOptions) { o.Password += "\x00" },
		"TLS NUL":          func(o *ConnectionOptions) { o.SSLMode += "\x00" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			options := valid
			mutate(&options)
			_, err := NewService().ConnectWithOptions(options)
			if err == nil {
				t.Fatal("invalid options accepted")
			}
			if strings.Contains(err.Error(), valid.Password) {
				t.Fatal("validation error exposed password")
			}
		})
	}
}

func TestJDBCPostgreSQLURLNormalization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, connectionURL := range []string{"jdbc:postgresql://user:secret@localhost/db?sslmode=disable", "jdbc:postgres://user:secret@localhost/db?sslmode=disable"} {
		_, _, err := NewProvider().Open(ctx, db.ConnectionConfig{URL: connectionURL})
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("JDBC PostgreSQL URL failed validation: %v", err)
		}
	}
	if _, _, err := NewProvider().Open(ctx, db.ConnectionConfig{URL: "jdbc:mysql://user:secret@localhost/db"}); err == nil || strings.Contains(err.Error(), "canceled") {
		t.Fatal("accepted a JDBC URL for another database engine")
	}
}
