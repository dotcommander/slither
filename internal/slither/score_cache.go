package slither

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxCacheEntries caps scores.json so a long-lived cache cannot grow without
// bound. On persist, entries beyond the cap are pruned, always retaining the
// keys used (hit or written) this run; cold entries for files no longer scanned
// are dropped first.
const maxCacheEntries = 5000

// maxScoreCacheBytes bounds cache loading before JSON decode. The persisted
// cache is an optimization; an oversized preexisting cache should be ignored
// instead of letting startup memory scale with a stale artifact.
const maxScoreCacheBytes = 8 << 20

// scoreCacheContractVersion invalidates results whose persisted shape predates
// the separation of deterministic and model-owned reasons.
const scoreCacheContractVersion = "content-id-model-reasons-v3"

// cachedScore is the persisted model result for one file. Only genuine model
// scores are stored — degraded (model_error) rows are never cached.
type cachedScore struct {
	Score        int      `json:"score"`
	Summary      string   `json:"summary"`
	ModelReasons []string `json:"model_reasons,omitempty"`
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
	data, err := readScoreCacheFile(path)
	if err != nil {
		return c
	}
	var entries map[string]cachedScore
	if err := json.Unmarshal(data, &entries); err != nil {
		return c
	}
	if entries != nil {
		for key, entry := range entries {
			clean, changed := scrubCachedScore(entry)
			if changed {
				entries[key] = clean
				c.dirty[key] = clean
			}
		}
		c.entries = entries
	}
	return c
}

func scrubCachedScore(entry cachedScore) (cachedScore, bool) {
	clean := entry
	clean.Summary = scrubOutputSecrets(entry.Summary)
	changed := clean.Summary != entry.Summary
	if len(entry.ModelReasons) > 0 {
		clean.ModelReasons = append([]string(nil), entry.ModelReasons...)
		for i, reason := range clean.ModelReasons {
			clean.ModelReasons[i] = scrubOutputSecrets(reason)
			changed = changed || clean.ModelReasons[i] != reason
		}
	}
	return clean, changed
}

func readScoreCacheFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxScoreCacheBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxScoreCacheBytes {
		return nil, fmt.Errorf("score cache exceeds %d bytes", maxScoreCacheBytes)
	}
	return data, nil
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

// prune drops entries when the cache exceeds maxCacheEntries. It retains newly
// written entries first, then cache hits, then cold entries, sorting each group
// so an over-cap run produces deterministic bytes. When the current run itself
// touches more than the cap, the cache remains bounded because it is only an
// optimization; evicted rows are safely rescored on a later run. Returns true
// when it removed entries so persist rewrites even without dirty writes.
func (c *scoreCache) prune() bool {
	if len(c.entries) <= maxCacheEntries {
		return false
	}
	kept := make(map[string]cachedScore, maxCacheEntries)
	keepKeys := func(keys []string) {
		sort.Strings(keys)
		for _, key := range keys {
			if len(kept) >= maxCacheEntries {
				return
			}
			if _, exists := kept[key]; exists {
				continue
			}
			if cs, ok := c.entries[key]; ok {
				kept[key] = cs
			}
		}
	}
	mapKeys := func(values map[string]cachedScore) []string {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		return keys
	}
	usedKeys := make([]string, 0, len(c.used))
	for key := range c.used {
		usedKeys = append(usedKeys, key)
	}
	keepKeys(mapKeys(c.dirty))
	keepKeys(usedKeys)
	keepKeys(mapKeys(c.entries))
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
	return atomicWriteFile(c.path, append(data, '\n'), 0o600)
}

func scoreCachePersistSkippedSignal(cache *scoreCache) string {
	if cache == nil {
		return ""
	}
	if err := cache.persist(); err != nil {
		return "score_cache:persist_failed"
	}
	return ""
}

// scoreCacheKey hashes the inputs that determine the model's score: the prompt
// contract, the model ID, the base URL, the fallback models, and the canonical
// projected evidence. projectEvidence is called with Index 0 because the batch
// index is positional, not semantic — the same file at a different rank must
// hash identically.
func scoreCacheKey(model, baseURL string, fallbackModels []string, e FileEvidence) string {
	return scoreCacheKeyWithPromptContract(model, baseURL, fallbackModels, batchScoringPromptTemplate, e)
}

func scoreCacheKeyWithPromptContract(model, baseURL string, fallbackModels []string, promptContract string, e FileEvidence) string {
	payload, _ := json.Marshal(projectEvidence(0, e))
	h := sha256.New()
	h.Write([]byte(scoreCacheContractVersion))
	h.Write([]byte{0})
	h.Write([]byte(promptContract))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(baseURL))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(fallbackModels, "\x00")))
	h.Write([]byte{0})
	h.Write([]byte(e.ContentID))
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// applyCachedScore applies a cached result exactly as ScoreBatch applies a fresh
// model result — including the "model" evidence layer and NO extra layer — so a
// warm-cache run produces byte-identical output to a cold run.
func applyCachedScore(e *FileEvidence, cs cachedScore) {
	if !applyModelScore(e, cs.Score, cs.Summary, cs.ModelReasons) {
		degradeEvidence(e, "model_error:invalid cached model score for "+e.Path)
	}
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
func scoreTopRowsCached(ctx context.Context, scorer evidenceScorer, rows []FileEvidence, cache *scoreCache) (hits, misses int, err error) {
	if scorer == nil || cache == nil || len(rows) == 0 {
		return 0, 0, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	model, baseURL, fallbackModels, contract := scorer.cacheKeyInputs()
	keys := make([]string, len(rows))
	var missIdx []int
	var missRows []FileEvidence
	for i := range rows {
		keys[i] = scoreCacheKeyWithPromptContract(model, baseURL, fallbackModels, contract, rows[i])
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
		return hits, misses, ctx.Err()
	}
	if err := scoreTopRows(ctx, scorer, missRows, modelBatchSize, modelScoreConcurrency); err != nil {
		return hits, misses, err
	}
	for j, idx := range missIdx {
		baselineReasonCount := len(rows[idx].Reasons)
		rows[idx] = missRows[j]
		if cacheableResult(missRows[j]) {
			cache.put(keys[idx], cachedScore{Score: missRows[j].Score, Summary: missRows[j].Summary, ModelReasons: modelReasons(missRows[j].Reasons[baselineReasonCount:])})
		}
	}
	return hits, misses, nil
}

func modelReasons(reasons []string) []string {
	var out []string
	for _, reason := range reasons {
		if strings.HasPrefix(reason, "model:") {
			out = append(out, strings.TrimPrefix(reason, "model:"))
		}
	}
	return out
}
