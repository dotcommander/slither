package slither

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCombinedHistoryMatchesIndependentSignals(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.test")
	runGitTestCommand(t, repo, "config", "user.name", "Test")
	for i := range 31 {
		subject := "chore: update"
		if i == 0 {
			subject = "feat: initial"
		} else if i == 30 {
			subject = "fix: repair"
		}
		for _, name := range []string{"a.go", "b.go"} {
			if err := os.WriteFile(filepath.Join(repo, name), []byte("package p\n// "+itoa(i)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		runGitTestCommand(t, repo, "add", "--", "a.go", "b.go")
		runGitTestCommand(t, repo, "commit", "-q", "-m", subject)
	}
	ctx := context.Background()
	window := gitHistoryWindow(90, currentTime())
	got := combinedHistory(ctx, repo, window)
	fix, fixSkip := bugfixTouchesByFile(ctx, repo, window)
	co, coSkip := cochangeByFile(ctx, repo, window)
	owners, ownerSkip := ownershipByFile(ctx, repo, window)
	if !reflect.DeepEqual(got.fixTouches, fix) || got.fixSkip != fixSkip || !reflect.DeepEqual(got.cochange, co) || got.cochangeSkip != coSkip || !reflect.DeepEqual(got.ownership, owners) || got.ownershipSkip != ownerSkip {
		t.Fatalf("combined history = %#v; independent fix=%#v (%q), co=%#v (%q), ownership=%#v (%q)", got, fix, fixSkip, co, coSkip, owners, ownerSkip)
	}
}

func TestReportSourcesReuseAndInvalidateChangedPrefix(t *testing.T) {
	// This test replaces the process-global reader seam, so it runs serially.
	previous := sourcePrefixReader
	reads := 0
	sourcePrefixReader = func(path string, limit int64) (string, bool, bool, error) {
		reads++
		return previous(path, limit)
	}
	t.Cleanup(func() { sourcePrefixReader = previous })
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := newReportSources()
	first, ok, _, err := sources.read(path, 1024)
	if err != nil || !ok {
		t.Fatalf("first read = %q %t %v", first, ok, err)
	}
	second, ok, _, err := sources.read(path, 1024)
	if err != nil || !ok || second != first || reads != 1 {
		t.Fatalf("cached read = %q %t %v; physical reads = %d", second, ok, err, reads)
	}
	if err := os.WriteFile(path, []byte("package sample\n// changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, ok, _, err := sources.read(path, 1024)
	if err != nil || !ok || third == first || reads != 2 {
		t.Fatalf("changed read = %q %t %v; physical reads = %d", third, ok, err, reads)
	}
}
