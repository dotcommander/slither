package slither

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setTempConfigDir redirects the slither config dir to a temp dir for the test.
// NOT parallel-safe: it mutates the userConfigDir package seam, so any test
// that calls it must NOT use t.Parallel.
func setTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = prev })
	return dir
}

func TestLoadOrCreateConfigFirstRunWritesDefaults(t *testing.T) {
	dir := setTempConfigDir(t)
	cfg, err := LoadOrCreateConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "" {
		t.Fatalf("first-run model = %q, want empty for offline-first default", cfg.Model)
	}
	want := defaultConfig()
	if cfg.BaseURL != want.BaseURL || cfg.APIKeyEnv != want.APIKeyEnv {
		t.Fatalf("base defaults = %q/%q, want %q/%q", cfg.BaseURL, cfg.APIKeyEnv, want.BaseURL, want.APIKeyEnv)
	}
	if cfg.Local != want.Local {
		t.Fatalf("local profile = %#v, want %#v", cfg.Local, want.Local)
	}
	path := filepath.Join(dir, "slither", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written on first run: %v", err)
	}
	if !strings.Contains(string(data), "\"base_url\"") ||
		!strings.Contains(string(data), "\"api_key_env\"") ||
		!strings.Contains(string(data), "\"fallback_models\"") {
		t.Fatalf("config JSON missing snake_case keys: %s", data)
	}
}

func TestLoadOrCreateConfigLoadsExistingFile(t *testing.T) {
	dir := setTempConfigDir(t)
	path := filepath.Join(dir, "slither", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"z-ai/glm-5.2","base_url":"https://example.test/v1","api_key_env":"MY_KEY","local":{"model":"lm","base_url":"http://127.0.0.1:9000/v1","api_key_env":"LOCAL_KEY"},"fallback_models":["a","b"]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadOrCreateConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "z-ai/glm-5.2" {
		t.Fatalf("model = %q, want loaded value", cfg.Model)
	}
	if cfg.Local.BaseURL != "http://127.0.0.1:9000/v1" {
		t.Fatalf("local base url = %q, want loaded value", cfg.Local.BaseURL)
	}
	if len(cfg.FallbackModels) != 2 {
		t.Fatalf("fallback models = %#v, want 2 entries", cfg.FallbackModels)
	}
}

func TestWriteConfigReplacesExistingFileAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"model":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	cfg.Model = "new-model"
	if err := writeConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"model": "new-model"`) {
		t.Fatalf("config was not replaced with new model: %s", data)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "config.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary config files left behind: %v", matches)
	}
}

func TestAtomicWriteFilePreservesTargetContentsWhenReplaceFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(path, "keep")
	if err := os.WriteFile(keep, []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := atomicWriteFile(path, []byte("replacement"), 0o644); err == nil {
		t.Fatal("atomicWriteFile unexpectedly replaced a non-empty directory")
	}
	data, err := os.ReadFile(keep)
	if err != nil {
		t.Fatalf("existing target contents not preserved: %v", err)
	}
	if string(data) != "still here" {
		t.Fatalf("existing target contents = %q, want preserved", data)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "config.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary config files left behind after failure: %v", matches)
	}
}

func TestResolveReportOptionsFlagOverridesConfig(t *testing.T) {
	t.Parallel()
	cfg := Config{Model: "config-model", BaseURL: "https://config.test/v1", APIKeyEnv: "CONFIG_KEY"}
	opts, err := resolveReportOptions(cfg, []string{"--model", "flag-model", "--base-url", "https://flag.test/v1", "."})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Model != "flag-model" {
		t.Fatalf("model = %q, want flag override", opts.Model)
	}
	if opts.BaseURL != "https://flag.test/v1" {
		t.Fatalf("base url = %q, want flag override", opts.BaseURL)
	}
	if opts.APIKeyEnv != "CONFIG_KEY" {
		t.Fatalf("api key env = %q, want config fallback", opts.APIKeyEnv)
	}
}

func TestResolveReportOptionsUsesConfigWhenNoFlag(t *testing.T) {
	t.Parallel()
	cfg := Config{Model: "config-model", BaseURL: "https://config.test/v1", APIKeyEnv: "CONFIG_KEY"}
	opts, err := resolveReportOptions(cfg, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Model != "config-model" || opts.BaseURL != "https://config.test/v1" {
		t.Fatalf("opts = %q/%q, want config values", opts.Model, opts.BaseURL)
	}
}

func TestResolveReportOptionsEmptyModelStaysDeterministic(t *testing.T) {
	t.Parallel()
	opts, err := resolveReportOptions(defaultConfig(), []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Model != "" {
		t.Fatalf("model = %q, want empty deterministic default", opts.Model)
	}
	scorer, err := NewModelScorer(opts)
	if err != nil {
		t.Fatal(err)
	}
	if scorer != nil {
		t.Fatal("expected nil scorer for empty model (deterministic path)")
	}
}

func TestResolveReportOptionsLocalUsesConfigProfile(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	opts, err := resolveReportOptions(cfg, []string{"--local", "."})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Model != cfg.Local.Model {
		t.Fatalf("model = %q, want local profile %q", opts.Model, cfg.Local.Model)
	}
	if opts.BaseURL != cfg.Local.BaseURL {
		t.Fatalf("base url = %q, want local profile %q", opts.BaseURL, cfg.Local.BaseURL)
	}
	if opts.APIKeyEnv != cfg.Local.APIKeyEnv {
		t.Fatalf("api key env = %q, want local profile %q", opts.APIKeyEnv, cfg.Local.APIKeyEnv)
	}
}

func TestResolveReportOptionsThreadsFallbackModels(t *testing.T) {
	t.Parallel()
	cfg := Config{Model: "m", BaseURL: "https://config.test/v1", FallbackModels: []string{"a", "b"}}
	opts, err := resolveReportOptions(cfg, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.FallbackModels) != 2 || opts.FallbackModels[0] != "a" {
		t.Fatalf("FallbackModels = %#v, want config values", opts.FallbackModels)
	}
}

func TestResolveReportOptionsLocalClearsFallbackModels(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	cfg.FallbackModels = []string{"a", "b"}
	opts, err := resolveReportOptions(cfg, []string{"--local", "."})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.FallbackModels) != 0 {
		t.Fatalf("FallbackModels = %#v, want cleared under --local", opts.FallbackModels)
	}
}

func TestResolveReportOptionsLocalKeepsExplicitFlags(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	opts, err := resolveReportOptions(cfg, []string{"--local", "--base-url", "https://explicit.example.com/v1", "--api-key-env", "MY_CUSTOM_KEY", "."})
	if err != nil {
		t.Fatal(err)
	}
	if opts.BaseURL != "https://explicit.example.com/v1" {
		t.Fatalf("base url = %q, want explicit override preserved", opts.BaseURL)
	}
	if opts.APIKeyEnv != "MY_CUSTOM_KEY" {
		t.Fatalf("api key env = %q, want explicit override preserved", opts.APIKeyEnv)
	}
	// Model was NOT explicitly passed, so local profile model should still apply.
	if opts.Model != cfg.Local.Model {
		t.Fatalf("model = %q, want local profile default %q", opts.Model, cfg.Local.Model)
	}
}

func TestResolveReportOptionsNoCacheFlag(t *testing.T) {
	t.Parallel()
	opts, err := resolveReportOptions(defaultConfig(), []string{"--no-cache", "."})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.NoCache {
		t.Fatal("--no-cache did not set opts.NoCache")
	}
}
