package slither

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// userConfigDir is the seam tests override to redirect the slither config
// directory. Production resolves os.UserConfigDir(); on darwin that is
// ~/Library/Application Support (XDG_CONFIG_HOME is NOT honored there), so an
// injectable var is the portable test seam rather than an env var.
var userConfigDir = os.UserConfigDir

// Config holds model-selection data sourced from the on-disk config file.
type Config struct {
	Model          string       `json:"model"`
	BaseURL        string       `json:"base_url"`
	APIKeyEnv      string       `json:"api_key_env"`
	Local          LocalProfile `json:"local"`
	FallbackModels []string     `json:"fallback_models"`
}

// LocalProfile is the --local OpenAI-compatible model profile.
type LocalProfile struct {
	Model     string `json:"model"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
}

// defaultConfig is the built-in seed written on first run. Model is empty so
// the deterministic offline fallback remains the default scoring path.
func defaultConfig() Config {
	return Config{
		Model:     "",
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "OPENROUTER_API_KEY",
		Local: LocalProfile{
			Model:     "Qwen3.6-35B-A3B-oQ4-fp16-mtp",
			BaseURL:   "http://127.0.0.1:8000/v1",
			APIKeyEnv: "SLITHER_API_KEY",
		},
		FallbackModels: []string{},
	}
}

// configPath returns the slither config.json path under the user config dir.
func configPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "slither", "config.json"), nil
}

// LoadOrCreateConfig loads the slither config file, creating it with built-in
// defaults on first run when it does not yet exist. Missing keys in an existing
// file keep their built-in defaults.
func LoadOrCreateConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := defaultConfig()
			if werr := writeConfig(path, cfg); werr != nil {
				return Config{}, werr
			}
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

func writeConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}
