package slither

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildReportFallbackScoresRiskyFiles(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "auth.go"), []byte("package p\n// TODO fix token handling\nfunc f(){ panic(\"x\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "readme.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) == 0 {
		t.Fatal("expected rows")
	}
	if report.Rows[0].Path != "auth.go" {
		t.Fatalf("top row = %q, want auth.go", report.Rows[0].Path)
	}
	if report.Rows[0].Score < 3 {
		t.Fatalf("score = %d, want at least 3", report.Rows[0].Score)
	}
	if !contains(report.Rows[0].EvidenceLayers, "path-risk") || !contains(report.Rows[0].EvidenceLayers, "content-risk") {
		t.Fatalf("layers = %#v, want path-risk and content-risk", report.Rows[0].EvidenceLayers)
	}
	if len(report.Rows[0].EvidenceLocations) == 0 {
		t.Fatalf("expected content evidence locations for %#v", report.Rows[0].Reasons)
	}
	if got := report.Rows[0].EvidenceLocations[0]; got.Line == 0 || got.Snippet == "" || !strings.HasPrefix(got.Reason, "content:") {
		t.Fatalf("bad evidence location: %#v", got)
	}
	if !contains(report.SkippedSignals, "git_ls_files:unavailable") || !contains(report.SkippedSignals, "model_scoring:not_configured") {
		t.Fatalf("skipped signals = %#v, want git/model skipped signals", report.SkippedSignals)
	}
}

func TestBuildReportClassifiesContentSecretsAsSecretRisk(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main\n\nconst token = \"sk-aaaaaaaaaaaaaaaaaaaaaaaa\"\n")

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "main.go")
	if row == nil {
		t.Fatal("missing main.go")
	}
	if !contains(row.Reasons, "content:provider_token_literal:1") {
		t.Fatalf("reasons = %#v, want provider token detector", row.Reasons)
	}
	if !contains(row.EvidenceLayers, "secret-risk") {
		t.Fatalf("layers = %#v, want secret-risk", row.EvidenceLayers)
	}
}

func TestBuildReportIgnoresPlaceholderCredentialLiterals(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", `package main

func configure() map[string]any {
	apiKey := "local-placeholder"
	help := "api_key: \"your-api-key-here\""
	_ = help
	return map[string]any{"api_key": apiKey}
}
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "main.go")
	if row == nil {
		t.Fatal("missing main.go")
	}
	if containsReasonPrefix(row.Reasons, "content:credential_assignment_literal:") {
		t.Fatalf("placeholder credential should not be reported as hardcoded secret: %#v", row.Reasons)
	}
}

func TestBuildReportIncludesUntrackedGitFiles(t *testing.T) {
	tmp := t.TempDir()
	runGitForTest(t, tmp, "init")
	writeFile(t, tmp, "docs/usage.md", "# usage\n")
	runGitForTest(t, tmp, "add", "docs/usage.md")
	writeFile(t, tmp, "auth.go", "package p\n// TODO fix token handling\nfunc f(){ panic(\"x\") }\n")

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if findRow(report, "auth.go") == nil {
		t.Fatalf("missing untracked auth.go; rows=%#v skipped=%#v", report.Rows, report.SkippedSignals)
	}
	if !contains(report.SkippedSignals, "git_ls_files:included_untracked:1") {
		t.Fatalf("skipped signals = %#v, want included_untracked signal", report.SkippedSignals)
	}
	if report.Discovery.Source != "git" || report.Discovery.GitTracked != 1 || report.Discovery.GitUntracked != 1 || report.Discovery.CandidateFiles != 2 {
		t.Fatalf("discovery = %#v, want git tracked/untracked audit", report.Discovery)
	}
}

func TestBuildReportSkipsDeletedTrackedGitFiles(t *testing.T) {
	tmp := t.TempDir()
	runGitForTest(t, tmp, "init")
	writeFile(t, tmp, "deleted.go", "package p\n// TODO deleted\n")
	writeFile(t, tmp, "auth.go", "package p\n// TODO fix token handling\nfunc f(){ panic(\"x\") }\n")
	runGitForTest(t, tmp, "add", "deleted.go", "auth.go")
	if err := os.Remove(filepath.Join(tmp, "deleted.go")); err != nil {
		t.Fatal(err)
	}

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if findRow(report, "auth.go") == nil {
		t.Fatalf("missing existing tracked auth.go; rows=%#v skipped=%#v", report.Rows, report.SkippedSignals)
	}
	if findRow(report, "deleted.go") != nil {
		t.Fatalf("deleted tracked file should not produce evidence; rows=%#v", report.Rows)
	}
	if !contains(report.SkippedSignals, "git_ls_files:missing_tracked:1") {
		t.Fatalf("skipped signals = %#v, want missing_tracked signal", report.SkippedSignals)
	}
	if report.Discovery.CandidateFiles != 1 {
		t.Fatalf("candidate files = %d, want only existing files counted", report.Discovery.CandidateFiles)
	}
}

func TestInspectFileSkipsDirectoryCandidates(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, ok, err := inspectFile(tmp, filepath.Join(tmp, "tests"), 1000, scoreContext{})
	if err != nil {
		t.Fatalf("directory candidate should not be fatal: %v", err)
	}
	if ok {
		t.Fatal("directory candidate should not produce file evidence")
	}
}

func TestRenderMarkdownIncludesSnakeIdentity(t *testing.T) {
	report := Report{Repo: "/repo", FilesSeen: 1, Build: BuildInfo{Version: "devel", Revision: "abcdef1234567890", GoVersion: "go1.26.0"}, Discovery: DiscoveryStats{Source: "git", GitTracked: 1, CandidateFiles: 1}, SkippedSignals: []string{"model_scoring:not_configured"}, Rows: []FileEvidence{{Path: "a.go", Score: 3, SeedScore: 1.5, EvidenceClass: "heuristic", Confidence: "low", EnvContractRisk: 3, EvidenceLayers: []string{"path-risk", "env-contract"}, Reasons: []string{"path:auth", "env_contract:missing"}, Summary: "sample"}}}
	md := RenderMarkdown(report)
	for _, want := range []string{"# Slither Report", "snake through", "Slither build:", "Discovery: source `git`", "Skipped signals", "## Executive Triage", "Confidence: high", "Actionability in ranked production rows:", "## Ranked Files", "## Detailed Signals", "actionability", "seed_score", "env_contract_risk=3", "path-risk", "`a.go`"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	if !strings.Contains(md, "Ranked production files: `1`; separated documentation rows: `0`; separated test/fixture rows: `0`; generated/support rows: `0`; detail-only weak rows: `0`; total reported rows: `1`") {
		t.Fatalf("markdown missing production/separated row counts:\n%s", md)
	}
	if strings.Contains(md, "workflow_security_risk | migration_safety_risk") {
		t.Fatalf("markdown still includes the old all-risk wide table:\n%s", md)
	}
	if start := markdownLineWithPrefix(md, "- Start with:"); strings.Contains(start, "path:") || strings.Contains(start, "content:") {
		t.Fatalf("start-here summary should use human signal labels, not raw reason keys: %s", start)
	}
}

func TestExecutiveActionabilityCountsRankedProductionRowsOnly(t *testing.T) {
	report := Report{Repo: "/repo", Rows: []FileEvidence{
		{Path: "internal/service.go", Score: 5, Confidence: "high", Actionability: ActionabilityInspect, ContentRisk: 5, CochangeRisk: 5, EvidenceLayers: []string{"content-risk", "cochange"}, Reasons: []string{"content:stateful_store:1"}},
		{Path: "internal/service_copy.go", Score: 3, Confidence: "medium", Actionability: ActionabilityInspect, ContentRisk: 5, CochangeRisk: 5, EvidenceLayers: []string{"content-risk", "cochange"}, Reasons: []string{"content:stateful_store:1"}},
		{Path: "README.md", Score: 5, Confidence: "low", Actionability: ActionabilityVerifyFirst, EvidenceLayers: []string{"path-risk"}, Reasons: []string{"path:readme"}},
		{Path: "internal/service_test.go", Score: 5, Confidence: "low", Actionability: ActionabilityVerifyFirst, EvidenceLayers: []string{"content-risk"}, Reasons: []string{"content:background_context:1"}},
	}}
	md := RenderMarkdown(report)
	line := markdownLineWithPrefix(md, "- Actionability in ranked production rows:")
	if !strings.Contains(line, "inspect:1") {
		t.Fatalf("actionability line = %q, want one ranked production inspect row", line)
	}
	for _, notWant := range []string{"inspect:2", "verify_first"} {
		if strings.Contains(line, notWant) {
			t.Fatalf("actionability line should not count separated or duplicate rows: %q", line)
		}
	}
}

func TestRenderJSONIncludesEvidenceEnvelope(t *testing.T) {
	report := Report{Repo: "/repo", FilesSeen: 1, Build: BuildInfo{Version: "v1.2.3", Revision: "abc123"}, Discovery: DiscoveryStats{Source: "git", GitTracked: 1, CandidateFiles: 1}, SkippedSignals: []string{"model_scoring:not_configured"}, Rows: []FileEvidence{{Path: "a.go", Score: 2, EvidenceLayers: []string{"path-risk"}, Reasons: []string{"path:auth"}, Summary: "sample"}}}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		RunLabel       string         `json:"run_label"`
		Discovery      DiscoveryStats `json:"discovery"`
		Build          BuildInfo      `json:"build"`
		FilesReported  int            `json:"files_reported"`
		SkippedSignals []string       `json:"skipped_signals"`
		Rows           []FileEvidence `json:"rows"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RunLabel != "slither_report" || payload.FilesReported != 1 {
		t.Fatalf("unexpected envelope: %#v", payload)
	}
	if payload.Discovery.Source != "git" || payload.Discovery.GitTracked != 1 || payload.Discovery.CandidateFiles != 1 {
		t.Fatalf("discovery = %#v, want git audit", payload.Discovery)
	}
	if payload.Build.Version != "v1.2.3" || payload.Build.Revision != "abc123" {
		t.Fatalf("build = %#v, want report provenance", payload.Build)
	}
	if !contains(payload.SkippedSignals, "model_scoring:not_configured") || !contains(payload.Rows[0].EvidenceLayers, "path-risk") {
		t.Fatalf("missing envelope evidence: %#v", payload)
	}
}

func TestActionabilityClassifiesRows(t *testing.T) {
	cases := []struct {
		name string
		row  FileEvidence
		want Actionability
	}{
		{
			name: "corroborated high-risk inspection",
			row:  FileEvidence{Path: "internal/auth.go", Score: 5, ContentRisk: 5, WorkflowSecurityRisk: 5, EvidenceLayers: []string{"content-risk", "workflow-security"}, Reasons: []string{"workflow_security:unpinned_actions:1"}},
			want: ActionabilityHighRiskInspect,
		},
		{
			name: "corroborated likely defect",
			row:  FileEvidence{Path: "internal/auth.go", Score: 5, ContentRisk: 5, WorkflowSecurityRisk: 5, EvidenceLayers: []string{"content-risk", "workflow-security"}, Reasons: []string{"content:ssrf_url_param:1", "workflow_security:unpinned_actions:1"}},
			want: ActionabilityLikelyDefect,
		},
		{
			name: "ordinary premium seed",
			row:  FileEvidence{Path: "internal/service.go", Score: 5, ContentRisk: 5, CochangeRisk: 5, EvidenceLayers: []string{"content-risk", "cochange"}, Reasons: []string{"content:stateful_store:1"}},
			want: ActionabilityInspect,
		},
		{
			name: "dependency policy is review not defect",
			row:  FileEvidence{Path: "go.mod", Score: 5, PathRisk: 4, ContentRisk: 4, DependencyHealthRisk: 4, EvidenceLayers: []string{"path-risk", "content-risk", "dependency-health"}, Reasons: []string{"dependency_health:go_module_replace"}},
			want: ActionabilityDependencyReview,
		},
		{
			name: "hotspot only",
			row:  FileEvidence{Path: "internal/hot.go", Score: 2, HotspotRisk: 4, EvidenceLayers: []string{"hotspot"}, Reasons: []string{"git:hotspot"}},
			want: ActionabilityHotspot,
		},
		{
			name: "low score weak intersection",
			row:  FileEvidence{Path: "internal/model_prompt.go", Score: 2, UnknownsRisk: 3, EvidenceLayers: []string{"unknowns", "work-marker", "churn"}, Reasons: []string{"unknowns:resource_factory:1"}},
			want: ActionabilityVerifyFirst,
		},
		{
			name: "detector fixture",
			row:  FileEvidence{Path: "internal/risk_test.go", Score: 5, ContentRisk: 5, EvidenceLayers: []string{"content-risk"}, Reasons: []string{"content:open_redirect_redirect_param:1", "content:ssrf_url_param:1", "content:path_traversal_join:1"}},
			want: ActionabilityVerifyFirst,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := actionabilityForRow(tc.row); got != tc.want {
				t.Fatalf("actionability = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderJSONIncludesActionability(t *testing.T) {
	report := Report{Repo: "/repo", FilesSeen: 1, Rows: []FileEvidence{{Path: "a.go", Score: 4, Actionability: ActionabilityInspect, EvidenceLayers: []string{"content-risk", "cochange"}, Reasons: []string{"content:stateful_store:1"}}}}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"actionability": "inspect"`) {
		t.Fatalf("json missing actionability:\n%s", data)
	}
}

