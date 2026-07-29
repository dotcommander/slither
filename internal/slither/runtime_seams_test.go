package slither

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeSeamCurrentTimeControlsReportAndStaleMarkerAge(t *testing.T) {
	fixed := time.Date(2000, 7, 2, 0, 0, 0, 0, time.UTC)
	previous := currentTime
	currentTime = func() time.Time { return fixed }
	t.Cleanup(func() { currentTime = previous })

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := BuildReport(context.Background(), Options{Repo: repo, Top: 1, MaxBytes: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if !report.GeneratedAt.Equal(fixed) {
		t.Fatalf("GeneratedAt = %s, want %s", report.GeneratedAt, fixed)
	}

	gitRepo := t.TempDir()
	runGitTestCommand(t, gitRepo, "init", "-q")
	runGitTestCommand(t, gitRepo, "config", "user.email", "slither@example.test")
	runGitTestCommand(t, gitRepo, "config", "user.name", "Slither Test")
	path := filepath.Join(gitRepo, "stale.go")
	if err := os.WriteFile(path, []byte("package stale\n// TODO: old marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, gitRepo, "add", "--", "stale.go")
	cmd := exec.Command("git", "-C", gitRepo, "commit", "-q", "-m", "old marker")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	stale, skip := staleMarkersByFile(context.Background(), gitRepo, []string{path}, 1<<10)
	if skip != "" {
		t.Fatalf("stale marker skip = %q", skip)
	}
	if got := stale["stale.go"].OldestDays; got != 183 {
		t.Fatalf("stale marker age = %d, want 183", got)
	}
}

func TestSourceReaderSeamRoutesInspectionAndScoreContextReads(t *testing.T) {
	previous := sourcePrefixReader
	var reads atomic.Int64
	sourcePrefixReader = func(path string, maxBytes int64) (string, bool, bool, error) {
		reads.Add(1)
		return previous(path, maxBytes)
	}
	t.Cleanup(func() { sourcePrefixReader = previous })

	repo := t.TempDir()
	for rel, text := range map[string]string{
		"go.mod":              "module example.test/seam\n\ngo 1.25\n",
		"main.go":             "package main\nimport \"example.test/seam/internal/dep\"\nfunc main() {}\n",
		"internal/dep/dep.go": "package dep\n// TODO: inspect dependency\nfunc Use() {}\n",
		"README.md":           "SEAM_TEST_VALUE is documented here.\n",
	} {
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := BuildReport(context.Background(), Options{Repo: repo, Top: 10, MaxBytes: 1 << 10}); err != nil {
		t.Fatal(err)
	}
	if got := reads.Load(); got < 7 {
		t.Fatalf("sourcePrefixReader calls = %d, want inspection plus score-context reads", got)
	}
}

func TestRuntimeSeamOutcomeWriterFuncAdapterPassesOneRecordAndContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), "key", "value")
	record := []byte(`{"schema":"slither.outcome/v1"}`)
	called := false
	writer := outcomeWriterFunc(func(gotCtx context.Context, gotRecord []byte) error {
		called = true
		if gotCtx != ctx {
			t.Fatal("outcome context was not forwarded")
		}
		if string(gotRecord) != string(record) {
			t.Fatalf("outcome record = %q, want %q", gotRecord, record)
		}
		return nil
	})
	if err := writer.WriteOutcome(ctx, record); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("outcome writer function was not called")
	}
}
