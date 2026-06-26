package slither

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// promptVersion is mixed into every cache key. Bump it whenever
// batchScoringPrompt (model_prompt.go) changes so stale entries are invalidated
// without a manual cache wipe.
const promptVersion = "1"

// cachedScore is the persisted model result for one file. Only genuine model
// scores are stored — degraded (model_error) rows are never cached.
type cachedScore struct {
	Score   int      `json:"score"`
	Summary string   `json:"summary"`
	Reasons []string `json:"reasons"`
}

// scoreCache is a read-through, content-hash result cache persisted as a single
// JSON map. It is read once and written once per run, never shared across the
// scoring worker goroutines.
type scoreCache struct {
	path    string
	entries map[string]cachedScore
	dirty   map[string]cachedScore
}

func scoreCachePath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "slither", "cache", "scores.json"), nil
}

// loadScoreCache reads the cache file, returning an empty (but usable) cache on
// any error — a missing, unreadable, or corrupt file never fails the run.
func loadScoreCache() *scoreCache {
	c := &scoreCache{entries: map[string]cachedScore{}, dirty: map[string]cachedScore{}}
	path, err := scoreCachePath()
	if err != nil {
		return c
	}
	c.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var entries map[string]cachedScore
	if err := json.Unmarshal(data, &entries); err != nil {
		return c
	}
	if entries != nil {
		c.entries = entries
	}
	return c
}

func (c *scoreCache) lookup(key string) (cachedScore, bool) {
	cs, ok := c.entries[key]
	return cs, ok
}

func (c *scoreCache) put(key string, cs cachedScore) {
	c.entries[key] = cs
	c.dirty[key] = cs
}

// persist writes the merged cache when new entries were added. Best-effort: the
// caller ignores the error since the cache is an optimization, not a result.
func (c *scoreCache) persist() error {
	if c.path == "" || len(c.dirty) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, append(data, '\n'), 0o644)
}

// scoreCacheKey hashes the inputs that determine the model's score: the prompt
// version, the model ID, and the canonical projected evidence. projectEvidence
// is called with Index 0 because the batch index is positional, not semantic —
// the same file at a different rank must hash identically.
func scoreCacheKey(model string, e FileEvidence) string {
	payload, _ := json.Marshal(projectEvidence(0, e))
	h := sha256.New()
	h.Write([]byte(promptVersion))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// applyCachedScore applies a cached result exactly as ScoreBatch applies a fresh
// model result — including the "model" evidence layer and NO extra layer — so a
// warm-cache run produces byte-identical output to a cold run.
func applyCachedScore(e *FileEvidence, cs cachedScore) {
	fallbackLayers := e.EvidenceLayers
	if cs.Score >= 1 && cs.Score <= 5 {
		e.Score = cs.Score
	}
	if cs.Summary != "" {
		e.Summary = cs.Summary
	}
	if len(cs.Reasons) > 0 {
		e.Reasons = cs.Reasons
	}
	e.EvidenceLayers = mergeLayers(fallbackLayers, []string{"model"})
}

// cacheableResult reports whether a scored row is a genuine model result (and
// thus safe to cache). Degraded rows carry a model-error layer and no model
// layer, so they are excluded.
func cacheableResult(e FileEvidence) bool {
	return stringSliceContains(e.EvidenceLayers, "model") &&
		!stringSliceContains(e.EvidenceLayers, "model-error")
}

// scoreTopRowsCached scores rows through the cache. The cache is read here
// (single-threaded), the concurrent scoring pass runs only over cache misses,
// and cache writes are collected afterward (single-threaded) — the worker
// goroutines in scoreTopRows never touch the cache, so no map is shared across
// goroutines. Keys are derived from the deterministic state before scoring, so a
// future run reproduces the same key. Misses keep their original positions.
func scoreTopRowsCached(ctx context.Context, scorer *ModelScorer, rows []FileEvidence, cache *scoreCache) {
	if scorer == nil || cache == nil || len(rows) == 0 {
		return
	}
	keys := make([]string, len(rows))
	var missIdx []int
	var missRows []FileEvidence
	for i := range rows {
		keys[i] = scoreCacheKey(scorer.model, rows[i])
		if cs, ok := cache.lookup(keys[i]); ok {
			applyCachedScore(&rows[i], cs)
			continue
		}
		missIdx = append(missIdx, i)
		missRows = append(missRows, rows[i])
	}
	if len(missRows) == 0 {
		return
	}
	scoreTopRows(ctx, scorer, missRows, modelBatchSize, modelScoreConcurrency)
	for j, idx := range missIdx {
		rows[idx] = missRows[j]
		if cacheableResult(missRows[j]) {
			cache.put(keys[idx], cachedScore{Score: missRows[j].Score, Summary: missRows[j].Summary, Reasons: missRows[j].Reasons})
		}
	}
}
