package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	_ "github.com/mattn/go-sqlite3"

	"tusk/internal/database"
	"tusk/internal/db"
	"tusk/internal/postgres"
	"tusk/internal/sqlite"
)

type SavedConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Mode     string `json:"mode"`
	SSLMode  string `json:"sslMode"`
	// Empty colors preserve the authenticated metadata of version 1 records.
	Color    string `json:"color,omitempty"`
	Provider string `json:"provider"`
	FilePath string `json:"filePath,omitempty"`
}

type SaveConnectionInput struct {
	Color   string               `json:"color"`
	Name    string               `json:"name"`
	Mode    string               `json:"mode"`
	URL     string               `json:"url"`
	Options db.ConnectionOptions `json:"options"`
}

type UpdateConnectionInput struct {
	Name            string               `json:"name"`
	Color           string               `json:"color"`
	Options         db.ConnectionOptions `json:"options"`
	URL             string               `json:"url"`
	ReplacePassword bool                 `json:"replacePassword"`
}

type connectionPayload struct {
	Mode     string               `json:"mode"`
	URL      string               `json:"url,omitempty"`
	Options  db.ConnectionOptions `json:"options,omitempty"`
	Password string               `json:"password,omitempty"`
}

type connectionKeyProvider func(create bool) ([]byte, error)

type ConnectionsService struct {
	mu       sync.Mutex
	database *database.Service
	path     string
	keys     connectionKeyProvider
	db       *sql.DB
	closed   bool
}

func NewConnectionsService(database *database.Service) *ConnectionsService {
	return newConnectionsService(database, "", func(create bool) ([]byte, error) {
		return loadKeychainConnectionKey("io.tusk.app.connections", "encryption-key-v1", create)
	})
}

func newConnectionsService(database *database.Service, path string, keys connectionKeyProvider) *ConnectionsService {
	return &ConnectionsService{database: database, path: path, keys: keys}
}

func (s *ConnectionsService) open() error {
	if s.closed {
		return errors.New("Saved connections are closed")
	}
	if s.db != nil {
		return nil
	}
	if s.path == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return errors.New("Could not locate the local settings folder")
		}
		s.path = filepath.Join(root, "Tusk", "connections.sqlite3")
	}
	absolute, err := filepath.Abs(s.path)
	if err != nil {
		return errors.New("Could not open saved connection storage")
	}
	s.path = absolute
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("Could not create the saved connections folder")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Saved connections require a private local folder")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return errors.New("Could not secure the saved connections folder")
	}
	if info, err := os.Lstat(s.path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Saved connections require a regular local database file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("Could not open saved connection storage")
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("Could not open saved connection storage")
	}
	err = file.Chmod(0600)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("Could not secure saved connection storage")
	}
	location := url.URL{Scheme: "file", Path: s.path}
	location.RawQuery = url.Values{
		"_busy_timeout": {"5000"}, "_journal_mode": {"DELETE"},
		"_secure_delete": {"on"}, "_txlock": {"immediate"},
	}.Encode()
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return errors.New("Could not open saved connection storage")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		db.Close()
		return errors.New("Could not initialize saved connection storage")
	}
	initialized := false
	defer func() {
		if !initialized {
			_ = tx.Rollback()
			_ = db.Close()
		}
	}()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("Could not read saved connection storage")
	}
	if version < 0 || version > 3 {
		return errors.New("Saved connections use an unsupported database version")
	}
	if version == 0 {
		var tables int
		if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil || tables != 0 {
			return errors.New("Saved connection storage has an unrecognized format")
		}
		if _, err := tx.Exec(`CREATE TABLE saved_connections (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, mode TEXT NOT NULL,
			host TEXT NOT NULL, port INTEGER NOT NULL, database_name TEXT NOT NULL,
			user_name TEXT NOT NULL, ssl_mode TEXT NOT NULL, encrypted_payload BLOB NOT NULL,
			color TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT 'postgres', file_path TEXT NOT NULL DEFAULT ''
		); PRAGMA user_version=3;`); err != nil {
			return errors.New("Could not initialize saved connection storage")
		}
	}
	if version == 1 {
		if _, err := tx.Exec("ALTER TABLE saved_connections ADD COLUMN color TEXT NOT NULL DEFAULT ''; PRAGMA user_version=2;"); err != nil {
			return errors.New("Could not upgrade saved connection storage")
		}
	}
	if version == 1 || version == 2 {
		if _, err := tx.Exec("ALTER TABLE saved_connections ADD COLUMN provider TEXT NOT NULL DEFAULT 'postgres'; ALTER TABLE saved_connections ADD COLUMN file_path TEXT NOT NULL DEFAULT ''; PRAGMA user_version=3;"); err != nil {
			return errors.New("Could not upgrade saved connection storage")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("Could not initialize saved connection storage")
	}
	initialized = true
	s.db = db
	return nil
}