func TestRenderJSONIncludesEvidenceLocations(t *testing.T) {
	report := Report{Repo: "/repo", FilesSeen: 1, Rows: []FileEvidence{{
		Path:              "a.go",
		Score:             3,
		EvidenceLayers:    []string{"content-risk"},
		Reasons:           []string{"content:shell_boundary:1"},
		EvidenceLocations: []EvidenceLocation{{Reason: "content:shell_boundary:1", Line: 12, Snippet: "exec.CommandContext(ctx, \"go\", \"test\")"}},
		Summary:           "sample",
	}}}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"evidence_locations"`) || !strings.Contains(string(data), `"line": 12`) {
		t.Fatalf("json missing evidence locations:\n%s", data)
	}
}

func TestBuildReportFiltersFocusIncludeExcludeAndWhyTop(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "internal/database/db.go", "package database\n\nfunc Query(){ panic(\"postgres pgx migration\") }\n")
	writeFile(t, tmp, "internal/database/db_test.go", "package database\n\nfunc TestQuery() {}\n")
	writeFile(t, tmp, "docs/notes.md", "postgres migration notes\n")
	writeFile(t, tmp, "internal/http/server.go", "package http\n\nfunc Serve() {}\n")

	report, err := BuildReport(context.Background(), Options{
		Repo:     tmp,
		Top:      10,
		MaxBytes: 2000,
		Focus:    "postgres|pgx|migration",
		Include:  []string{"internal/**"},
		Exclude:  []string{"**/*_test.go"},
		WhyTop:   3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if findRow(report, "internal/database/db.go") == nil {
		t.Fatalf("focused database row missing: rows=%#v skipped=%#v", report.Rows, report.SkippedSignals)
	}
	for _, notWant := range []string{"internal/database/db_test.go", "docs/notes.md", "internal/http/server.go"} {
		if findRow(report, notWant) != nil {
			t.Fatalf("filtered row %q should not be present: %#v", notWant, report.Rows)
		}
	}
	if len(report.WhyTop) != 1 || report.WhyTop[0].Path != "internal/database/db.go" {
		t.Fatalf("why_top = %#v, want focused top row", report.WhyTop)
	}
	if report.Filters.Focus == "" || len(report.Filters.Include) != 1 || len(report.Filters.Exclude) != 1 {
		t.Fatalf("filters not preserved in report: %#v", report.Filters)
	}
}

func TestBuildReportInventoryDataIntegrityFiltersRows(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "migrations/001.sql", "DROP TABLE users;\n")
	writeFile(t, tmp, "internal/http/server.go", "package http\n\nfunc Serve(){ panic(\"x\") }\n")

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 2000, Inventory: "data-integrity"})
	if err != nil {
		t.Fatal(err)
	}
	if findRow(report, "migrations/001.sql") == nil {
		t.Fatalf("data-integrity migration missing: %#v", report.Rows)
	}
	if findRow(report, "internal/http/server.go") != nil {
		t.Fatalf("non-data-integrity row should be filtered: %#v", report.Rows)
	}
	if !reviewPlanHasLane(report.ReviewPlan, "data-integrity") {
		t.Fatalf("data-integrity inventory should retain lane grouping: %#v", report.ReviewPlan)
	}
}

func TestRenderMarkdownCompactsReviewPlanFiles(t *testing.T) {
	report := Report{
		Repo:      "/repo",
		FilesSeen: 6,
		Rows: []FileEvidence{{
			Path:           "internal/config/config.go",
			Score:          5,
			EvidenceClass:  "git_history",
			Confidence:     "high",
			EvidenceLayers: []string{"path-risk", "content-risk"},
			Reasons:        []string{"path:config"},
		}},
		ReviewPlan: []ReviewLane{{
			Lane:   "lifecycle-concurrency",
			Files:  []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"},
			Gates:  []string{"context propagation", "goroutine ownership", "shutdown cleanup", "race coverage"},
			Verify: []string{"go test -race ./...", "go test ./..."},
			Why:    []string{"concurrency, context, or flaky-test signals need lifecycle checks"},
		}},
	}

	md := RenderMarkdown(report)
	for _, want := range []string{"| lane | files | top files | omitted | gates | verify | why |", "`lifecycle-concurrency` | 6 | a.go, b.go, c.go, d.go | 2 | context propagation, goroutine ownership, shutdown cleanup, +1 more"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "a.go, b.go, c.go, d.go, e.go, f.go") {
		t.Fatalf("review plan did not compact file list:\n%s", md)
	}
}

func TestStartHerePrefersProductionRankedRows(t *testing.T) {
	rows := []FileEvidence{
		{Path: "internal/a_test.go", Score: 5, Confidence: "high", EvidenceLayers: []string{"content-risk", "flake-risk"}, Reasons: []string{"content:background_context:2"}},
		{Path: "internal/a.go", Score: 5, Confidence: "high", EvidenceLayers: []string{"content-risk", "hotspot"}, Reasons: []string{"content:resource_lifecycle:1"}},
	}
	got := startHere(rows)
	if !strings.Contains(got, "`internal/a.go`") {
		t.Fatalf("startHere should prefer production-ranked rows over separated test rows: %s", got)
	}
}

func TestRenderMarkdownPlacesCullLedgerBeforeExhaustiveRows(t *testing.T) {
	report := Report{
		Repo:      "/repo",
		FilesSeen: 1,
		Rows: []FileEvidence{{
			Path:           "auth.go",
			Score:          5,
			EvidenceClass:  "heuristic",
			Confidence:     "high",
			VerifyCmd:      "go test ./...",
			EvidenceLayers: []string{"path-risk", "content-risk"},
			Reasons:        []string{"path:auth"},
		}},
		CullLedger: &CullLedger{
			StopReason:     "deterministic cull complete",
			RowsConsidered: 1,
			KeptForPremium: CullBucket{Count: 1, Examples: []CullEntry{{
				Path:                          "auth.go",
				Score:                         5,
				Confidence:                    "high",
				VerifyCmd:                     "go test ./...",
				StrongestEvidenceIntersection: "path-risk + content-risk",
				Reason:                        "strong multi-layer seed",
			}}},
		},
	}

	md := RenderMarkdown(report)
	cullIndex := strings.Index(md, "## Cheap-Model Cull Ledger")
	rankedIndex := strings.Index(md, "## Ranked Files")
	if cullIndex < 0 || rankedIndex < 0 || cullIndex > rankedIndex {
		t.Fatalf("cull ledger should appear before ranked files:\n%s", md)
	}
	for _, want := range []string{"| file | score | confidence | actionability | verify | strongest_evidence_intersection | reason |", "`auth.go` | 5 | high | - | go test ./... | path-risk + content-risk | strong multi-layer seed"} {
		if !strings.Contains(md, want) {
			t.Fatalf("cull ledger missing %q:\n%s", want, md)
		}
	}
}

func TestBuildCullLedgerCarriesActionability(t *testing.T) {
	report := Report{
		Repo: "/repo",
		Rows: []FileEvidence{{
			Path:           "internal/auth.go",
			Score:          5,
			Confidence:     "high",
			Actionability:  ActionabilityInspect,
			ContentRisk:    5,
			CochangeRisk:   5,
			EvidenceLayers: []string{"content-risk", "cochange"},
			Reasons:        []string{"content:stateful_store:1"},
		}},
	}
	ledger := BuildCullLedger(report)
	if len(ledger.KeptForPremium.Examples) != 1 {
		t.Fatalf("kept examples = %d, want 1", len(ledger.KeptForPremium.Examples))
	}
	if got := ledger.KeptForPremium.Examples[0].Actionability; got != ActionabilityInspect {
		t.Fatalf("cull actionability = %q, want %q", got, ActionabilityInspect)
	}
}

func TestCullSurfaceKeyKeepsRootFilesSeparatedByKind(t *testing.T) {
	goMod := FileEvidence{Path: "go.mod", EvidenceLayers: []string{"path-risk", "content-risk"}}
	mainGo := FileEvidence{Path: "main.go", EvidenceLayers: []string{"path-risk", "content-risk"}}
	configGo := FileEvidence{Path: "config.go", EvidenceLayers: []string{"path-risk", "content-risk"}}
	configCopyGo := FileEvidence{Path: "config_copy.go", EvidenceLayers: []string{"path-risk", "content-risk"}}
	if cullSurfaceKey(goMod) == cullSurfaceKey(mainGo) {
		t.Fatalf("root go.mod and main.go should not share duplicate surface key %q", cullSurfaceKey(goMod))
	}
	if cullSurfaceKey(configGo) != cullSurfaceKey(configCopyGo) {
		t.Fatalf("same-kind root Go files should still share duplicate surface keys: %q vs %q", cullSurfaceKey(configGo), cullSurfaceKey(configCopyGo))
	}
}

func TestCullSurfaceKeySeparatesPathRiskReasons(t *testing.T) {
	config := FileEvidence{
		Path:           "internal/slither/config.go",
		EvidenceLayers: []string{"path-risk", "content-risk", "unknowns"},
		Reasons:        []string{"path:config", "content:resource_factory:1"},
	}
	cache := FileEvidence{
		Path:           "internal/slither/score_cache.go",
		EvidenceLayers: []string{"path-risk", "content-risk", "cochange"},
		Reasons:        []string{"path:cache", "content:resource_factory:1"},
	}
	if cullSurfaceKey(config) == cullSurfaceKey(cache) {
		t.Fatalf("config and cache rows should not share duplicate surface key %q", cullSurfaceKey(config))
	}
}

func TestRenderMarkdownCapsDetailedSignals(t *testing.T) {
	rows := make([]FileEvidence, maxDetailedMarkdownRows+2)
	for i := range rows {
		rows[i] = FileEvidence{Path: fmt.Sprintf("internal/file_%03d.go", i), Score: 1, EvidenceClass: "heuristic", Actionability: ActionabilityVerifyFirst, Reasons: []string{"low-signal"}}
	}
	md := RenderMarkdown(Report{Repo: "/repo", Rows: rows})
	if !strings.Contains(md, "Showing the first `80` of `82` rows") {
		t.Fatalf("markdown missing detailed-signal cap note:\n%s", md)
	}
	if strings.Contains(md, "internal/file_081.go") {
		t.Fatalf("markdown should omit detailed rows beyond cap:\n%s", md)
	}
}

func TestRenderMarkdownOmitsCulledRowsFromRankedFiles(t *testing.T) {
	report := Report{
		Repo:      "/repo",
		FilesSeen: 3,
		Rows: []FileEvidence{
			{
				Path:           "internal/storage/store.go",
				Score:          5,
				Confidence:     "high",
				ContentRisk:    12,
				EvidenceLayers: []string{"content-risk", "unknowns", "hotspot"},
				Reasons:        []string{"content:stateful_store"},
			},
			{
				Path:           "internal/storage/store_test.go",
				Score:          5,
				Confidence:     "low",
				ContentRisk:    12,
				EvidenceLayers: []string{"content-risk", "unknowns", "hotspot"},
				Reasons:        []string{"content:test_helper"},
			},
			{
				Path:           "internal/query/retrieval_quality_history_testdata.jsonl",
				Score:          5,
				Confidence:     "medium",
				ContentRisk:    8,
				EvidenceLayers: []string{"content-risk", "churn"},
				Reasons:        []string{"content:fixture_history"},
			},
			{
				Path:           "internal/storage/store_copy.go",
				Score:          3,
				Confidence:     "medium",
				ContentRisk:    12,
				EvidenceLayers: []string{"content-risk", "unknowns", "hotspot"},
				Reasons:        []string{"content:stateful_store"},
			},
		},
	}
	ledger := BuildCullLedger(report)
	report.CullLedger = &ledger

	md := RenderMarkdown(report)
	rankedStart := strings.Index(md, "## Ranked Files")
	detailedStart := strings.Index(md, "## Detailed Signals")
	if rankedStart < 0 || detailedStart < 0 {
		t.Fatalf("missing ranked or detailed section:\n%s", md)
	}
	ranked := md[rankedStart:detailedStart]
	if strings.Contains(ranked, "internal/storage/store_test.go") {
		t.Fatalf("test-only row should be omitted from ranked files:\n%s", ranked)
	}
	if strings.Contains(ranked, "internal/query/retrieval_quality_history_testdata.jsonl") {
		t.Fatalf("testdata fixture row should be omitted from ranked files:\n%s", ranked)
	}
	if strings.Contains(ranked, "internal/storage/store_copy.go") {
		t.Fatalf("duplicate-surface row should be omitted from ranked files:\n%s", ranked)
	}
	if !strings.Contains(ranked, "Generated/support, documentation, test/fixture, duplicate-surface, needs-more-evidence, low-signal, and weak-score rows are omitted here") {
		t.Fatalf("ranked section missing omission note:\n%s", ranked)
	}
	detailed := md[detailedStart:]
	if !strings.Contains(detailed, "internal/storage/store_test.go") {
		t.Fatalf("detailed signals should retain full evidence rows:\n%s", detailed)
	}
	if !strings.Contains(detailed, "internal/storage/store_copy.go") {
		t.Fatalf("detailed signals should retain duplicate evidence rows:\n%s", detailed)
	}
}

func TestRenderMarkdownSeparatesDocumentationRowsFromRankedFiles(t *testing.T) {
	report := Report{
		Repo:      "/repo",
		FilesSeen: 2,
		Rows: []FileEvidence{
			{
				Path:           "internal/storage/store.go",
				Score:          5,
				Confidence:     "high",
				ContentRisk:    12,
				EvidenceLayers: []string{"content-risk", "hotspot"},
				Reasons:        []string{"content:stateful_store"},
			},
			{
				Path:           "docs/configuration.md",
				Score:          5,
				Confidence:     "medium",
				CochangeRisk:   8,
				EvidenceLayers: []string{"cochange", "ownership", "churn"},
				Reasons:        []string{"cochange:partners:5", "cochange:max_jaccard:0.54"},
			},
			{
				Path:           "docs/api/test/readme_test.go",
				Score:          5,
				Confidence:     "high",
				ContentRisk:    12,
				FlakeRisk:      5,
				EvidenceLayers: []string{"content-risk", "flake-risk", "churn"},
				Reasons:        []string{"flake:nondeterministic_or_io:2", "content:doc_contract_test"},
			},
		},
	}

	md := RenderMarkdown(report)
	rankedStart := strings.Index(md, "## Ranked Files")
	docsStart := strings.Index(md, "## Documentation Rows")
	testRiskStart := strings.Index(md, "## Test Risk Rows")
	detailedStart := strings.Index(md, "## Detailed Signals")
	if rankedStart < 0 || docsStart < 0 || testRiskStart < 0 || detailedStart < 0 {
		t.Fatalf("missing ranked, docs, test-risk, or detailed section:\n%s", md)
	}
	ranked := md[rankedStart:docsStart]
	if strings.Contains(ranked, "docs/configuration.md") {
		t.Fatalf("documentation row should be omitted from production ranked files:\n%s", ranked)
	}
	docs := md[docsStart:testRiskStart]
	if !strings.Contains(docs, "docs/configuration.md") || !strings.Contains(docs, "cochange:partners:5") {
		t.Fatalf("documentation section missing docs row:\n%s", docs)
	}
	if strings.Contains(docs, "docs/api/test/readme_test.go") {
		t.Fatalf("documentation section should not include executable docs test row:\n%s", docs)
	}
	testRisk := md[testRiskStart:detailedStart]
	if !strings.Contains(testRisk, "docs/api/test/readme_test.go") || !strings.Contains(testRisk, "flake:nondeterministic_or_io:2") {
		t.Fatalf("test-risk section missing docs test row:\n%s", testRisk)
	}
	detailed := md[detailedStart:]
	if !strings.Contains(detailed, "docs/configuration.md") {
		t.Fatalf("detailed signals should retain documentation evidence row:\n%s", detailed)
	}
}

func TestRenderMarkdownSeparatesTestRiskRowsFromRankedFiles(t *testing.T) {
	report := Report{
		Repo:      "/repo",
		FilesSeen: 2,
		Rows: []FileEvidence{
			{
				Path:           "internal/storage/store.go",
				Score:          5,
				Confidence:     "high",
				ContentRisk:    12,
				EvidenceLayers: []string{"content-risk", "hotspot"},
				Reasons:        []string{"content:stateful_store"},
			},
			{
				Path:           "internal/storage/store_test.go",
				Score:          5,
				Confidence:     "high",
				ContentRisk:    12,
				FlakeRisk:      5,
				EvidenceLayers: []string{"content-risk", "flake-risk", "hotspot"},
				Reasons:        []string{"flake:nondeterministic_or_io:2", "content:test_helper"},
			},
		},
	}

	md := RenderMarkdown(report)
	rankedStart := strings.Index(md, "## Ranked Files")
	testRiskStart := strings.Index(md, "## Test Risk Rows")
	detailedStart := strings.Index(md, "## Detailed Signals")
	if rankedStart < 0 || testRiskStart < 0 || detailedStart < 0 {
		t.Fatalf("missing ranked, test-risk, or detailed section:\n%s", md)
	}
	ranked := md[rankedStart:testRiskStart]
	if strings.Contains(ranked, "internal/storage/store_test.go") {
		t.Fatalf("test-risk row should be omitted from production ranked files:\n%s", ranked)
	}
	testRisk := md[testRiskStart:detailedStart]
	if !strings.Contains(testRisk, "internal/storage/store_test.go") || !strings.Contains(testRisk, "flake:nondeterministic_or_io:2") {
		t.Fatalf("test-risk section missing flaky test row:\n%s", testRisk)
	}
	detailed := md[detailedStart:]
	if !strings.Contains(detailed, "internal/storage/store_test.go") {
		t.Fatalf("detailed signals should retain test-risk evidence row:\n%s", detailed)
	}
}

func TestRenderMarkdownShowsDiscriminatingDominantLayers(t *testing.T) {
	report := Report{
		Repo:      "/repo",
		FilesSeen: 2,
		Rows: []FileEvidence{
			{
				Path:           "internal/storage/store.go",
				Score:          4,
				Confidence:     "medium",
				EvidenceLayers: []string{"churn", "content-risk", "unknowns"},
				Reasons:        []string{"content:store"},
			},
			{
				Path:           "internal/cli/root.go",
				Score:          3,
				Confidence:     "medium",
				EvidenceLayers: []string{"churn", "ownership"},
				Reasons:        []string{"ownership:risky_single_author"},
			},
		},
	}

	md := RenderMarkdown(report)
	if !strings.Contains(md, "Dominant discriminating evidence layers") {
		t.Fatalf("missing discriminating layer summary:\n%s", md)
	}
	if strings.Contains(md, "churn:2") {
		t.Fatalf("ubiquitous churn should not dominate layer summary:\n%s", md)
	}
	for _, want := range []string{"content-risk:1", "ownership:1", "unknowns:1"} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing discriminating layer %q:\n%s", want, md)
		}
	}
}

func TestTopReasonsDemotesGenericBoolModeSignal(t *testing.T) {
	row := FileEvidence{
		Reasons: []string{
			"content:go_bool_mode_flag_param:4",
			"content:lint_suppression:5",
			"content:error_context_dropped:2",
			"content:looped_io_or_query:1",
			"unknowns:nested_loop_scale:1",
		},
	}

	got := topReasons(row, 3)
	if contains(got, "content:go_bool_mode_flag_param:4") {
		t.Fatalf("generic bool-mode reason should be demoted from top reasons: %#v", got)
	}
	if contains(got, "content:lint_suppression:5") {
		t.Fatalf("generic lint-suppression reason should be demoted from top reasons: %#v", got)
	}
	for _, want := range []string{"content:error_context_dropped:2", "content:looped_io_or_query:1", "unknowns:nested_loop_scale:1"} {
		if !contains(got, want) {
			t.Fatalf("top reasons missing %q: %#v", want, got)
		}
	}
}

func TestBuildReportAddsEvidenceMetadataAndReviewPlan(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.test/app\n\nreplace example.test/lib => ../lib\n")
	writeFile(t, tmp, "cmd/app/main.go", `package main

import "os"

func main() {
	// TODO fix token handling before this command ships.
	if os.Getenv("SECRET_TOKEN") == "" {
		panic("missing token")
	}
	os.Exit(1)
}
`)
	writeFile(t, tmp, "internal/config/config.go", `package config

import "os"

func Load() string {
	return os.Getenv("SECRET_TOKEN")
}
`+strings.Repeat("// config filler\n", 80))

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) == 0 {
		t.Fatal("expected report rows")
	}
	for _, row := range report.Rows {
		if row.ID == "" || row.EvidenceClass == "" || row.Confidence == "" {
			t.Fatalf("row missing metadata: %#v", row)
		}
		if strings.HasSuffix(row.Path, ".go") && row.VerifyCmd == "" {
			t.Fatalf("go row missing verify command: %#v", row)
		}
	}
	if !reviewQueueHasGroup(report.FirstReadQueue, "user-surface") {
		t.Fatalf("first-read queue missing user-surface: %#v", report.FirstReadQueue)
	}
	if !reviewPlanHasLane(report.ReviewPlan, "cli-ux") {
		t.Fatalf("review plan missing cli-ux lane: %#v", report.ReviewPlan)
	}
	cliLane := findReviewLane(report.ReviewPlan, "cli-ux")
	if !contains(cliLane.Gates, "help-text accuracy") || !contains(cliLane.Verify, "go build ./...") {
		t.Fatalf("cli lane missing gates/verify: %#v", cliLane)
	}
	readme := findRow(report, "README.md")
	if readme != nil && readme.VerifyCmd != "go test ./..." {
		t.Fatalf("README verify command = %q, want repo-local go test", readme.VerifyCmd)
	}
}

func TestBuildReportUsesNearestPackageScriptForJavaScriptVerification(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "workers/package.json", `{"scripts":{"typecheck":"tsc --noEmit","build":"wrangler deploy --dry-run"}}`)
	writeFile(t, tmp, "workers/package-lock.json", "{}")
	writeFile(t, tmp, "workers/src/index.ts", `export default {
	async fetch(request: Request): Promise<Response> {
		return new Response(request.url)
	}
}`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "workers/src/index.ts")
	if row == nil {
		t.Fatal("missing worker row")
	}
	if row.VerifyCmd != "npm --prefix workers run typecheck" {
		t.Fatalf("verify command = %q, want nearest package typecheck", row.VerifyCmd)
	}
}

func TestBuildReviewPlanExcludesSeparatedDocsAndHistoryOnlyTests(t *testing.T) {
	_, plan := BuildReviewPlan([]FileEvidence{
		{
			Path:           "internal/service/service.go",
			Score:          5,
			ContentRisk:    12,
			EvidenceLayers: []string{"content-risk", "hotspot", "churn"},
			Reasons:        []string{"content:async_or_concurrent_boundary:1"},
		},
		{
			Path:           "docs/archive/testing/removed_test.go.archive",
			Score:          5,
			ContentRisk:    12,
			EvidenceLayers: []string{"content-risk", "size", "churn"},
			Reasons:        []string{"content:background_context:1"},
		},
		{
			Path:           "internal/service/history_test.go",
			Score:          5,
			ContentRisk:    12,
			EvidenceLayers: []string{"content-risk", "churn"},
			Reasons:        []string{"content:background_context:1"},
		},
		{
			Path:           "internal/service/flaky_test.go",
			Score:          5,
			FlakeRisk:      6,
			EvidenceLayers: []string{"flake-risk", "churn"},
			Reasons:        []string{"flake:nondeterministic_or_io:1"},
		},
		{
			Path:           "config/cache.php",
			Score:          2,
			PathRisk:       6,
			EvidenceLayers: []string{"path-risk"},
			Reasons:        []string{"path:cache"},
		},
		{
			Path:           "app/Services/SupermemoryClient.php",
			Score:          3,
			ContentRisk:    6,
			EvidenceLayers: []string{"content-risk", "sdk-dx"},
			Reasons:        []string{"content:reliability_policy_boundary:3"},
		},
	})

	for _, lane := range plan {
		if contains(lane.Files, "docs/archive/testing/removed_test.go.archive") ||
			contains(lane.Files, "internal/service/history_test.go") ||
			contains(lane.Files, "config/cache.php") {
			t.Fatalf("review plan should not include separated docs, history-only tests, or weak production rows: %#v", plan)
		}
	}
	if !reviewPlanContainsFile(plan, "app/Services/SupermemoryClient.php") {
		t.Fatalf("review plan should retain score-3 alternate production row: %#v", plan)
	}
	testLane := findReviewLane(plan, "test-risk")
	if testLane.Lane == "" || !contains(testLane.Files, "internal/service/flaky_test.go") {
		t.Fatalf("review plan should retain explicit test-risk row: %#v", plan)
	}
	for _, lane := range plan {
		if lane.Lane != "test-risk" && contains(lane.Files, "internal/service/flaky_test.go") {
			t.Fatalf("test-risk row should not pollute production review lane %q: %#v", lane.Lane, plan)
		}
	}
}

func TestReviewLaneRulesAvoidSchemaAndChurnFalsePositives(t *testing.T) {
	migration := FileEvidence{
		Path:                "migrations/001_initial_schema.sql",
		Score:               5,
		MigrationSafetyRisk: 6,
		EvidenceLayers:      []string{"migration-safety", "size"},
	}
	if !isDataIntegrityRow(migration) {
		t.Fatal("migration row should be data-integrity")
	}
	if isAPIContractRow(migration) {
		t.Fatal("database schema migration should not be an API contract row")
	}

	churnedCommand := FileEvidence{
		Path:           "cmd/create.go",
		Score:          5,
		Churn:          144,
		EvidenceLayers: []string{"churn", "bugfix-history"},
	}
	if isDataIntegrityRow(churnedCommand) {
		t.Fatal("plain churn or bugfix history should not make a CLI file data-integrity")
	}
}

func TestCaveatDoesNotDowngradePremiumKeepRows(t *testing.T) {
	row := FileEvidence{
		Path:           "config/auth.php",
		Score:          4,
		PathRisk:       8,
		ContentRisk:    8,
		EvidenceLayers: []string{"path-risk", "content-risk"},
	}
	if !keepForPremium(row) || !needsMoreEvidence(row) {
		t.Fatalf("fixture should exercise premium keep plus lexical evidence overlap")
	}
	if got := caveatForRow(row); got != "" {
		t.Fatalf("premium keep row caveat = %q, want no needs-more-evidence caveat", got)
	}
}

func TestCaveatLabelsDetectorFixtureRows(t *testing.T) {
	row := FileEvidence{
		Path: "internal/slither/report_test.go",
		Reasons: []string{
			"content:unsafe_yaml_load:1",
			"content:prototype_pollution_request_merge:1",
			"content:credential_assignment_literal:1",
		},
		EvidenceLayers: []string{"content-risk", "secret-risk"},
	}
	if !isDetectorFixtureRow(row) {
		t.Fatal("expected detector fixture row")
	}
	if got := caveatForRow(row); !strings.Contains(got, "detector-like snippets") {
		t.Fatalf("detector fixture caveat = %q", got)
	}

	source := row
	source.Path = "internal/service/auth.go"
	if isDetectorFixtureRow(source) {
		t.Fatal("source file should not be detector fixture row")
	}
}

func TestBuildReviewPlanPreservesRankedFileOrderBeforeTruncation(t *testing.T) {
	rows := []FileEvidence{
		{Path: "migrations/001_initial_schema.sql", Score: 5, PathRisk: 9, MigrationSafetyRisk: 6, EvidenceLayers: []string{"path-risk", "migration-safety"}},
	}
	for i := 0; i < 14; i++ {
		rows = append(rows, FileEvidence{
			Path:                fmt.Sprintf("internal/database/alpha_%02d.go", i),
			Score:               5,
			PathRisk:            4,
			MigrationSafetyRisk: 6,
			EvidenceLayers:      []string{"path-risk", "migration-safety"},
		})
	}

	_, plan := BuildReviewPlan(rows)
	lane := findReviewLane(plan, "data-integrity")
	if lane.Lane == "" {
		t.Fatalf("missing data-integrity lane: %#v", plan)
	}
	if len(lane.Files) == 0 || lane.Files[0] != "migrations/001_initial_schema.sql" {
		t.Fatalf("data-integrity files should preserve ranked order before truncation: %#v", lane.Files)
	}
	if len(lane.Files) != 12 {
		t.Fatalf("data-integrity lane should still cap displayed files at 12: %#v", lane.Files)
	}
}

func TestBuildReviewPlanCarriesDisplayedFileVerifyCommands(t *testing.T) {
	sqlVerify := `psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_initial_schema.sql`
	_, plan := BuildReviewPlan([]FileEvidence{{
		Path:                "migrations/001_initial_schema.sql",
		Score:               5,
		MigrationSafetyRisk: 6,
		EvidenceLayers:      []string{"migration-safety"},
		VerifyCmd:           sqlVerify,
	}})

	lane := findReviewLane(plan, "data-integrity")
	if lane.Lane == "" {
		t.Fatalf("missing data-integrity lane: %#v", plan)
	}
	if !contains(lane.Verify, "go test ./...") || !contains(lane.Verify, sqlVerify) {
		t.Fatalf("data-integrity lane should include generic and file-specific verify commands: %#v", lane.Verify)
	}
}

func TestBuildReviewPlanUsesRepoNativeVerifyCommands(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "composer.json", `{"scripts":{"test":["@php artisan test"]}}`)
	writeFile(t, tmp, "package.json", `{"scripts":{"build":"vite build"}}`)
	writeFile(t, tmp, "phpunit.xml", `<phpunit/>`)

	if got := verifyCmdForPathInRepo(tmp, ".env.example"); got != "composer test" {
		t.Fatalf("env verify command = %q", got)
	}
	if got := verifyCmdForPathInRepo(tmp, "app/Services/SupermemoryClient.php"); got != "php -l app/Services/SupermemoryClient.php" {
		t.Fatalf("php verify command = %q", got)
	}
	if got := verifyCmdForPathInRepo(tmp, "README.md"); got != "composer test" {
		t.Fatalf("README verify command = %q", got)
	}
	if got := verifyCmdForPathInRepo(tmp, "composer.json"); got != "composer validate --strict" {
		t.Fatalf("composer verify command = %q", got)
	}
	if got := verifyCmdForPathInRepo(tmp, "package-lock.json"); got != "npm run build" {
		t.Fatalf("package verify command = %q", got)
	}
	if got := verifyCmdForPathInRepo(tmp, "docs/remote-eval-report.html"); got != "" {
		t.Fatalf("generated report verify command = %q, want empty", got)
	}
	goRepo := t.TempDir()
	writeFile(t, goRepo, "go.mod", "module example.test/slitherfixture\n")
	writeFile(t, goRepo, "internal/config/defaults/config.yaml", "model: test\n")
	writeFile(t, goRepo, "internal/config/config.go", "package config\n")
	if got := verifyCmdForPathInRepo(goRepo, "internal/config/defaults/config.yaml"); got != "go test ./internal/config/..." {
		t.Fatalf("go config verify command = %q", got)
	}
	if got := verifyCmdForPathInRepo(goRepo, "runs/run-label.sh"); got != "bash -n runs/run-label.sh" {
		t.Fatalf("shell verify command = %q", got)
	}

	_, plan := BuildReviewPlanForRepo(tmp, []FileEvidence{{
		Path:           "app/Services/SupermemoryClient.php",
		Score:          5,
		ContentRisk:    6,
		EvidenceLayers: []string{"content-risk", "sdk-dx"},
		Reasons:        []string{"content:shell_boundary:1"},
		VerifyCmd:      verifyCmdForPathInRepo(tmp, "app/Services/SupermemoryClient.php"),
	}})
	lane := findReviewLane(plan, "error-handling")
	if lane.Lane == "" {
		t.Fatalf("missing error-handling lane: %#v", plan)
	}
	if contains(lane.Verify, "go vet ./...") || contains(lane.Verify, "go test ./...") {
		t.Fatalf("PHP repo lane should not carry Go verify commands: %#v", lane.Verify)
	}
	if !contains(lane.Verify, "composer validate --strict") || !contains(lane.Verify, "composer test") || !contains(lane.Verify, "php -l app/Services/SupermemoryClient.php") {
		t.Fatalf("PHP repo lane verify commands = %#v", lane.Verify)
	}
}

func TestRenderJSONIncludesCullLedger(t *testing.T) {
	report := Report{
		Repo:      "/repo",
		FilesSeen: 1,
		Rows: []FileEvidence{{
			Path:           "auth.go",
			Score:          5,
			EvidenceClass:  "heuristic",
			Confidence:     "high",
			VerifyCmd:      "go test ./...",
			PathRisk:       5,
			ContentRisk:    5,
			EvidenceLayers: []string{"path-risk", "content-risk", "test-void"},
		}},
	}
	ledger := BuildCullLedger(report)
	report.CullLedger = &ledger
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		CullLedger *CullLedger `json:"cull_ledger"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CullLedger == nil || payload.CullLedger.KeptForPremium.Count != 1 {
		t.Fatalf("missing cull ledger: %s", data)
	}
	if got := payload.CullLedger.KeptForPremium.Examples[0].StrongestEvidenceIntersection; got == "" || got == "unknown" {
		t.Fatalf("strongest intersection = %q, want concrete evidence", got)
	}
	if payload.CullLedger.KeptForPremium.Examples[0].Confidence != "high" {
		t.Fatalf("cull example missing confidence: %#v", payload.CullLedger.KeptForPremium.Examples[0])
	}
	if len(payload.CullLedger.ReviewPlan) == 0 {
		t.Fatalf("cull ledger missing review plan: %#v", payload.CullLedger)
	}
}

