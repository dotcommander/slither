package slither

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandHelpCatalogAndCompletionAgree(t *testing.T) {
	for _, spec := range commandSpecs() {
		var help strings.Builder
		if err := Run(context.Background(), []string{spec.Name, "--help"}, &help, &strings.Builder{}); err != nil {
			t.Fatalf("%s help: %v", spec.Name, err)
		}
		for _, flag := range spec.Flags {
			if !strings.Contains(help.String(), flag.Name) {
				t.Fatalf("%s help missing %s", spec.Name, flag.Name)
			}
		}
	}
	for _, shell := range []string{"bash", "zsh"} {
		var output strings.Builder
		if err := Run(context.Background(), []string{"completion", shell}, &output, &strings.Builder{}); err != nil {
			t.Fatal(err)
		}
		for _, spec := range commandSpecs() {
			if !strings.Contains(output.String(), spec.Name) {
				t.Fatalf("%s completion missing %s", shell, spec.Name)
			}
		}
	}
	for _, args := range [][]string{{"missing"}, {"help", "missing"}, {"help", "report", "extra"}} {
		if err := Run(context.Background(), args, &strings.Builder{}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "run slither --help") {
			t.Fatalf("%v error = %v, want root help guidance", args, err)
		}
	}
	if completion := bashCompletion(); !strings.Contains(completion, "help) COMPREPLY=") {
		t.Fatalf("bash completion does not complete help command operands:\n%s", completion)
	}
	if completion := zshCompletion(); !strings.Contains(completion, "help) _describe 'command' help_commands") {
		t.Fatalf("zsh completion does not complete help command operands:\n%s", completion)
	} else {
		for _, want := range []string{"'help:show command help'", "'--help:show root help'", "'-h:show root help'", "compdef _slither slither"} {
			if !strings.Contains(completion, want) {
				t.Fatalf("zsh completion missing %q:\n%s", want, completion)
			}
		}
		_, helpOperands, ok := strings.Cut(completion, "  help_commands=(\n")
		if !ok {
			t.Fatalf("zsh completion missing canonical help operand array:\n%s", completion)
		}
		helpOperands, _, ok = strings.Cut(helpOperands, "  )\n")
		if !ok {
			t.Fatalf("zsh completion has unterminated help operand array:\n%s", completion)
		}
		for _, invalid := range []string{"'help:", "'--help:", "'-h:"} {
			if strings.Contains(helpOperands, invalid) {
				t.Fatalf("zsh help operands contain invalid target %q:\n%s", invalid, helpOperands)
			}
		}
	}
	var stdout strings.Builder
	if err := Run(context.Background(), []string{"outputs", "--json"}, &stdout, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	var catalog outputCatalog
	if err := json.Unmarshal([]byte(stdout.String()), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Schema != outputCatalogSchema || len(catalog.Surfaces) < 7 {
		t.Fatalf("catalog = %#v", catalog)
	}
	catalogFlags := map[string]bool{}
	for _, surface := range catalog.Surfaces {
		for _, flag := range surface.Flags {
			catalogFlags[flag] = true
		}
	}
	for _, spec := range commandSpecs() {
		for _, flag := range spec.Flags {
			if !catalogFlags[flag.Name] {
				t.Fatalf("catalog missing registered flag %s", flag.Name)
			}
		}
	}
}

func TestCommandHelpFlagsWorkAtAnyPositionWithoutStealingValues(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "report after valued flag", args: []string{"report", "--top", "20", "--help"}, want: "slither report [repo] [flags]"},
		{name: "report before valued flag", args: []string{"report", "--help", "--top", "20"}, want: "slither report [repo] [flags]"},
		{name: "report after positional repo", args: []string{"report", ".", "-h"}, want: "slither report [repo] [flags]"},
		{name: "agent after positional repo", args: []string{"agent", ".", "--help", "--outcomes", "ledger.jsonl"}, want: "slither agent [repo] [--outcomes path]"},
		{name: "eval after flags", args: []string{"eval", "--json", "--help"}, want: "slither eval --outcomes path"},
		{name: "completion after operand", args: []string{"completion", "bash", "--help"}, want: "slither completion bash|zsh"},
		{name: "version after flag", args: []string{"version", "--build", "-h"}, want: "slither version [--build|--json]"},
		{name: "doctor after flag", args: []string{"doctor", "--json", "--help"}, want: "slither doctor [--json]"},
		{name: "outputs after positional", args: []string{"outputs", "report", "--help"}, want: "slither outputs [surface] [--json]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout strings.Builder
			if err := Run(context.Background(), test.args, &stdout, &strings.Builder{}); err != nil {
				t.Fatalf("Run(%v): %v", test.args, err)
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("Run(%v) output = %q, want command help containing %q", test.args, stdout.String(), test.want)
			}
		})
	}

	for _, test := range []struct {
		command string
		args    []string
	}{
		{command: "report", args: []string{"--out", "--help", "--top", "20"}},
		{command: "agent", args: []string{"--outcomes", "--help"}},
		{command: "agent", args: []string{"-outcomes", "-h"}},
		{command: "eval", args: []string{"--outcomes", "--help", "--report", "report.json", "--json"}},
		{command: "eval", args: []string{"-outcomes", "-h", "-report", "report.json", "--json"}},
	} {
		t.Run(test.command+" help-like value", func(t *testing.T) {
			if commandHelpRequested(test.command, test.args) {
				t.Fatalf("commandHelpRequested(%q, %v) = true, want false", test.command, test.args)
			}
		})
	}

	var stdout strings.Builder
	err := Run(context.Background(), []string{"report", "--out", "--help", "--top", "not-a-number"}, &stdout, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "invalid value") {
		t.Fatalf("report value named --help error = %v, want flag parse error", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("report value named --help printed command help: %q", stdout.String())
	}
}

