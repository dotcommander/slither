package slither

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDoublestarPatternMatchesPathSegmentsAtAnyDepth(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{pattern: "internal/**", path: "internal/a.go", want: true},
		{pattern: "internal/**", path: "internal/pkg/a.go", want: true},
		{pattern: "internal/**", path: "internality/a.go", want: false},
		{pattern: "foo/**/bar/*.go", path: "foo/bar/a.go", want: true},
		{pattern: "foo/**/bar/*.go", path: "foo/one/bar/a.go", want: true},
		{pattern: "foo/**/bar/*.go", path: "foo/one/two/bar/a.go", want: true},
		{pattern: "foo/**/bar/*.go", path: "foo/one/bar/deeper/a.go", want: false},
		{pattern: "a/**/b/**/c.go", path: "a/one/b/two/three/c.go", want: true},
		{pattern: "**/*_test.go", path: "root_test.go", want: true},
		{pattern: "**/*_test.go", path: "pkg/root_test.go", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+":"+tt.path, func(t *testing.T) {
			got, err := pathPatternMatches(tt.pattern, tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("pathPatternMatches(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestDoublestarPatternRejectsUnreachedInvalidSegment(t *testing.T) {
	t.Parallel()
	if _, err := pathPatternMatches("z/**/[", "a.go"); err == nil {
		t.Fatal("invalid trailing segment was accepted behind a nonmatching prefix")
	}
}

func TestReadTextPrefixRetainsCompleteUTF8AtCutoff(t *testing.T) {
	for _, runeText := range []string{"¢", "€", "😀"} {
		t.Run(runeText, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.go")
			if err := os.WriteFile(path, []byte("abc"+runeText+"tail"), 0o600); err != nil {
				t.Fatal(err)
			}
			text, ok, truncated, err := readTextPrefixWithStatus(path, 4)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || !truncated || text != "abc" {
				t.Fatalf("prefix = %q, ok=%v, truncated=%v; want complete prefix", text, ok, truncated)
			}
		})
	}
}

func TestReadTextPrefixRejectsInvalidUTF8(t *testing.T) {
	for name, data := range map[string][]byte{
		"invalid byte":   {'a', 0xff, 'b'},
		"incomplete eof": {'a', 0xe2, 0x82},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.go")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if text, ok, truncated, err := readTextPrefixWithStatus(path, 100); err != nil || ok || truncated || text != "" {
				t.Fatalf("text=%q ok=%v truncated=%v err=%v; want invalid text rejected", text, ok, truncated, err)
			}
		})
	}
}

func TestReadTextPrefixIgnoresInvalidBytesAfterValidCutoff(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "source.go")
	if err := os.WriteFile(path, []byte{'a', 'b', 'c', 'd', 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	text, ok, truncated, err := readTextPrefixWithStatus(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if text != "abcd" || !ok || !truncated {
		t.Fatalf("text=%q ok=%v truncated=%v; want valid bounded prefix", text, ok, truncated)
	}
}

func TestReadTextPrefixHandlesMaximumLimitWithoutOverflow(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "source.go")
	if err := os.WriteFile(path, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	text, ok, truncated, err := readTextPrefixWithStatus(path, maxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if text != "package sample\n" || !ok || truncated {
		t.Fatalf("text=%q ok=%v truncated=%v; want complete file", text, ok, truncated)
	}
}

func TestBuildReportDisclosesContentTruncation(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "auth.go")
	if err := os.WriteFile(path, []byte("package auth\n// 😀 tail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := BuildReport(context.Background(), Options{Repo: repo, MaxBytes: 17})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(report.SkippedSignals, "scan:content_truncated:1") {
		t.Fatalf("skipped signals = %#v, want truncation disclosure", report.SkippedSignals)
	}
}

func TestGitFilesPreservesUnusualNames(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	want := []string{"  spaced.go  ", "..risk.go", "café.go", "line\nbreak.go"}
	for _, rel := range want {
		if err := os.WriteFile(filepath.Join(repo, rel), []byte("package sample\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitTestCommand(t, repo, "add", "--", ".")
	got, err := gitFiles(context.Background(), repo, "--cached")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range want {
		if !slices.Contains(got, rel) {
			t.Fatalf("gitFiles() = %#v, missing %q", got, rel)
		}
	}
	paths, missing, irregular := appendGitFiles(repo, got)
	if missing != 0 || irregular != 0 || len(paths) != len(want) {
		t.Fatalf("appendGitFiles: paths=%#v missing=%d irregular=%d", paths, missing, irregular)
	}
}

func TestContainmentAcceptsDotDotNameAndRejectsParent(t *testing.T) {
	repo := t.TempDir()
	inside := filepath.Join(repo, "..risk.go")
	outside := filepath.Join(filepath.Dir(repo), "outside.go")
	if err := os.WriteFile(inside, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	if _, ok, err := inspectFile(repo, inside, 1000, scoreContext{}); err != nil || !ok {
		t.Fatalf("inside ..name: ok=%v err=%v", ok, err)
	}
	if _, ok, err := inspectFile(repo, outside, 1000, scoreContext{}); err != nil || ok {
		t.Fatalf("parent traversal: ok=%v err=%v", ok, err)
	}
}

func TestRunGitOutputLimitReportsCapAndCommandErrors(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "long-name.go"), []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "--", ".")
	if _, err := runGitOutputLimit(context.Background(), repo, 4, "ls-files"); !errors.Is(err, errGitOutputLimit) {
		t.Fatalf("cap error = %v, want errGitOutputLimit", err)
	}
	if _, skip := churnByFile(context.Background(), filepath.Join(repo, "missing"), 90); skip != "command_failed" {
		t.Fatalf("history skip = %q, want surfaced command failure", skip)
	}
}

func TestStaleMarkersSurfacePartialBlameFailure(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	runGitTestCommand(t, repo, "config", "user.email", "slither@example.test")
	runGitTestCommand(t, repo, "config", "user.name", "Slither Test")
	tracked := filepath.Join(repo, "tracked.go")
	untracked := filepath.Join(repo, "untracked.go")
	for _, path := range []string{tracked, untracked} {
		if err := os.WriteFile(path, []byte("package sample\n// TODO: old marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitTestCommand(t, repo, "add", "--", "tracked.go")
	cmd := exec.Command("git", "-C", repo, "commit", "-q", "-m", "old marker")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	stale, skip := staleMarkersByFile(context.Background(), repo, []string{tracked, untracked}, 1000)
	if stale["tracked.go"].StaleCount != 1 {
		t.Fatalf("stale evidence = %#v, want tracked marker", stale)
	}
	if !strings.HasPrefix(skip, "marker blame command_failed:1") {
		t.Fatalf("skip = %q, want visible partial blame failure", skip)
	}
}

func runGitTestCommand(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}
