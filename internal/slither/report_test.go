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

func TestRenderMarkdownIncludesSnakeIdentity(t *testing.T) {
	report := Report{Repo: "/repo", FilesSeen: 1, Discovery: DiscoveryStats{Source: "git", GitTracked: 1, CandidateFiles: 1}, SkippedSignals: []string{"model_scoring:not_configured"}, Rows: []FileEvidence{{Path: "a.go", Score: 2, EvidenceLayers: []string{"path-risk"}, Reasons: []string{"path:auth"}, Summary: "sample"}}}
	md := RenderMarkdown(report)
	for _, want := range []string{"# Slither Report", "snake through", "Discovery: source `git`", "Skipped signals", "seed_score", "env_contract_risk", "path-risk", "`a.go`"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRenderJSONIncludesEvidenceEnvelope(t *testing.T) {
	report := Report{Repo: "/repo", FilesSeen: 1, Discovery: DiscoveryStats{Source: "git", GitTracked: 1, CandidateFiles: 1}, SkippedSignals: []string{"model_scoring:not_configured"}, Rows: []FileEvidence{{Path: "a.go", Score: 2, EvidenceLayers: []string{"path-risk"}, Reasons: []string{"path:auth"}, Summary: "sample"}}}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		RunLabel       string         `json:"run_label"`
		Discovery      DiscoveryStats `json:"discovery"`
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
	if !contains(payload.SkippedSignals, "model_scoring:not_configured") || !contains(payload.Rows[0].EvidenceLayers, "path-risk") {
		t.Fatalf("missing envelope evidence: %#v", payload)
	}
}

func TestBuildReportAddsEvidenceMetadataAndReviewPlan(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.test/app\n\nreplace example.test/lib => ../lib\n")
	writeFile(t, tmp, "cmd/app/main.go", `package main

import "os"

func main() {
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

func TestBuildCullLedgerBucketsRows(t *testing.T) {
	report := Report{Repo: "/repo", Rows: []FileEvidence{
		{Path: "auth.go", Score: 5, PathRisk: 5, ContentRisk: 5, EvidenceLayers: []string{"path-risk", "content-risk", "test-void"}},
		{Path: "api/generated.pb.go", Score: 5, EvidenceLayers: []string{"content-risk"}},
		{Path: "auth_test.go", Score: 3, EvidenceLayers: []string{"content-risk"}},
		{Path: "weak.go", Score: 3, ContentRisk: 5, EvidenceLayers: []string{"content-risk"}},
		{Path: "config.go", Score: 3, EnvContractRisk: 3, EvidenceLayers: []string{"path-risk", "env-contract"}},
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

func TestBuildCullLedgerDemotesGeneratedReportsAndHistoryOnlyTests(t *testing.T) {
	report := Report{Repo: "/repo", Rows: []FileEvidence{
		{Path: "internal/actions/digest/digest.go", Score: 5, ContentRisk: 5, CochangeRisk: 7, OwnershipRisk: 7, EvidenceLayers: []string{"content-risk", "cochange", "ownership"}},
		{Path: "docs/remote-eval-report.html", Score: 5, ContentRisk: 5, OwnershipRisk: 7, EvidenceLayers: []string{"content-risk", "ownership", "size", "churn"}},
		{Path: "internal/actions/digest/digest_test.go", Score: 5, ContentRisk: 5, CochangeRisk: 7, OwnershipRisk: 7, EvidenceLayers: []string{"content-risk", "cochange", "ownership"}},
		{Path: "testdata/extraction/expected/chunk-006.json", Score: 4, CochangeRisk: 7, OwnershipRisk: 7, EvidenceLayers: []string{"cochange", "ownership", "churn"}},
	}}

	ledger := BuildCullLedger(report)
	if ledger.KeptForPremium.Count != 1 {
		t.Fatalf("kept count = %d, want only source row kept: %#v", ledger.KeptForPremium.Count, ledger)
	}
	if ledger.Generated.Count != 1 {
		t.Fatalf("generated count = %d, want docs HTML generated bucket: %#v", ledger.Generated.Count, ledger)
	}
	if ledger.TestOnly.Count != 2 {
		t.Fatalf("test-only count = %d, want test and testdata rows demoted: %#v", ledger.TestOnly.Count, ledger)
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
		defaultBaseURL,
		localModel,
		localBaseURL,
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help output missing %q: %s", want, help)
		}
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

func TestParseModelScoreExtractsJSON(t *testing.T) {
	got, err := parseModelScore("sure\n{\"score\":4,\"summary\":\"hot\",\"reasons\":[\"auth\"]}")
	if err != nil {
		t.Fatal(err)
	}
	if got.Score != 4 || got.Summary != "hot" || len(got.Reasons) != 1 {
		t.Fatalf("unexpected score: %#v", got)
	}
}

func TestFallbackScoreIgnoresDetectorLiterals(t *testing.T) {
	score, reasons := fallbackScore("scan.go", `var fallbackContentTerms = []fallbackTerm{
	pattern("todo", "\bTODO\b", 2, 5, "work-marker"),
	pattern("provider_token_literal", "sk-example", 5, 5, "secret-risk"),
}`, 200)
	if score != 1 {
		t.Fatalf("score = %d, want 1; reasons=%#v", score, reasons)
	}
	if len(reasons) != 1 || reasons[0] != "low-signal" {
		t.Fatalf("reasons = %#v, want low-signal", reasons)
	}
}

func TestBuildReportPopulatesTriageLanes(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.test/app\n\nreplace example.test/lib => ../lib\n")
	writeFile(t, tmp, "README.md", "APP_ENV is documented\n")
	writeFile(t, tmp, "config.go", `package p

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
	config := findRow(report, "config.go")
	if config == nil || config.EnvContractRisk == 0 || !contains(config.EvidenceLayers, "env-contract") || !config.TestGap {
		t.Fatalf("config env/test-gap lanes missing: %#v", config)
	}
	flaky := findRow(report, "flaky_test.go")
	if flaky == nil || flaky.FlakeRisk == 0 || !contains(flaky.EvidenceLayers, "flake-risk") {
		t.Fatalf("flake lane missing: %#v", flaky)
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
	assertReasons("Dockerfile", "container_build:root_user", "container_build:apt_update_split:1", "container_build:apt_install_recommends:1", "container_build:apt_cache_not_cleaned:1")
	assertReasons("k8s/deploy.yaml", "kubernetes_security:privileged:1", "kubernetes_security:missing_run_as_non_root")
	assertReasons("main.tf", "terraform_security:public_s3_acl:1", "terraform_security:s3_public_block_disabled:1")
	assertReasons("openapi.yaml", "openapi_contract:anonymous_security:1", "openapi_contract:basic_auth_scheme")
	assertReasons("server.go", "cors_security:reflected_origin:1", "cors_security:wildcard_methods:1", "cors_security:wildcard_headers:1", "cors_security:long_preflight_cache_with_broad_policy", "cookie_security:host_prefix_has_domain", "cookie_security:broad_domain_scope")
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

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
