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
	return &ModelScorer{wh: wh, model: opts.Model, baseURL: opts.BaseURL}, nil
}

func providerForBaseURL(baseURL string) string {
	if strings.Contains(baseURL, "openrouter.ai") {
		return "openrouter"
	}
	return ""
}

func (s *ModelScorer) Score(ctx context.Context, evidence FileEvidence) (FileEvidence, error) {
	prompt := scoringPrompt(evidence)
	callCtx, cancel := context.WithTimeout(ctx, modelCallTimeout)
	defer cancel()
	resp, err := s.wh.Text().Model(s.model).Prompt(prompt).Temperature(0).MaxTokens(modelMaxOutputTokens).Generate(callCtx)
	if err != nil {
		return evidence, fmt.Errorf("wormhole score %s: %w", evidence.Path, err)
	}
	parsed, err := parseModelScore(resp.Content())
	if err != nil {
		return evidence, fmt.Errorf("parse model score %s: %w", evidence.Path, err)
	}
	if parsed.Score >= 1 && parsed.Score <= 5 {
		evidence.Score = parsed.Score
	}
	if parsed.Summary != "" {
		evidence.Summary = parsed.Summary
	}
	if len(parsed.Reasons) > 0 {
		evidence.Reasons = parsed.Reasons
	}
	return evidence, nil
}
