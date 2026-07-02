package slither

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildReportDefaultsMaxBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	fixturePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(fixturePath, []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := BuildReport(context.Background(), Options{Repo: dir, Top: 10})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	if len(report.Rows) < 1 {
		t.Fatalf("expected at least 1 row when MaxBytes defaults, got %d", len(report.Rows))
	}
}
