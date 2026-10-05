package slither

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const llmAgentFixtureDir = "testdata/llm_agent"
const oneMiB = 1 << 20

const (
	exampleReportID   = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	exampleEvidenceID = "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func TestLegacyRootHelpGolden(t *testing.T) {
	var stdout strings.Builder
	if err := Run(context.Background(), nil, &stdout, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	assertFixtureBytes(t, "root-help.txt", []byte(stdout.String()))
}

func TestLegacyJSONEnvelopeGolden(t *testing.T) {
	report := Report{
		Repo:           "/repo",
		GeneratedAt:    time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		Days:           90,
		PatternsSource: "embedded:triage_patterns.json",
		FilesSeen:      1,
		Discovery:      DiscoveryStats{Source: "filesystem", FilesystemFiles: 1, CandidateFiles: 1},
		SkippedSignals: []string{"model_scoring:not_configured"},
		Rows:           []FileEvidence{{Path: "auth.go", Bytes: 42, Lines: 3, Score: 4, SeedScore: 4, PathRisk: 3, ContentRisk: 4, EvidenceLayers: []string{"path-risk", "content-risk"}, Reasons: []string{"path:auth", "content:unsafe_query:1"}, Summary: "auth entry point"}},
	}
	got, err := renderLegacyJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.TrimSuffix(fixtureBytes(t, "legacy-envelope.json"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("legacy-envelope.json mismatch (-want +got):\n- %s\n+ %s", want, got)
	}
}

func TestContractFixturesValidate(t *testing.T) {
	report := fixtureJSONObject(t, "report-v1.json")
	requireReportV1Schema(t, report)
	validateReportV1Fixture(t, report)

	for _, name := range []string{"agent-request.jsonl", "agent-response.jsonl", "agent-error.jsonl"} {
		for _, record := range fixtureJSONL(t, name) {
			requireSchema(t, record, "slither.agent/v1")
			if id, ok := record["id"].(string); !ok || !validRequestID(id) {
				t.Fatalf("%s request id = %#v, want printable ASCII 1..128 bytes", name, record["id"])
			}
		}
	}
	requests := fixtureJSONL(t, "agent-request.jsonl")
	if len(requests) != 5 {
		t.Fatalf("agent requests = %d, want one fixture for each operation", len(requests))
	}
	for index, want := range []string{"hello", "scan", "query", "context", "feedback"} {
		if got, _ := requests[index]["op"].(string); got != want {
			t.Fatalf("agent request %d operation = %q, want %q", index, got, want)
		}
	}
	queryIDs := jsonArray(t, requests[2]["target_ids"], "query target ids")
	if got, _ := queryIDs[0].(string); !validLogicalFileID(got) {
		t.Fatalf("query target id = %#v, want slither:file: base64 ID", queryIDs[0])
	}
	responses := fixtureJSONL(t, "agent-response.jsonl")
	if len(responses) != 1 {
		t.Fatalf("agent responses = %d, want fixed hello response", len(responses))
	}
	hello := jsonObject(t, responses[0]["result"], "hello result")
	if enabled, ok := hello["feedback_enabled"].(bool); !ok || enabled {
		t.Fatalf("hello feedback_enabled = %#v, want false", hello["feedback_enabled"])
	}
	for _, key := range []string{"max_request_bytes", "max_context_bytes"} {
		if got, ok := fixtureInt(hello[key]); !ok || got != oneMiB {
			t.Fatalf("hello %s = %#v, want %d", key, hello[key], oneMiB)
		}
	}
	assertStringArray(t, hello["schemas"], []string{"slither.report/v1", "slither.context/v1", "slither.outcome/v1", "slither.eval/v1"}, "hello schemas")
	assertStringArray(t, hello["operations"], []string{"hello", "scan", "query", "context", "feedback"}, "hello operations")

	errors := fixtureJSONL(t, "agent-error.jsonl")
	var errorCodes []string
	for _, record := range errors {
		errorBody, ok := record["error"].(map[string]any)
		if !ok {
			t.Fatalf("agent error record = %#v", record)
		}
		code, _ := errorBody["code"].(string)
		errorCodes = append(errorCodes, code)
	}
	slices.Sort(errorCodes)
	wantCodes := []string{"budget_too_large", "budget_too_small", "canceled", "feedback_disabled", "feedback_write_failed", "invalid_request", "request_too_large", "scan_failed", "stale_evidence", "target_not_found", "unsupported_op", "unsupported_schema"}
	if !slices.Equal(errorCodes, wantCodes) {
		t.Fatalf("error codes = %#v, want %#v", errorCodes, wantCodes)
	}

	contextPacket := fixtureJSONObject(t, "context-v1.json")
	requireSchema(t, contextPacket, "slither.context/v1")
	budget, budgetOK := fixtureInt(contextPacket["budget_bytes"])
	used, usedOK := fixtureInt(contextPacket["used_bytes"])
	if !budgetOK || !usedOK || budget <= 0 || budget > oneMiB || used < 0 || used > budget {
		t.Fatalf("context byte accounting = budget=%v used=%v", contextPacket["budget_bytes"], contextPacket["used_bytes"])
	}
	encodedContext := bytes.TrimSuffix(fixtureBytes(t, "context-v1.json"), []byte("\n"))
	if len(encodedContext) != used {
		t.Fatalf("context used_bytes = %d, want encoded context bytes %d", used, len(encodedContext))
	}
	if used > budget || used > oneMiB {
		t.Fatalf("context fixture bytes = %d, budget = %d", used, budget)
	}
	targets := jsonArray(t, contextPacket["targets"], "context targets")
	if len(targets) != 1 {
		t.Fatalf("context targets = %#v, want one representative target", targets)
	}
	target := jsonObject(t, targets[0], "context target")
	assertJSONKeys(t, target, []string{"id", "evidence_id", "path", "actionability", "caveat", "evidence_locations", "proof_obligation", "resolution", "components"})
	if !validLogicalFileID(target["id"].(string)) || !validSHA256Identity(target["evidence_id"].(string)) {
		t.Fatalf("context target identities = %#v", target)
	}
	proof := jsonObject(t, target["proof_obligation"], "proof obligation")
	assertJSONKeys(t, proof, []string{"id", "target_id", "hypothesis", "support", "falsifiers", "promotion_gate", "verify"})
	if !validSHA256Identity(proof["id"].(string)) || proof["target_id"] != target["id"] {
		t.Fatalf("context proof identity = %#v", proof)
	}
	if _, ok := proof["support"].([]any); !ok {
		t.Fatalf("context proof support = %#v, want array", proof["support"])
	}
	if _, ok := proof["verify"].([]any); !ok {
		t.Fatalf("context proof verify = %#v, want array", proof["verify"])
	}
	falsifiers := jsonArray(t, proof["falsifiers"], "context proof falsifiers")
	for index, value := range falsifiers {
		falsifier := jsonObject(t, value, fmt.Sprintf("context falsifier %d", index))
		if _, ok := falsifier["operation"].(string); !ok {
			t.Fatalf("context falsifier %d operation = %#v", index, falsifier["operation"])
		}
		for key := range falsifier {
			if key != "operation" && key != "path" && key != "query" && key != "subject" && key != "command" {
				t.Fatalf("context falsifier %d has unexpected key %q", index, key)
			}
		}
	}
	components := jsonArray(t, target["components"], "context components")
	if len(components) != 1 {
		t.Fatalf("context components = %#v", components)
	}
	assertJSONKeys(t, jsonObject(t, components[0], "context component"), []string{"kind", "id", "path", "text"})

	validateOutcomeFixture(t, "outcome.jsonl", false)
	validateOutcomeFixture(t, "outcome-partial-tail.jsonl", true)
	evaluation := fixtureJSONObject(t, "eval-v1.json")
	requireSchema(t, evaluation, "slither.eval/v1")
}

func assertStringArray(t *testing.T, value any, want []string, name string) {
	t.Helper()
	values := jsonArray(t, value, name)
	got := make([]string, len(values))
	for index, value := range values {
		var ok bool
		if got[index], ok = value.(string); !ok {
			t.Fatalf("%s[%d] = %#v, want string", name, index, value)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}

func TestLegacyJSONEnvelopeKeyInventory(t *testing.T) {
	row := FileEvidence{
		ID: "file:auth.go", Path: "auth.go", EvidenceClass: "production", Confidence: "high", Actionability: ActionabilityLikelyDefect, Caveat: "caveat", VerifyCmd: "go test ./...", OmittedReason: "none",
		Bytes: 42, Lines: 3, Score: 4, SeedScore: 3.5, Churn: 1, CommitTouches: 2, ChurnAfterCreation: 1, ChurnProfile: "reworked", FixTouches: 2, Markers: 3, Imports: 4, IncomingRefs: 5,
		SmellRisk: 1, HotspotRisk: 1, SDKDXRisk: 1, UnknownsRisk: 1, EnvContractRisk: 1, WorkflowSecurityRisk: 1, MigrationSafetyRisk: 1, ContainerBuildRisk: 1, KubernetesSecurityRisk: 1, TerraformSecurityRisk: 1, OpenAPIContractRisk: 1, CORSSecurityRisk: 1, CookieSecurityRisk: 1, DependencyHealthRisk: 1, CentralityRisk: 1, CochangeRisk: 1, OwnershipRisk: 1, FlakeRisk: 1, OracleRisk: 1, StaleMarkerRisk: 1,
		TestGap: true, PathRisk: 1, ContentRisk: 1, EvidenceLayers: []string{"content-risk"}, Reasons: []string{"content:unsafe_query:1"}, EvidenceLocations: []EvidenceLocation{{Reason: "content:unsafe_query:1", Line: 2, Snippet: "query"}}, CullDecision: CullDecisionKeptForPremium, CullReason: "strong signal", Summary: "summary", Excerpt: "excerpt",
	}
	queue := ReviewQueue{ID: "queue-1", Group: "group", Lane: "lane", EvidenceClass: "production", Confidence: "high", Reasons: []string{"reason"}, Caveat: "caveat", Files: []string{"auth.go"}, OmittedReason: "none"}
	lane := ReviewLane{ID: "lane-1", Lane: "lane", Group: "group", EvidenceClass: "production", Confidence: "high", Files: []string{"auth.go"}, Caveat: "caveat", Gates: []string{"gate"}, Verify: []string{"go test ./..."}, Why: []string{"reason"}, OmittedReason: "none"}
	bucket := CullBucket{Count: 1, Examples: []CullEntry{{Path: "auth.go", Score: 4, EvidenceClass: "production", Confidence: "high", Actionability: ActionabilityLikelyDefect, Caveat: "caveat", VerifyCmd: "go test ./...", EvidenceLayers: []string{"content-risk"}, StrongestEvidenceIntersection: "content-risk", Reason: "reason"}}}
	ledger := &CullLedger{RunLabel: "slither_cull_ledger", Repo: "/repo", GeneratedAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), RowsConsidered: 1, StopReason: "complete", SkippedSignals: []string{"signal"}, KeptForPremium: bucket, Alternates: bucket, Generated: bucket, Documentation: bucket, TestOnly: bucket, LowSignal: bucket, Duplicate: bucket, NeedsEvidence: bucket, FirstReadQueue: []ReviewQueue{queue}, ReviewPlan: []ReviewLane{lane}}
	report := Report{Repo: "/repo", GeneratedAt: ledger.GeneratedAt, Days: 90, PatternsSource: "patterns", FilesSeen: 1, Discovery: DiscoveryStats{Source: "git", GitTracked: 1, GitUntracked: 1, FilesystemFiles: 1, CandidateFiles: 1}, Model: "model", BaseURL: "https://example.test/v1", Build: BuildInfo{Module: "example.test/slither", Version: "v1", Revision: "abc", Modified: true, GoVersion: "go1.26"}, SkippedSignals: []string{"signal"}, Rows: []FileEvidence{row}, FirstReadQueue: []ReviewQueue{queue}, ReviewPlan: []ReviewLane{lane}, Filters: ReportFilters{Focus: "auth", Include: []string{"**/*.go"}, Exclude: []string{"**/*_test.go"}, Inventory: "data-integrity"}, WhyTop: []WhyTopEntry{{Rank: 1, Path: "auth.go", Score: 4, Confidence: "high", Actionability: ActionabilityLikelyDefect, Evidence: []string{"content-risk"}, Reasons: []string{"reason"}, ScoreBreakdown: []WhyTopScoreComponent{{Name: "content", Value: 1}}, VerifyCmd: "go test ./...", Note: "note"}}, FreshnessHint: "fresh", CullLedger: ledger, CacheStats: &CacheStats{Hits: 1, Misses: 2}}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, envelope, []string{"run_label", "repo", "generated_at", "days", "patterns_source", "build", "files_seen", "files_reported", "row_count", "discovery", "model", "base_url", "skipped_signals", "filters", "rows", "why_top", "freshness_hint", "first_read_queue", "review_plan", "cull_ledger", "cache_stats", "schema_version", "report_id", "source_state", "parameters"})
	assertJSONKeys(t, jsonObject(t, envelope["build"], "build"), []string{"module", "version", "revision", "modified", "go_version"})
	assertJSONKeys(t, jsonObject(t, envelope["discovery"], "discovery"), []string{"source", "git_tracked", "git_untracked", "filesystem_files", "candidate_files"})
	assertJSONKeys(t, jsonObject(t, envelope["filters"], "filters"), []string{"focus", "include", "exclude", "inventory"})
	rows, ok := envelope["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("rows = %#v", envelope["rows"])
	}
	jsonRow, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("row = %#v", rows[0])
	}
	assertJSONKeys(t, jsonRow, []string{"id", "path", "evidence_class", "confidence", "actionability", "caveat", "verify_cmd", "omitted_reason", "bytes", "lines", "score", "seed_score", "churn", "commit_touches", "churn_after_creation", "churn_profile", "fix_touches", "markers", "imports", "incoming_refs", "smell_risk", "hotspot_risk", "sdk_dx_risk", "unknowns_risk", "env_contract_risk", "workflow_security_risk", "migration_safety_risk", "container_build_risk", "kubernetes_security_risk", "terraform_security_risk", "openapi_contract_risk", "cors_security_risk", "cookie_security_risk", "dependency_health_risk", "centrality_risk", "cochange_risk", "ownership_risk", "flake_risk", "oracle_risk", "stale_marker_risk", "test_gap", "path_risk", "content_risk", "evidence_layers", "reasons", "evidence_locations", "cull_decision", "cull_reason", "summary", "excerpt"})
	evidenceLocations := jsonArray(t, jsonRow["evidence_locations"], "evidence_locations")
	assertJSONKeys(t, jsonObject(t, evidenceLocations[0], "evidence location"), []string{"reason", "line", "snippet"})
	whyTop := jsonArray(t, envelope["why_top"], "why_top")
	assertJSONKeys(t, jsonObject(t, whyTop[0], "why_top item"), []string{"rank", "path", "score", "confidence", "actionability", "evidence", "reasons", "score_breakdown", "verify_cmd", "note"})
	scoreBreakdown := jsonArray(t, jsonObject(t, whyTop[0], "why_top item")["score_breakdown"], "score_breakdown")
	assertJSONKeys(t, jsonObject(t, scoreBreakdown[0], "score_breakdown item"), []string{"name", "value"})
	queueItems := jsonArray(t, envelope["first_read_queue"], "first_read_queue")
	assertJSONKeys(t, jsonObject(t, queueItems[0], "first_read_queue item"), []string{"id", "group", "lane", "evidence_class", "confidence", "reasons", "caveat", "files", "omitted_reason"})
	reviewPlan := jsonArray(t, envelope["review_plan"], "review_plan")
	assertJSONKeys(t, jsonObject(t, reviewPlan[0], "review_plan item"), []string{"id", "lane", "group", "evidence_class", "confidence", "files", "caveat", "gates", "verify", "why", "omitted_reason"})
	ledgerJSON := jsonObject(t, envelope["cull_ledger"], "cull_ledger")
	assertJSONKeys(t, ledgerJSON, []string{"run_label", "repo", "generated_at", "rows_considered", "stop_reason", "skipped_signals", "kept_for_premium", "alternates", "culled_generated_or_report", "culled_documentation", "culled_test_only", "culled_low_signal", "culled_duplicate_surface", "needs_more_evidence", "first_read_queue", "review_plan"})
	for _, key := range []string{"kept_for_premium", "alternates", "culled_generated_or_report", "culled_documentation", "culled_test_only", "culled_low_signal", "culled_duplicate_surface", "needs_more_evidence"} {
		bucketJSON := jsonObject(t, ledgerJSON[key], "cull bucket "+key)
		assertJSONKeys(t, bucketJSON, []string{"count", "examples"})
		examples := jsonArray(t, bucketJSON["examples"], "cull examples "+key)
		assertJSONKeys(t, jsonObject(t, examples[0], "cull entry "+key), []string{"path", "score", "evidence_class", "confidence", "actionability", "caveat", "verify_cmd", "evidence_layers", "strongest_evidence_intersection", "reason"})
	}
	assertJSONKeys(t, jsonObject(t, envelope["cache_stats"], "cache_stats"), []string{"hits", "misses"})
}

func TestContractFixtureOutcomeValidationRejectsPrivateOrUnexpectedData(t *testing.T) {
	base := validOutcomeRecord()
	tests := []struct {
		name string
		set  func(map[string]any)
	}{
		{"repository path field", func(record map[string]any) { record["repository_path"] = "/private/repo" }},
		{"file path field", func(record map[string]any) { record["file_path"] = "internal/auth.go" }},
		{"note text field", func(record map[string]any) { record["note_text"] = "review notes" }},
		{"details object", func(record map[string]any) { record["details"] = map[string]any{"credential": "sentinel"} }},
		{"path-like lane", func(record map[string]any) { record["lane"] = "/private/repo" }},
		{"relative path-like lane", func(record map[string]any) { record["lane"] = "internal/auth.go" }},
		{"auth file lane", func(record map[string]any) { record["lane"] = "auth.go" }},
		{"bearer credential lane", func(record map[string]any) { record["lane"] = "Bearer sk-example" }},
		{"arbitrary note lane", func(record map[string]any) { record["lane"] = "review this later" }},
		{"malformed identity", func(record map[string]any) { record["evidence_id"] = "sha256:abc" }},
		{"nonhex identity", func(record map[string]any) {
			record["evidence_id"] = "sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
		}},
		{"non-scalar lane", func(record map[string]any) { record["lane"] = []any{"kept_for_premium"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := validOutcomeRecord()
			test.set(record)
			if err := validateOutcomeRecord(record); err == nil {
				t.Fatal("private or unexpected outcome data was accepted")
			}
		})
	}
	if err := validateOutcomeRecord(base); err != nil {
		t.Fatalf("valid outcome record rejected: %v", err)
	}
}

func TestContractFixtureOutcomePartialTailRules(t *testing.T) {
	complete, err := json.Marshal(validOutcomeRecord())
	if err != nil {
		t.Fatal(err)
	}
	interiorMalformed := append(append(append([]byte(nil), complete...), '\n'), []byte(`{"schema":}`)...)
	interiorMalformed = append(interiorMalformed, '\n')
	interiorMalformed = append(interiorMalformed, complete...)
	if _, _, err := parseOutcomeJSONL(interiorMalformed, true); err == nil {
		t.Fatal("interior malformed outcome record was accepted")
	}
	partialTail := append(append(append([]byte(nil), complete...), '\n'), []byte(`{"schema":"slither.outcome/v1"`)...)
	if records, malformed, err := parseOutcomeJSONL(partialTail, true); err != nil || malformed != 1 || len(records) != 1 {
		t.Fatalf("partial tail parse = records=%d malformed=%d err=%v, want one complete record and one ignored tail", len(records), malformed, err)
	}
}

func TestContractFixtureRecordsRemainWithinOneMiB(t *testing.T) {
	for _, name := range []string{"report-v1.json", "context-v1.json", "eval-v1.json"} {
		if got := len(fixtureBytes(t, name)); got > oneMiB {
			t.Fatalf("%s is %d bytes, exceeds 1 MiB", name, got)
		}
	}
	for _, name := range []string{"agent-request.jsonl", "agent-response.jsonl", "agent-error.jsonl", "outcome.jsonl", "outcome-partial-tail.jsonl"} {
		for _, line := range bytes.Split(fixtureBytes(t, name), []byte("\n")) {
			if len(line) > oneMiB {
				t.Fatalf("%s record is %d bytes, exceeds 1 MiB", name, len(line))
			}
		}
	}
}

func TestReportV1IdentityContract(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "auth.go"), []byte("package auth\n// TODO: verify authorization\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := BuildReport(context.Background(), Options{Repo: repo, Top: 10, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildReport(context.Background(), Options{Repo: repo, Top: 10, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if first.SchemaVersion != reportSchemaVersion || !validSHA256Identity(first.ReportID) || !validSHA256Identity(first.SourceState.TreeDigest) {
		t.Fatalf("report identity = %#v", first)
	}
	if first.ReportID != second.ReportID || first.SourceState.TreeDigest != second.SourceState.TreeDigest {
		t.Fatalf("identities drifted: first=%s second=%s", first.ReportID, second.ReportID)
	}
	if first.SourceState.Kind != "filesystem" || first.SourceState.Head != "" || first.SourceState.Dirty {
		t.Fatalf("filesystem source state = %#v", first.SourceState)
	}
	if len(first.Rows) != 1 || !validSHA256Identity(first.Rows[0].ContentID) || !validSHA256Identity(first.Rows[0].EvidenceID) {
		t.Fatalf("row identities = %#v", first.Rows)
	}
	data, err := RenderJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		SchemaVersion string           `json:"schema_version"`
		ReportID      string           `json:"report_id"`
		SourceState   SourceState      `json:"source_state"`
		Parameters    ReportParameters `json:"parameters"`
		Rows          []FileEvidence   `json:"rows"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != reportSchemaVersion || payload.ReportID != first.ReportID || payload.Parameters.MaxBytes != 1024 || payload.Rows[0].ScoreProvenance.SelectedBy != "deterministic" {
		t.Fatalf("v1 JSON envelope = %#v", payload)
	}
}

func TestScoreProvenanceContract(t *testing.T) {
	deterministic := baseEvidence("auth.go", 3)
	setDeterministicProvenance(&deterministic)
	if deterministic.ScoreProvenance.SelectedBy != "deterministic" || deterministic.ScoreProvenance.Deterministic != 3 || deterministic.ScoreProvenance.Model != nil {
		t.Fatalf("deterministic provenance = %#v", deterministic.ScoreProvenance)
	}
	scorer := &ModelScorer{generate: func(_ context.Context, _ string, _ int) (string, error) {
		return `[{"index":0,"score":3,"summary":"same score","reasons":["review"]}]`, nil
	}}
	model, err := scorer.ScoreBatch(context.Background(), []FileEvidence{deterministic})
	if err != nil {
		t.Fatal(err)
	}
	if model[0].ScoreProvenance.SelectedBy != "model" || model[0].ScoreProvenance.Model == nil || *model[0].ScoreProvenance.Model != 3 {
		t.Fatalf("model provenance = %#v", model[0].ScoreProvenance)
	}
	invalid := &ModelScorer{generate: func(_ context.Context, _ string, _ int) (string, error) {
		return `[{"index":0,"score":9}]`, nil
	}}
	degraded, err := invalid.ScoreBatch(context.Background(), []FileEvidence{deterministic})
	if err != nil {
		t.Fatal(err)
	}
	if degraded[0].Score != 3 || degraded[0].ScoreProvenance.SelectedBy != "deterministic" || degraded[0].ScoreProvenance.Model != nil || !hasModelError(degraded[0].Reasons) {
		t.Fatalf("invalid model result = %#v", degraded[0])
	}
}

func TestContextPacketSelectionContract(t *testing.T) {
	TestBuildContextPacketSelectionFocusAndDefault(t)
}

func TestContextPacketBudgetContract(t *testing.T) {
	TestBuildContextPacketBudgetAccountingAndOmissions(t)
}

func TestContextPacketSafetyContract(t *testing.T) {
	TestBuildContextPacketSafetyCancellationAndUniqueReads(t)
}

func TestContextPacketDeterminismContract(t *testing.T) {
	TestBuildContextPacketDeclarationLexicalAndComponents(t)
}

func TestContextPacketCancellationContract(t *testing.T) {
	TestBuildContextPacketSafetyCancellationAndUniqueReads(t)
}

func TestContextPacketNoRescanContract(t *testing.T) {
	TestContextImportGraphPreservesCountsAndRanking(t)
}

func TestProofObligationContract(t *testing.T) {
	assertProofObligationContract(t)
}

func TestPromotionGateContract(t *testing.T) {
	assertPromotionGateContract(t)
}

func TestAgentFramingContract(t *testing.T) {
	TestAgentProtocolFramingBoundaries(t)
}

func TestAgentOperationsContract(t *testing.T) {
	TestAgentOperationsAndOfflineContract(t)
}

func TestAgentRecoveryContract(t *testing.T) {
	TestAgentRecoveryAndFeedbackSeam(t)
}

func TestAgentEOFContract(t *testing.T) {
	TestAgentEOFAndSnapshotInvalidation(t)
}

func TestAgentHelpContract(t *testing.T) {
	TestAgentHelpAndOutcomesSeam(t)
}

func TestAgentCancellationContract(t *testing.T) {
	TestAgentCancellationAndFatalOutput(t)
}

func TestOutcomeSecurityPrivacyContract(t *testing.T) {
	TestOutcomeDerivesTypedRecordAndAppendsOwnerOnly(t)
	TestOutcomeWriterRejectsUnsafeExistingTargets(t)
}

func TestEvalMultiReportContract(t *testing.T) {
	TestEvalStreamsMultiReportOutcomesAndPartialTail(t)
	TestEvalRejectsDuplicateReportIdentityAndIntegrityMismatch(t)
}

func assertFixtureBytes(t *testing.T, name string, got []byte) {
	t.Helper()
	want := fixtureBytes(t, name)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch (-want +got):\n- %s\n+ %s", name, want, got)
	}
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(llmAgentFixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureJSONObject(t *testing.T, name string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(fixtureBytes(t, name), &value); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return value
}

func fixtureJSONL(t *testing.T, name string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for index, line := range bytes.Split(fixtureBytes(t, name), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatalf("decode %s line %d: %v", name, index+1, err)
		}
		records = append(records, value)
	}
	return records
}

func requireSchema(t *testing.T, value map[string]any, want string) {
	t.Helper()
	if got, _ := value["schema"].(string); got != want {
		t.Fatalf("schema = %q, want %q", got, want)
	}
}

func requireReportV1Schema(t *testing.T, value map[string]any) {
	t.Helper()
	if got, _ := value["schema_version"].(string); got != "slither.report/v1" {
		t.Fatalf("report schema_version = %q, want %q", got, "slither.report/v1")
	}
	if _, hasLegacySchemaKey := value["schema"]; hasLegacySchemaKey {
		t.Fatal("report-v1 fixture used generic schema instead of schema_version")
	}
}

func validateReportV1Fixture(t *testing.T, report map[string]any) {
	t.Helper()
	if reportID, ok := report["report_id"].(string); !ok || !validSHA256Identity(reportID) {
		t.Fatalf("report_id = %#v, want lowercase sha256 identity", report["report_id"])
	}
	source := jsonObject(t, report["source_state"], "source_state")
	if kind, _ := source["kind"].(string); kind != "git" && kind != "filesystem" {
		t.Fatalf("source_state.kind = %#v", source["kind"])
	}
	if tree, ok := source["tree_digest"].(string); !ok || !validSHA256Identity(tree) {
		t.Fatalf("source_state.tree_digest = %#v, want lowercase sha256 identity", source["tree_digest"])
	}
	if _, ok := source["dirty"].(bool); !ok {
		t.Fatalf("source_state.dirty = %#v, want bool", source["dirty"])
	}

	parameters := jsonObject(t, report["parameters"], "parameters")
	for _, key := range []string{"days", "max_bytes", "top"} {
		if value, ok := fixtureInt(parameters[key]); !ok || value < 0 || (key != "top" && value == 0) {
			t.Fatalf("parameters.%s = %#v, want required non-negative integer", key, parameters[key])
		}
	}
	if patternsID, ok := parameters["patterns_id"].(string); !ok || !validSHA256Identity(patternsID) {
		t.Fatalf("parameters.patterns_id = %#v, want lowercase sha256 identity", parameters["patterns_id"])
	}
	if model, hasModel := parameters["model"].(string); hasModel && model != "" {
		if baseURL, _ := parameters["base_url"].(string); baseURL == "" {
			t.Fatal("model parameters missing base_url")
		}
		if contractID, ok := parameters["model_contract_id"].(string); !ok || !validSHA256Identity(contractID) {
			t.Fatalf("parameters.model_contract_id = %#v, want lowercase sha256 identity", parameters["model_contract_id"])
		}
		fallbacks, ok := parameters["fallback_models"].([]any)
		if !ok {
			t.Fatalf("parameters.fallback_models = %#v, want string array", parameters["fallback_models"])
		}
		for _, fallback := range fallbacks {
			if value, ok := fallback.(string); !ok || value == "" {
				t.Fatalf("fallback model = %#v, want non-empty string", fallback)
			}
		}
	}

	rows := jsonArray(t, report["rows"], "rows")
	if len(rows) == 0 {
		t.Fatal("report-v1 fixture has no rows")
	}
	for index, rawRow := range rows {
		row := jsonObject(t, rawRow, "row")
		id, _ := row["id"].(string)
		if !validLogicalFileID(id) {
			t.Fatalf("row %d id = %#v, want stable logical file identity", index, row["id"])
		}
		for _, key := range []string{"content_id", "evidence_id"} {
			if identity, ok := row[key].(string); !ok || !validSHA256Identity(identity) {
				t.Fatalf("row %d %s = %#v, want lowercase sha256 identity", index, key, row[key])
			}
		}
		score, scoreOK := fixtureInt(row["score"])
		provenance := jsonObject(t, row["score_provenance"], "score_provenance")
		deterministic, deterministicOK := fixtureInt(provenance["deterministic"])
		selectedBy, selectedOK := provenance["selected_by"].(string)
		if !scoreOK || !deterministicOK || !selectedOK || (selectedBy != "deterministic" && selectedBy != "model") {
			t.Fatalf("row %d score provenance = %#v", index, provenance)
		}
		modelScore, hasModelScore := fixtureInt(provenance["model"])
		switch selectedBy {
		case "deterministic":
			if hasModelScore || score != deterministic {
				t.Fatalf("row %d deterministic provenance inconsistent: %#v", index, provenance)
			}
		case "model":
			if !hasModelScore || score != modelScore {
				t.Fatalf("row %d model provenance inconsistent: %#v", index, provenance)
			}
		}
	}
}

func validLogicalFileID(id string) bool {
	const prefix = "slither:file:"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	path, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, prefix))
	return err == nil && len(path) > 0 && stableFileID(string(path)) == id
}

func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, b := range []byte(id) {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

func fixtureInt(value any) (int, bool) {
	number, ok := value.(float64)
	return int(number), ok && number == float64(int(number))
}

func validateOutcomeFixture(t *testing.T, name string, wantPartialTail bool) {
	t.Helper()
	records, malformed, err := parseOutcomeJSONL(fixtureBytes(t, name), wantPartialTail)
	if err != nil {
		t.Fatalf("validate %s: %v", name, err)
	}
	wantMalformed := 0
	if wantPartialTail {
		wantMalformed = 1
	}
	if malformed != wantMalformed {
		t.Fatalf("%s malformed records = %d, want %d", name, malformed, wantMalformed)
	}
	if len(records) == 0 {
		t.Fatalf("%s contains no complete outcome records", name)
	}
	for _, record := range records {
		if err := validateOutcomeRecord(record); err != nil {
			t.Fatalf("%s invalid outcome record: %v", name, err)
		}
	}
}

func parseOutcomeJSONL(raw []byte, allowPartialTail bool) ([]map[string]any, int, error) {
	lines := bytes.Split(raw, []byte("\n"))
	lastRecord := len(lines) - 1
	if lastRecord >= 0 && len(lines[lastRecord]) == 0 {
		lastRecord--
	}
	var records []map[string]any
	malformed := 0
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			malformed++
			if allowPartialTail && index == lastRecord && strings.Contains(err.Error(), "unexpected end of JSON input") {
				continue
			}
			return nil, malformed, fmt.Errorf("malformed outcome line %d: %w", index+1, err)
		}
		records = append(records, record)
	}
	return records, malformed, nil
}

func validOutcomeRecord() map[string]any {
	return map[string]any{"schema": "slither.outcome/v1", "timestamp": "2024-01-02T03:04:05Z", "report_id": exampleReportID, "evidence_id": exampleEvidenceID, "lane": "kept_for_premium", "rank": float64(1), "score": float64(4), "verdict": "confirmed", "files_opened": float64(2), "tool_calls": float64(3), "review_ms": float64(125)}
}

func validateOutcomeRecord(record map[string]any) error {
	allowed := []string{"schema", "timestamp", "report_id", "evidence_id", "lane", "rank", "score", "verdict", "files_opened", "tool_calls", "review_ms"}
	if len(record) != len(allowed) {
		return fmt.Errorf("outcome keys = %d, want %d", len(record), len(allowed))
	}
	for _, key := range allowed {
		value, ok := record[key]
		if !ok {
			return fmt.Errorf("missing outcome key %q", key)
		}
		switch value.(type) {
		case string, float64, bool, nil:
		default:
			return fmt.Errorf("outcome key %q is not scalar", key)
		}
	}
	if schema, _ := record["schema"].(string); schema != "slither.outcome/v1" {
		return fmt.Errorf("schema = %q", schema)
	}
	timestamp, ok := record["timestamp"].(string)
	if !ok {
		return fmt.Errorf("timestamp is not a string")
	}
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		return fmt.Errorf("timestamp is not RFC3339: %w", err)
	}
	for _, key := range []string{"report_id", "evidence_id"} {
		identity, ok := record[key].(string)
		if !ok || !validSHA256Identity(identity) {
			return fmt.Errorf("%s is not a lowercase sha256 identity", key)
		}
	}
	lane, ok := record["lane"].(string)
	if !ok || !validOutcomeLane(lane) {
		return fmt.Errorf("lane is not a known derived lane")
	}
	rank, ok := fixtureInt(record["rank"])
	if !ok || rank <= 0 {
		return fmt.Errorf("rank is not a positive integer")
	}
	if _, ok := fixtureInt(record["score"]); !ok {
		return fmt.Errorf("score is not an integer")
	}
	if verdict, _ := record["verdict"].(string); !slices.Contains([]string{"confirmed", "refuted", "unknown", "skipped"}, verdict) {
		return fmt.Errorf("verdict is invalid")
	}
	for _, key := range []string{"files_opened", "tool_calls", "review_ms"} {
		if number, ok := fixtureInt(record[key]); !ok || number < 0 {
			return fmt.Errorf("%s is not a non-negative integer", key)
		}
	}
	return nil
}

func validSHA256Identity(identity string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(identity, prefix) || len(identity) != len(prefix)+64 {
		return false
	}
	for _, b := range identity[len(prefix):] {
		if !(b >= '0' && b <= '9') && !(b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func validOutcomeLane(lane string) bool {
	return map[string]bool{
		"kept_for_premium":           true,
		"alternates":                 true,
		"culled_generated_or_report": true,
		"culled_documentation":       true,
		"culled_test_only":           true,
		"culled_low_signal":          true,
		"culled_duplicate_surface":   true,
		"needs_more_evidence":        true,
	}[lane]
}

func assertJSONKeys(t *testing.T, value map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	slices.Sort(got)
	want = append([]string(nil), want...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("JSON keys = %#v, want %#v", got, want)
	}
}

func jsonObject(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want object", label, value)
	}
	return object
}

func jsonArray(t *testing.T, value any, label string) []any {
	t.Helper()
	array, ok := value.([]any)
	if !ok || len(array) == 0 {
		t.Fatalf("%s = %#v, want nonempty array", label, value)
	}
	return array
}
