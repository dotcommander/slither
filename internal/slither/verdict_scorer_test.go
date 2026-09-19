package slither

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotcommander/verdict"
)

// fakeVerdictProvider is an in-package verdict.Provider fake: it records the
// Score requests it receives and plays back configured per-call results and
// errors in call order. blockUntilDone makes Score wait for ctx cancellation
// and return it, for the parent-cancellation test.
type fakeVerdictProvider struct {
	mu             sync.Mutex
	scores         []verdict.ScoreResult
	errs           []error
	requests       []verdict.ScoreRequest
	blockUntilDone bool
}

func (f *fakeVerdictProvider) Score(ctx context.Context, req verdict.ScoreRequest) (verdict.ScoreResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	call := len(f.requests) - 1
	f.mu.Unlock()
	if f.blockUntilDone {
		<-ctx.Done()
		return verdict.ScoreResult{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if call < len(f.errs) && f.errs[call] != nil {
		return verdict.ScoreResult{}, f.errs[call]
	}
	if call < len(f.scores) {
		return f.scores[call], nil
	}
	return verdict.ScoreResult{}, errors.New("fakeVerdictProvider: no configured result for call")
}

func (f *fakeVerdictProvider) Choose(context.Context, verdict.ChoiceRequest) (verdict.ChoiceResult, error) {
	return verdict.ChoiceResult{}, errors.New("fakeVerdictProvider: Choose not implemented")
}

func (f *fakeVerdictProvider) Ask(context.Context, verdict.NoulRequest) (verdict.NoulResult, error) {
	return verdict.NoulResult{}, errors.New("fakeVerdictProvider: Ask not implemented")
}

func (f *fakeVerdictProvider) Capabilities() verdict.Caps { return verdict.Caps{} }

// verdictScorerWithFake builds a VerdictScorer over the fake, never the Jev
// HTTP adapter, so these tests stay offline.
func verdictScorerWithFake(t *testing.T, fake *fakeVerdictProvider) *VerdictScorer {
	t.Helper()
	client, err := verdict.New(verdict.Config{Provider: fake})
	if err != nil {
		t.Fatal(err)
	}
	return &VerdictScorer{client: client, model: "jev-model", baseURL: "https://jev.test/endpoint"}
}

func verdictScoreResult(level int) verdict.ScoreResult {
	return verdict.ScoreResult{Scores: []verdict.Scored{{DimensionID: verdictDimensionID, Level: level}}}
}

func TestNewVerdictScorerEmptyModelNotConfigured(t *testing.T) {
	t.Parallel()
	scorer, err := NewVerdictScorer(Options{BaseURL: "https://jev.test/endpoint"})
	if scorer != nil || err != nil {
		t.Fatalf("NewVerdictScorer = (%v, %v), want (nil, nil) for empty model", scorer, err)
	}
}

func TestNewVerdictScorerRequiresBaseURL(t *testing.T) {
	t.Parallel()
	scorer, err := NewVerdictScorer(Options{Model: "jev-model"})
	if scorer != nil || err == nil || !strings.Contains(err.Error(), "--base-url") {
		t.Fatalf("NewVerdictScorer = (%v, %v), want base-url error", scorer, err)
	}
}

func TestVerdictScoreBatchAppliesLevelAsModelScore(t *testing.T) {
	t.Parallel()
	fake := &fakeVerdictProvider{scores: []verdict.ScoreResult{verdictScoreResult(3)}}
	s := verdictScorerWithFake(t, fake)
	out, err := s.ScoreBatch(context.Background(), []FileEvidence{baseEvidence("a.go", 2)})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Score != 4 {
		t.Fatalf("score = %d, want 4 (verdict level 3 plus one)", out[0].Score)
	}
	if out[0].ScoreProvenance.SelectedBy != "model" || out[0].ScoreProvenance.Model == nil || *out[0].ScoreProvenance.Model != 4 {
		t.Fatalf("provenance = %+v, want model-selected 4", out[0].ScoreProvenance)
	}
	if !hasLayer(out[0].EvidenceLayers, "model") {
		t.Fatalf("layers = %v, want model layer merged", out[0].EvidenceLayers)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(fake.requests))
	}
	req := fake.requests[0]
	if req.Task != verdictScoringTask {
		t.Fatalf("task = %q, want %q", req.Task, verdictScoringTask)
	}
	if req.Policy.ID != verdictPolicyID || req.Policy.Version != verdictPolicyVersion || req.Policy.Digest == "" {
		t.Fatalf("policy = %+v, want slither policy with computed digest", req.Policy)
	}
	if len(req.Dimensions) != 1 || req.Dimensions[0].ID != verdictDimensionID || len(req.Dimensions[0].Levels) != 5 {
		t.Fatalf("dimensions = %+v, want one %s dimension with 5 levels", req.Dimensions, verdictDimensionID)
	}
	if req.Dimensions[0].Instruction != verdictDimensionInstruction {
		t.Fatalf("instruction = %q, want rubric guidance", req.Dimensions[0].Instruction)
	}
	if req.Input == "" || !json.Valid([]byte(req.Input)) {
		t.Fatalf("input = %q, want non-empty projected-evidence JSON", req.Input)
	}
}

func TestVerdictScoreBatchProviderErrorDegradesAndContinues(t *testing.T) {
	t.Parallel()
	fake := &fakeVerdictProvider{
		errs:   []error{errors.New("endpoint exploded")},
		scores: []verdict.ScoreResult{{}, verdictScoreResult(4)},
	}
	s := verdictScorerWithFake(t, fake)
	out, err := s.ScoreBatch(context.Background(), []FileEvidence{baseEvidence("a.go", 2), baseEvidence("b.go", 3)})
	if err != nil {
		t.Fatalf("provider failure should degrade, got error: %v", err)
	}
	if out[0].Score != 2 || !hasModelError(out[0].Reasons) {
		t.Fatalf("row0 = score %d reasons %#v, want deterministic 2 with model_error", out[0].Score, out[0].Reasons)
	}
	if out[0].ScoreProvenance.SelectedBy != "deterministic" {
		t.Fatalf("row0 selected by %q, want deterministic", out[0].ScoreProvenance.SelectedBy)
	}
	if out[1].Score != 5 || out[1].ScoreProvenance.SelectedBy != "model" {
		t.Fatalf("row1 = score %d selected by %q, want model 5", out[1].Score, out[1].ScoreProvenance.SelectedBy)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("provider calls = %d, want 2 (later files still scored)", len(fake.requests))
	}
}

func TestVerdictScoreBatchParentCancellationPropagates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &fakeVerdictProvider{blockUntilDone: true}
	s := verdictScorerWithFake(t, fake)
	outCh := make(chan []FileEvidence, 1)
	errCh := make(chan error, 1)
	// Exit condition: ScoreBatch returns once cancel() fires and the blocked
	// fake unblocks; both channels are buffered so the goroutine cannot leak.
	go func() {
		out, err := s.ScoreBatch(ctx, []FileEvidence{baseEvidence("a.go", 2)})
		outCh <- out
		errCh <- err
	}()
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ScoreBatch error = %v, want context.Canceled", err)
		}
		out := <-outCh
		if hasModelError(out[0].Reasons) {
			t.Fatalf("parent cancellation degraded into model_error: %#v", out[0].Reasons)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ScoreBatch did not return after cancellation")
	}
}

