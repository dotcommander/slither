package slither

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectFilesSkipsVanishedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	realPath := filepath.Join(dir, "real.go")
	if err := os.WriteFile(realPath, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	vanishedPath := filepath.Join(dir, "vanished.go")
	paths := []string{realPath, vanishedPath}

	rows, skipped, err := inspectFiles(context.Background(), dir, paths, 1<<20, scoreContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if skipped != 1 {
		t.Fatalf("expected skip count 1, got %d", skipped)
	}
}
