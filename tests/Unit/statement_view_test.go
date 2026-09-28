package unit_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/arandu-io/framework/foundation"
)

func TestStatementPublicationUsesNativeDataTable(t *testing.T) {
	publications, err := foundation.Publications(module(t))
	if err != nil {
		t.Fatal(err)
	}
	var source string
	for _, publication := range publications {
		err = fs.WalkDir(publication.Files, publication.From, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, "statement.kyse.go") {
				return walkErr
			}
			raw, readErr := fs.ReadFile(publication.Files, path)
			if readErr != nil {
				return readErr
			}
			source = string(raw)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if source == "" {
		t.Fatal("statement published view not found")
	}
	for _, want := range []string{"components.DataTable(statementTable(.))", "ID: \"wallet-statement\"", "control.columns"} {
		if !strings.Contains(source, want) {
			t.Errorf("statement view does not contain %q", want)
		}
	}
	if strings.Contains(source, "<table") {
		t.Error("statement view bypasses Kyse with a raw HTML table")
	}
	if strings.Contains(source, ")@endif") || strings.Contains(source, ") selected @endif") {
		t.Error("statement view carries an inline template directive instead of structural Kyse markup")
	}
}
