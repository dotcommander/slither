package slither

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestContentIdentitySensitivity(t *testing.T) {
	a := contentIdentity([]byte("prefix-a"), 100, true)
	if a == contentIdentity([]byte("prefix-b"), 100, true) || a == contentIdentity([]byte("prefix-a"), 101, true) || a == contentIdentity([]byte("prefix-a"), 100, false) {
		t.Fatal("content identity missed an inspected-byte, size, or truncation change")
	}
	// Deliberately, same-size changes beyond an uninspected truncated prefix do
	// not change content_id; the full digest would require another source read.
	if a != contentIdentity([]byte("prefix-a"), 100, true) {
		t.Fatal("identical inspected prefix was not stable")
	}
}

func TestReportParametersNormalizeFilterOrder(t *testing.T) {
	a := normalizedReportParameters(Options{Days: 1, MaxBytes: 2, Top: 3, Include: []string{"b", "a"}, Exclude: []string{"y", "x"}}, "sha256:patterns")
	b := normalizedReportParameters(Options{Days: 1, MaxBytes: 2, Top: 3, Include: []string{"a", "b"}, Exclude: []string{"x", "y"}}, "sha256:patterns")
	if sha256Identity("test", a) != sha256Identity("test", b) {
		t.Fatalf("equivalent filters did not normalize: %#v %#v", a, b)
	}
}

func TestReportParametersStripBaseURLUserinfo(t *testing.T) {
	t.Parallel()
	parameters := normalizedReportParameters(Options{
		Model:   "model",
		BaseURL: "https://user:private-password@example.test/v1",
	}, "sha256:patterns")
	if parameters.BaseURL != "https://example.test/v1" {
		t.Fatalf("base URL = %q, want userinfo-free endpoint", parameters.BaseURL)
	}
}

func TestReportParametersPreserveFallbackModelOrderAndMultiplicity(t *testing.T) {
	base := Options{Days: 1, MaxBytes: 2, Top: 3, Model: "model", BaseURL: "https://example.test/v1", FallbackModels: []string{"a", "b", "a"}}
	first := normalizedReportParameters(base, "sha256:patterns")
	if !reflect.DeepEqual(first.FallbackModels, []string{"a", "b", "a"}) {
		t.Fatalf("fallback models = %#v", first.FallbackModels)
	}
	reordered := base
	reordered.FallbackModels = []string{"b", "a", "a"}
	if sha256Identity("parameters", first) == sha256Identity("parameters", normalizedReportParameters(reordered, "sha256:patterns")) {
		t.Fatal("fallback order did not affect parameters")
	}
	report := Report{SchemaVersion: reportSchemaVersion, SourceState: SourceState{Kind: "filesystem", TreeDigest: "sha256:tree"}, Parameters: first, Rows: []FileEvidence{{EvidenceID: "sha256:evidence", Score: 3}}}
	firstReportID := reportIdentity(report)
	report.Parameters = normalizedReportParameters(reordered, "sha256:patterns")
	if reportIdentity(report) == firstReportID {
		t.Fatal("fallback order did not affect report identity")
	}
	withoutDuplicate := base
	withoutDuplicate.FallbackModels = []string{"a", "b"}
	if sha256Identity("parameters", first) == sha256Identity("parameters", normalizedReportParameters(withoutDuplicate, "sha256:patterns")) {
		t.Fatal("fallback multiplicity did not affect parameters")
	}
}

func TestModelContractIdentityCoversTypedComponents(t *testing.T) {
	base := modelContractIdentityFor("prompt", "cache", "projection")
	for _, changed := range []string{
		modelContractIdentityFor("prompt changed", "cache", "projection"),
		modelContractIdentityFor("prompt", "cache changed", "projection"),
		modelContractIdentityFor("prompt", "cache", "projection changed"),
	} {
		if changed == base {
			t.Fatal("model contract did not change with a typed component")
		}
	}
}

