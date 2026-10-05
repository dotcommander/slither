package slither

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBugfixSubjectPattern(t *testing.T) {
	t.Parallel()
	tests := []struct {
		subject string
		want    bool
	}{
		{"fix: harden retrieval and index invariants", true},
		{"fix(openai): validate complete embedding results", true},
		{"Fixes #123", true},
		{"bugfix: nil map write", true},
		{"hotfix: rollback migration", true},
		{"fix: add regression tests for panic on broken input", true},
		{"fix: crash in server", true},
		{"fix: crashed worker", true},
		{"fix: handle panicked handler", true},
		{`fix(retrieval): "broken" oracle`, true},
		// Words merely containing a keyword must not count (observed: a
		// feat commit about nomic task *prefixes* inflated fix_touches).
		{"feat(embedding): add request kinds and nomic task prefixes for local embedding servers", false},
		{"chore: normalize suffix handling", false},
		{"test: add fixtures for parser", false},
		{"feat: new prefix matcher", false},
		{"docs: describe infix operators", false},
	}
	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			t.Parallel()
			if got := bugfixSubjectPattern.MatchString(tt.subject); got != tt.want {
				t.Fatalf("bugfixSubjectPattern(%q) = %t, want %t", tt.subject, got, tt.want)
			}
		})
	}
}

func TestBugfixTouchesByFileCountsWordBoundarySubjectsOnly(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.com")
	runGitTestCommand(t, repo, "config", "user.name", "Test")

	write := func(i int, message string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "adapter.go"), []byte(fmt.Sprintf("package p\n// %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitTestCommand(t, repo, "add", "--", "adapter.go")
		runGitTestCommand(t, repo, "commit", "-q", "-m", message)
	}
	write(1, "chore: initial import")
	for i := 2; i <= 29; i++ {
		write(i, fmt.Sprintf("chore: filler %d", i))
	}
	// The subject contains "prefixes", which the old substring grep matched.
	write(30, "feat(embedding): add request kinds and nomic task prefixes for local embedding servers")
	write(31, "fix(openai): validate complete embedding results")

	touches, skip := bugfixTouchesByFile(context.Background(), repo, gitHistoryWindow(90, currentTime()))
	if skip != "" {
		t.Fatalf("bugfixTouchesByFile skip = %q, want empty", skip)
	}
	if touches["adapter.go"] != 1 {
		t.Fatalf("adapter.go fix touches = %d, want 1 (word-boundary fix only, not the prefixes subject)", touches["adapter.go"])
	}
}

func TestLocalImportGraphLabelsOwnerlessPackageFallback(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	files := []string{
		filepath.Join(repo, "go.mod"),
		filepath.Join(repo, "main.go"),
		filepath.Join(repo, "hub", "alpha.go"),
		filepath.Join(repo, "hub", "beta.go"),
		filepath.Join(repo, "svc", "svc.go"),
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(files[0], "module example.test/mod\n\ngo 1.26\n")
	write(files[1], "package main\n\nimport (\n\t\"example.test/mod/hub\"\n\t\"example.test/mod/svc\"\n)\n\nvar _ = hub.A\nvar _ = svc.S\n")
	write(files[2], "package hub\n\nvar A = 1\n")
	write(files[3], "package hub\n\nvar B = 2\n")
	write(files[4], "package svc\n\nvar S = 3\n")

	counts, _, packageLevel := localImportGraph(repo, files, 1<<20)

	// hub has no preferred owner name, so the alphabetically first file
	// absorbs the package fan-in and the attribution is labeled package-level.
	if counts["hub/alpha.go"] != 1 {
		t.Fatalf("hub/alpha.go incoming refs = %d, want 1", counts["hub/alpha.go"])
	}
	if !packageLevel["hub/alpha.go"] {
		t.Fatalf("hub/alpha.go must be labeled package-level fallback attribution")
	}
	if counts["hub/beta.go"] != 0 {
		t.Fatalf("hub/beta.go incoming refs = %d, want 0 (fallback attributes one file)", counts["hub/beta.go"])
	}
	// svc has the natural owner name; attribution is file-level.
	if counts["svc/svc.go"] != 1 {
		t.Fatalf("svc/svc.go incoming refs = %d, want 1", counts["svc/svc.go"])
	}
	if packageLevel["svc/svc.go"] {
		t.Fatalf("svc/svc.go must not be labeled package-level; the owner name resolved the hub")
	}
}

func TestCentralityReasonDistinguishesPackageLevelAttribution(t *testing.T) {
	t.Parallel()
	_, fileReasons := centralityRisk(32, false, 3, 6)
	_, pkgReasons := centralityRisk(32, true, 3, 6)
	if len(fileReasons) == 0 || len(pkgReasons) == 0 {
		t.Fatalf("expected reasons for both attributions: %v / %v", fileReasons, pkgReasons)
	}
	if fileReasons[0] != "centrality:incoming_refs:32" {
		t.Fatalf("file-level reason = %q", fileReasons[0])
	}
	if pkgReasons[0] != "centrality:package_refs:32" {
		t.Fatalf("package-level reason = %q", pkgReasons[0])
	}
}
