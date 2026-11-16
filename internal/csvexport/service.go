package csvexport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const maxCSVBytes = 10 << 20

type Service struct{}

func NewService() *Service { return &Service{} }

func (s *Service) SaveCSV(filename, content string) (bool, error) {
	if len(content) > maxCSVBytes {
		return false, errors.New("CSV export is too large. Select fewer rows and try again")
	}
	app := application.Get()
	if app == nil {
		return false, errors.New("The save dialog is unavailable. Open Tusk and try again")
	}
	destination, err := app.Dialog.SaveFile().
		SetFilename(csvFilename(filename)).
		AddFilter("CSV files", "*.csv").
		PromptForSingleSelection()
	if err != nil {
		return false, fmt.Errorf("Could not open the save dialog: %w", err)
	}
	if destination == "" {
		return false, nil
	}
	if err := writeCSV(destination, content); err != nil {
		return false, fmt.Errorf("Could not save the CSV file. Choose a writable location and try again: %w", err)
	}
	return true, nil
}

func csvFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == ':' {
			return '-'
		}
		return r
	}, filepath.Base(name))
	if name == "." || name == string(filepath.Separator) || name == "" || len(name) > 200 {
		name = "rows"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".csv") {
		name += ".csv"
	}
	return name
}

func writeCSV(destination, content string) error {
	mode := os.FileMode(0600)
	if info, err := os.Stat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("Choose a file rather than a directory")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".tusk-export-*.csv")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}