func TestVersionDoctorAndUsageContracts(t *testing.T) {
	var version strings.Builder
	if err := Run(context.Background(), []string{"version", "--json"}, &version, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(version.String(), `"schema":"slither.version/v1"`) {
		t.Fatalf("version = %s", version.String())
	}
	if err := Run(context.Background(), []string{"version", "--build", "--json"}, &strings.Builder{}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "run slither version --help") {
		t.Fatalf("version conflict = %v", err)
	}
	dir := setTempConfigDir(t)
	var doctor strings.Builder
	if err := Run(context.Background(), []string{"doctor", "--json"}, &doctor, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "slither", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("doctor created config: %v", err)
	}
	if !strings.Contains(doctor.String(), `"schema":"slither.doctor/v1"`) || !strings.Contains(doctor.String(), `"config_state":"missing"`) {
		t.Fatalf("doctor = %s", doctor.String())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unsafe doctor request: %s %#v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	path := filepath.Join(dir, "slither", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"model":"local","base_url":"` + server.URL + `","api_key_env":"TEST_DOCTOR_KEY","local":{},"fallback_models":[]}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	doctor.Reset()
	if err := Run(context.Background(), []string{"doctor", "--json"}, &doctor, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doctor.String(), `"status":"reachable"`) {
		t.Fatalf("loopback doctor = %s", doctor.String())
	}
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"doctor"}, &strings.Builder{}, &strings.Builder{}); err == nil {
		t.Fatal("malformed config accepted")
	}
}