func TestRenderJSONIncludesRowCullDispositionsWhenCullLedgerPresent(t *testing.T) {
	report := Report{Repo: "/repo", Rows: []FileEvidence{
		{Path: "auth.go", Score: 5, PathRisk: 5, ContentRisk: 5, EvidenceLayers: []string{"path-risk", "content-risk", "test-void"}},
		{Path: "api/generated.pb.go", Score: 5, EvidenceLayers: []string{"content-risk"}},
		{Path: "README.md", Score: 5, EvidenceLayers: []string{"path-risk"}},
		{Path: "auth_test.go", Score: 3, EvidenceLayers: []string{"content-risk"}},
		{Path: "weak.go", Score: 3, ContentRisk: 5, EvidenceLayers: []string{"content-risk"}},
		{Path: "config.go", Score: 3, ContentRisk: 1, EnvContractRisk: 3, EvidenceLayers: []string{"path-risk", "env-contract"}},
		{Path: "config_copy.go", Score: 2, EvidenceLayers: []string{"path-risk", "env-contract"}},
		{Path: "notes.go", Score: 1, EvidenceLayers: []string{"low-signal"}},
	}}
	ledger := BuildCullLedger(report)
	report.CullLedger = &ledger

	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Rows []FileEvidence `json:"rows"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}

	assertDisposition := func(path string, decision CullDecision, actionability Actionability, reasonPart string) {
		t.Helper()
		row := findPayloadRow(payload.Rows, path)
		if row == nil {
			t.Fatalf("missing row %s in payload rows %#v", path, payload.Rows)
		}
		if row.CullDecision != decision {
			t.Fatalf("%s cull_decision = %q, want %q", path, row.CullDecision, decision)
		}
		if row.Actionability != actionability {
			t.Fatalf("%s actionability = %q, want %q", path, row.Actionability, actionability)
		}
		if !strings.Contains(row.CullReason, reasonPart) {
			t.Fatalf("%s cull_reason = %q, want substring %q", path, row.CullReason, reasonPart)
		}
	}

	assertDisposition("auth.go", CullDecisionKeptForPremium, ActionabilityInspect, "strong multi-layer seed")
	assertDisposition("api/generated.pb.go", CullDecisionGenerated, ActionabilityVerifyFirst, "generated")
	assertDisposition("README.md", CullDecisionDocumentation, ActionabilityVerifyFirst, "documentation")
	assertDisposition("auth_test.go", CullDecisionTestOnly, ActionabilityVerifyFirst, "test or fixture")
	assertDisposition("weak.go", CullDecisionNeedsEvidence, ActionabilityVerifyFirst, "needs corroboration")
	assertDisposition("config.go", CullDecisionAlternates, ActionabilityInspect, "budget remains")
	assertDisposition("config_copy.go", CullDecisionDuplicate, ActionabilityVerifyFirst, "same evidence surface")
	assertDisposition("notes.go", CullDecisionLowSignal, ActionabilityVerifyFirst, "low score")
}

func TestBuildCullLedgerDowngradesCulledExampleActionability(t *testing.T) {
	report := Report{Repo: "/repo", Rows: []FileEvidence{
		{Path: "internal/strong.go", Score: 5, Actionability: ActionabilityInspect, ContentRisk: 5, CochangeRisk: 5, EvidenceLayers: []string{"content-risk", "cochange"}},
		{Path: "internal/duplicate.go", Score: 3, Actionability: ActionabilityInspect, ContentRisk: 5, EvidenceLayers: []string{"content-risk", "cochange"}},
	}}
	ledger := BuildCullLedger(report)
	if len(ledger.Duplicate.Examples) != 1 {
		t.Fatalf("duplicate examples = %#v, want one duplicate example", ledger.Duplicate.Examples)
	}
	if got := ledger.Duplicate.Examples[0].Actionability; got != ActionabilityVerifyFirst {
		t.Fatalf("duplicate example actionability = %q, want %q", got, ActionabilityVerifyFirst)
	}
}

func TestBuildCullLedgerBucketsRows(t *testing.T) {
	report := Report{Repo: "/repo", Rows: []FileEvidence{
		{Path: "auth.go", Score: 5, PathRisk: 5, ContentRisk: 5, EvidenceLayers: []string{"path-risk", "content-risk", "test-void"}},
		{Path: "api/generated.pb.go", Score: 5, EvidenceLayers: []string{"content-risk"}},
		{Path: "auth_test.go", Score: 3, EvidenceLayers: []string{"content-risk"}},
		{Path: "weak.go", Score: 3, ContentRisk: 5, EvidenceLayers: []string{"content-risk"}},
		{Path: "config.go", Score: 3, ContentRisk: 1, EnvContractRisk: 3, EvidenceLayers: []string{"path-risk", "env-contract"}},
		{Path: "config_copy.go", Score: 2, EvidenceLayers: []string{"path-risk", "env-contract"}},
		{Path: "notes.go", Score: 1, EvidenceLayers: []string{"low-signal"}},
	}}

	ledger := BuildCullLedger(report)
	if ledger.KeptForPremium.Count != 1 {
		t.Fatalf("kept count = %d, want 1: %#v", ledger.KeptForPremium.Count, ledger)
	}
	if ledger.Generated.Count != 1 || ledger.TestOnly.Count != 1 || ledger.NeedsEvidence.Count != 1 || ledger.Alternates.Count != 1 || ledger.Duplicate.Count != 1 || ledger.LowSignal.Count != 1 {
		t.Fatalf("unexpected bucket counts: %#v", ledger)
	}
	for _, bucket := range []CullBucket{ledger.KeptForPremium, ledger.Generated, ledger.TestOnly, ledger.NeedsEvidence, ledger.Alternates, ledger.Duplicate, ledger.LowSignal} {
		if bucket.Count > 0 && len(bucket.Examples) == 0 {
			t.Fatalf("bucket missing examples: %#v", bucket)
		}
	}
}

func TestRowsWithCullDispositionsDowngradesCulledActionability(t *testing.T) {
	rows := []FileEvidence{
		{Path: "internal/strong.go", Score: 5, Actionability: ActionabilityLikelyDefect, ContentRisk: 5, WorkflowSecurityRisk: 5, EvidenceLayers: []string{"content-risk", "workflow-security"}},
		{Path: "internal/duplicate.go", Score: 3, Actionability: ActionabilityInspect, ContentRisk: 5, EvidenceLayers: []string{"content-risk", "workflow-security"}},
		{Path: "internal/hot.go", Score: 1, Actionability: ActionabilityHotspot, HotspotRisk: 3, EvidenceLayers: []string{"hotspot"}},
	}
	withCull := rowsWithCullDispositions(rows)
	if withCull[0].Actionability != ActionabilityLikelyDefect || withCull[0].CullDecision != CullDecisionKeptForPremium {
		t.Fatalf("kept row = %#v, want intrinsic likely defect", withCull[0])
	}
	for _, row := range withCull[1:] {
		if row.Actionability != ActionabilityVerifyFirst {
			t.Fatalf("culled row actionability = %q for %#v, want verify_first", row.Actionability, row)
		}
	}
}

func TestRowsWithCullDispositionsMarksPremiumOverflowAsAlternate(t *testing.T) {
	var rows []FileEvidence
	for i := 0; i < maxPremiumCullSeeds+1; i++ {
		rows = append(rows, FileEvidence{
			Path:           fmt.Sprintf("internal/pkg/file_%02d.go", i),
			Score:          5,
			SeedScore:      float64(maxPremiumCullSeeds + 1 - i),
			ContentRisk:    5,
			UnknownsRisk:   3,
			EvidenceLayers: []string{"content-risk", "unknowns", "hotspot"},
		})
	}

	withDispositions := rowsWithCullDispositions(rows)
	kept := 0
	overflow := 0
	for _, row := range withDispositions {
		switch row.CullDecision {
		case CullDecisionKeptForPremium:
			kept++
		case CullDecisionAlternates:
			overflow++
			if !strings.Contains(row.CullReason, "beyond kept_for_premium cap") {
				t.Fatalf("overflow reason = %q", row.CullReason)
			}
		default:
			t.Fatalf("unexpected disposition for premium candidate %#v", row)
		}
	}
	if kept != maxPremiumCullSeeds || overflow != 1 {
		t.Fatalf("kept=%d overflow=%d, want kept=%d overflow=1", kept, overflow, maxPremiumCullSeeds)
	}
}

func TestBuildCullLedgerDemotesGeneratedReportsAndHistoryOnlyTests(t *testing.T) {
	report := Report{Repo: "/repo", Rows: []FileEvidence{
		{Path: "internal/actions/digest/digest.go", Score: 5, ContentRisk: 5, CochangeRisk: 7, OwnershipRisk: 7, EvidenceLayers: []string{"content-risk", "cochange", "ownership"}},
		{Path: "docs/remote-eval-report.html", Score: 5, ContentRisk: 5, OwnershipRisk: 7, EvidenceLayers: []string{"content-risk", "ownership", "size", "churn"}},
		{Path: "data/me/research/tasks/web-cache-2026-02-26/lva-case-detail.html", Score: 5, ContentRisk: 5, EvidenceLayers: []string{"content-risk", "size"}},
		{Path: "prototypes/examples/variant-01/index.html", Score: 5, ContentRisk: 5, EvidenceLayers: []string{"content-risk", "size"}},
		{Path: "stubs/agent.stub", Score: 5, ContentRisk: 5, EvidenceLayers: []string{"content-risk"}},
		{Path: "database/.gitignore", Score: 5, PathRisk: 5, EvidenceLayers: []string{"path-risk"}},
		{Path: "internal/slither/patterns/triage_patterns.json", Score: 5, CochangeRisk: 7, OwnershipRisk: 7, EvidenceLayers: []string{"cochange", "ownership", "size"}},
		{Path: "docs/archive/testing/removed-tldr-command/tldr_command_test.go.archive", Score: 5, ContentRisk: 5, OwnershipRisk: 7, EvidenceLayers: []string{"content-risk", "ownership", "size", "churn"}},
		{Path: "internal/actions/digest/digest_test.go", Score: 5, ContentRisk: 5, CochangeRisk: 7, OwnershipRisk: 7, EvidenceLayers: []string{"content-risk", "cochange", "ownership"}},
		{Path: "internal/actions/flaky_test.go", Score: 5, ContentRisk: 5, FlakeRisk: 6, EvidenceLayers: []string{"content-risk", "flake-risk", "churn"}},
		{Path: "testdata/extraction/expected/chunk-006.json", Score: 4, CochangeRisk: 7, OwnershipRisk: 7, EvidenceLayers: []string{"cochange", "ownership", "churn"}},
	}}

	ledger := BuildCullLedger(report)
	if ledger.KeptForPremium.Count != 1 {
		t.Fatalf("kept count = %d, want only source row kept: %#v", ledger.KeptForPremium.Count, ledger)
	}
	if ledger.Generated.Count != 6 {
		t.Fatalf("generated count = %d, want docs HTML, data cache, prototype, stub, gitignore, and pattern-catalog artifacts generated bucket: %#v", ledger.Generated.Count, ledger)
	}
	if ledger.Documentation.Count != 1 {
		t.Fatalf("documentation count = %d, want docs archive bucket: %#v", ledger.Documentation.Count, ledger)
	}
	if ledger.TestOnly.Count != 3 {
		t.Fatalf("test-only count = %d, want test and testdata rows demoted: %#v", ledger.TestOnly.Count, ledger)
	}
	for _, entry := range ledger.KeptForPremium.Examples {
		if isTestPath(entry.Path) || isDocumentationPath(entry.Path) {
			t.Fatalf("test/doc row should not enter premium production seeds: %#v", ledger.KeptForPremium)
		}
	}
}

func TestBuildCullLedgerCapsPremiumSeeds(t *testing.T) {
	var rows []FileEvidence
	for i := 0; i < maxPremiumCullSeeds+2; i++ {
		rows = append(rows, FileEvidence{
			Path:           fmt.Sprintf("internal/pkg/file_%02d.go", i),
			Score:          5,
			ContentRisk:    5,
			UnknownsRisk:   3,
			EvidenceLayers: []string{"content-risk", "unknowns", "hotspot"},
		})
	}

	ledger := BuildCullLedger(Report{Repo: "/repo", Rows: rows})
	if ledger.KeptForPremium.Count != maxPremiumCullSeeds {
		t.Fatalf("kept count = %d, want cap %d", ledger.KeptForPremium.Count, maxPremiumCullSeeds)
	}
	if ledger.Alternates.Count != 2 {
		t.Fatalf("alternates count = %d, want overflow rows", ledger.Alternates.Count)
	}
}

func TestBuildCullLedgerSortsPremiumSeedsBeforeCap(t *testing.T) {
	var rows []FileEvidence
	for i := 0; i < maxPremiumCullSeeds; i++ {
		rows = append(rows, FileEvidence{
			Path:           fmt.Sprintf("internal/medium/file_%02d.go", i),
			Score:          4,
			SeedScore:      float64(maxPremiumCullSeeds - i),
			ContentRisk:    8,
			EvidenceLayers: []string{"content-risk", "unknowns"},
			Confidence:     "medium",
		})
	}
	rows = append(rows, FileEvidence{
		Path:           "internal/high/late.go",
		Score:          5,
		SeedScore:      1,
		ContentRisk:    20,
		HotspotRisk:    4,
		EvidenceLayers: []string{"content-risk", "unknowns", "hotspot"},
		Confidence:     "high",
	})

	ledger := BuildCullLedger(Report{Repo: "/repo", Rows: rows})
	if ledger.KeptForPremium.Count != maxPremiumCullSeeds || ledger.Alternates.Count != 1 {
		t.Fatalf("unexpected bucket counts: %#v", ledger)
	}
	if ledger.KeptForPremium.Examples[0].Path != "internal/high/late.go" {
		t.Fatalf("late high-confidence row should lead premium seeds: %#v", ledger.KeptForPremium.Examples[:3])
	}
	for _, entry := range ledger.Alternates.Examples {
		if entry.Path == "internal/high/late.go" {
			t.Fatalf("late high-confidence row should not be overflow alternate: %#v", ledger.Alternates)
		}
	}
}

func TestConfidenceForRowCalibratesDeterministicEvidence(t *testing.T) {
	strongSource := FileEvidence{
		Path:           "internal/storage/sqlite.go",
		Score:          5,
		ContentRisk:    18,
		HotspotRisk:    4,
		EvidenceLayers: []string{"content-risk", "unknowns", "hotspot"},
	}
	if got := confidenceForRow(strongSource); got != "high" {
		t.Fatalf("strong source confidence = %q, want high", got)
	}

	generated := strongSource
	generated.Path = "api/generated.pb.go"
	if got := confidenceForRow(generated); got != "low" {
		t.Fatalf("generated confidence = %q, want low", got)
	}

	testOnly := strongSource
	testOnly.Path = "internal/storage/sqlite_test.go"
	testOnly.FlakeRisk = 0
	testOnly.OracleRisk = 0
	if got := confidenceForRow(testOnly); got != "low" {
		t.Fatalf("test-only confidence = %q, want low", got)
	}

	premiumButNotStrong := FileEvidence{
		Path:           "internal/storage/store.go",
		Score:          4,
		ContentRisk:    8,
		EvidenceLayers: []string{"content-risk", "unknowns"},
	}
	if got := confidenceForRow(premiumButNotStrong); got != "medium" {
		t.Fatalf("premium-but-not-strong confidence = %q, want medium", got)
	}
}

func TestOwnershipRiskRequiresCorroboratedPressure(t *testing.T) {
	if score, reasons := ownershipRisk(ownershipInfo{AuthorCount: 1, Touches: 1, TopShare: 1}, 0, 0, 2, 4); score != 0 || len(reasons) != 0 {
		t.Fatalf("single-touch ownership score = %d, reasons = %#v; want no risk", score, reasons)
	}

	score, reasons := ownershipRisk(ownershipInfo{AuthorCount: 1, Touches: 6, TopShare: 1}, 140, 0, 0, 0)
	if score != 3 || !contains(reasons, "ownership:risky_single_author") || !contains(reasons, "ownership:concentrated_touches:6") {
		t.Fatalf("repeated solo ownership score = %d, reasons = %#v; want weak corroborated signal", score, reasons)
	}
	if contains(reasons, "ownership:top_author_share:1") {
		t.Fatalf("single-author ownership reasons = %#v, want no redundant top-share reason", reasons)
	}

	score, reasons = ownershipRisk(ownershipInfo{AuthorCount: 2, Touches: 12, TopShare: 0.85}, 130, 0, 0, 0)
	if score != 7 || !contains(reasons, "ownership:top_author_share:0.85") {
		t.Fatalf("two-author concentration score = %d, reasons = %#v; want stronger ownership risk", score, reasons)
	}
}

func TestCochangeRiskRequiresMeaningfulRelationship(t *testing.T) {
	if score, reasons := cochangeRisk(cochangeInfo{PartnerCount: 1, MaxJaccard: 0.21}, 700, 4, 0, 12, 0); score != 0 || len(reasons) != 0 {
		t.Fatalf("weak one-partner cochange score = %d, reasons = %#v; want no cochange risk", score, reasons)
	}

	score, reasons := cochangeRisk(cochangeInfo{PartnerCount: 1, MaxJaccard: 0.36}, 700, 4, 0, 12, 0)
	if score == 0 || !contains(reasons, "cochange:max_jaccard:0.36") {
		t.Fatalf("single strong cochange score = %d, reasons = %#v; want retained cochange risk", score, reasons)
	}

	score, reasons = cochangeRisk(cochangeInfo{PartnerCount: 3, MaxJaccard: 0.22}, 700, 4, 0, 12, 0)
	if score == 0 || !contains(reasons, "cochange:partners:3") {
		t.Fatalf("multi-partner cochange score = %d, reasons = %#v; want retained cochange risk", score, reasons)
	}
}

func TestSortReportRowsPrioritizesScoreAndConfidenceBeforeSeedScore(t *testing.T) {
	rows := []FileEvidence{
		{Path: "config.go", Score: 4, SeedScore: 3.0, Confidence: "medium"},
		{Path: "storage.go", Score: 5, SeedScore: 2.0, Confidence: "high"},
		{Path: "cli.go", Score: 5, SeedScore: 2.5, Confidence: "medium"},
	}

	sortReportRows(rows)
	got := []string{rows[0].Path, rows[1].Path, rows[2].Path}
	want := []string{"storage.go", "cli.go", "config.go"}
	if !slicesEqual(got, want) {
		t.Fatalf("sorted rows = %#v, want %#v", got, want)
	}
}

func TestSelectRowsForTopKeepsRequestedProductionRows(t *testing.T) {
	rows := []FileEvidence{
		{Path: "docs/archive/old_test.go.archive", Score: 5, ContentRisk: 20, EvidenceLayers: []string{"content-risk", "size"}},
		{Path: "internal/a.go", Score: 5, ContentRisk: 20, EvidenceLayers: []string{"content-risk", "hotspot"}},
		{Path: "internal/a_test.go", Score: 5, FlakeRisk: 6, EvidenceLayers: []string{"flake-risk", "churn"}},
		{Path: "internal/b.go", Score: 5, ContentRisk: 18, EvidenceLayers: []string{"content-risk", "unknowns"}},
		{Path: "internal/c.go", Score: 5, ContentRisk: 16, EvidenceLayers: []string{"content-risk", "churn"}},
		{Path: "internal/d.go", Score: 5, ContentRisk: 14, EvidenceLayers: []string{"content-risk", "ownership"}},
	}

	selected := selectRowsForTop(rows, 3)
	if got := len(rankedMarkdownRows(selected)); got != 3 {
		t.Fatalf("ranked rows = %d, want requested production top count; selected=%#v", got, selected)
	}
	if !containsPath(selected, "docs/archive/old_test.go.archive") || !containsPath(selected, "internal/a_test.go") {
		t.Fatalf("separated high-signal rows before cutoff should be retained: %#v", selected)
	}
	if containsPath(selected, "internal/d.go") {
		t.Fatalf("production row beyond requested top should be omitted: %#v", selected)
	}
}

func TestRankedMarkdownRowsExcludeLowSignalCompletenessRows(t *testing.T) {
	rows := []FileEvidence{
		{Path: "config/auth.php", Score: 4, PathRisk: 8, ContentRisk: 8, EvidenceLayers: []string{"path-risk", "content-risk"}},
		{Path: "internal/a.go", Score: 3, ContentRisk: 6, EvidenceLayers: []string{"content-risk", "sdk-dx"}},
		{Path: "config/cache.php", Score: 2, PathRisk: 6, EvidenceLayers: []string{"path-risk"}},
		{Path: "internal/slither/patterns/triage_patterns.json", Score: 5, CochangeRisk: 8, OwnershipRisk: 4, EvidenceLayers: []string{"cochange", "ownership", "size"}},
		{Path: ".gitignore", Score: 1, EvidenceLayers: []string{"low-signal"}},
		{Path: "artisan", Score: 1, EvidenceLayers: []string{"low-signal"}},
	}
	ranked := rankedMarkdownRows(rows)
	if !containsPath(ranked, "config/auth.php") || !containsPath(ranked, "internal/a.go") {
		t.Fatalf("ranked rows = %#v, want premium keep and score-3 alternate rows", ranked)
	}
	if containsPath(ranked, "config/cache.php") ||
		containsPath(ranked, "internal/slither/patterns/triage_patterns.json") ||
		containsPath(ranked, ".gitignore") ||
		containsPath(ranked, "artisan") {
		t.Fatalf("ranked rows should exclude weak score-2, support-artifact, and low-signal rows: %#v", ranked)
	}

	selected := selectRowsForTop(rows, 80)
	if !containsPath(selected, ".gitignore") || !containsPath(selected, "artisan") {
		t.Fatalf("low-signal rows should remain in reported evidence when ranked rows are below top: %#v", selected)
	}
}

func TestSeedScoreDemotesGeneratedAndTestOnlyArtifacts(t *testing.T) {
	source := FileEvidence{Path: "internal/actions/digest/digest.go", Score: 5, ContentRisk: 5, CochangeRisk: 7, OwnershipRisk: 7, EvidenceLayers: []string{"content-risk", "cochange", "ownership"}}
	generated := source
	generated.Path = "docs/extraction-scoreboard.json"
	testOnly := source
	testOnly.Path = "internal/actions/digest/digest_test.go"

	sourceScore := seedScore(source)
	if generatedScore := seedScore(generated); generatedScore >= sourceScore {
		t.Fatalf("generated score = %.2f, want below source score %.2f", generatedScore, sourceScore)
	}
	if testScore := seedScore(testOnly); testScore >= sourceScore {
		t.Fatalf("test score = %.2f, want below source score %.2f", testScore, sourceScore)
	}
	if verifyCmdForPath("testdata/extraction/expected/chunk-006.json") != "go test ./..." {
		t.Fatalf("testdata verify command = %q, want repo-level go test", verifyCmdForPath("testdata/extraction/expected/chunk-006.json"))
	}
	if got := verifyCmdForPath("internal/slither/patterns/triage_patterns.json"); got != "" {
		t.Fatalf("pattern catalog verify command = %q, want empty generated/support command", got)
	}
	if got := verifyCmdForPath("migrations/001_initial_schema.sql"); got != `psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_initial_schema.sql` {
		t.Fatalf("sql migration verify command = %q", got)
	}
	if got := verifyCmdForPath("data/scripts/gene_cache_fetch.ts"); got != "bun build --no-bundle --outfile /tmp/slither-bun-check.js data/scripts/gene_cache_fetch.ts" {
		t.Fatalf("package-less TypeScript verify command = %q", got)
	}
}

func reviewQueueHasGroup(queue []ReviewQueue, group string) bool {
	for _, item := range queue {
		if item.Group == group {
			return true
		}
	}
	return false
}

func reviewPlanHasLane(plan []ReviewLane, lane string) bool {
	return findReviewLane(plan, lane).Lane != ""
}

func findReviewLane(plan []ReviewLane, lane string) ReviewLane {
	for _, item := range plan {
		if item.Lane == lane {
			return item
		}
	}
	return ReviewLane{}
}

func reviewPlanContainsFile(plan []ReviewLane, path string) bool {
	for _, lane := range plan {
		if contains(lane.Files, path) {
			return true
		}
	}
	return false
}

func TestNormalizeReportArgsAllowsFlagsAfterRepo(t *testing.T) {
	got := normalizeReportArgs([]string{".", "--out", "report.md", "--top=5", "--days", "30", "--patterns", "patterns.json", "--local"})
	want := []string{"--out", "report.md", "--top=5", "--days", "30", "--patterns", "patterns.json", "--local", "."}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("normalizeReportArgs = %#v, want %#v", got, want)
	}
}

func TestRunReportHelpReturnsNil(t *testing.T) {
	var stdout strings.Builder
	if err := Run(context.Background(), []string{"report", "--help"}, &stdout, &strings.Builder{}); err != nil {
		t.Fatalf("Run report --help error = %v", err)
	}
	help := stdout.String()
	for _, want := range []string{
		"slither report",
		fmt.Sprintf("--top %d", defaultTop),
		fmt.Sprintf("--max-bytes %d", defaultMaxBytes),
		fmt.Sprintf("--days %d", defaultDays),
		defaultConfig().BaseURL,
		defaultConfig().Local.Model,
		defaultConfig().Local.BaseURL,
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help output missing %q: %s", want, help)
		}
	}
}

func TestRunVersionBuildReportsProvenance(t *testing.T) {
	var stdout strings.Builder
	if err := Run(context.Background(), []string{"version", "--build"}, &stdout, &strings.Builder{}); err != nil {
		t.Fatalf("Run version --build error = %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"slither ", "module:", "modified:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("version --build missing %q: %s", want, got)
		}
	}
}

func TestRunReportCompletionReportsRowsAndRankedFiles(t *testing.T) {
	setTempConfigDir(t)
	tmp := t.TempDir()
	out := filepath.Join(tmp, "report.md")
	writeFile(t, tmp, "internal/a.go", "package internal\n\nfunc A(){ panic(\"x\") }\n")

	var stdout strings.Builder
	err := Run(context.Background(), []string{"report", tmp, "--out", out, "--top", "1"}, &stdout, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if !strings.Contains(got, "report rows") || !strings.Contains(got, "ranked files") {
		t.Fatalf("completion output should distinguish report rows from ranked files: %q", got)
	}
}

func TestRunReportDefaultsToDeterministicFallback(t *testing.T) {
	setTempConfigDir(t)
	tmp := t.TempDir()
	writeFile(t, tmp, "internal/a.go", "package internal\n\nfunc A(){ panic(\"x\") }\n")

	var stdout strings.Builder
	err := Run(context.Background(), []string{"report", tmp, "--json", "--out", "-"}, &stdout, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	var payload reportEnvelope
	if err := json.Unmarshal([]byte(stdout.String()), &payload); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, stdout.String())
	}
	if payload.Model != "" {
		t.Fatalf("default report should not configure model scoring; model=%q", payload.Model)
	}
	if payload.BaseURL != "" {
		t.Fatalf("default report should not emit an inactive model base URL; base_url=%q", payload.BaseURL)
	}
	if !contains(payload.SkippedSignals, "model_scoring:not_configured") {
		t.Fatalf("skipped signals = %#v, want deterministic fallback signal", payload.SkippedSignals)
	}
}

func TestDocsUseRuntimeDefaults(t *testing.T) {
	docs := readTextFile(t, filepath.Join("..", "..", "docs", "usage.md"))
	for _, want := range []string{
		fmt.Sprintf("| `--top` | `%d` |", defaultTop),
		fmt.Sprintf("| `--max-bytes` | `%d` |", defaultMaxBytes),
		fmt.Sprintf("| `--days` | `%d` |", defaultDays),
		fmt.Sprintf("--top %d", defaultTop),
	} {
		if !strings.Contains(docs, want) {
			t.Fatalf("docs/usage.md missing %q", want)
		}
	}
	for _, stale := range []string{"| `--top` | `30` |", "| `--max-bytes` | `20000` |", "--top 30"} {
		if strings.Contains(docs, stale) {
			t.Fatalf("docs/usage.md still contains stale default %q", stale)
		}
	}

	readme := readTextFile(t, filepath.Join("..", "..", "README.md"))
	if !strings.Contains(readme, fmt.Sprintf("--top %d", defaultTop)) {
		t.Fatalf("README.md missing runtime top default %d", defaultTop)
	}
}

func TestParseModelScoresExtractsArray(t *testing.T) {
	got, err := parseModelScores("sure\n[{\"index\":0,\"score\":4,\"summary\":\"hot\",\"reasons\":[\"auth\"]}]")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Index != 0 || got[0].Score != 4 || got[0].Summary != "hot" || len(got[0].Reasons) != 1 {
		t.Fatalf("unexpected scores: %#v", got)
	}
}

func TestContentRiskIgnoresDetectorLiterals(t *testing.T) {
	patterns, err := loadScoringPatterns("")
	if err != nil {
		t.Fatal(err)
	}

	// Detector-literal text must yield no content reasons.
	detectorText := "var fallbackpathterms = true\n" +
		"var pattern( = true\n" +
		"func example() {\n" +
		"  _ = fallbackcontentterms\n" +
		"}\n"
	score, reasons := contentRisk(patterns, "scan.go", detectorText)
	if score != 0 {
		t.Fatalf("detector literal score = %d, want 0; reasons=%#v", score, reasons)
	}
	if len(reasons) != 0 {
		t.Fatalf("detector literals produced reasons = %#v, want none", reasons)
	}

	// Genuine secret line must still be detected.
	tokenText := "package main\nconst k = \"sk-aaaaaaaaaaaaaaaaaaaaaaaa\"\n"
	tokenScore, tokenReasons := contentRisk(patterns, "main.go", tokenText)
	if tokenScore == 0 {
		t.Fatalf("token literal score = 0, want > 0; reasons=%#v", tokenReasons)
	}
	found := false
	for _, r := range tokenReasons {
		if strings.HasPrefix(r, "content:provider_token_literal:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected content:provider_token_literal: reason; got %#v", tokenReasons)
	}
}

func TestEmbeddedPatternsDoNotOverclaimEmbeddingPersistence(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "internal/actions/embed.go", `package actions

type Store interface {
	SaveEmbedding(id string, vector []float32, version string) error
}

func EmbedVersion(model string) string { return model }

func PersistEmbedding(store Store, id string, embedding []float32, version string) error {
	return store.SaveEmbedding(id, embedding, version)
}
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "internal/actions/embed.go")
	if row == nil {
		t.Fatal("missing embed row")
	}
	for _, reason := range row.Reasons {
		if strings.Contains(reason, "embedding_without_persistence") {
			t.Fatalf("reasons overclaim missing persistence: %#v", row.Reasons)
		}
	}
	if !containsReasonPrefix(row.Reasons, "content:vector_embedding_surface:") {
		t.Fatalf("reasons missing calibrated vector surface signal: %#v", row.Reasons)
	}
}

