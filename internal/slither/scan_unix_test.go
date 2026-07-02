//go:build unix

package slither

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestScanSkipsIrregularEntries(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "auth.go"), []byte("package p\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fifoPath := filepath.Join(repo, "pipe.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(repo, "escape.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "nonexistent.go"), filepath.Join(repo, "dangling.go")); err != nil {
		t.Fatal(err)
	}

	report, err := BuildReport(context.Background(), Options{Repo: repo, Top: 10, MaxBytes: 500_000, Days: 90})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d; rows=%#v", len(report.Rows), report.Rows)
	}
	if report.Rows[0].Path != "auth.go" {
		t.Fatalf("top row = %q, want auth.go", report.Rows[0].Path)
	}

	found := false
	for _, signal := range report.SkippedSignals {
		if strings.HasPrefix(signal, "filesystem_walk:irregular_skipped:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("skipped signals = %#v, want filesystem_walk:irregular_skipped signal", report.SkippedSignals)
	}
}
