package slither

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultOut      = "slither-report.md"
	defaultTop      = 80
	defaultMaxBytes = 500_000
	defaultDays     = 90
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return RunWithIO(ctx, args, os.Stdin, stdout, stderr)
}

// RunWithIO is the in-process entry point. Agent mode needs explicit standard
// input so callers can exercise its JSONL protocol without replacing process
// globals; legacy commands intentionally retain their existing behavior.
func RunWithIO(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printRootHelp(stdout)
		return nil
	}
	if args[0] == "help" {
		if len(args) == 1 {
			printRootHelp(stdout)
			return nil
		}
		if len(args) != 2 {
			return usageError("", errors.New("help accepts at most one command"))
		}
		if len(args) == 2 && printCommandHelp(stdout, args[1]) {
			return nil
		}
		return usageError("", fmt.Errorf("unknown command %q", args[1]))
	}
	if commandHelpRequested(args[0], args[1:]) {
		if printCommandHelp(stdout, args[0]) {
			return nil
		}
	}
	// Keep the established `slither COMMAND help` spelling, while only treating
	// flags as help requests when they are not values for another flag.
	if len(args) > 1 && args[1] == "help" {
		if printCommandHelp(stdout, args[0]) {
			return nil
		}
	}

	switch args[0] {
	case "version":
		return runVersion(args[1:], stdout)
	case "doctor":
		err := runDoctor(ctx, args[1:], stdout)
		return commandError("doctor", err)
	case "outputs":
		err := runOutputs(args[1:], stdout)
		return commandError("outputs", err)
	case "completion":
		err := runCompletion(args[1:], stdout)
		return commandError("completion", err)
	case "report":
		err := runReport(ctx, args[1:], stdout)
		return commandError("report", err)
	case "agent":
		err := runAgent(ctx, args[1:], stdin, stdout, stderr)
		return commandError("agent", err)
	case "eval":
		err := runEval(ctx, args[1:], stdout)
		return commandError("eval", err)
	default:
		return usageError("", fmt.Errorf("unknown command %q", args[0]))
	}
}

// commandHelpRequested recognizes command help anywhere in the argument list.
// It deliberately skips values consumed by the command's known value flags, so
// paths and other values named --help or -h retain their ordinary meaning.
func commandHelpRequested(command string, args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
		if commandFlagConsumesValue(command, arg) && index+1 < len(args) {
			index++
		}
	}
	return false
}

func commandFlagConsumesValue(command, arg string) bool {
	switch command {
	case "report":
		switch arg {
		case "-out", "--out", "-top", "--top", "-max-bytes", "--max-bytes", "-days", "--days", "-patterns", "--patterns", "-focus", "--focus", "-include", "--include", "-exclude", "--exclude", "-why-top", "--why-top", "-inventory", "--inventory", "-model", "--model", "-base-url", "--base-url", "-api-key-env", "--api-key-env":
			return true
		}
	case "agent":
		return arg == "-outcomes" || arg == "--outcomes"
	case "eval":
		return arg == "-outcomes" || arg == "--outcomes" ||
			arg == "-report" || arg == "--report" ||
			arg == "-out" || arg == "--out"
	}
	return false
}

type cliUsageError struct {
	command string
	err     error
}

func (e cliUsageError) Error() string {
	if e.command == "" {
		return e.err.Error() + "; run slither --help"
	}
	return e.err.Error() + "; run slither " + e.command + " --help"
}
func (e cliUsageError) Unwrap() error { return e.err }
func usageError(command string, err error) error {
	if err == nil {
		return nil
	}
	return cliUsageError{command: command, err: err}
}

func commandError(command string, err error) error {
	if err == nil {
		return nil
	}
	var usage cliUsageError
	if errors.As(err, &usage) {
		return err
	}
	return err
}

func runVersion(args []string, w io.Writer) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "--build" && args[0] != "--json") {
		return usageError("version", fmt.Errorf("version accepts only --build or --json"))
	}
	info := CurrentBuildInfo()
	if len(args) > 0 && args[0] == "--json" {
		data, err := json.Marshal(struct {
			Schema string `json:"schema"`
			BuildInfo
		}{Schema: "slither.version/v1", BuildInfo: info})
		if err != nil {
			return fmt.Errorf("encode version: %w", err)
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	if len(args) > 0 && args[0] == "--build" {
		fmt.Fprintf(w, "slither %s\n", info.Summary())
		if info.Module != "" {
			fmt.Fprintf(w, "module: %s\n", info.Module)
		}
		if info.Revision != "" {
			fmt.Fprintf(w, "revision: %s\n", info.Revision)
		}
		if info.GoVersion != "" {
			fmt.Fprintf(w, "go: %s\n", info.GoVersion)
		}
		fmt.Fprintf(w, "modified: %t\n", info.Modified)
		return nil
	}
	fmt.Fprintf(w, "slither %s\n", info.Version)
	return nil
}

func existingReportFreshnessHint(ctx context.Context, opts Options) string {
	if opts.Out == "-" {
		return ""
	}
	outInfo, err := os.Stat(opts.Out)
	if err != nil {
		return ""
	}
	newest, newestPath := newestScannedFileModTime(ctx, opts)
	if newestPath == "" || !newest.After(outInfo.ModTime()) {
		return "existing output was current relative to scanned files before this run"
	}
	return fmt.Sprintf("existing output was stale before this run; newest scanned file `%s` is newer than `%s`", newestPath, opts.Out)
}

func newestScannedFileModTime(ctx context.Context, opts Options) (time.Time, string) {
	paths, _, _, err := discoverFiles(ctx, opts.Repo)
	if err != nil {
		return time.Time{}, ""
	}
	paths, _, err = filterDiscoveredPaths(opts.Repo, paths, opts.Include, opts.Exclude)
	if err != nil {
		return time.Time{}, ""
	}
	absOut, _ := filepath.Abs(opts.Out)
	var newest time.Time
	var newestPath string
	for _, path := range paths {
		rel := filterRelPath(opts.Repo, path)
		if shouldSkip(rel) {
			continue
		}
		absPath, _ := filepath.Abs(path)
		if absOut != "" && absPath == absOut {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
			newestPath = rel
		}
	}
	return newest, newestPath
}

func runReport(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		printCommandHelp(stdout, "report")
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
	if opts.Out != "-" {
		opts.Out = filepath.Clean(opts.Out)
	}

	report, err := BuildReport(ctx, opts)
	if err != nil {
		return err
	}
	report.FreshnessHint = existingReportFreshnessHint(ctx, opts)
	if opts.Cull {
		ledger := BuildCullLedger(report)
		report.CullLedger = &ledger
	}
	var output []byte
	if opts.Summary {
		summary := BuildReportSummary(report)
		if opts.JSON {
			output, err = RenderSummaryJSON(summary)
			if err == nil {
				output = append(output, '\n')
			}
		} else {
			output = []byte(RenderSummaryMarkdown(summary))
		}
	} else if opts.JSON {
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
	if err := atomicWriteFile(opts.Out, output, 0o600); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	fmt.Fprintf(stdout, "slither wrote %s with %d report rows and %d ranked files\n", opts.Out, report.FilesScored, len(rankedMarkdownRows(report.Rows)))
	return nil
}