func TestLoopedIOPatternRequiresCallInsideLoop(t *testing.T) {
	patterns, err := loadScoringPatterns("")
	if err != nil {
		t.Fatal(err)
	}

	configLike := `package config

type QueryConfig struct{ Terms map[string]string }

func ensureDefaults(cfg QueryConfig, defaults QueryConfig) {
	for key, entry := range defaults.Terms {
		if _, ok := cfg.Terms[key]; !ok {
			cfg.Terms[key] = entry
		}
	}
}
`
	_, reasons := contentRisk(patterns, "internal/config/config.go", configLike)
	if containsReasonPrefix(reasons, "content:looped_io_or_query:") {
		t.Fatalf("config-like loop should not be looped I/O: %#v", reasons)
	}

	loopedQuery := `package storage

func hydrate(ids []string, db interface{ QueryContext(any, string, ...any) (any, error) }) error {
	for _, id := range ids {
		if _, err := db.QueryContext(nil, "SELECT * FROM entries WHERE id = ?", id); err != nil {
			return err
		}
	}
	return nil
}
`
	_, reasons = contentRisk(patterns, "internal/storage/store.go", loopedQuery)
	if !containsReasonPrefix(reasons, "content:looped_io_or_query:") {
		t.Fatalf("query call inside loop should be looped I/O: %#v", reasons)
	}
}

