package slither

import (
	"context"
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
