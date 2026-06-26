package slither

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
}

func TestScoreBatchMissingIndexFallsBack(t *testing.T) {
	t.Parallel()
	rows := []FileEvidence{baseEvidence("a.go", 2), baseEvidence("b.go", 2)}
	s := &ModelScorer{generate: func(_ context.Context, _ string, _ int) (string, error) {
		return `[{"index":0,"score":5,"summary":"a","reasons":["ra"]}]`, nil
	}}
	out, _ := s.ScoreBatch(context.Background(), rows)
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
	out, _ := s.ScoreBatch(context.Background(), rows)
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
	if strings.Contains(s, "EXCERPT_") || strings.Contains(s, "excerpt") {
		t.Fatalf("excerpt leaked into projection: %s", s)
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
	scoreTopRows(context.Background(), s, rows, modelBatchSize, modelScoreConcurrency)
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