func TestErrorContextDroppedIgnoresWrappedFmtErrorf(t *testing.T) {
	patterns, err := loadScoringPatterns("")
	if err != nil {
		t.Fatal(err)
	}

	wrapped := `package scan

func inspect(rel string, err error) error {
	return fmt.Errorf("stat %s: %w", rel, err)
}
`
	_, reasons := contentRisk(patterns, "internal/slither/scan.go", wrapped)
	if containsReasonPrefix(reasons, "content:error_context_dropped:") {
		t.Fatalf("wrapped %%w error should not be reported as dropped context: %#v", reasons)
	}

	unwrapped := `package scan

func inspect(rel string, err error) error {
	return fmt.Errorf("stat %s: %v", rel, err)
}
`
	_, reasons = contentRisk(patterns, "internal/slither/scan.go", unwrapped)
	if !containsReasonPrefix(reasons, "content:error_context_dropped:") {
		t.Fatalf("unwrapped fmt.Errorf error should be reported: %#v", reasons)
	}
}

func TestNestedLoopRiskRequiresActualNestedLoop(t *testing.T) {
	separateLoops := `package config

func ensureDefaults(a, b map[string]string) {
	for key, value := range b {
		a[key] = value
	}
	for key, value := range a {
		_ = key + value
	}
}
`
	if score, reasons := unknownsRisk("internal/config/config.go", separateLoops); score != 0 || containsReasonPrefix(reasons, "unknowns:nested_loop_scale:") {
		t.Fatalf("separate loops should not be nested-loop risk: score=%d reasons=%#v", score, reasons)
	}

	nestedLoop := `package storage

func pairs(groups [][]string) int {
	count := 0
	for _, group := range groups {
		for _, item := range group {
			if item != "" {
				count++
			}
		}
	}
	return count
}
`
	if score, reasons := unknownsRisk("internal/storage/store.go", nestedLoop); score == 0 || !containsReasonPrefix(reasons, "unknowns:nested_loop_scale:") {
		t.Fatalf("actual nested loop should be nested-loop risk: score=%d reasons=%#v", score, reasons)
	}
}

