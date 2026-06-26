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

// maxCacheEntries caps scores.json so a long-lived cache cannot grow without
// bound. On persist, entries beyond the cap are pruned, always retaining the
// keys used (hit or written) this run; cold entries for files no longer scanned
// are dropped first.
const maxCacheEntries = 5000

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
	used    map[string]bool
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
	c := &scoreCache{entries: map[string]cachedScore{}, dirty: map[string]cachedScore{}, used: map[string]bool{}}
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
	if ok {
		c.markUsed(key)
	}
	return cs, ok
}

func (c *scoreCache) put(key string, cs cachedScore) {
	c.entries[key] = cs
	c.dirty[key] = cs
	c.markUsed(key)
}

// markUsed records a key touched this run. Lazy-inits the map so cache literals
// constructed without a used map (e.g. in tests) never panic on a nil write.
func (c *scoreCache) markUsed(key string) {
	if c.used == nil {
		c.used = map[string]bool{}
	}
	c.used[key] = true
}

// prune drops cold entries when the cache exceeds maxCacheEntries, always
// keeping every key used this run, then filling remaining capacity with
// arbitrary other entries up to the cap. Returns true when it removed entries so
// persist knows to rewrite even when no dirty writes occurred.
func (c *scoreCache) prune() bool {
	if len(c.entries) <= maxCacheEntries {
		return false
	}
	kept := make(map[string]cachedScore, maxCacheEntries)
	for key := range c.used {
		if cs, ok := c.entries[key]; ok {
			kept[key] = cs
		}
	}
	for key, cs := range c.entries {
		if len(kept) >= maxCacheEntries {
			break
		}
		if _, ok := kept[key]; ok {
			continue
		}
		kept[key] = cs
	}
	if len(kept) == len(c.entries) {
		return false
	}
	c.entries = kept
	return true
}

// persist writes the merged cache when new entries were added. Best-effort: the
// caller ignores the error since the cache is an optimization, not a result.
func (c *scoreCache) persist() error {
	if c.path == "" {
		return nil
	}
	pruned := c.prune()
	if len(c.dirty) == 0 && !pruned {
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
func scoreTopRowsCached(ctx context.Context, scorer *ModelScorer, rows []FileEvidence, cache *scoreCache) (hits, misses int) {
	if scorer == nil || cache == nil || len(rows) == 0 {
		return 0, 0
	}
	keys := make([]string, len(rows))
	var missIdx []int
	var missRows []FileEvidence
	for i := range rows {
		keys[i] = scoreCacheKey(scorer.model, rows[i])
		if cs, ok := cache.lookup(keys[i]); ok {
			applyCachedScore(&rows[i], cs)
			hits++
			continue
		}
		missIdx = append(missIdx, i)
		missRows = append(missRows, rows[i])
	}
	misses = len(missRows)
	if len(missRows) == 0 {
		return hits, misses
	}
	scoreTopRows(ctx, scorer, missRows, modelBatchSize, modelScoreConcurrency)
	for j, idx := range missIdx {
		rows[idx] = missRows[j]
		if cacheableResult(missRows[j]) {
			cache.put(keys[idx], cachedScore{Score: missRows[j].Score, Summary: missRows[j].Summary, Reasons: missRows[j].Reasons})
		}
	}
	return hits, misses
}
