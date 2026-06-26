package slither

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/garyblankenship/wormhole/pkg/types"
	"github.com/garyblankenship/wormhole/pkg/wormhole"
)

const (
	// modelMaxOutputTokens caps the scoring response; the expected JSON is tiny.
	modelMaxOutputTokens = 256
	// modelCallTimeout bounds a single model scoring call so one slow request
	// cannot stall the whole scan.
	modelCallTimeout = 90 * time.Second
)

type ModelScorer struct {
	wh      *wormhole.Wormhole
	model   string
	baseURL string
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
	var wh *wormhole.Wormhole
	if provider != "" {
		wh = wormhole.New(wormhole.WithProviderFromEnv(provider), wormhole.WithDefaultProvider(provider))
	} else {
		apiKey := ""
		if opts.APIKeyEnv != "" {
			apiKey = os.Getenv(opts.APIKeyEnv)
		}
		wh = wormhole.New(
			wormhole.WithOpenAICompatible("openai", opts.BaseURL, types.NewProviderConfig(apiKey)),
			wormhole.WithDefaultProvider("openai"),
		)
	}
	scorer := &ModelScorer{wh: wh, model: opts.Model, baseURL: opts.BaseURL}
	scorer.generate = func(ctx context.Context, prompt string, maxTokens int) (string, error) {
		resp, err := scorer.wh.Text().Model(scorer.model).Prompt(prompt).Temperature(0).MaxTokens(maxTokens).Generate(ctx)
		if err != nil {
			return "", err
		}
		return resp.Content(), nil
	}
	return scorer, nil
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
// Degradation never aborts the scan, so the returned error is reserved (nil).
func (s *ModelScorer) ScoreBatch(ctx context.Context, batch []FileEvidence) ([]FileEvidence, error) {
	out := make([]FileEvidence, len(batch))
	copy(out, batch)
	if len(out) == 0 {
		return out, nil
	}
	prompt := batchScoringPrompt(batch)
	maxTokens := modelMaxOutputTokens * len(batch)
	callCtx, cancel := context.WithTimeout(ctx, modelCallTimeout)
	defer cancel()
	content, err := s.generate(callCtx, prompt, maxTokens)
	if err != nil {
		degradeBatch(out, fmt.Errorf("wormhole score batch: %w", err))
		return out, nil
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
		fallbackLayers := out[i].EvidenceLayers
		sc, ok := byIndex[i]
		if !ok {
			out[i].Reasons = append(out[i].Reasons, "model_error:no model score for "+out[i].Path)
			out[i].EvidenceLayers = evidenceLayersForReasons(out[i].Reasons)
			continue
		}
		if sc.Score >= 1 && sc.Score <= 5 {
			out[i].Score = sc.Score
		}
		if sc.Summary != "" {
			out[i].Summary = sc.Summary
		}
		if len(sc.Reasons) > 0 {
			out[i].Reasons = sc.Reasons
		}
		out[i].EvidenceLayers = mergeLayers(fallbackLayers, []string{"model"})
	}
	return out, nil
}

// degradeBatch applies the deterministic-fallback model_error signal to every
// file, mirroring the single-file error path: keep the deterministic score,
// append a model_error reason, then recompute evidence layers from reasons.
func degradeBatch(out []FileEvidence, err error) {
	for i := range out {
		out[i].Reasons = append(out[i].Reasons, "model_error:"+err.Error())
		out[i].EvidenceLayers = evidenceLayersForReasons(out[i].Reasons)
	}
}

// Score scores a single file by delegating to ScoreBatch. Retained for
// compatibility; ScoreBatch is the primary path. The returned evidence already
// carries any model_error signal, so the error is nil on the normal path.
func (s *ModelScorer) Score(ctx context.Context, evidence FileEvidence) (FileEvidence, error) {
	out, err := s.ScoreBatch(ctx, []FileEvidence{evidence})
	if err != nil {
		return evidence, err
	}
	return out[0], nil
}