func TestUnknownsRiskFlagsRecursiveControlFlow(t *testing.T) {
	recursive := `package graph

func walk(node *Node) int {
	if node == nil {
		return 0
	}
	return 1 + walk(node.Next)
}
`
	if score, reasons := unknownsRisk("internal/graph/walk.go", recursive); score == 0 || !containsReasonPrefix(reasons, "unknowns:recursive_control_flow:") {
		t.Fatalf("recursive function should be recursion risk: score=%d reasons=%#v", score, reasons)
	}

	nonRecursive := `package graph

func walk(node *Node) int {
	return walkNode(node)
}
`
	if score, reasons := unknownsRisk("internal/graph/walk.go", nonRecursive); containsReasonPrefix(reasons, "unknowns:recursive_control_flow:") {
		t.Fatalf("non-recursive helper call should not be recursion risk: score=%d reasons=%#v", score, reasons)
	}

	selectorCall := `package db

type DB struct{ inner interface{ Close() error } }

func (db *DB) Close() error {
	if db.inner != nil {
		return db.inner.Close()
	}
	return nil
}
`
	if score, reasons := unknownsRisk("internal/db/db.go", selectorCall); containsReasonPrefix(reasons, "unknowns:recursive_control_flow:") {
		t.Fatalf("selector call with same method name should not be recursion risk: score=%d reasons=%#v", score, reasons)
	}

	recursiveMethod := `package graph

type Node struct{ Next *Node }

func (node *Node) Walk() int {
	if node == nil {
		return 0
	}
	return 1 + node.Walk()
}
`
	if score, reasons := unknownsRisk("internal/graph/node.go", recursiveMethod); score == 0 || !containsReasonPrefix(reasons, "unknowns:recursive_control_flow:") {
		t.Fatalf("recursive receiver method should be recursion risk: score=%d reasons=%#v", score, reasons)
	}
}

func TestEmbeddedPatternPrecisionFixtures(t *testing.T) {
	patterns, err := loadScoringPatterns("")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		text      string
		want      []string
		wantNot   []string
		wantCount map[string]int
	}{
		{
			name: "open redirect from request query",
			text: `app.get("/login", (req, res) => {
	res.redirect(req.query.next)
})`,
			want: []string{"open_redirect_request_target"},
			wantNot: []string{
				"idor_request_param_object_lookup",
				"ssrf_user_controlled_url_fetch",
			},
		},
		{
			name: "prototype pollution request merge",
			text: `app.post("/settings", (req, res) => {
	const opts = Object.assign({}, defaults, req.body)
	res.json(opts)
})`,
			want: []string{"prototype_pollution_request_merge"},
			wantNot: []string{
				"nosql_request_object_query",
			},
		},
		{
			name: "unsafe yaml load",
			text: `def load_config(stream):
	return yaml.load(stream)
`,
			want:    []string{"unsafe_yaml_load"},
			wantNot: []string{"unsafe_python_deserialization"},
		},
		{
			name: "safe yaml load",
			text: `def load_config(stream):
	return yaml.safe_load(stream)
`,
			wantNot: []string{"unsafe_yaml_load"},
		},
		{
			name: "credential literal excludes placeholders",
			text: `const apiKey = "YOUR_API_KEY"
const password = "YOUR_PASSWORD"
`,
			wantNot: []string{"credential_assignment_literal"},
		},
		{
			name: "credential literal flags plausible secret",
			text: `const apiKey = "sk-aaaaaaaaaaaaaaaaaaaaaaaa"
`,
			want: []string{"credential_assignment_literal"},
		},
		{
			name:      "max matches caps repeated token literals",
			text:      strings.Repeat("sk-aaaaaaaaaaaaaaaaaaaaaaaa\n", 8),
			want:      []string{"provider_token_literal"},
			wantCount: map[string]int{"provider_token_literal": 5},
		},
		{
			name: "blocking inline worker ignores ordinary classify helper",
			text: `func BuildCullLedger(report Report) {
	dispositions := classifyCullDispositions(report.Rows)
	_ = dispositions
}
`,
			wantNot: []string{"blocking_inline_worker"},
		},
		{
			name: "blocking inline worker keeps explicit content classifier",
			text: `func score(text string) {
	result := classifyContent(text)
	_ = result
}
`,
			want: []string{"blocking_inline_worker"},
		},
		{
			name: "read all ignores bounded limit reader",
			text: `func read(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxBytes))
}
`,
			wantNot: []string{"read_all_or_global_growth"},
		},
		{
			name: "read all keeps unbounded reader",
			text: `func read(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}
`,
			want: []string{"read_all_or_global_growth"},
		},
		{
			name: "read all ignores local decode map",
			text: `func load(data []byte) error {
	var entries map[string]cachedScore
	return json.Unmarshal(data, &entries)
}
`,
			wantNot: []string{"read_all_or_global_growth"},
		},
		{
			name: "read all keeps package global map",
			text: `var scoreCache map[string]cachedScore
`,
			want: []string{"read_all_or_global_growth"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contentPatternMatchCounts(patterns, tt.text)
			for _, id := range tt.want {
				if got[id] == 0 {
					t.Fatalf("pattern %q did not match; matches=%v", id, got)
				}
			}
			for _, id := range tt.wantNot {
				if got[id] != 0 {
					t.Fatalf("pattern %q unexpectedly matched %d times; matches=%v", id, got[id], got)
				}
			}
			for id, want := range tt.wantCount {
				if got[id] != want {
					t.Fatalf("pattern %q matched %d times, want %d; matches=%v", id, got[id], want, got)
				}
			}
		})
	}
}

func TestContentRiskSkipsDocumentationProse(t *testing.T) {
	patterns, err := loadScoringPatterns("")
	if err != nil {
		t.Fatal(err)
	}

	score, reasons := contentRisk(patterns, "docs/detectors.md", `This fixture says Object.assign({}, defaults, req.body) and yaml.load(stream)
should be matched only when test cases expect those detector ids.
`)
	if score != 0 || len(reasons) != 0 {
		t.Fatalf("documentation prose should not run code-content detectors: score=%d reasons=%#v", score, reasons)
	}
}

