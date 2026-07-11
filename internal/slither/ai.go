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
		out[i].Reasons = appendModelReasons(out[i].Reasons, sc.Reasons)
		out[i].EvidenceLayers = mergeLayers(fallbackLayers, []string{"model"})
	}
	return out, nil
}

// appendModelReasons preserves detector-owned reason keys and namespaces model
// prose so downstream consumers can distinguish evidence from interpretation.
func appendModelReasons(reasons, modelReasons []string) []string {
	for _, reason := range modelReasons {
		reason = strings.TrimSpace(reason)
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
		out[i].Reasons = append(out[i].Reasons, "model_error:"+err.Error())
		out[i].EvidenceLayers = evidenceLayersForReasons(out[i].Reasons)
	}
}
