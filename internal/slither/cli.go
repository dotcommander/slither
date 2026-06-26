package slither

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultOut      = "slither-report.md"
	defaultTop      = 80
	defaultMaxBytes = 500_000
	defaultDays     = 90
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}

	switch args[0] {
	case "report":
		return runReport(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printHelp(w io.Writer) {
	cfg := defaultConfig()
	fmt.Fprintf(w, `slither - a cheap-model scout that creeps through every path

Usage:
  slither report [repo] [--out %s] [--top %d] [--max-bytes %d] [--days %d]
  slither report [repo] --json --out slither-report.json
  slither report [repo] --cull --json --out slither-cull.json
  slither report [repo] --patterns triage_patterns.json --json
  slither report [repo] --model z-ai/glm-5.2 --base-url %s
  slither report [repo] --local

Model scoring:
  Slither uses github.com/garyblankenship/wormhole for model calls, matching distill.
  If --model is omitted, slither uses deterministic fallback scoring.
  --local selects %s at %s unless overridden.
`, defaultOut, defaultTop, defaultMaxBytes, defaultDays, cfg.BaseURL, cfg.Local.Model, cfg.Local.BaseURL)
}

func normalizeReportArgs(args []string) []string {
	flagsWithValues := map[string]bool{
		"-out": true, "--out": true,
		"-top": true, "--top": true,
		"-max-bytes": true, "--max-bytes": true,
		"-days": true, "--days": true,
		"-patterns": true, "--patterns": true,
		"-model": true, "--model": true,
		"-base-url": true, "--base-url": true,
		"-api-key-env": true, "--api-key-env": true,
	}
	var flags []string
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		flags = append(flags, arg)
		name := arg
		if before, _, ok := strings.Cut(arg, "="); ok {
			name = before
		}
		if flagsWithValues[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positionals...)
}

// resolveReportOptions builds Options by precedence: explicit CLI flag >
// config-file value > built-in default. Config values are seeded as the flag
// defaults, so an unset flag yields the config value and an empty Model keeps
// the deterministic offline path.
func resolveReportOptions(cfg Config, args []string) (Options, error) {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := Options{Repo: ".", Out: defaultOut, Top: defaultTop, MaxBytes: defaultMaxBytes, Days: defaultDays, Model: cfg.Model, BaseURL: cfg.BaseURL, APIKeyEnv: cfg.APIKeyEnv, FallbackModels: cfg.FallbackModels}
	fs.StringVar(&opts.Out, "out", opts.Out, "Markdown report path, or - for stdout")
	fs.IntVar(&opts.Top, "top", opts.Top, "ranked production files to include")
	fs.Int64Var(&opts.MaxBytes, "max-bytes", opts.MaxBytes, "maximum bytes to inspect per file")
	fs.IntVar(&opts.Days, "days", opts.Days, "history window in days for churn and bug-fix signals")
	fs.StringVar(&opts.Patterns, "patterns", "", "JSON path/content pattern file")
	fs.StringVar(&opts.Model, "model", opts.Model, "cheap model ID for wormhole scoring")
	fs.StringVar(&opts.BaseURL, "base-url", opts.BaseURL, "OpenAI-compatible base URL")
	fs.StringVar(&opts.APIKeyEnv, "api-key-env", opts.APIKeyEnv, "environment variable containing the API key")
	fs.BoolVar(&opts.Local, "local", false, "use local OpenAI-compatible model profile")
	fs.BoolVar(&opts.JSON, "json", false, "emit a machine-readable JSON evidence envelope")
	fs.BoolVar(&opts.Cull, "cull", false, "append a cheap-model cull ledger over reported rows")
	fs.BoolVar(&opts.NoCache, "no-cache", false, "disable the content-hash score cache")
	if err := fs.Parse(normalizeReportArgs(args)); err != nil {
		return Options{}, err
	}
	if fs.NArg() > 1 {
		return Options{}, errors.New("report accepts at most one repo path")
	}
	if fs.NArg() == 1 {
		opts.Repo = fs.Arg(0)
	}
	if opts.Top <= 0 {
		return Options{}, errors.New("--top must be positive")
	}
	if opts.MaxBytes <= 0 {
		return Options{}, errors.New("--max-bytes must be positive")
	}
	if opts.Days <= 0 {
		return Options{}, errors.New("--days must be positive")
	}
	if opts.JSON && opts.Out == defaultOut {
		opts.Out = "slither-report.json"
	}
	if opts.Local {
		// Config fallback IDs are provider-specific (OpenRouter) and do not apply
		// to the local single-model server; clear them to avoid futile failover.
		opts.FallbackModels = nil
		if opts.Model == "" {
			opts.Model = cfg.Local.Model
		}
		if opts.BaseURL == "" || opts.BaseURL == cfg.BaseURL {
			opts.BaseURL = cfg.Local.BaseURL
		}
		if opts.APIKeyEnv == cfg.APIKeyEnv {
			opts.APIKeyEnv = cfg.Local.APIKeyEnv
		}
	}
	return opts, nil
}

func runReport(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		printHelp(stdout)
		return nil
	}
	cfg, err := LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	opts, err := resolveReportOptions(cfg, args)
	if err != nil {
		return err
	}

	repo, err := filepath.Abs(opts.Repo)
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}
	info, err := os.Stat(repo)
	if err != nil {
		return fmt.Errorf("stat repo: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo is not a directory: %s", repo)
	}
	opts.Repo = repo

	report, err := BuildReport(ctx, opts)
	if err != nil {
		return err
	}
	if opts.Cull {
		ledger := BuildCullLedger(report)
		report.CullLedger = &ledger
	}
	var output []byte
	if opts.JSON {
		output, err = RenderJSON(report)
		if err != nil {
			return err
		}
		output = append(output, '\n')
	} else {
		output = []byte(RenderMarkdown(report))
	}
	if opts.Out == "-" {
		_, err = stdout.Write(output)
		return err
	}
	if err := os.WriteFile(opts.Out, output, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	fmt.Fprintf(stdout, "slither wrote %s with %d report rows and %d ranked files\n", opts.Out, report.FilesScored, len(rankedMarkdownRows(report.Rows)))
	return nil
}