func TestSourceStateTracksGitHeadAndDirty(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	runGitTestCommand(t, repo, "config", "user.email", "slither@example.test")
	runGitTestCommand(t, repo, "config", "user.name", "Slither Test")
	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "--", "main.go")
	runGitTestCommand(t, repo, "commit", "-q", "-m", "initial")
	clean, err := BuildReport(context.Background(), Options{Repo: repo, Top: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if clean.SourceState.Kind != "git" || clean.SourceState.Head == "" || clean.SourceState.Dirty {
		t.Fatalf("clean git state = %#v", clean.SourceState)
	}
	if err := os.WriteFile(file, []byte("package main\n// changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := BuildReport(context.Background(), Options{Repo: repo, Top: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.SourceState.Dirty || dirty.SourceState.Head != clean.SourceState.Head || dirty.SourceState.TreeDigest == clean.SourceState.TreeDigest {
		t.Fatalf("dirty git state = %#v clean = %#v", dirty.SourceState, clean.SourceState)
	}
	runGitTestCommand(t, repo, "add", "--", "main.go")
	staged, err := BuildReport(context.Background(), Options{Repo: repo, Top: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !staged.SourceState.Dirty {
		t.Fatalf("staged git state = %#v, want dirty", staged.SourceState)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked, err := BuildReport(context.Background(), Options{Repo: repo, Top: 10, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !untracked.SourceState.Dirty {
		t.Fatalf("untracked git state = %#v, want dirty", untracked.SourceState)
	}
}

func TestSourceStateUnbornGitRepository(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := BuildReport(context.Background(), Options{Repo: repo, Top: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceState.Kind != "git" || report.SourceState.Head != "" || !report.SourceState.Dirty {
		t.Fatalf("unborn source state = %#v", report.SourceState)
	}
}

func TestEvidenceIdentityExcludesModelOutput(t *testing.T) {
	row := baseEvidence("auth.go", 3)
	row.ContentID = contentIdentity([]byte("auth"), 4, false)
	setDeterministicProvenance(&row)
	finalizeEvidenceMetadata("", &row)
	first := evidenceIdentity("", row)
	if !applyModelScore(&row, 5, "model prose", []string{"model interpretation"}) {
		t.Fatal("valid model result was rejected")
	}
	if got := evidenceIdentity("", row); got != first {
		t.Fatalf("model output changed evidence id: %s != %s", got, first)
	}
	base := Report{SchemaVersion: reportSchemaVersion, SourceState: SourceState{Kind: "filesystem", TreeDigest: "sha256:tree"}, Parameters: ReportParameters{Days: 1, MaxBytes: 2, PatternsID: "sha256:patterns"}, Rows: []FileEvidence{{EvidenceID: first, Score: 3}}}
	firstReport := reportIdentity(base)
	base.Rows[0].Score = 5
	if reportIdentity(base) == firstReport {
		t.Fatal("selected score did not change report identity")
	}
}

func TestEvidenceIdentityProductionScoringParity(t *testing.T) {
	base := baseEvidence("auth.go", 3)
	base.ContentID = contentIdentity([]byte("auth"), 4, false)
	setDeterministicProvenance(&base)
	finalizeEvidenceMetadata("", &base)
	want := evidenceIdentity("", base)
	assertID := func(name string, row FileEvidence) {
		t.Helper()
		finalizeEvidenceMetadata("", &row)
		if got := evidenceIdentity("", row); got != want {
			t.Fatalf("%s evidence ID = %s, want %s", name, got, want)
		}
	}
	assertID("deterministic", base)
	for name, response := range map[string]string{
		"valid":   `[{"index":0,"score":5,"summary":"model","reasons":["reason"]}]`,
		"equal":   `[{"index":0,"score":3,"summary":"model","reasons":["reason"]}]`,
		"invalid": `[{"index":0,"score":9,"summary":"model","reasons":["reason"]}]`,
		"missing": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			scorer := &ModelScorer{model: "m", generate: func(_ context.Context, _ string, _ int) (string, error) { return response, nil }}
			out, err := scorer.ScoreBatch(context.Background(), []FileEvidence{base})
			if err != nil {
				t.Fatal(err)
			}
			assertID(name, out[0])
		})
	}
	degradedScorer := &ModelScorer{model: "m", generate: func(_ context.Context, _ string, _ int) (string, error) { return "", errors.New("provider") }}
	degraded, err := degradedScorer.ScoreBatch(context.Background(), []FileEvidence{base})
	if err != nil {
		t.Fatal(err)
	}
	assertID("degraded", degraded[0])

	cache := &scoreCache{entries: map[string]cachedScore{}, dirty: map[string]cachedScore{}}
	freshScorer := &ModelScorer{model: "m", generate: func(_ context.Context, _ string, _ int) (string, error) {
		return `[{"index":0,"score":5,"summary":"model","reasons":["reason"]}]`, nil
	}}
	cold := []FileEvidence{base}
	if _, _, err := scoreTopRowsCached(context.Background(), freshScorer, cold, cache); err != nil {
		t.Fatal(err)
	}
	warm := []FileEvidence{base}
	if _, _, err := scoreTopRowsCached(context.Background(), freshScorer, warm, cache); err != nil {
		t.Fatal(err)
	}
	noCache := []FileEvidence{base}
	if err := scoreTopRows(context.Background(), freshScorer, noCache, 1, 1); err != nil {
		t.Fatal(err)
	}
	assertID("cold", cold[0])
	assertID("warm", warm[0])
	assertID("no-cache", noCache[0])
}

func TestGitMetadataBoundAndFailureSignalsParticipateInTreeDigest(t *testing.T) {
	if _, err := readBoundedGitMetadata(bytes.NewReader(bytes.Repeat([]byte("x"), maxGitMetadataBytes+1))); !errors.Is(err, errGitMetadataTooLarge) {
		t.Fatalf("bounded metadata error = %v", err)
	}
	previous := gitMetadataRunner
	gitMetadataRunner = func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "rev-parse", "symbolic-ref":
			return "", errGitMetadataTooLarge
		case "status":
			return "", errGitMetadataTooLarge
		default:
			return "", errors.New("unexpected git metadata call")
		}
	}
	t.Cleanup(func() { gitMetadataRunner = previous })
	state, signals, err := sourceStateForReport(context.Background(), "repo", DiscoveryStats{Source: "git"}, []FileEvidence{{Path: "a.go", ContentID: "sha256:a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Head != "" || !state.Dirty || !stringSliceContains(signals, "git_metadata:head_unavailable") || !stringSliceContains(signals, "git_metadata:dirty_unavailable") {
		t.Fatalf("metadata failure state=%#v signals=%#v", state, signals)
	}
	gitMetadataRunner = func(_ context.Context, _ string, _ ...string) (string, error) { return "", nil }
	withoutLimitations, _, err := sourceStateForReport(context.Background(), "repo", DiscoveryStats{Source: "git"}, []FileEvidence{{Path: "a.go", ContentID: "sha256:a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.TreeDigest == withoutLimitations.TreeDigest {
		t.Fatal("metadata limitations did not affect tree digest")
	}
}

func TestReportIdentityRowOrderAndSourceLimitationsSensitivity(t *testing.T) {
	base := Report{SchemaVersion: reportSchemaVersion, SourceState: SourceState{Kind: "filesystem", TreeDigest: "sha256:tree"}, Parameters: ReportParameters{Days: 1, MaxBytes: 2, PatternsID: "sha256:patterns"}, Rows: []FileEvidence{{EvidenceID: "sha256:a", Score: 1}, {EvidenceID: "sha256:b", Score: 2}}}
	first := reportIdentity(base)
	base.Rows[0], base.Rows[1] = base.Rows[1], base.Rows[0]
	if reportIdentity(base) == first {
		t.Fatal("row order did not affect report identity")
	}
}

func TestV1JSONIsPrivateNonMutatingAndDoesNotChangeCompatibilityViews(t *testing.T) {
	row := baseEvidence("auth.go", 3)
	row.ContentID = contentIdentity([]byte("auth"), 4, false)
	setDeterministicProvenance(&row)
	finalizeEvidenceMetadata("", &row)
	row.EvidenceID = evidenceIdentity("", row)
	row.Summary = "Bearer abcdefghijklmnop"
	report := Report{SchemaVersion: reportSchemaVersion, ReportID: "sha256:report", SourceState: SourceState{Kind: "filesystem", TreeDigest: "sha256:tree"}, Parameters: ReportParameters{Days: 1, MaxBytes: 2, PatternsID: "sha256:patterns"}, Rows: []FileEvidence{row}}
	before := report
	markdownBefore := RenderMarkdown(report)
	queueBefore, planBefore := BuildReviewPlan(report.Rows)
	ledgerBefore := BuildCullLedger(report)
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"schema_version"`)) || !bytes.Contains(data, []byte(`"content_id"`)) || bytes.Contains(data, []byte("abcdefghijklmnop")) {
		t.Fatalf("v1 JSON did not add/redact expected fields: %s", data)
	}
	if !reflect.DeepEqual(report, before) || RenderMarkdown(report) != markdownBefore {
		t.Fatal("JSON rendering mutated report or Markdown behavior")
	}
	withoutIdentities := report
	withoutIdentities.Rows = append([]FileEvidence(nil), report.Rows...)
	withoutIdentities.Rows[0].ContentID = ""
	withoutIdentities.Rows[0].EvidenceID = ""
	withoutIdentities.Rows[0].ScoreProvenance = ScoreProvenance{}
	if RenderMarkdown(withoutIdentities) != markdownBefore {
		t.Fatal("identity fields changed Markdown behavior")
	}
	queueAfter, planAfter := BuildReviewPlan(report.Rows)
	ledgerAfter := BuildCullLedger(report)
	if !reflect.DeepEqual(queueBefore, queueAfter) || !reflect.DeepEqual(planBefore, planAfter) || !reflect.DeepEqual(ledgerBefore, ledgerAfter) {
		t.Fatal("identity fields changed review-plan or cull behavior")
	}
	queueWithout, planWithout := BuildReviewPlan(withoutIdentities.Rows)
	ledgerWithout := BuildCullLedger(withoutIdentities)
	if !reflect.DeepEqual(queueBefore, queueWithout) || !reflect.DeepEqual(planBefore, planWithout) || !reflect.DeepEqual(ledgerBefore, ledgerWithout) {
		t.Fatal("identity fields changed compatibility views")
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "report_id", "source_state", "parameters", "rows"} {
		if _, ok := envelope[key]; !ok {
			t.Fatalf("v1 JSON missing %s", key)
		}
	}
}

func TestSourceStateCancellationFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := sourceStateForReport(ctx, t.TempDir(), DiscoveryStats{Source: "filesystem"}, nil, nil); err != context.Canceled {
		t.Fatalf("source state cancellation error = %v, want context.Canceled", err)
	}
}
