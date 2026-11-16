// Package db defines the connection and query contract shared by database providers.
package db

import (
	"context"
	"encoding/json"
	"strings"
)

const (
	QueryRowLimit   = 1000
	ResultByteLimit = 5 << 20
)

type ConnectionInfo struct {
	Provider string `json:"provider"`
	FilePath string `json:"filePath,omitempty"`
	Database string `json:"database"`
	User     string `json:"user"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
}

type ConnectionOptions struct {
	Provider string `json:"provider,omitempty"`
	FilePath string `json:"filePath,omitempty"`
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
	Columns                  []ColumnInfo `json:"columns"`
	Rows                     [][]*string  `json:"rows"`
	RowCount                 int          `json:"rowCount"`
	RowsAffected             string       `json:"rowsAffected"`
	DurationMs               int64        `json:"durationMs"`
	Truncated                bool         `json:"truncated"`
	CommandTag               string       `json:"commandTag"`
	DeletionTarget           *RowTarget   `json:"deletionTarget,omitempty"`
	RemovalUnavailableReason string       `json:"removalUnavailableReason"`
}

type RowTarget struct {
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// ConnectionConfig carries form settings or a URL; Password overrides URL defaults when saved.
type ConnectionConfig struct {
	Options  ConnectionOptions
	URL      string
	Password *string
}

// Provider opens a session without changing the app's current connection on failure.
type Provider interface {
	Open(context.Context, ConnectionConfig) (Store, ConnectionInfo, error)
}

// Store is one database session. The application serializes operations and owns cancellation.
type Store interface {
	ListSchemas(context.Context) ([]string, error)
	ListTables(context.Context) ([]TableInfo, error)
	ListColumns(context.Context) ([]CatalogColumn, error)
	Query(context.Context, string, int) (QueryResult, error)
	Close() error
}

func QuoteIdentifier(name string) string { return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\"" }

func AppendResultRow(result *QueryResult, values [][]byte, byteCount *int, byteLimit int) bool {
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