func (s *ConnectionsService) ListConnections() ([]SavedConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, name, host, port, database_name, user_name, mode, ssl_mode, color, provider, file_path
		FROM saved_connections ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, errors.New("Could not list saved connections")
	}
	defer rows.Close()
	connections := make([]SavedConnection, 0)
	for rows.Next() {
		var saved SavedConnection
		if err := rows.Scan(&saved.ID, &saved.Name, &saved.Host, &saved.Port, &saved.Database, &saved.User, &saved.Mode, &saved.SSLMode, &saved.Color, &saved.Provider, &saved.FilePath); err != nil {
			return nil, errors.New("Could not read a saved connection")
		}
		connections = append(connections, saved)
	}
	if rows.Err() != nil {
		return nil, errors.New("Could not list saved connections")
	}
	return connections, nil
}

func (s *ConnectionsService) SaveConnection(input SaveConnectionInput) (SavedConnection, error) {
	saved, payload, err := validateSavedConnection(input)
	if err != nil {
		return SavedConnection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return SavedConnection{}, err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return SavedConnection{}, errors.New("Could not save the connection. Local storage may be busy")
	}
	defer tx.Rollback()
	var existing SavedConnection
	var existingPayload []byte
	err = tx.QueryRow(`SELECT id, name, host, port, database_name, user_name, mode, ssl_mode, color, provider, file_path, encrypted_payload
		FROM saved_connections LIMIT 1`).Scan(&existing.ID, &existing.Name, &existing.Host, &existing.Port, &existing.Database, &existing.User, &existing.Mode, &existing.SSLMode, &existing.Color, &existing.Provider, &existing.FilePath, &existingPayload)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SavedConnection{}, errors.New("Could not read saved connection storage")
	}
	existingFound := err == nil
	key, err := s.key(!existingFound)
	if err != nil {
		return SavedConnection{}, err
	}
	if existingFound {
		if _, err := decryptConnection(key, existing, existingPayload); err != nil {
			return SavedConnection{}, err
		}
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return SavedConnection{}, errors.New("Could not generate a saved connection ID")
	}
	saved.ID = hex.EncodeToString(id)
	encrypted, err := encryptConnection(key, saved, payload)
	if err != nil {
		return SavedConnection{}, err
	}
	if _, err := tx.Exec(`INSERT INTO saved_connections
		(id, name, mode, host, port, database_name, user_name, ssl_mode, color, provider, file_path, encrypted_payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, saved.ID, saved.Name, saved.Mode, saved.Host, saved.Port, saved.Database, saved.User, saved.SSLMode, saved.Color, saved.Provider, saved.FilePath, encrypted); err != nil {
		return SavedConnection{}, errors.New("Could not save the connection")
	}
	if err := tx.Commit(); err != nil {
		return SavedConnection{}, errors.New("Could not commit the saved connection")
	}
	return saved, nil
}

func validateSavedConnection(input SaveConnectionInput) (SavedConnection, connectionPayload, error) {
	var saved SavedConnection
	var payload connectionPayload
	name, color, err := savedConnectionIdentity(input.Name, input.Color)
	if err != nil {
		return saved, payload, err
	}
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	provider := strings.ToLower(strings.TrimSpace(input.Options.Provider))
	if provider == "sqlite" {
		if mode != "file" {
			return saved, payload, errors.New("Choose a SQLite database file")
		}
		path, err := sqlite.ValidatePath(input.Options.FilePath)
		if err != nil {
			return saved, payload, err
		}
		options := db.ConnectionOptions{Provider: "sqlite", FilePath: path}
		saved = SavedConnection{Name: name, Color: color, Mode: "file", Provider: "sqlite", FilePath: path, Database: filepath.Base(path)}
		return saved, connectionPayload{Mode: "file", Options: options}, nil
	}
	if provider != "" && provider != "postgres" {
		return saved, payload, errors.New("Choose a supported database provider")
	}
	input.Options.Provider, input.Options.FilePath = "postgres", ""
	var connectionURL string
	switch mode {
	case "fields":
		var err error
		connectionURL, err = postgres.ConnectionURLForOptions(input.Options)
		if err != nil {
			return saved, payload, err
		}
		payload = connectionPayload{Mode: mode, Options: input.Options}
	case "url":
		connectionURL = strings.TrimSpace(input.URL)
		if strings.HasPrefix(connectionURL, "jdbc:postgresql://") || strings.HasPrefix(connectionURL, "jdbc:postgres://") {
			connectionURL = strings.TrimPrefix(connectionURL, "jdbc:")
		}
		payload = connectionPayload{Mode: mode, URL: connectionURL}
	default:
		return saved, payload, errors.New("Choose connection fields or a PostgreSQL URL")
	}
	if len(connectionURL) > 1<<20 {
		return saved, payload, errors.New("Connection settings must be smaller than 1 MiB")
	}
	parsed, err := url.Parse(connectionURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return saved, payload, errors.New("Enter a valid PostgreSQL connection URL")
	}
	sslMode := savedURLTLSMode(parsed.RawQuery)
	if mode == "url" && sslMode == "" {
		return saved, payload, errors.New("Saved URL connections require an explicit TLS mode, for example ?sslmode=prefer")
	}
	config, err := pgx.ParseConfig(connectionURL)
	if err != nil {
		return saved, payload, errors.New("Invalid connection settings. Check the host, port, credentials, and TLS options")
	}
	if mode == "url" {
		payload.Password = config.Password
	}
	if sslMode == "" {
		sslMode = "prefer"
	}
	saved = SavedConnection{Name: name, Provider: "postgres", Mode: mode, Host: config.Host, Port: int(config.Port), Database: config.Database, User: config.User, SSLMode: sslMode, Color: color}
	return saved, payload, nil
}

func savedConnectionIdentity(name, color string) (string, string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 100 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", "", errors.New("Connection names must contain 1 to 100 characters without control characters")
	}
	color = strings.ToLower(strings.TrimSpace(color))
	if color != "" {
		if len(color) != 7 || color[0] != '#' {
			return "", "", errors.New("Choose a valid connection color")
		}
		if _, err := hex.DecodeString(color[1:]); err != nil {
			return "", "", errors.New("Choose a valid connection color")
		}
	}
	return name, color, nil
}

// Updates authenticate the existing record and replace it atomically, retaining its ID.
func (s *ConnectionsService) UpdateConnection(id string, input UpdateConnectionInput) (SavedConnection, error) {
	if !validSavedConnectionID(id) {
		return SavedConnection{}, errors.New("Choose a valid saved connection")
	}
	name, color, err := savedConnectionIdentity(input.Name, input.Color)
	if err != nil {
		return SavedConnection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return SavedConnection{}, err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return SavedConnection{}, errors.New("Could not update the connection. Local storage may be busy")
	}
	defer tx.Rollback()
	var saved SavedConnection
	var encrypted []byte
	if err := tx.QueryRow(`SELECT id, name, host, port, database_name, user_name, mode, ssl_mode, color, provider, file_path, encrypted_payload
		FROM saved_connections WHERE id = ?`, id).Scan(&saved.ID, &saved.Name, &saved.Host, &saved.Port, &saved.Database, &saved.User, &saved.Mode, &saved.SSLMode, &saved.Color, &saved.Provider, &saved.FilePath, &encrypted); err != nil {
		return SavedConnection{}, errors.New("Saved connection was not found or could not be read")
	}
	key, err := s.key(false)
	if err != nil {
		return SavedConnection{}, err
	}
	payload, err := decryptConnection(key, saved, encrypted)
	if err != nil {
		return SavedConnection{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(input.Options.Provider))
	if provider != "" && provider != saved.Provider {
		return SavedConnection{}, errors.New("A saved connection's database provider cannot be changed. Create a new connection instead")
	}
	if saved.Mode == "fields" || saved.Mode == "file" || strings.TrimSpace(input.URL) != "" {
		options := input.Options
		options.Provider = saved.Provider
		if saved.Mode == "fields" && !input.ReplacePassword {
			options.Password = payload.Options.Password
		}
		saved, payload, err = validateSavedConnection(SaveConnectionInput{Name: name, Color: color, Mode: saved.Mode, URL: input.URL, Options: options})
		if err != nil {
			return SavedConnection{}, err
		}
		saved.ID = id
	} else {
		saved.Name, saved.Color = name, color
	}
	encrypted, err = encryptConnection(key, saved, payload)
	if err != nil {
		return SavedConnection{}, err
	}
	if _, err := tx.Exec(`UPDATE saved_connections SET name=?, mode=?, host=?, port=?, database_name=?, user_name=?, ssl_mode=?, color=?, provider=?, file_path=?, encrypted_payload=? WHERE id=?`,
		saved.Name, saved.Mode, saved.Host, saved.Port, saved.Database, saved.User, saved.SSLMode, saved.Color, saved.Provider, saved.FilePath, encrypted, id); err != nil {
		return SavedConnection{}, errors.New("Could not update the saved connection")
	}
	if err := tx.Commit(); err != nil {
		return SavedConnection{}, errors.New("Could not commit the updated connection")
	}
	return saved, nil
}

func savedURLTLSMode(query string) string {
	mode := ""
	ssl, sslWasLast := "", false
	// pgx uses the final ssl/sslmode parameter, including the ssl=true alias.
	for _, parameter := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(parameter, "=")
		key, _ = url.PathUnescape(key)
		if key == "sslmode" || key == "ssl" {
			value, _ = url.PathUnescape(value)
			if key == "sslmode" {
				mode = value
				sslWasLast = false
			} else {
				ssl, sslWasLast = value, true
			}
		}
	}
	if sslWasLast && ssl == "true" {
		mode = "require"
	}
	return mode
}

func (s *ConnectionsService) key(create bool) ([]byte, error) {
	key, err := s.keys(create)
	if err != nil || len(key) != 32 {
		return nil, errors.New("The saved connection encryption key is unavailable. Unlock macOS Keychain; existing saved connections require their original key")
	}
	return key, nil
}

func connectionCipher(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("The saved connection encryption key is invalid")
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func encryptConnection(key []byte, saved SavedConnection, payload connectionPayload) ([]byte, error) {
	plain, err := json.Marshal(payload)
	if err != nil || len(plain) > 1<<20 {
		return nil, errors.New("Connection settings must be smaller than 1 MiB")
	}
	defer clear(plain)
	aead, err := connectionCipher(key)
	if err != nil {
		return nil, err
	}
	aad, _ := json.Marshal(saved)
	return append([]byte{1}, aead.Seal(nil, nil, plain, append([]byte("tusk.connection.v1:"), aad...))...), nil
}

func decryptConnection(key []byte, saved SavedConnection, encrypted []byte) (connectionPayload, error) {
	var payload connectionPayload
	aead, err := connectionCipher(key)
	if err != nil {
		return payload, err
	}
	if len(encrypted) < 1+aead.Overhead() || encrypted[0] != 1 || len(encrypted) > (1<<20)+1+aead.Overhead() {
		return payload, errors.New("Saved connection data is damaged or uses an unsupported encryption format")
	}
	aad, _ := json.Marshal(saved)
	plain, err := aead.Open(nil, nil, encrypted[1:], append([]byte("tusk.connection.v1:"), aad...))
	if err != nil {
		return payload, errors.New("Saved connection could not be decrypted. Its encryption key is different or the data was changed")
	}
	defer clear(plain)
	if json.Unmarshal(plain, &payload) != nil || (payload.Mode != "fields" && payload.Mode != "url" && payload.Mode != "file") {
		return connectionPayload{}, errors.New("Saved connection data has an invalid format")
	}
	return payload, nil
}

func validSavedConnectionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *ConnectionsService) ConnectSaved(id string) (db.ConnectionInfo, error) {
	if !validSavedConnectionID(id) {
		return db.ConnectionInfo{}, errors.New("Choose a valid saved connection")
	}
	s.mu.Lock()
	if err := s.open(); err != nil {
		s.mu.Unlock()
		return db.ConnectionInfo{}, err
	}
	var saved SavedConnection
	var encrypted []byte
	err := s.db.QueryRow(`SELECT id, name, host, port, database_name, user_name, mode, ssl_mode, color, provider, file_path, encrypted_payload
		FROM saved_connections WHERE id = ?`, id).Scan(&saved.ID, &saved.Name, &saved.Host, &saved.Port, &saved.Database, &saved.User, &saved.Mode, &saved.SSLMode, &saved.Color, &saved.Provider, &saved.FilePath, &encrypted)
	if err != nil {
		s.mu.Unlock()
		return db.ConnectionInfo{}, errors.New("Saved connection was not found or could not be read")
	}
	key, err := s.key(false)
	if err != nil {
		s.mu.Unlock()
		return db.ConnectionInfo{}, err
	}
	payload, err := decryptConnection(key, saved, encrypted)
	s.mu.Unlock()
	if err != nil {
		return db.ConnectionInfo{}, err
	}
	if payload.Mode == "fields" || payload.Mode == "file" {
		return s.database.ConnectWithOptions(payload.Options)
	}
	config, err := pgx.ParseConfig(payload.URL)
	if err != nil {
		return db.ConnectionInfo{}, errors.New("Saved URL settings could not be resolved. Check its TLS files and connection options")
	}
	if config.Host != saved.Host || int(config.Port) != saved.Port || config.Database != saved.Database || config.User != saved.User {
		return db.ConnectionInfo{}, errors.New("Saved URL resolves to different connection settings. Save it again with an explicit host, port, username, and database")
	}
	return database.ConnectSavedURL(s.database, payload.URL, payload.Password)
}

func (s *ConnectionsService) DeleteConnection(id string) error {
	if !validSavedConnectionID(id) {
		return errors.New("Choose a valid saved connection")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return err
	}
	result, err := s.db.Exec("DELETE FROM saved_connections WHERE id = ?", id)
	if err != nil {
		return errors.New("Could not delete the saved connection")
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return errors.New("Saved connection was not found")
	}
	return nil
}

func (s *ConnectionsService) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
}
