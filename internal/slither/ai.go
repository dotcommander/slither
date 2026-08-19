package slither

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/garyblankenship/wormhole/v3"
	"github.com/garyblankenship/wormhole/v3/types"
)

const (
	// modelMaxOutputTokens caps the scoring response; the expected JSON is tiny.
	modelMaxOutputTokens = 256
	// modelCallTimeout bounds a single model scoring call so one slow request
	// cannot stall the whole scan.
	modelCallTimeout = 90 * time.Second
)

type ModelScorer struct {
	wh             *wormhole.Wormhole
	model          string
	baseURL        string
	fallbackModels []string
	// generate performs one model call and returns the raw response content.
	// Injectable so batch scoring can be tested without a live model.
	generate func(ctx context.Context, prompt string, maxTokens int) (string, error)
}

func NewModelScorer(opts Options) (*ModelScorer, error) {
	if opts.Model == "" {
		return nil, nil
	}
	if opts.BaseURL == "" {
		return nil, errors.New("model scoring requires --base-url")
	}
	provider := providerForBaseURL(opts.BaseURL)
	apiKey := modelAPIKey(opts)
	if provider == "" {
		provider = "openai"
	}
	// Configure even known providers explicitly. Falling back to the provider's
	// conventional environment variable here could send that credential to a
	// caller-supplied endpoint.
	wh := wormhole.New(
		wormhole.WithOpenAICompatible(provider, opts.BaseURL, types.NewProviderConfig(apiKey)),
		wormhole.WithDefaultProvider(provider),
	)
	scorer := &ModelScorer{wh: wh, model: opts.Model, baseURL: opts.BaseURL, fallbackModels: opts.FallbackModels}
	scorer.generate = func(ctx context.Context, prompt string, maxTokens int) (string, error) {
		resp, err := scorer.wh.Text().Model(scorer.model).WithFallback(scorer.fallbackModels...).Prompt(prompt).Temperature(0).MaxTokens(maxTokens).Generate(ctx)
		if err != nil {
			return "", err
		}
		return resp.Content(), nil
	}
	return scorer, nil
}

func (s *ModelScorer) Close() error {
	if s == nil || s.wh == nil {
		return nil
	}
	return s.wh.Close()
}

func modelAPIKey(opts Options) string {
	if opts.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(opts.APIKeyEnv)
}

func providerForBaseURL(baseURL string) string {
	if strings.Contains(baseURL, "openrouter.ai") {
		return "openrouter"
	}
	return ""
}

// ScoreBatch scores up to len(batch) files in a single model call and returns a
// finalized slice in the same order as the input. Each file the model returned
// a valid result for has its model fields applied and the "model" evidence
// layer merged; files whose index is missing — or every file when the call or
// parse fails — degrade to their deterministic score plus a model_error signal.
// Provider failures and the scorer's internal timeout degrade deterministically;
// cancellation of the caller's context is returned so the scan can stop.
func (s *ModelScorer) ScoreBatch(ctx context.Context, batch []FileEvidence) ([]FileEvidence, error) {
	out := make([]FileEvidence, len(batch))
	copy(out, batch)
	if len(out) == 0 {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	prompt := batchScoringPrompt(batch)
	maxTokens := modelMaxOutputTokens * len(batch)
	callCtx, cancel := context.WithTimeout(ctx, modelCallTimeout)
	defer cancel()
	content, err := s.generate(callCtx, prompt, maxTokens)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, ctxErr
		}
		degradeBatch(out, fmt.Errorf("wormhole score batch: %w", err))
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	scores, err := parseModelScores(content)
	if err != nil {
		degradeBatch(out, fmt.Errorf("parse model score batch: %w", err))
		return out, nil
	}
	byIndex := make(map[int]batchModelScore, len(scores))
	for _, sc := range scores {
		byIndex[sc.Index] = sc
	}
	for i := range out {
		sc, ok := byIndex[i]
		if !ok {
			degradeEvidence(&out[i], "model_error:no model score for "+out[i].Path)
			continue
		}
		if !applyModelScore(&out[i], sc.Score, sc.Summary, sc.Reasons) {
			degradeEvidence(&out[i], "model_error:invalid model score for "+out[i].Path)
		}
	}
	return out, nil
}

// appendModelReasons preserves detector-owned reason keys and namespaces model
// prose so downstream consumers can distinguish evidence from interpretation.
func appendModelReasons(reasons, modelReasons []string) []string {
	for _, reason := range modelReasons {
		reason = scrubOutputSecrets(strings.TrimSpace(reason))
		if reason == "" {
			continue
		}
		if !strings.HasPrefix(reason, "model:") {
			reason = "model:" + reason
		}
		if !stringSliceContains(reasons, reason) {
			reasons = append(reasons, reason)
		}
	}
	return reasons
}

// degradeBatch applies the deterministic-fallback model_error signal to every
// file, mirroring the single-file error path: keep the deterministic score,
// append a model_error reason, then recompute evidence layers from reasons.
func degradeBatch(out []FileEvidence, err error) {
	for i := range out {
		degradeEvidence(&out[i], "model_error:"+scrubOutputSecrets(err.Error()))
	}
}

// applyModelScore is the one selection path shared by fresh and cached model
// results. Invalid scores never change compatibility score or provenance.
func applyModelScore(row *FileEvidence, score int, summary string, reasons []string) bool {
	if score < 1 || score > 5 {
		return false
	}
	if row.ScoreProvenance.SelectedBy == "" {
		setDeterministicProvenance(row)
	}
	row.Score = score
	modelScore := score
	row.ScoreProvenance.Model = &modelScore
	row.ScoreProvenance.SelectedBy = "model"
	if summary != "" {
		row.Summary = scrubOutputSecrets(summary)
	}
	row.Reasons = appendModelReasons(row.Reasons, reasons)
	row.EvidenceLayers = mergeLayers(row.EvidenceLayers, []string{"model"})
	return true
}

func degradeEvidence(row *FileEvidence, reason string) {
	if row.ScoreProvenance.SelectedBy == "" {
		setDeterministicProvenance(row)
	}
	row.Score = row.ScoreProvenance.Deterministic
	row.ScoreProvenance.Model = nil
	row.ScoreProvenance.SelectedBy = "deterministic"
	row.Reasons = append(row.Reasons, reason)
	row.EvidenceLayers = evidenceLayersForReasons(row.Reasons)
}
