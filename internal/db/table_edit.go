package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type TableEditInfo struct {
	KeyColumns      []string `json:"keyColumns"`
	ReadOnlyColumns []string `json:"readOnlyColumns"`
	Reason          string   `json:"reason"`
}

type RowChange struct {
	Original []*string          `json:"original"`
	Values   map[string]*string `json:"values"`
	Delete   bool               `json:"delete"`
}

// TableEditor is optional so query-only stores can remain read-only.
type TableEditor interface {
	TableEditInfo(context.Context, string, string) (TableEditInfo, error)
	SaveTableChanges(context.Context, string, string, []string, []RowChange) (int, error)
}

func ValidateChanges(columns []string, changes []RowChange) error {
	if len(columns) == 0 || len(changes) == 0 || len(changes) > QueryRowLimit {
		return errors.New("Choose between 1 and 1000 rows to save")
	}
	known := make(map[string]bool, len(columns))
	for _, column := range columns {
		if column == "" || strings.ContainsRune(column, '\x00') || known[column] {
			return errors.New("The table columns changed. Refresh the table and try again")
		}
		known[column] = true
	}
	for _, change := range changes {
		if len(change.Original) != len(columns) || change.Delete && len(change.Values) != 0 || !change.Delete && len(change.Values) == 0 {
			return errors.New("A row change is incomplete. Undo changes and try again")
		}
		for name := range change.Values {
			if !known[name] {
				return errors.New("The table columns changed. Refresh the table and try again")
			}
		}
	}
	payload, err := json.Marshal(struct {
		Columns []string
		Changes []RowChange
	}{columns, changes})
	if err != nil || len(payload) > 10<<20 {
		return errors.New("Changes exceed 10 MiB. Save fewer rows at a time")
	}
	return nil
}
