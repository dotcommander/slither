package slither

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScoreCacheKeyStableAndSensitive(t *testing.T) {
	t.Parallel()
	a := baseEvidence("x.go", 2)
	if scoreCacheKey("m", a) != scoreCacheKey("m", a) {
		t.Fatal("key not stable for identical inputs")
	}
	if scoreCacheKey("m1", a) == scoreCacheKey("m2", a) {
		t.Fatal("key did not change with model")
	}
	b := baseEvidence("x.go", 2)
	b.ContentRisk = 99 // a projected scoring input
	if scoreCacheKey("m", a) == scoreCacheKey("m", b) {
		t.Fatal("key did not change with projected evidence")
	}
}

func TestScoreTopRowsCachedHitSkipsGenerate(t *testing.T) {
	t.Parallel()
	cache := &scoreCache{entries: map[string]cachedScore{}, dirty: map[string]cachedScore{}}
	row := baseEvidence("a.go", 2)
	cache.entries[scoreCacheKey("m", row)] = cachedScore{Score: 5, Summary: "cached", Reasons: []string{"rc"}}
	s := &ModelScorer{model: "m", generate: func(_ context.Context, _ string, _ int) (string, error) {
		t.Fatal("generate called for a cached row")
		return "", nil
	}}
	rows := []FileEvidence{row}
	scoreTopRowsCached(context.Background(), s, rows, cache)
	if rows[0].Score != 5 || rows[0].Summary != "cached" {
		t.Fatalf("cached result not applied: %+v", rows[0])
	}
	if !hasLayer(rows[0].EvidenceLayers, "model") || hasLayer(rows[0].EvidenceLayers, "cache") {
		t.Fatalf("layers = %v, want model and NOT cache (transparency)", rows[0].EvidenceLayers)
	}
}

func TestScoreTopRowsCachedDegradedNotCached(t *testing.T) {
	t.Parallel()
	cache := &scoreCache{entries: map[string]cachedScore{}, dirty: map[string]cachedScore{}}
	row := baseEvidence("a.go", 3)
	s := &ModelScorer{model: "m", generate: func(_ context.Context, _ string, _ int) (string, error) {
		return "", context.DeadlineExceeded
	}}
	rows := []FileEvidence{row}
	scoreTopRowsCached(context.Background(), s, rows, cache)
	if _, ok := cache.lookup(scoreCacheKey("m", row)); ok {
		t.Fatal("degraded model_error result was cached")
	}
	if len(cache.dirty) != 0 {
		t.Fatalf("degraded result marked dirty: %v", cache.dirty)
	}
}

func TestScoreTopRowsCachedMissPersists(t *testing.T) {
	setTempConfigDir(t) // NOT parallel: mutates userConfigDir seam
	cache := loadScoreCache()
	row := baseEvidence("a.go", 2)
	s := &ModelScorer{model: "m", generate: func(_ context.Context, _ string, _ int) (string, error) {
		return `[{"index":0,"score":4,"summary":"fresh","reasons":["r"]}]`, nil
	}}
	rows := []FileEvidence{row}
	scoreTopRowsCached(context.Background(), s, rows, cache)
	if rows[0].Score != 4 {
		t.Fatalf("miss not scored: %+v", rows[0])
	}
	if err := cache.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	reloaded := loadScoreCache()
	cs, ok := reloaded.lookup(scoreCacheKey("m", row))
	if !ok || cs.Score != 4 {
		t.Fatalf("persisted entry missing or wrong: %+v ok=%v", cs, ok)
	}
}

func TestLoadScoreCacheCorruptIgnored(t *testing.T) {
	dir := setTempConfigDir(t) // NOT parallel
	path := filepath.Join(dir, "slither", "cache", "scores.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := loadScoreCache()
	if len(cache.entries) != 0 {
		t.Fatalf("corrupt cache not ignored: %v", cache.entries)
	}
}

func TestLoadScoreCacheOversizedIgnored(t *testing.T) {
	dir := setTempConfigDir(t) // NOT parallel
	path := filepath.Join(dir, "slither", "cache", "scores.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, maxScoreCacheBytes+1)); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cache := loadScoreCache()
	if len(cache.entries) != 0 {
		t.Fatalf("oversized cache not ignored: %v", cache.entries)
	}
}

func TestScoreCachePrunesOverCapKeepingUsed(t *testing.T) {
	t.Parallel()
	c := &scoreCache{entries: map[string]cachedScore{}, dirty: map[string]cachedScore{}, used: map[string]bool{}}
	for i := 0; i < maxCacheEntries+10; i++ {
		c.entries[fmt.Sprintf("k%d", i)] = cachedScore{Score: 3}
	}
	usedKeys := []string{"k0", "k1", "k2"}
	for _, k := range usedKeys {
		if _, ok := c.lookup(k); !ok {
			t.Fatalf("seed key %s missing", k)
		}
	}
	if !c.prune() {
		t.Fatal("prune should report a change when over cap")
	}
	if len(c.entries) > maxCacheEntries {
		t.Fatalf("entries = %d, want <= %d", len(c.entries), maxCacheEntries)
	}
	for _, k := range usedKeys {
		if _, ok := c.entries[k]; !ok {
			t.Fatalf("used key %s was evicted by prune", k)
		}
	}
}

func TestScoreCacheUnderCapNotPruned(t *testing.T) {
	t.Parallel()
	c := &scoreCache{entries: map[string]cachedScore{}, dirty: map[string]cachedScore{}, used: map[string]bool{}}
	c.entries["a"] = cachedScore{Score: 2}
	if c.prune() {
		t.Fatal("under-cap prune should report no change")
	}
	if len(c.entries) != 1 {
		t.Fatalf("entries = %d, want 1 (untouched)", len(c.entries))
	}
}

func TestScoreCacheAllHitOverCapRewritesSmaller(t *testing.T) {
	setTempConfigDir(t) // NOT parallel: mutates userConfigDir seam
	big := map[string]cachedScore{}
	for i := 0; i < maxCacheEntries+50; i++ {
		big[fmt.Sprintf("k%d", i)] = cachedScore{Score: 3}
	}
	path, err := scoreCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(big, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	c := loadScoreCache()
	if _, ok := c.lookup("k0"); !ok { // an all-hit run: lookups only, no put
		t.Fatal("seed key k0 missing after reload")
	}
	if len(c.dirty) != 0 {
		t.Fatalf("all-hit run should have no dirty writes: %v", c.dirty)
	}
	if err := c.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("over-cap file not shrunk: before=%d after=%d", before.Size(), after.Size())
	}
	reloaded := loadScoreCache()
	if len(reloaded.entries) > maxCacheEntries {
		t.Fatalf("persisted entries = %d, want <= cap %d", len(reloaded.entries), maxCacheEntries)
	}
}

func TestScoreCachePersistSkippedSignal(t *testing.T) {
	t.Parallel()
	if got := scoreCachePersistSkippedSignal(nil); got != "" {
		t.Fatalf("nil cache signal = %q, want empty", got)
	}
	if got := scoreCachePersistSkippedSignal(&scoreCache{}); got != "" {
		t.Fatalf("pathless cache signal = %q, want empty", got)
	}

	dir := t.TempDir()
	cache := &scoreCache{
		path:    dir,
		entries: map[string]cachedScore{"k": {Score: 4}},
		dirty:   map[string]cachedScore{"k": {Score: 4}},
	}
	if got := scoreCachePersistSkippedSignal(cache); got != "score_cache:persist_failed" {
		t.Fatalf("failed persist signal = %q, want score_cache:persist_failed", got)
	}
}
