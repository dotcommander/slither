package slither

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dotcommander/verdict"
	"github.com/dotcommander/verdict/jev"
)

const (
	// verdictPolicyID names the slither evidence-worthiness policy inside the
	// verdict policy namespace.
	verdictPolicyID = "slither.evidence-worthiness"
	// verdictPolicyVersion is the policy revision; a changed rubric is a new
	// version and therefore a new cache namespace.
	verdictPolicyVersion = "1"
	// verdictDimensionID is the single scored axis of the policy.
	verdictDimensionID = "evidence_worthiness"
	// verdictScoringTask is the task statement sent with every Score call.
	verdictScoringTask = "Score one file as a premium-model target."
	// verdictDimensionInstruction is the scoring guidance. It is verbatim from
	// batchScoringPromptTemplate so both model paths judge on the same rubric.
	verdictDimensionInstruction = "Prefer files with impact times opportunity: central code, security/config/persistence boundaries, churn-like clues, complexity, TODO/FIXME, risky APIs, weak tests, or architectural leverage."
)

// verdictLevelDescriptions is the five-level rubric, verbatim from the scale
// line in batchScoringPromptTemplate. Index 0 is the weakest rung; the 0-based
// level maps to slither's 1..5 score by adding one.
var verdictLevelDescriptions = []string{
	"low signal",
	"minor",
	"plausible",
	"strong",
	"urgent/high leverage",
}

// verdictPolicyDigest is the sha256 hex digest over the task, the instruction,
// and each level description joined with NUL separators. It is computed once at
// package init so the policy text can never drift from its declared digest.
var verdictPolicyDigest = computeVerdictPolicyDigest()

func computeVerdictPolicyDigest() string {
	parts := make([]string, 0, 2+len(verdictLevelDescriptions))
	parts = append(parts, verdictScoringTask, verdictDimensionInstruction)
	parts = append(parts, verdictLevelDescriptions...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// evidenceScorer is the seam between the report scan and the two scoring
// backends: the wormhole batch prompt (ModelScorer) and the verdict/Jev typed
// judgment (VerdictScorer). cacheKeyInputs exposes the inputs that determine a
// score so the score cache can key both backends without knowing their shape.
type evidenceScorer interface {
	ScoreBatch(context.Context, []FileEvidence) ([]FileEvidence, error)
	Close() error
	cacheKeyInputs() (model, baseURL string, fallbackModels []string, contract string)
}

// newEvidenceScorer selects the scoring backend for opts. A nil interface with
// a nil error means scoring is not configured (empty model), mirroring
// NewModelScorer's contract; construction errors propagate.
func newEvidenceScorer(opts Options) (evidenceScorer, error) {
	if opts.Jev {
		scorer, err := NewVerdictScorer(opts)
		if scorer == nil {
			// Return a literal nil interface (never a typed nil) so callers'
			// nil checks keep working for the not-configured case.
			return nil, err
		}
		return scorer, err
	}
	scorer, err := NewModelScorer(opts)
	if scorer == nil {
		return nil, err
	}
	return scorer, err
}

// VerdictScorer scores the top evidence band one file at a time through the
// verdict typed-judgment client over its Jev (TypeSafe SystemOne) adapter.
type VerdictScorer struct {
	client  *verdict.Client
	model   string
	baseURL string
}

// NewVerdictScorer mirrors NewModelScorer: an empty model means scoring is not
// configured (nil, nil), and a missing base URL is a configuration error. The
// API key is read from the environment variable NAMED by opts.APIKeyEnv — the
// same explicit-credential rule the wormhole path follows. The verdict client
// gets no exact cache (one-shot runs; slither's score cache stays the
// authority) and the Jev adapter's default 30s-timeout HTTP client.
func NewVerdictScorer(opts Options) (*VerdictScorer, error) {
	if opts.Model == "" {
		return nil, nil
	}
	if opts.BaseURL == "" {
		return nil, errors.New("jev scoring requires --base-url")
	}
	adapter, err := jev.NewJev(jev.Config{BaseURL: opts.BaseURL, Model: opts.Model, APIKey: modelAPIKey(opts)})
	if err != nil {
		return nil, fmt.Errorf("jev scoring: %w", err)
	}
	client, err := verdict.New(verdict.Config{Provider: adapter})
	if err != nil {
		return nil, fmt.Errorf("jev scoring: %w", err)
	}
	return &VerdictScorer{client: client, model: opts.Model, baseURL: opts.BaseURL}, nil
}

// Close satisfies evidenceScorer; the Jev adapter owns no closable state.
func (s *VerdictScorer) Close() error { return nil }

// ScoreBatch scores each file through one verdict Score call, in order.
// Provider failures degrade the affected row to its deterministic score with a
// model_error signal and the remaining files keep scoring; cancellation of the
// caller's context is returned so the scan can stop. Files are scored
// sequentially: the outer scoreTopRows pool already runs 4 concurrent batch
// lanes, and inner goroutines would multiply past that bound.
func (s *VerdictScorer) ScoreBatch(ctx context.Context, batch []FileEvidence) ([]FileEvidence, error) {
	out := make([]FileEvidence, len(batch))
	copy(out, batch)
	if len(out) == 0 {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	for i := range out {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := s.scoreOne(ctx, &out[i]); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return out, ctxErr
			}
			degradeEvidence(&out[i], "model_error:"+scrubOutputSecrets(err.Error()))
			continue
		}
	}
	return out, nil
}

// scoreOne runs one verdict Score for a single file and applies the result
// through the shared applyModelScore selection path.
func (s *VerdictScorer) scoreOne(ctx context.Context, row *FileEvidence) error {
	payload, err := json.Marshal(projectEvidence(0, *row))
	if err != nil {
		return fmt.Errorf("jev score: %w", err)
	}
	req := verdict.ScoreRequest{
		Task:  verdictScoringTask,
		Input: string(payload),
		Dimensions: []verdict.Dimension{{
			ID:          verdictDimensionID,
			Instruction: verdictDimensionInstruction,
			Levels:      verdictLevels(),
		}},
		Policy: verdict.PolicyRef{ID: verdictPolicyID, Version: verdictPolicyVersion, Digest: verdictPolicyDigest},
		Scope:  verdict.Scope{}, // zero value is the public namespace; slither scans are not tenant-scoped
	}
	result, err := s.client.Score(ctx, req)
	if err != nil {
		return fmt.Errorf("jev score: %w", err)
	}
	for _, scored := range result.Scores {
		if scored.DimensionID != verdictDimensionID {
			continue
		}
		// verdict levels are 0-based; slither's scale is 1..5. No generation
		// comes back from Score, so summary and reasons stay empty.
		score := scored.Level + 1
		if !applyModelScore(row, score, "", nil) {
			return fmt.Errorf("jev score: invalid model score %d for path %s", score, row.Path)
		}
		return nil
	}
	return fmt.Errorf("jev score: no %s dimension score for path %s", verdictDimensionID, row.Path)
}

func verdictLevels() []verdict.Level {
	levels := make([]verdict.Level, len(verdictLevelDescriptions))
	for i, description := range verdictLevelDescriptions {
		levels[i] = verdict.Level{Description: description}
	}
	return levels
}

// cacheKeyInputs namespaces jev scores under the policy digest instead of the
// wormhole batch prompt, so the two backends never share cache entries.
func (s *VerdictScorer) cacheKeyInputs() (string, string, []string, string) {
	return s.model, s.baseURL, nil, "jev:" + verdictPolicyDigest
}
