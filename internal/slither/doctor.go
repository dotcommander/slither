package slither

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"time"
)

const (
	doctorSchema       = "slither.doctor/v1"
	doctorPreflightCap = 1 << 20
)

var doctorLookupIPAddr = net.DefaultResolver.LookupIPAddr

var errDoctorNonLoopback = errors.New("doctor target resolves outside loopback")

type doctorResult struct {
	Schema           string          `json:"schema"`
	ConfigState      string          `json:"config_state"`
	ModelMode        string          `json:"model_mode"`
	APIKeyEnvPresent bool            `json:"api_key_env_present"`
	Preflight        doctorPreflight `json:"preflight"`
	Warnings         []string        `json:"warnings"`
}

type doctorPreflight struct {
	Attempted bool   `json:"attempted"`
	Status    string `json:"status"`
}

func runDoctor(ctx context.Context, args []string, stdout io.Writer) error {
	jsonOutput, err := parseDoctorArgs(args)
	if err != nil {
		return usageError("doctor", err)
	}
	result, err := doctor(ctx)
	if err != nil {
		return err
	}
	if jsonOutput {
		data, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode doctor: %w", err)
		}
		_, err = fmt.Fprintln(stdout, string(data))
		return err
	}
	fmt.Fprintf(stdout, "config: %s\nmodel mode: %s\napi key environment present: %t\nlocal preflight: %s\n", result.ConfigState, result.ModelMode, result.APIKeyEnvPresent, result.Preflight.Status)
	for _, warning := range result.Warnings {
		fmt.Fprintf(stdout, "warning: %s\n", warning)
	}
	return nil
}

func parseDoctorArgs(args []string) (bool, error) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		return false, nil
	}
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && args[0] == "--json" {
		return true, nil
	}
	return false, errors.New("doctor accepts only optional --json")
}

func doctor(ctx context.Context) (doctorResult, error) {
	if err := ctx.Err(); err != nil {
		return doctorResult{}, err
	}
	cfg, state, err := loadConfigReadOnly()
	if err != nil {
		return doctorResult{}, err
	}
	mode := "deterministic"
	if cfg.Model != "" {
		mode = "configured-model"
	}
	result := doctorResult{Schema: doctorSchema, ConfigState: state, ModelMode: mode, APIKeyEnvPresent: cfg.APIKeyEnv != "" && os.Getenv(cfg.APIKeyEnv) != "", Preflight: doctorPreflight{Status: "not_configured"}, Warnings: []string{}}
	if cfg.Model == "" {
		result.Warnings = append(result.Warnings, "model scoring is not configured; reports use deterministic scoring")
		return result, nil
	}
	preflight, warning, err := doctorModelsPreflight(ctx, cfg.BaseURL)
	if err != nil {
		return doctorResult{}, err
	}
	result.Preflight = preflight
	if warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	return result, nil
}

func loadConfigReadOnly() (Config, string, error) {
	configPath, err := configPath()
	if err != nil {
		return Config{}, "", fmt.Errorf("resolve config: %w", err)
	}
	data, err := readConfig(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return defaultConfig(), "missing", nil
	}
	if err != nil {
		return Config{}, "", fmt.Errorf("read config: %w", err)
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, "", fmt.Errorf("parse config: %w", err)
	}
	return cfg, "loaded", nil
}

func doctorModelsPreflight(ctx context.Context, baseURL string) (doctorPreflight, string, error) {
	if err := ctx.Err(); err != nil {
		return doctorPreflight{}, "", err
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return doctorPreflight{Status: "refused_unsafe_endpoint"}, "configured model endpoint is unsafe; preflight was not sent", nil
	}
	u.Path = path.Join(u.Path, "models")
	u.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return doctorPreflight{}, "", fmt.Errorf("build loopback preflight: %w", err)
	}
	// Do not let URL credentials, caller defaults, redirects, or proxies add
	// authorization or redirect a loopback check to a remote host.
	req.Header.Del("Authorization")
	response, err := newDoctorHTTPClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return doctorPreflight{}, "", ctx.Err()
		}
		if errors.Is(err, errDoctorNonLoopback) {
			return doctorPreflight{Status: "refused_non_loopback"}, "configured model endpoint does not resolve exclusively to loopback", nil
		}
		return doctorPreflight{Attempted: true, Status: "unreachable"}, "loopback /models preflight did not succeed", nil
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, doctorPreflightCap+1))
	if err != nil {
		return doctorPreflight{}, "", fmt.Errorf("read loopback preflight: %w", err)
	}
	if len(data) > doctorPreflightCap {
		return doctorPreflight{Attempted: true, Status: "response_too_large"}, "loopback /models response exceeded the 1 MiB safety cap", nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return doctorPreflight{Attempted: true, Status: "unreachable"}, "loopback /models preflight returned a non-success status", nil
	}
	var payload any
	if json.Unmarshal(data, &payload) != nil {
		return doctorPreflight{Attempted: true, Status: "invalid_response"}, "loopback /models preflight returned invalid JSON", nil
	}
	return doctorPreflight{Attempted: true, Status: "reachable"}, "", nil
}

func newDoctorHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: doctorDialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func doctorDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split loopback address: %w", err)
	}
	addresses, err := doctorLookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve loopback host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errDoctorNonLoopback
	}
	for _, address := range addresses {
		if !address.IP.IsLoopback() {
			return nil, errDoctorNonLoopback
		}
	}
	// Dial the resolved address rather than a hostname so resolution cannot be
	// repeated by the dialer after this explicit loopback check.
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
}