func TestVerdictScoreBatchEmptyBatchSkipsProvider(t *testing.T) {
	t.Parallel()
	fake := &fakeVerdictProvider{}
	s := verdictScorerWithFake(t, fake)
	out, err := s.ScoreBatch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("out = %#v, want empty", out)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("provider calls = %d, want 0 for an empty batch", len(fake.requests))
	}
}

func TestVerdictCacheKeyNamespaceDiffersFromModelScorer(t *testing.T) {
	t.Parallel()
	model := &ModelScorer{model: "m", baseURL: "https://endpoint.test"}
	jevScorer := &VerdictScorer{model: "m", baseURL: "https://endpoint.test"}
	mModel, mBase, mFallbacks, mContract := model.cacheKeyInputs()
	vModel, vBase, vFallbacks, vContract := jevScorer.cacheKeyInputs()
	if vContract == mContract {
		t.Fatalf("cache contracts collide: %q", vContract)
	}
	if vFallbacks != nil {
		t.Fatalf("jev fallbacks = %#v, want nil", vFallbacks)
	}
	if mModel != vModel || mBase != vBase {
		t.Fatalf("model/baseURL = %q/%q vs %q/%q, want threaded through unchanged", mModel, mBase, vModel, vBase)
	}
	row := baseEvidence("a.go", 2)
	mKey := scoreCacheKeyWithPromptContract(mModel, mBase, mFallbacks, mContract, row)
	vKey := scoreCacheKeyWithPromptContract(vModel, vBase, vFallbacks, vContract, row)
	if mKey == vKey {
		t.Fatalf("cache keys collide across backends: %s", mKey)
	}
}
