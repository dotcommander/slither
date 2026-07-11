package slither

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func baseEvidence(path string, score int) FileEvidence {
	return FileEvidence{
		Path:           path,
		Score:          score,
		Lines:          120,
		ContentRisk:    5,
		EvidenceLayers: []string{"content-risk"},
		Reasons:        []string{"content:x"},
		Summary:        "det " + path,
		Excerpt:        "EXCERPT_" + path,
	}
}

func hasLayer(layers []string, want string) bool {
	for _, l := range layers {
		if l == want {
			return true
		}
	}
	return false
}

func hasModelError(reasons []string) bool {
	for _, r := range reasons {
		if strings.HasPrefix(r, "model_error:") {
			return true
		}
	}
	return false
}

func TestScoreBatchMapsResultsByIndex(t *testing.T) {
	t.Parallel()
	rows := []FileEvidence{baseEvidence("a.go", 2), baseEvidence("b.go", 2), baseEvidence("c.go", 2)}
	s := &ModelScorer{generate: func(_ context.Context, _ string, _ int) (string, error) {
		return `[{"index":0,"score":5,"summary":"a hot","reasons":["ra"]},{"index":1,"score":4,"summary":"b warm","reasons":["rb"]},{"index":2,"score":3,"summary":"c mild","reasons":["rc"]}]`, nil
	}}
	out, err := s.ScoreBatch(context.Background(), rows)
	if err != nil {
		t.Fatalf("ScoreBatch err: %v", err)
	}
	if out[0].Score != 5 || out[1].Score != 4 || out[2].Score != 3 {
		t.Fatalf("scores = %d/%d/%d", out[0].Score, out[1].Score, out[2].Score)
	}
	if out[0].Summary != "a hot" || !hasLayer(out[0].EvidenceLayers, "model") {
		t.Fatalf("row0 not model-scored: %+v", out[0])
	}
	if !hasLayer(out[0].EvidenceLayers, "content-risk") {
		t.Fatalf("deterministic layer lost: %+v", out[0].EvidenceLayers)
	}
	if !stringSliceContains(out[0].Reasons, "content:x") || !stringSliceContains(out[0].Reasons, "model:ra") {
		t.Fatalf("row0 reasons = %#v, want deterministic and namespaced model reasons", out[0].Reasons)
	}
}

func TestScoreBatchMissingIndexFallsBack(t *testing.T) {
	t.Parallel()
	rows := []FileEvidence{baseEvidence("a.go", 2), baseEvidence("b.go", 2)}
	s := &ModelScorer{generate: func(_ context.Context, _ string, _ int) (string, error) {
		return `[{"index":0,"score":5,"summary":"a","reasons":["ra"]}]`, nil
	}}
	out, err := s.ScoreBatch(context.Background(), rows)
	if err != nil {
		t.Fatalf("missing model index should degrade, got error: %v", err)
	}
	if out[1].Score != 2 {
		t.Fatalf("missing-index score = %d, want deterministic 2", out[1].Score)
	}
	if !hasModelError(out[1].Reasons) || !hasLayer(out[1].EvidenceLayers, "model-error") {
		t.Fatalf("missing-index row lacks model_error: %+v", out[1])
	}
}

func TestScoreBatchCallErrorDegradesAll(t *testing.T) {
	t.Parallel()
	rows := []FileEvidence{baseEvidence("a.go", 4), baseEvidence("b.go", 3)}
	s := &ModelScorer{generate: func(_ context.Context, _ string, _ int) (string, error) {
		return "", context.DeadlineExceeded
	}}
	out, err := s.ScoreBatch(context.Background(), rows)
	if err != nil {
		t.Fatalf("provider timeout should degrade, got error: %v", err)
	}
	if out[0].Score != 4 || out[1].Score != 3 {
		t.Fatalf("degraded scores changed: %d/%d", out[0].Score, out[1].Score)
	}
	for i := range out {
		if !hasModelError(out[i].Reasons) {
			t.Fatalf("row %d missing model_error", i)
		}
	}
}

func TestProjectEvidenceOmitsExcerptAndZeroRisks(t *testing.T) {
	t.Parallel()
	e := baseEvidence("a.go", 3) // ContentRisk=5 set; all other risks zero
	payload, err := json.Marshal(projectEvidence(0, e))
	if err != nil {
		t.Fatal(err)
	}
	s := string(payload)
	if strings.Contains(s, "EXCERPT_") || strings.Contains(s, "excerpt") || strings.Contains(s, "summary") {
		t.Fatalf("excerpt or summary leaked into projection: %s", s)
	}
	if strings.Contains(s, "path_risk") || strings.Contains(s, "ownership_risk") {
		t.Fatalf("zero risk signal not omitted: %s", s)
	}
	if !strings.Contains(s, "content_risk") {
		t.Fatalf("non-zero content_risk missing: %s", s)
	}
}