func TestSummaryHealthAndInventoryContracts(t *testing.T) {
	rows := []FileEvidence{
		{ID: "one", EvidenceID: "one", Path: "cmd/a.go", Score: 5, SeedScore: 4, Confidence: "high", Actionability: ActionabilityInspect, EvidenceLayers: []string{"content-risk"}, ScoreProvenance: ScoreProvenance{Deterministic: 5, SelectedBy: "deterministic"}},
		{ID: "two", EvidenceID: "two", Path: "db/store.go", Score: 5, SeedScore: 4, Confidence: "medium", Actionability: ActionabilityHighRiskInspect, EvidenceLayers: []string{"content-risk"}, MigrationSafetyRisk: 1, ScoreProvenance: ScoreProvenance{Deterministic: 5, SelectedBy: "deterministic"}},
	}
	report := Report{ReportID: "sha256:test", Rows: rows, FilesSeen: 2, FilesScored: 2}
	summary := BuildReportSummary(report)
	if summary.RankingHealth.TopK != 15 || summary.RankingHealth.Saturation != 0.5 || len(summary.StartHere) == 0 || len(summary.StartHere) > 10 {
		t.Fatalf("summary = %#v", summary)
	}
	if _, plan := BuildInventoryForRepo("", rows, "data-integrity"); len(plan) != 1 || plan[0].Lane != "data-integrity" {
		t.Fatalf("inventory = %#v", plan)
	}
	if _, err := resolveReportOptions(defaultConfig(), []string{"--inventory", "missing"}); err == nil {
		t.Fatal("invalid inventory accepted")
	}
	setTempConfigDir(t)
	repo := t.TempDir()
	writeFile(t, repo, "main.go", "package main\nfunc main() { panic(\"x\") }\n")
	var output strings.Builder
	if err := Run(context.Background(), []string{"report", repo, "--summary", "--json", "--out", "-", "--top", "1"}, &output, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	var rendered reportSummary
	if err := json.Unmarshal([]byte(output.String()), &rendered); err != nil {
		t.Fatal(err)
	}
	if rendered.Schema != summarySchema || len(rendered.StartHere) > 10 {
		t.Fatalf("rendered summary = %#v", rendered)
	}
}

func TestSummaryRanksAndCullActionabilityMatchFullReport(t *testing.T) {
	rows := []FileEvidence{
		{ID: "fixture", EvidenceID: "fixture", Path: "internal/auth_test.go", Score: 5, Confidence: "high", Actionability: ActionabilityLikelyDefect, EvidenceLayers: []string{"content-risk", "test-risk"}},
		{ID: "kept", EvidenceID: "kept", Path: "internal/auth.go", Score: 5, Confidence: "high", Actionability: ActionabilityLikelyDefect, ContentRisk: 5, WorkflowSecurityRisk: 5, EvidenceLayers: []string{"content-risk", "workflow-security"}},
		{ID: "documentation", EvidenceID: "documentation", Path: "docs/architecture.md", Score: 4, Confidence: "medium", Actionability: ActionabilityHotspot, EvidenceLayers: []string{"path-risk", "git-history"}},
		{ID: "alternate", EvidenceID: "alternate", Path: "cmd/slither/main.go", Score: 3, Confidence: "medium", Actionability: ActionabilityInspect, EvidenceLayers: []string{"git-history", "cochange"}},
	}
	report := Report{ReportID: "sha256:test", Rows: rows, FilesSeen: len(rows), FilesScored: len(rows)}

	intrinsic := BuildReportSummary(report)
	if got := intrinsic.Actionability[string(ActionabilityLikelyDefect)]; got != 2 {
		t.Fatalf("intrinsic likely_defect count = %d, want 2", got)
	}
	if got := intrinsic.Actionability[string(ActionabilityHotspot)]; got != 1 {
		t.Fatalf("intrinsic hotspot count = %d, want 1", got)
	}
	if got := intrinsic.Actionability[string(ActionabilityInspect)]; got != 1 {
		t.Fatalf("intrinsic inspect count = %d, want 1", got)
	}
	assertSummaryRanks(t, intrinsic.StartHere, []string{"internal/auth.go", "cmd/slither/main.go"})
	fallback := BuildReportSummary(Report{Rows: []FileEvidence{rows[0], rows[2]}})
	assertSummaryRanks(t, fallback.StartHere, []string{"internal/auth_test.go", "docs/architecture.md"})

	ledger := BuildCullLedger(report)
	report.CullLedger = &ledger
	culled := BuildReportSummary(report)
	assertSummaryRanks(t, culled.StartHere, []string{"internal/auth.go", "cmd/slither/main.go"})

	fullData, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		Rows []FileEvidence `json:"rows"`
	}
	if err := json.Unmarshal(fullData, &full); err != nil {
		t.Fatal(err)
	}
	fullCounts := map[string]int{}
	intrinsicRows := rowsByID(rows)
	for _, row := range full.Rows {
		fullCounts[string(row.Actionability)]++
		switch row.CullDecision {
		case CullDecisionKeptForPremium, CullDecisionAlternates:
			if row.Actionability != intrinsicRows[row.ID].Actionability {
				t.Fatalf("%s actionability = %q, want intrinsic %q", row.CullDecision, row.Actionability, intrinsicRows[row.ID].Actionability)
			}
		default:
			if row.Actionability != ActionabilityVerifyFirst {
				t.Fatalf("%s actionability = %q, want %q", row.CullDecision, row.Actionability, ActionabilityVerifyFirst)
			}
		}
	}
	if !equalIntMap(culled.Actionability, fullCounts) {
		t.Fatalf("summary actionability = %#v, full JSON = %#v", culled.Actionability, fullCounts)
	}

	summaryData, err := RenderSummaryJSON(culled)
	if err != nil {
		t.Fatal(err)
	}
	var jsonSummary reportSummary
	if err := json.Unmarshal(summaryData, &jsonSummary); err != nil {
		t.Fatal(err)
	}
	markdown := RenderSummaryMarkdown(culled)
	assertSummaryRanks(t, jsonSummary.StartHere, []string{"internal/auth.go", "cmd/slither/main.go"})
	for _, row := range jsonSummary.StartHere {
		want := "| " + itoa(row.Rank) + " | `" + row.Path + "` |"
		if !strings.Contains(markdown, want) {
			t.Fatalf("Markdown summary missing JSON rank/path %q:\n%s", want, markdown)
		}
	}
}

func assertSummaryRanks(t *testing.T, rows []summaryStartRow, wantPaths []string) {
	t.Helper()
	if len(rows) != len(wantPaths) {
		t.Fatalf("start_here rows = %d, want %d: %#v", len(rows), len(wantPaths), rows)
	}
	for i, row := range rows {
		if row.Rank != i+1 || row.Path != wantPaths[i] {
			t.Fatalf("start_here[%d] = rank %d path %q, want rank %d path %q", i, row.Rank, row.Path, i+1, wantPaths[i])
		}
	}
}

func rowsByID(rows []FileEvidence) map[string]FileEvidence {
	indexed := make(map[string]FileEvidence, len(rows))
	for _, row := range rows {
		indexed[row.ID] = row
	}
	return indexed
}

func TestEvalMarkdownIsPrivate(t *testing.T) {
	text := renderEvalMarkdown(evalResult{Reports: 1, Rows: 2, Outcomes: evalOutcomes{Confirmed: 1}, Calibration: evalCalibration{TopK: 15, Labeled: 1, Found: 1}, Warnings: evalWarnings{UnmatchedOutcomes: 1}})
	for _, forbidden := range []string{".json", "sha256:", "202"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("markdown leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "Ledger warnings") || !strings.Contains(text, "Top-K") {
		t.Fatalf("markdown incomplete: %s", text)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"eval", "--json", "--markdown"}, &out, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "run slither eval --help") {
		t.Fatalf("conflicting eval flags = %v", err)
	}
}
