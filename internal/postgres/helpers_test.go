package postgres

import (
	"strconv"
	"testing"
	"tusk/internal/database"
	"tusk/internal/db"
)

type Service = database.Service

const queryRowLimit = db.QueryRowLimit

func NewService() *database.Service {
	return database.NewService(map[string]db.Provider{"postgres": NewProvider()})
}
func appendResultRow(result *QueryResult, values [][]byte, bytes *int, limit int) bool {
	return db.AppendResultRow(result, values, bytes, limit)
}
func backendPID(t *testing.T, s *Service) uint32 {
	t.Helper()
	result, err := s.RunQuery("SELECT pg_backend_pid()")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.ParseUint(*result.Rows[0][0], 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(pid)
}