func TestScoreTopRowsPreservesOrderAndCount(t *testing.T) {
	t.Parallel()
	rows := make([]FileEvidence, 20)
	for i := range rows {
		rows[i] = baseEvidence("f"+itoa(i)+".go", 2)
	}
	// Stub echoes each file's own path into its summary, so a batch written to
	// the wrong index range surfaces as a row whose summary != its own path.
	s := &ModelScorer{generate: func(_ context.Context, prompt string, _ int) (string, error) {
		parts := strings.SplitN(prompt, "Files:\n", 2)
		if len(parts) != 2 {
			t.Errorf("prompt missing Files: marker")
			return "", nil
		}
		var proj []scoringEvidence
		if err := json.Unmarshal([]byte(parts[1]), &proj); err != nil {
			return "", err
		}
		out := make([]batchModelScore, len(proj))
		for i, p := range proj {
			out[i] = batchModelScore{Index: p.Index, Score: 5, Summary: p.Path, Reasons: []string{"r"}}
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}}
	if err := scoreTopRows(context.Background(), s, rows, modelBatchSize, modelScoreConcurrency); err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		want := "f" + itoa(i) + ".go"
		if rows[i].Path != want {
			t.Fatalf("row %d path = %q, want %q (order corrupted)", i, rows[i].Path, want)
		}
		if rows[i].Summary != want {
			t.Fatalf("row %d summary = %q, want %q (batch mis-mapped)", i, rows[i].Summary, want)
		}
	}
}

func TestScoreBatchParentCancellationPropagates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	s := &ModelScorer{generate: func(_ context.Context, _ string, _ int) (string, error) {
		cancel()
		return "", context.Canceled
	}}
	out, err := s.ScoreBatch(ctx, []FileEvidence{baseEvidence("a.go", 4)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScoreBatch error = %v, want context.Canceled", err)
	}
	if hasModelError(out[0].Reasons) {
		t.Fatalf("parent cancellation degraded into model_error: %#v", out[0].Reasons)
	}
}

func TestScoreTopRowsParentCancellationPropagates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := scoreTopRows(ctx, &ModelScorer{}, []FileEvidence{baseEvidence("a.go", 2)}, 1, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scoreTopRows error = %v, want context.Canceled", err)
	}
}

func TestBuildReportModelPhaseParentCancellationPropagates(t *testing.T) {
	t.Setenv("SLITHER_TEST_API_KEY", "test-key")
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(server.Close)

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nfunc evalInput(v string) { eval(v) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := BuildReport(ctx, Options{Repo: repo, Model: "test-model", BaseURL: server.URL, APIKeyEnv: "SLITHER_TEST_API_KEY", Top: 1, NoCache: true})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		cancel()
		close(release)
		t.Fatal("model request did not start")
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("BuildReport error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("BuildReport did not stop after cancellation")
	}
}

func TestNewModelScorerThreadsFallbackModels(t *testing.T) {
	t.Parallel()
	opts := Options{Model: "primary", BaseURL: "https://example.test/v1", FallbackModels: []string{"fb1", "fb2"}}
	s, err := NewModelScorer(opts)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected scorer for non-empty model")
	}
	if len(s.fallbackModels) != 2 || s.fallbackModels[0] != "fb1" || s.fallbackModels[1] != "fb2" {
		t.Fatalf("fallbackModels = %#v, want [fb1 fb2]", s.fallbackModels)
	}
}

func TestNewModelScorerCustomAPIKeyEnvHonored(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel; keep this test serial.
	t.Setenv("MY_CUSTOM_API_KEY", "sk-custom-fake")
	opts := Options{
		Model:     "primary",
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "MY_CUSTOM_API_KEY",
	}
	s, err := NewModelScorer(opts)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected scorer for non-empty model with custom API key env")
	}
}

func TestNewModelScorerOpenRouterDoesNotRequireImplicitDefaultEnv(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel; keep this test serial.
	t.Setenv("OPENROUTER_API_KEY", "sk-default-env")
	opts := Options{
		Model:   "primary",
		BaseURL: "https://openrouter.ai/api/v1",
		// APIKeyEnv empty must not implicitly select OPENROUTER_API_KEY.
	}
	s, err := NewModelScorer(opts)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected scorer for non-empty model without an explicit credential env")
	}
	if got := modelAPIKey(opts); got != "" {
		t.Fatalf("modelAPIKey = %q, want no implicit OPENROUTER_API_KEY", got)
	}
}