func TestContentRiskLocationsPreserveLineNumbersAfterDetectorLiteralFiltering(t *testing.T) {
	patterns, err := loadScoringPatterns("")
	if err != nil {
		t.Fatal(err)
	}

	text := "pattern(\"fixture\", `yaml.load(stream)`, 1, 1, \"content-risk\")\n" +
		"const untouched = true\n" +
		"func load(stream any) {\n" +
		"\tyaml.load(stream)\n" +
		"}\n"

	score, reasons, locations := contentRiskWithLocations(patterns, "internal/app/loader.go", text)
	if score == 0 {
		t.Fatalf("expected content risk; reasons=%#v locations=%#v", reasons, locations)
	}
	for _, location := range locations {
		if strings.HasPrefix(location.Reason, "content:unsafe_yaml_load:") {
			if location.Line != 4 {
				t.Fatalf("unsafe yaml line = %d, want 4; location=%#v", location.Line, location)
			}
			if location.Snippet != "yaml.load(stream)" {
				t.Fatalf("unsafe yaml snippet = %q", location.Snippet)
			}
			return
		}
	}
	t.Fatalf("unsafe yaml location not found; reasons=%#v locations=%#v", reasons, locations)
}

func contentPatternMatchCounts(patterns scoringPatterns, text string) map[string]int {
	matches := contentPatternMatches(patterns, textWithoutDetectorLiterals(text))
	counts := make(map[string]int, len(matches))
	for _, match := range matches {
		counts[match.pattern.ID] = match.count
	}
	return counts
}

func TestUnknownsEvidenceLocationsPointAtFirstConcreteLine(t *testing.T) {
	text := `package demo

import (
	"database/sql"
	"os"
	"regexp"
)

func load() {
	_ = os.Getenv("DISTILL_MODEL")
	_ = regexp.MustCompile("x+")
	_, _ = sql.Open("sqlite", "")
}
`
	_, reasons := unknownsRisk("internal/demo.go", text)
	locations := unknownsEvidenceLocations("internal/demo.go", text, reasons)
	if len(locations) != 2 {
		t.Fatalf("locations = %#v, want env and resource locations for reasons %#v", locations, reasons)
	}
	if locations[0].Reason != "unknowns:env_assumptions:1" || locations[0].Line != 10 || !strings.Contains(locations[0].Snippet, "os.Getenv") {
		t.Fatalf("bad env location: %#v", locations[0])
	}
	if locations[1].Reason != "unknowns:resource_factory:2" || locations[1].Line != 11 || !strings.Contains(locations[1].Snippet, "regexp.MustCompile") {
		t.Fatalf("bad resource location: %#v", locations[1])
	}
}

func TestOpenAPIContractRiskRequiresSpecSurface(t *testing.T) {
	code := `package query

func hasAPIProse(fields []string) bool {
	if containsAnyField(fields, "openapi") {
		return containsAnyField(fields, "schema", "endpoint", "route", "spec", "users")
	}
	switch fields[0] {
	case "update", "delete":
		return true
	default:
		return false
	}
}
`
	if score, reasons := openAPIContractRisk("internal/query/classifier.go", code); score != 0 || len(reasons) != 0 {
		t.Fatalf("code mentioning openapi should not be an OpenAPI contract: score=%d reasons=%#v", score, reasons)
	}

	spec := `openapi: 3.0.0
paths:
  /users:
    get:
      responses:
        "200":
          description: ok
`
	if score, reasons := openAPIContractRisk("api.yaml", spec); score == 0 || !contains(reasons, "openapi_contract:missing_security_contract") {
		t.Fatalf("OpenAPI spec should retain contract risk: score=%d reasons=%#v", score, reasons)
	}
}

func TestBuildReportPopulatesTriageLanes(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.test/app\n\nreplace example.test/lib => ../lib\n")
	writeFile(t, tmp, "README.md", "APP_ENV is documented\n")
	writeFile(t, tmp, "internal/config/config.go", `package config

import "os"

func Load() string {
	return os.Getenv("SECRET_TOKEN")
}
`+strings.Repeat("// config filler\n", 80))
	writeFile(t, tmp, "flaky_test.go", `package p

import (
	"testing"
	"time"
)

func TestSlow(t *testing.T) {
	time.Sleep(time.Second)
}
`)
	writeFile(t, tmp, ".work/notes.md", "auth secret TODO\n")

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if findRow(report, ".work/notes.md") != nil {
		t.Fatal(".work file should be excluded from report")
	}
	goMod := findRow(report, "go.mod")
	if goMod == nil || goMod.DependencyHealthRisk == 0 || !contains(goMod.EvidenceLayers, "dependency-health") {
		t.Fatalf("go.mod dependency lane missing: %#v", goMod)
	}
	config := findRow(report, "internal/config/config.go")
	if config == nil || config.EnvContractRisk == 0 || !contains(config.EvidenceLayers, "env-contract") || !config.TestGap {
		t.Fatalf("config env/test-gap lanes missing: %#v", config)
	}
	flaky := findRow(report, "flaky_test.go")
	if flaky == nil || flaky.FlakeRisk == 0 || !contains(flaky.EvidenceLayers, "flake-risk") {
		t.Fatalf("flake lane missing: %#v", flaky)
	}
}

func TestBuildReportDoesNotFlagGoPackageWithTestsAsTestGap(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "internal/storage/store.go", `package storage

func StoreEmbedding() {}
`+strings.Repeat("// filler\n", 90))
	writeFile(t, tmp, "internal/storage/sqlite_test.go", `package storage

import "testing"

func TestStorePackage(t *testing.T) {}
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "internal/storage/store.go")
	if row == nil {
		t.Fatal("missing store row")
	}
	if row.TestGap || contains(row.EvidenceLayers, "test-void") {
		t.Fatalf("package-level tests should suppress test gap: %#v", row)
	}
}

func TestBuildReportCountsGoModuleImports(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.test/app\n")
	writeFile(t, tmp, "internal/core/core.go", `package core

func RetryAfterRateLimit() {}
`)
	writeFile(t, tmp, "cmd/one.go", `package cmd

import "example.test/app/internal/core"

func one() { core.RetryAfterRateLimit() }
`)
	writeFile(t, tmp, "cmd/two.go", `package cmd

import (
	alias "example.test/app/internal/core"
)

func two() { alias.RetryAfterRateLimit() }
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "internal/core/core.go")
	if row == nil {
		t.Fatal("missing imported core row")
	}
	if row.IncomingRefs != 2 || row.CentralityRisk == 0 || !contains(row.EvidenceLayers, "centrality") {
		t.Fatalf("go import centrality missing: incoming=%d centrality=%d layers=%#v reasons=%#v", row.IncomingRefs, row.CentralityRisk, row.EvidenceLayers, row.Reasons)
	}
}

func TestBuildReportAssignsGoImportCentralityToPackageOwners(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.test/app\n")
	writeFile(t, tmp, "providers/provider.go", `package providers

type Provider struct{}
`)
	writeFile(t, tmp, "providers/constants.go", `package providers

const DefaultRetries = 3
`)
	writeFile(t, tmp, "cmd/serve.go", `package cmd

import "example.test/app/providers"

func serve() providers.Provider { return providers.Provider{} }
`)
	writeFile(t, tmp, "internal/gateway/server.go", `package gateway

import "example.test/app/providers"

func newServer() providers.Provider { return providers.Provider{} }
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	provider := findRow(report, "providers/provider.go")
	if provider == nil || provider.IncomingRefs != 2 || provider.CentralityRisk == 0 {
		t.Fatalf("provider owner centrality missing: %#v", provider)
	}
	constants := findRow(report, "providers/constants.go")
	if constants == nil {
		t.Fatal("missing constants row")
	}
	if constants.IncomingRefs != 0 || constants.CentralityRisk != 0 || contains(constants.EvidenceLayers, "centrality") {
		t.Fatalf("non-owner helper should not inherit package centrality: %#v", constants)
	}
}

func TestBuildReportUsesCustomPatterns(t *testing.T) {
	tmp := t.TempDir()
	patterns := filepath.Join(tmp, "patterns.json")
	if err := os.WriteFile(patterns, []byte(`{
		"path_terms": [{"term":"special","weight":5}],
		"content_patterns": [{"id":"danger_call","pattern":"Danger\\(","weight":5,"max_matches":2}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "special.go", "package p\nfunc f(){ Danger() }\n")

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 5, MaxBytes: 2000, Patterns: patterns})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "special.go")
	if row == nil {
		t.Fatal("missing special.go")
	}
	if row.PathRisk == 0 || row.ContentRisk == 0 || !contains(row.Reasons, "content:danger_call:1") {
		t.Fatalf("custom patterns not applied: %#v", row)
	}
	if report.PatternsSource == "builtin" || !strings.HasSuffix(report.PatternsSource, "patterns.json") {
		t.Fatalf("patterns source = %q, want custom path", report.PatternsSource)
	}
}

func TestBuildReportUsesEmbeddedTriagePatternsByDefault(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "afk/worker.go", "package p\nfunc Run(){}\n")

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 5, MaxBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "afk/worker.go")
	if row == nil {
		t.Fatal("missing afk/worker.go")
	}
	if report.PatternsSource != "embedded:triage_patterns.json" {
		t.Fatalf("patterns source = %q, want embedded triage patterns", report.PatternsSource)
	}
	if !contains(row.Reasons, "path:afk") {
		t.Fatalf("embedded full path terms not applied: %#v", row.Reasons)
	}
}

func TestBuildReportIncludesRefinedSecurityContentPatterns(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		path string
		text string
		want string
	}{
		{
			path: "api/graphql.js",
			text: `const server = new ApolloServer({ typeDefs, resolvers, introspection: true, allowBatchedHttpRequests: true })`,
			want: "content:graphql_server_surface:1",
		},
		{
			path: "api/socket.go",
			text: `var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}; const upstream = "ws://api.example.test/feed"`,
			want: "content:websocket_endpoint_surface:1",
		},
		{
			path: "api/redirect.js",
			text: `app.get("/login", (req, res) => res.redirect(req.query.next))`,
			want: "content:open_redirect_request_target:1",
		},
		{
			path: "api/csrf.js",
			text: `fetch("/settings", { method: "POST", credentials: "include", body: form })`,
			want: "content:csrf_credentialed_state_change_client:1",
		},
		{
			path: "api/idor.js",
			text: `app.get("/users/:id", async (req, res) => res.json(await User.findById(req.params.id)))`,
			want: "content:idor_request_param_object_lookup:1",
		},
		{
			path: "api/mass.js",
			text: `app.post("/users", async (req, res) => res.json(await User.create(req.body)))`,
			want: "content:mass_assignment_request_body_write:1",
		},
		{
			path: "api/nosql.js",
			text: `app.get("/users", async (req, res) => res.json(await db.users.find(req.query).toArray()))`,
			want: "content:nosql_request_object_query:1",
		},
		{
			path: "api/ssrf.py",
			text: `def preview(request): return requests.get(request.args.get("url")).text`,
			want: "content:ssrf_user_controlled_url_fetch:1",
		},
		{
			path: "api/pickle.py",
			text: `def restore(request): return pickle.loads(request.data)`,
			want: "content:unsafe_python_deserialization:1",
		},
		{
			path: "api/xml.java",
			text: `DocumentBuilderFactory factory = DocumentBuilderFactory.newInstance(); factory.setFeature("http://xml.org/sax/features/external-general-entities", true);`,
			want: "content:xxe_java_xml_factory_surface:1",
		},
		{
			path: "api/prototype.js",
			text: `const opts = lodash.merge({}, req.body)`,
			want: "content:prototype_pollution_request_merge:1",
		},
		{
			path: "api/upload.go",
			text: `func download(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join(root, r.URL.Query().Get("path"))) }`,
			want: "content:path_traversal_user_file_access:1",
		},
	}
	for _, tc := range cases {
		writeFile(t, tmp, tc.path, tc.text)
	}

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: len(cases), MaxBytes: 4000})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		row := findRow(report, tc.path)
		if row == nil {
			t.Fatalf("missing row %s", tc.path)
		}
		if row.ContentRisk == 0 || !contains(row.EvidenceLayers, "content-risk") || !contains(row.Reasons, tc.want) {
			t.Fatalf("%s missing refined content pattern %q: %#v", tc.path, tc.want, row)
		}
	}
}

func TestEmbeddedEntrypointExitDoesNotTreatGoTestFailuresAsProcessExits(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.test/app\n")
	writeFile(t, tmp, "cmd/app/main.go", `package main

import "log"

func main() {
	log.Fatalf("config: %v", load())
}

func load() error { return nil }
`)
	writeFile(t, tmp, "internal/app/app_test.go", `package app

import "testing"

func TestApp(t *testing.T) {
	t.Fatalf("ordinary test assertion failure")
}
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	mainRow := findRow(report, "cmd/app/main.go")
	if mainRow == nil || !contains(mainRow.Reasons, "content:entrypoint_exit:1") {
		t.Fatalf("main row missing entrypoint exit signal: %#v", mainRow)
	}
	testRow := findRow(report, "internal/app/app_test.go")
	if testRow == nil {
		t.Fatal("missing test row")
	}
	if containsReasonPrefix(testRow.Reasons, "content:entrypoint_exit:") {
		t.Fatalf("test assertion should not be an entrypoint exit signal: %#v", testRow.Reasons)
	}
}

func TestBuildReportPopulatesArtifactSecurityLanes(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, ".github/workflows/ci.yml", `on: pull_request_target
permissions: write-all
jobs:
  test:
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.head.sha }}
      - uses: example/action@v1
      - run: echo "${{ github.event.pull_request.title }}"
`)
	writeFile(t, tmp, "db/migrations/001.sql", "DROP TABLE users;\nCREATE INDEX idx_users_email ON users(email);\n")
	writeFile(t, tmp, "Dockerfile", "FROM alpine:latest\nRUN apt-get update\nRUN apt-get install curl\nRUN curl https://example.test/install.sh | sh\nUSER root\n")
	writeFile(t, tmp, "openapi.yaml", `openapi: 3.0.0
components:
  securitySchemes:
    basic:
      type: http
      scheme: basic
paths:
  /users:
    get:
      security: []
      responses: {}
servers:
  - url: http://api.example.test
`)
	writeFile(t, tmp, "k8s/deploy.yaml", "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n      - name: app\n        securityContext:\n          privileged: true\n")
	writeFile(t, tmp, "main.tf", `resource "aws_security_group_rule" "ssh" {
  type = "ingress"
  from_port = 22
  to_port = 22
  cidr_blocks = ["0.0.0.0/0"]
}
resource "aws_s3_bucket" "public" {
  acl = "public-read"
}
resource "aws_s3_bucket_public_access_block" "public" {
  block_public_acls = false
}
`)
	writeFile(t, tmp, "client.go", "package p\ntype Client struct{}\nfunc (c *Client) Fetch(id string){}\n")
	writeFile(t, tmp, "server.go", `package p

