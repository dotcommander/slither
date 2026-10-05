package slither

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestDocumentationOnlySource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "comment-only package doc",
			text: "// Package vectors provides helpers.\n" +
				"//\n" +
				"// Recall and precision helpers support custom compact storage\n" +
				"// and approximate pre-filtering for embedding pipelines.\n" +
				strings.Repeat("// prose line about utils and metrics\n", 8) +
				"package vector\n",
			want: true,
		},
		{
			name: "block-comment-heavy file",
			text: "package main\n\n/*\n" + strings.Repeat(" prose about helpers\n", 10) + "*/\n",
			want: true,
		},
		{
			name: "hash-comment script doc",
			text: "# prose about helpers\n" + strings.Repeat("# recall metrics\n", 9) + "x = 1\n",
			want: true,
		},
		{
			name: "normal code file",
			text: "package main\n\nfunc main() {\n\tprintln(\"helpers\")\n}\n" + strings.Repeat("// comment\n", 10),
			want: false,
		},
		{
			name: "small file is not documentation classed",
			text: "// tiny\npackage p\n",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := documentationOnlySource(tt.text); got != tt.want {
				t.Fatalf("documentationOnlySource(%s) = %t, want %t", tt.name, got, tt.want)
			}
		})
	}
}

func TestShouldSkipExcludesOwnReportOutputs(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{"slither-summary.md", "slither-report.json", "sub/slither-summary.json", "docs/slither-cull-2026.md"} {
		if !shouldSkip(rel) {
			t.Fatalf("shouldSkip(%q) = false, want true (own output artifact)", rel)
		}
	}
	for _, rel := range []string{"README.md", "internal/report.go", "docs/summary.md"} {
		if shouldSkip(rel) {
			t.Fatalf("shouldSkip(%q) = true, want false (not an own output)", rel)
		}
	}
}

func TestBuildReportHygieneDocOnlyExamplesSelfOutputs(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Comment-only package doc full of content-pattern vocabulary.
	write("vector/doc.go", "// Package vectors provides helpers and utils.\n"+
		"//\n"+
		"// Recall and precision metrics support custom compact storage and\n"+
		"// approximate pre-filtering for embedding pipelines.\n"+
		strings.Repeat("// prose about helpers, recall, and embedding\n", 8)+
		"package vector\n")
	// Example quickstart with real code and no nearby test.
	write("examples/demo/main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n"+
		strings.Repeat("\tfmt.Println(\"step\")\n\tfmt.Println(\"helper\")\n", 20)+"\n}\n")
	// Control: a regular code file using pattern vocabulary in code.
	write("worker.go", "package main\n\nfunc embedHelper() int { return 1 }\n\nfunc run() {\n"+
		strings.Repeat("\tembedHelper()\n", 10)+"}\n")
	// Slither's own output artifact written into the repo root.
	write("slither-summary.md", "# Slither Summary\n\n- rows: 3\n")

	report, err := BuildReport(context.Background(), Options{Repo: repo, Top: 50, MaxBytes: 1 << 20, Days: 90})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	rows := map[string]FileEvidence{}
	for _, r := range report.Rows {
		rows[r.Path] = r
	}

	doc, ok := rows["vector/doc.go"]
	if !ok {
		t.Fatal("vector/doc.go row missing")
	}
	if doc.ContentRisk != 0 || doc.UnknownsRisk != 0 {
		t.Fatalf("doc-only file scored content=%d unknowns=%d, want 0/0", doc.ContentRisk, doc.UnknownsRisk)
	}
	hasDocMarker := false
	for _, reason := range doc.Reasons {
		if strings.HasPrefix(reason, "content:") {
			t.Fatalf("doc-only file carries content reason %q", reason)
		}
		if reason == "doc_only:content_patterns_skipped" {
			hasDocMarker = true
		}
		if reason == "test_gap:no nearby test" {
			t.Fatalf("doc-only file carries test_gap")
		}
	}
	if !hasDocMarker {
		t.Fatalf("doc-only file lacks doc_only:content_patterns_skipped marker, reasons=%v", doc.Reasons)
	}

	demo, ok := rows["examples/demo/main.go"]
	if !ok {
		t.Fatal("examples/demo/main.go row missing")
	}
	for _, reason := range demo.Reasons {
		if reason == "test_gap:no nearby test" {
			t.Fatalf("example carries test_gap; the user-surface lane owns example review")
		}
	}

	if _, ok := rows["slither-summary.md"]; ok {
		t.Fatal("slither-summary.md discovered as evidence row; own outputs must be skipped")
	}

	worker, ok := rows["worker.go"]
	if !ok {
		t.Fatal("worker.go row missing")
	}
	if worker.ContentRisk == 0 {
		t.Fatalf("control code file lost content risk; doc-only gate over-applied, reasons=%v", worker.Reasons)
	}
}

