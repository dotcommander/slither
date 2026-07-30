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
	stale, skip := staleMarkersByFile(context.Background(), gitRepo, []string{path}, 1<<10, fixed)
	if skip != "" {
		t.Fatalf("stale marker skip = %q", skip)
	}
	if got := stale["stale.go"].OldestDays; got != 183 {
		t.Fatalf("stale marker age = %d, want 183", got)
	}
}

func TestOptionsAsOfControlsHistoryAndStaleMarkerSignals(t *testing.T) {
	asOf := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	runGitTestCommand(t, repo, "config", "user.email", "slither@example.test")
	runGitTestCommand(t, repo, "config", "user.name", "Slither Test")

	oldPath := filepath.Join(repo, "auth.go")
	if err := os.WriteFile(oldPath, []byte("package sample\n// TODO: old marker\nfunc Old() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "--", "auth.go")
	commitAt(t, repo, "2023-01-01T00:00:00Z", "add old marker")

	recentPath := filepath.Join(repo, "recent.go")
	if err := os.WriteFile(recentPath, []byte("package sample\nfunc Recent() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "--", "recent.go")
	commitAt(t, repo, "2023-12-15T00:00:00Z", "add recent source")

	futurePath := filepath.Join(repo, "future.go")
	if err := os.WriteFile(futurePath, []byte("package sample\nfunc Future() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "--", "future.go")
	commitAt(t, repo, "2024-02-01T00:00:00Z", "add future source")

	previous := currentTime
	t.Cleanup(func() { currentTime = previous })
	build := func(wallClock time.Time) Report {
		t.Helper()
		currentTime = func() time.Time { return wallClock }
		report, err := BuildReport(context.Background(), Options{
			Repo: repo, Top: 10, MaxBytes: 1 << 10, Days: 90, AsOf: asOf, NoCache: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}

	first := build(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	second := build(time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC))
	if !first.GeneratedAt.Equal(asOf) || !second.GeneratedAt.Equal(asOf) {
		t.Fatalf("generated times = %s and %s, want %s", first.GeneratedAt, second.GeneratedAt, asOf)
	}
	if first.ReportID != second.ReportID {
		t.Fatalf("fixed-AsOf report IDs differ: %s != %s", first.ReportID, second.ReportID)
	}
	firstRecent := evidenceByPath(t, first.Rows, "recent.go")
	secondRecent := evidenceByPath(t, second.Rows, "recent.go")
	if firstRecent.Churn == 0 || firstRecent.Churn != secondRecent.Churn {
		t.Fatalf("recent churn = %d and %d, want identical non-zero history", firstRecent.Churn, secondRecent.Churn)
	}
	if future := evidenceByPath(t, first.Rows, "future.go"); future.Churn != 0 {
		t.Fatalf("future churn = %d, want absolute until cutoff to exclude it", future.Churn)
	}
	firstOld := evidenceByPath(t, first.Rows, "auth.go")
	secondOld := evidenceByPath(t, second.Rows, "auth.go")
	if firstOld.StaleMarkerRisk == 0 || firstOld.StaleMarkerRisk != secondOld.StaleMarkerRisk {
		t.Fatalf("stale marker risk = %d and %d, want identical non-zero signal", firstOld.StaleMarkerRisk, secondOld.StaleMarkerRisk)
	}
}

func commitAt(t *testing.T, repo, timestamp, message string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "commit", "-q", "-m", message)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+timestamp, "GIT_COMMITTER_DATE="+timestamp)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
}

func evidenceByPath(t *testing.T, rows []FileEvidence, path string) FileEvidence {
	t.Helper()
	for _, row := range rows {
		if row.Path == path {
			return row
		}
	}
	t.Fatalf("missing evidence row %q", path)
	return FileEvidence{}
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
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("key"), "value")
	report := Report{ReportID: exampleReportID}
	feedback := outcomeFeedback{ReportID: exampleReportID, EvidenceID: exampleEvidenceID, Verdict: "confirmed"}
	called := false
	writer := outcomeWriterFunc(func(gotCtx context.Context, gotReport Report, gotFeedback outcomeFeedback) error {
		called = true
		if gotCtx != ctx {
			t.Fatal("outcome context was not forwarded")
		}
		if gotReport.ReportID != report.ReportID || gotFeedback != feedback {
			t.Fatalf("outcome input = %#v/%#v, want %#v/%#v", gotReport, gotFeedback, report, feedback)
		}
		return nil
	})
	if err := writer.WriteOutcome(ctx, report, feedback); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("outcome writer function was not called")
	}
}