func headers() {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
	http.SetCookie(w, &http.Cookie{Name:"__Host-session", Value: token, Domain: ".example.test", SameSite: http.SameSiteNoneMode, Partitioned: true})
}
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 20, MaxBytes: 4000})
	if err != nil {
		t.Fatal(err)
	}
	assertLane := func(path, layer string, risk func(FileEvidence) int) {
		t.Helper()
		row := findRow(report, path)
		if row == nil {
			t.Fatalf("missing row %s", path)
		}
		if risk(*row) == 0 || !contains(row.EvidenceLayers, layer) {
			t.Fatalf("%s missing %s lane: %#v", path, layer, row)
		}
	}
	assertLane(".github/workflows/ci.yml", "workflow-security", func(row FileEvidence) int { return row.WorkflowSecurityRisk })
	assertLane("db/migrations/001.sql", "migration-safety", func(row FileEvidence) int { return row.MigrationSafetyRisk })
	assertLane("Dockerfile", "container-build", func(row FileEvidence) int { return row.ContainerBuildRisk })
	assertLane("k8s/deploy.yaml", "kubernetes-security", func(row FileEvidence) int { return row.KubernetesSecurityRisk })
	assertLane("main.tf", "terraform-security", func(row FileEvidence) int { return row.TerraformSecurityRisk })
	assertLane("openapi.yaml", "openapi-contract", func(row FileEvidence) int { return row.OpenAPIContractRisk })
	assertLane("client.go", "sdk-dx", func(row FileEvidence) int { return row.SDKDXRisk })
	assertLane("server.go", "cors-security", func(row FileEvidence) int { return row.CORSSecurityRisk })
	assertLane("server.go", "cookie-security", func(row FileEvidence) int { return row.CookieSecurityRisk })
	assertReasons := func(path string, reasons ...string) {
		t.Helper()
		row := findRow(report, path)
		if row == nil {
			t.Fatalf("missing row %s", path)
		}
		for _, reason := range reasons {
			if !contains(row.Reasons, reason) {
				t.Fatalf("%s missing reason %s: %#v", path, reason, row.Reasons)
			}
		}
	}
	assertReasons(".github/workflows/ci.yml", "workflow_security:inline_event_context:1", "workflow_security:unpinned_actions:2")
	assertReasons("db/migrations/001.sql", "migration_safety:blocking_index:1")
	if row := findRow(report, "db/migrations/001.sql"); row == nil || containsReasonPrefix(row.Reasons, "content:unsafe_index_creation:") {
		t.Fatalf("migration index risk should stay in migration-safety lane: %#v", row)
	}
	assertReasons("Dockerfile", "container_build:root_user", "container_build:apt_update_split:1", "container_build:apt_install_recommends:1", "container_build:apt_cache_not_cleaned:1")
	assertReasons("k8s/deploy.yaml", "kubernetes_security:privileged:1", "kubernetes_security:missing_run_as_non_root")
	assertReasons("main.tf", "terraform_security:public_s3_acl:1", "terraform_security:s3_public_block_disabled:1")
	assertReasons("openapi.yaml", "openapi_contract:anonymous_security:1", "openapi_contract:basic_auth_scheme")
	assertReasons("server.go", "cors_security:reflected_origin:1", "cors_security:wildcard_methods:1", "cors_security:wildcard_headers:1", "cors_security:long_preflight_cache_with_broad_policy", "cookie_security:host_prefix_has_domain", "cookie_security:broad_domain_scope")
}

func TestBuildReportDoesNotTreatDetectorSourceAsCookieRisk(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "internal/slither/artifact_risk.go", `package slither

func cookieSecurityRisk(text string) []string {
	if regexpMust("(?i)(set-cookie|http\\.SetCookie|res\\.cookie)").FindStringIndex(text) == nil {
		return nil
	}
	return []string{
		"cookie_security:sensitive_cookie_missing_httponly",
		"cookie_security:host_prefix_missing_secure",
	}
}
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 5, MaxBytes: 4000})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "internal/slither/artifact_risk.go")
	if row == nil {
		t.Fatal("missing detector source row")
	}
	if row.CookieSecurityRisk != 0 || contains(row.EvidenceLayers, "cookie-security") {
		t.Fatalf("detector source should not self-match cookie policy: %#v", row)
	}
}

func TestBuildReportPopulatesDependencyAndSDKRefinements(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "package.json", `{
  "dependencies": {
    "a": "1", "b": "1", "c": "1", "d": "1", "e": "1", "f": "1", "g": "1",
    "h": "1", "i": "1", "j": "1", "k": "1", "l": "1", "m": "1", "n": "1",
    "o": "1", "p": "1", "q": "1", "r": "1", "s": "1", "t": "1", "u": "1"
  },
  "peerDependencies": {"react": ">=18"}
}`)
	writeFile(t, tmp, "composer.json", `{
  "require": {"a/a": "*"},
  "conflict": {"old/pkg": "*"}
}`)
	writeFile(t, tmp, "requirements.txt", strings.Repeat("pkg==1.0\n", 21))
	writeFile(t, tmp, "client.go", `package p
type Client struct{}
type Provider interface{ Do() error }
func (c *Client) Generate(prompt string) {}
func (c *Client) GenerateWithContext(ctx context.Context, prompt string) {}
func (c *Client) Stream(prompt string) {}
func (c *Client) StreamWithContext(ctx context.Context, prompt string) {}
func (c *Client) QuickText(prompt string) string { return "" }
`)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 6000})
	if err != nil {
		t.Fatal(err)
	}
	pkg := findRow(report, "package.json")
	if pkg == nil || pkg.DependencyHealthRisk == 0 || !contains(pkg.Reasons, "dependency_health:large_dependency_tree:21") || !contains(pkg.Reasons, "dependency_health:peer_dependency_surface:1") || !contains(pkg.Reasons, "dependency_health:missing_license") {
		t.Fatalf("package health refinements missing: %#v", pkg)
	}
	composer := findRow(report, "composer.json")
	if composer == nil || !contains(composer.Reasons, "dependency_health:conflict_constraints:1") || !contains(composer.Reasons, "dependency_health:missing_license") {
		t.Fatalf("composer health refinements missing: %#v", composer)
	}
	requirements := findRow(report, "requirements.txt")
	if requirements == nil || !contains(requirements.Reasons, "dependency_health:large_dependency_tree:21") {
		t.Fatalf("requirements health refinements missing: %#v", requirements)
	}
	client := findRow(report, "client.go")
	if client == nil || client.SDKDXRisk == 0 || !contains(client.Reasons, "sdk_dx:duplicate_client_method_groups:2") || !contains(client.Reasons, "sdk_dx:introspection_or_quick_api") || !contains(client.Reasons, "sdk_dx:testability_hook") {
		t.Fatalf("sdk dx refinements missing: %#v", client)
	}
}

func TestBuildReportPopulatesGitHistoryLanes(t *testing.T) {
	tmp := t.TempDir()
	runGitForTest(t, tmp, "init")
	runGitForTest(t, tmp, "config", "user.email", "one@example.test")
	runGitForTest(t, tmp, "config", "user.name", "One")
	writeFile(t, tmp, "auth.go", "package p\n// TODO stale auth risk\nfunc Auth(){ password := \"secret\"; _ = password }\n")
	writeFile(t, tmp, "config.go", "package p\nfunc Config(){ _ = Auth }\n")
	runGitForTest(t, tmp, "add", ".")
	runGitWithEnvForTest(t, tmp, []string{"GIT_AUTHOR_DATE=2025-01-01T12:00:00Z", "GIT_COMMITTER_DATE=2025-01-01T12:00:00Z"}, "commit", "-m", "initial")
	for i := 0; i < 30; i++ {
		writeFile(t, tmp, "auth.go", "package p\n// TODO stale auth risk\nfunc Auth(){ password := \"secret\"; _ = password }\n// change "+string(rune('a'+i%26))+"\n")
		writeFile(t, tmp, "config.go", "package p\nfunc Config(){ _ = Auth }\n// change "+string(rune('a'+i%26))+"\n")
		runGitForTest(t, tmp, "add", ".")
		runGitWithEnvForTest(t, tmp, []string{"GIT_AUTHOR_DATE=2026-05-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-05-01T12:00:00Z"}, "commit", "-m", "fix auth bug")
	}

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 4000, Days: 700})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(report, "auth.go")
	if row == nil {
		t.Fatal("missing auth.go")
	}
	if row.Churn == 0 || row.FixTouches == 0 || row.CochangeRisk == 0 || row.OwnershipRisk == 0 || row.StaleMarkerRisk == 0 {
		t.Fatalf("history lanes missing: %#v", row)
	}
	for _, layer := range []string{"cochange", "ownership", "stale-marker"} {
		if !contains(row.EvidenceLayers, layer) {
			t.Fatalf("layers = %#v, want %s", row.EvidenceLayers, layer)
		}
	}
}

func TestRunGitHonorsCanceledContext(t *testing.T) {
	tmp := t.TempDir()
	runGitForTest(t, tmp, "init")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if out := runGit(ctx, tmp, "status", "--short"); out != "" {
		t.Fatalf("runGit output = %q, want empty output after cancellation", out)
	}
}

func TestBuildReportRejectsInvalidPatterns(t *testing.T) {
	tmp := t.TempDir()
	patterns := filepath.Join(tmp, "patterns.json")
	if err := os.WriteFile(patterns, []byte(`{"content_patterns":[{"id":"bad","pattern":"[","weight":5,"max_matches":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "app.go", "package p\n")

	_, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 5, MaxBytes: 2000, Patterns: patterns})
	if err == nil || !strings.Contains(err.Error(), "content_patterns[bad].pattern is invalid") {
		t.Fatalf("err = %v, want invalid pattern error", err)
	}
}

func runGitForTest(t *testing.T, repo string, args ...string) {
	t.Helper()
	runGitWithEnvForTest(t, repo, nil, args...)
}

func runGitWithEnvForTest(t *testing.T, repo string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, root, rel, text string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func findRow(report Report, path string) *FileEvidence {
	for i := range report.Rows {
		if report.Rows[i].Path == path {
			return &report.Rows[i]
		}
	}
	return nil
}

func findPayloadRow(rows []FileEvidence, path string) *FileEvidence {
	for i := range rows {
		if rows[i].Path == path {
			return &rows[i]
		}
	}
	return nil
}

func markdownLineWithPrefix(markdown, prefix string) string {
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func containsReasonPrefix(items []string, prefix string) bool {
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func containsPath(rows []FileEvidence, path string) bool {
	for _, row := range rows {
		if row.Path == path {
			return true
		}
	}
	return false
}

func slicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestScoreTopRowsCachedReportsHitMissCounts(t *testing.T) {
	t.Parallel()
	cache := &scoreCache{entries: map[string]cachedScore{}, dirty: map[string]cachedScore{}, used: map[string]bool{}}
	hitRow := baseEvidence("hit.go", 2)
	missRow := baseEvidence("miss.go", 2)
	cache.entries[scoreCacheKey("m", "", nil, hitRow)] = cachedScore{Score: 5, Summary: "cached"}
	s := &ModelScorer{model: "m", generate: func(_ context.Context, _ string, _ int) (string, error) {
		return `[{"index":0,"score":4,"summary":"fresh","reasons":["r"]}]`, nil
	}}
	rows := []FileEvidence{hitRow, missRow}
	hits, misses := scoreTopRowsCached(context.Background(), s, rows, cache)
	if hits != 1 || misses != 1 {
		t.Fatalf("hits=%d misses=%d, want 1 and 1", hits, misses)
	}
}

func TestRenderMarkdownIncludesCacheStats(t *testing.T) {
	report := Report{
		Repo:       "/repo",
		Model:      "m",
		BaseURL:    "http://x",
		Rows:       []FileEvidence{{Path: "a.go", Score: 3, Reasons: []string{"path:auth"}}},
		CacheStats: &CacheStats{Hits: 7, Misses: 3},
	}
	md := RenderMarkdown(report)
	if !strings.Contains(md, "Score cache: `7` hits, `3` misses") {
		t.Fatalf("markdown missing cache stats footer:\n%s", md)
	}
}

func TestRenderJSONIncludesCacheStats(t *testing.T) {
	report := Report{Repo: "/repo", CacheStats: &CacheStats{Hits: 2, Misses: 4}, Rows: []FileEvidence{{Path: "a.go"}}}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		CacheStats *CacheStats `json:"cache_stats"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CacheStats == nil || payload.CacheStats.Hits != 2 || payload.CacheStats.Misses != 4 {
		t.Fatalf("cache stats missing from envelope: %s", data)
	}
}

func TestLifecycleLaneRoutesConcurrentBoundaryRows(t *testing.T) {
	t.Parallel()
	row := FileEvidence{
		Path:           "internal/service/concurrent.go",
		Score:          5,
		ContentRisk:    12,
		FlakeRisk:      0,
		EvidenceLayers: []string{"content-risk"},
		Reasons:        []string{"content:async_or_concurrent_boundary:1"},
	}

	if !isLifecycleRow(row) {
		t.Fatal("isLifecycleRow should match a concurrent-boundary reason")
	}

	prio := reviewLaneFilePriority("lifecycle-concurrency", row)
	if prio <= 0 {
		t.Fatalf("reviewLaneFilePriority for lifecycle-concurrency lane should be > 0, got %d", prio)
	}
}

func TestReviewReasonNeedlesMatchLivePatternCatalog(t *testing.T) {
	t.Parallel()
	patterns, err := loadScoringPatterns("")
	if err != nil {
		t.Fatal(err)
	}

	needles := []string{
		"shell_boundary",
		"process_exit",
		"error_context_dropped",
		"background_context",
		"concurrent",
		"read_all_or_global_growth",
	}

	for _, needle := range needles {
		found := false
		for _, cp := range patterns.ContentPatterns {
			if strings.Contains(cp.ID, needle) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("needle %q is not a substring of any content-pattern ID; pattern catalog rename would orphan a review-lane needle", needle)
		}
	}
}

func TestRenderOmitsCacheStatsWhenAbsent(t *testing.T) {
	report := Report{Repo: "/repo", Rows: []FileEvidence{{Path: "a.go"}}}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cache_stats") {
		t.Fatalf("run without cache should omit cache_stats from JSON: %s", data)
	}
	if md := RenderMarkdown(report); strings.Contains(md, "Score cache:") {
		t.Fatalf("run without cache should omit cache stats from markdown:\n%s", md)
	}
}