func TestAsyncMessagingBoundaryRequiresCallShape(t *testing.T) {
	t.Parallel()
	patterns, err := loadScoringPatterns("")
	if err != nil {
		t.Fatalf("loadScoringPatterns: %v", err)
	}
	var async *contentPattern
	for i := range patterns.ContentPatterns {
		if patterns.ContentPatterns[i].ID == "async_messaging_boundary" {
			async = &patterns.ContentPatterns[i]
			break
		}
	}
	if async == nil {
		t.Fatal("async_messaging_boundary pattern missing from embedded catalog")
	}
	// Go exported identifiers are always capitalized, so bare capitalized
	// generic words collide with ordinary domain fields (observed: the
	// retrieval eval Topic struct field scored as pub/sub middleware).
	for _, src := range []string{
		"type ScoreSignals struct {\n\tTopic string\n}\n",
		"result := ScoredResult{ID: id, Topic: candidate.Topic}\n",
		"const maxQueueDepth = 4\n",
	} {
		if matched, err := async.Pattern.MatchString(src); err != nil || matched {
			t.Fatalf("async_messaging_boundary matched non-messaging shape %q (matched=%t err=%v)", src, matched, err)
		}
	}
	for _, src := range []string{
		"p.Publish(ctx, msg)\n",
		"ch.Subscribe(topic)\n",
		"// ack after the Kafka commit before acknowledging\n",
	} {
		matched, err := async.Pattern.MatchString(src)
		if err != nil || !matched {
			t.Fatalf("async_messaging_boundary missed real messaging shape %q (matched=%t err=%v)", src, matched, err)
		}
	}
}

func TestFlakeRiskDistinguishesSeededRandomness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{"seeded source is deterministic", "rng := rand.New(rand.NewSource(42))\n_ = rng\n", false},
		{"global rand is nondeterministic", "if rand.Intn(2) == 0 {\n", true},
		{"v2 global rand is nondeterministic", "if rand.IntN(2) == 0 {\n", true},
		{"time-seeded source is nondeterministic", "rng := rand.New(rand.NewSource(time.Now().UnixNano()))\n_ = rng\n", true},
		{"httptest server is the deterministic standard", "srv := httptest.NewServer(h)\n_ = srv\n", false},
		{"real network call is flake-prone", "resp, err := http.Get(\"https://example.com\")\n_ = resp\n_ = err\n", true},
		{"frozen clock input is deterministic", "at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)\n_ = at\n", false},
		{"live clock read is nondeterministic", "now := time.Now()\n_ = now\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			score, reasons := testFlakeRisk("pkg/x_test.go", tt.src)
			fired := false
			for _, reason := range reasons {
				if reason == "flake:nondeterministic_or_io:1" {
					fired = true
				}
			}
			if fired != tt.want {
				t.Fatalf("nondeterministic_or_io fired=%t, want %t (reasons=%v, score=%d)", fired, tt.want, reasons, score)
			}
		})
	}
}

func TestFlakeRiskIgnoresSynctestBubbles(t *testing.T) {
	t.Parallel()
	src := "package memory\n\nimport (\n\t\"testing\"\n\t\"testing/synctest\"\n\t\"time\"\n)\n\nfunc TestExpiry(t *testing.T) {\n\tsynctest.Run(t, func(t *testing.T) {\n\t\ttime.Sleep(31 * time.Minute)\n\t})\n}\n"
	score, reasons := testFlakeRisk("judgment/memory/memory_test.go", src)
	for _, reason := range reasons {
		if reason == "flake:fixed_wait:1" {
			t.Fatalf("synctest-bubbled sleep counted as fixed_wait (reasons=%v)", reasons)
		}
	}
	if score != 0 {
		t.Fatalf("synctest test scored flake risk %d, want 0 (reasons=%v)", score, reasons)
	}
	real := "package p\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestPoll(t *testing.T) {\n\ttime.Sleep(50 * time.Millisecond)\n}\n"
	if score, reasons := testFlakeRisk("pkg/p_test.go", real); score == 0 || len(reasons) == 0 {
		t.Fatalf("real-clock sleep not flagged (score=%d reasons=%v)", score, reasons)
	}
}

func TestIsTestFileRecognizesTestScripts(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{"scripts/check_boundaries_test.sh", "scripts/run_test.bash", "a/b_test.zsh"} {
		if !isTestFile(rel) {
			t.Fatalf("isTestFile(%q) = false, want true", rel)
		}
	}
	for _, rel := range []string{"scripts/release.sh", "scripts/deploy.bash"} {
		if isTestFile(rel) {
			t.Fatalf("isTestFile(%q) = true, want false", rel)
		}
	}
}
