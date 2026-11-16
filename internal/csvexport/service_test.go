package csvexport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCSV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.csv")
	content := "id,note\r\n9223372036854775807,\"雪, hello\""
	if err := writeCSV(path, content); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != content {
		t.Fatalf("content = %q, err = %v", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("new file permissions: info = %v, err = %v", info, err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if err := writeCSV(path, "replacement"); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement = %q, err = %v", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("replacement permissions: info = %v, err = %v", info, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files were not cleaned up: %v, %v", files, err)
	}
}

func TestWriteCSVFailureKeepsDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.csv")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeCSV(path, "replacement"); err == nil {
		t.Fatal("expected directory destination to fail")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || !files[0].IsDir() {
		t.Fatalf("failure changed destination or left temporary files: %v, %v", files, err)
	}
}

func TestCSVFilename(t *testing.T) {
	for input, want := range map[string]string{
		"/private/path/table.csv": "table.csv", "../table": "table.csv", "": "rows.csv",
		"/": "rows.csv", "table.CSV": "table.CSV", "name:with\x00control": "name-with-control.csv",
		strings.Repeat("a", 201): "rows.csv",
	} {
		if got := csvFilename(input); got != want {
			t.Errorf("csvFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSaveCSVRejectsOversizedPayloadBeforeDialog(t *testing.T) {
	saved, err := NewService().SaveCSV("rows.csv", strings.Repeat("x", maxCSVBytes+1))
	if saved || err == nil || !strings.Contains(err.Error(), "Select fewer rows") {
		t.Fatalf("saved = %v, err = %v", saved, err)
	}
}
