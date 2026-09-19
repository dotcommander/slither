package slither

import (
	"fmt"
	"io"
	"strings"
)

type commandFlag struct {
	Name        string `json:"name"`
	Default     string `json:"default,omitempty"`
	Constraint  string `json:"constraint,omitempty"`
	Description string `json:"description"`
}

type commandSpec struct {
	Name     string        `json:"name"`
	Usage    string        `json:"usage"`
	Summary  string        `json:"summary"`
	Output   string        `json:"output"`
	Examples []string      `json:"examples"`
	Flags    []commandFlag `json:"flags"`
	AgentOps []string      `json:"agent_operations,omitempty"`
}

func commandSpecs() []commandSpec {
	cfg := defaultConfig()
	return []commandSpec{
		{Name: "version", Usage: "slither version [--build|--json]", Summary: "print build provenance", Output: "text by default; slither.version/v1 with --json", Examples: []string{"slither version --build", "slither version --json"}, Flags: []commandFlag{{"--build", "false", "exclusive with --json", "include module, revision, and Go provenance"}, {"--json", "false", "exclusive with --build", "emit slither.version/v1"}}},
		{Name: "doctor", Usage: "slither doctor [--json]", Summary: "read-only configuration and local model readiness", Output: "text by default; slither.doctor/v1 with --json", Examples: []string{"slither doctor", "slither doctor --json"}, Flags: []commandFlag{{"--json", "false", "no config/cache writes or non-loopback requests", "emit slither.doctor/v1"}}},
		{Name: "outputs", Usage: "slither outputs [surface] [--json]", Summary: "choose a report, protocol, or ledger output", Output: "text by default; slither.output-catalog/v1 with --json", Examples: []string{"slither outputs", "slither outputs report-summary --json"}, Flags: []commandFlag{{"--json", "false", "at most one surface", "emit slither.output-catalog/v1"}}},
		{Name: "completion", Usage: "slither completion bash|zsh", Summary: "generate dependency-free shell completion", Output: "shell script on stdout", Examples: []string{"source <(slither completion bash)"}},
		{Name: "report", Usage: "slither report [repo] [flags]", Summary: "scan a repository into a ranked evidence report", Output: "Markdown or slither.report/v1 JSON; --summary selects concise output; --local selects " + cfg.Local.Model + " at " + cfg.Local.BaseURL, Examples: []string{"slither report . --out slither-report.md --top 80", "slither report . --summary --json --out -", "slither report . --inventory security --json"}, Flags: []commandFlag{
			{"--out", defaultOut, "- writes stdout", "output path"}, {"--top", itoa(defaultTop), "positive", "ranked production target"}, {"--max-bytes", itoa(int(defaultMaxBytes)), "positive", "per-file inspection cap"}, {"--days", itoa(defaultDays), "positive", "history window"}, {"--patterns", "embedded", "path or catalog JSON", "override triage patterns"}, {"--focus", "", "regular expression", "filter post-scan evidence"}, {"--include", "", "repeat or comma-separate glob", "include paths"}, {"--exclude", "", "repeat or comma-separate glob", "exclude paths"}, {"--why-top", "0", "non-negative", "top explanation count"}, {"--inventory", "", strings.Join(reviewLaneNamesForMetadata(), ", "), "single review-lane inventory"}, {"--model", "", "", "wormhole model ID"}, {"--base-url", cfg.BaseURL, "OpenAI-compatible URL", "model endpoint"}, {"--api-key-env", cfg.APIKeyEnv, "environment variable name", "credential source"}, {"--local", "false", "", "use local profile"}, {"--jev", "false", "", "use Jev typed-verdict profile"}, {"--json", "false", "with --summary emits slither.summary/v1", "machine-readable output"}, {"--summary", "false", "", "concise report output"}, {"--cull", "false", "", "include cull aggregates"}, {"--no-cache", "false", "", "disable model score cache"},
		}},
		{Name: "agent", Usage: "slither agent [repo] [--outcomes path]", Summary: "run the bounded JSONL agent bridge", Output: "slither.agent/v1 JSONL on stdout", Examples: []string{"printf '%s\\n' '{\"schema\":\"slither.agent/v1\",\"id\":\"hello\",\"op\":\"hello\"}' | slither agent ."}, Flags: []commandFlag{{"--outcomes", "", "parent must exist; private 0600 ledger", "opt in to feedback persistence"}}, AgentOps: []string{"hello", "scan", "query", "context", "feedback"}},
		{Name: "eval", Usage: "slither eval --outcomes path --report path [--report path...] (--json|--markdown) [--out -]", Summary: "evaluate outcome-ledger labels against reports", Output: "slither.eval/v1 JSON or privacy-minimal Markdown", Examples: []string{"slither eval --outcomes outcomes.jsonl --report report.json --markdown"}, Flags: []commandFlag{{"--outcomes", "", "required exactly once", "outcome ledger"}, {"--report", "", "repeat; one or more required", "report JSON input"}, {"--json", "false", "exactly one of --json/--markdown", "emit slither.eval/v1"}, {"--markdown", "false", "exactly one of --json/--markdown", "human evaluation summary"}, {"--out", "-", "cannot alias an input", "output path or stdout"}}},
	}
}

func reviewLaneNamesForMetadata() []string {
	names := make([]string, 0, len(reviewLanePriority))
	for lane := range reviewLanePriority {
		names = append(names, lane)
	}
	// The priority map is stable policy; use it instead of map iteration.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if reviewLanePriority[names[j]] < reviewLanePriority[names[i]] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

func findCommandSpec(name string) (commandSpec, bool) {
	for _, spec := range commandSpecs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return commandSpec{}, false
}

func commandFlagNames(name string) []string {
	spec, ok := findCommandSpec(name)
	if !ok {
		return nil
	}
	flags := make([]string, 0, len(spec.Flags))
	for _, flag := range spec.Flags {
		flags = append(flags, flag.Name)
	}
	return flags
}

func printRootHelp(w io.Writer) {
	fmt.Fprint(w, "slither - bounded repository evidence and review packets\n\nUsage:\n  slither <command> [flags]\n\nCommands:\n")
	for _, spec := range commandSpecs() {
		fmt.Fprintf(w, "  %-12s %s\n", spec.Name, spec.Summary)
	}
	fmt.Fprint(w, "\nRun slither help <command> or slither <command> --help for flags, limits, examples, and output behavior.\n")
}

func printCommandHelp(w io.Writer, name string) bool {
	spec, ok := findCommandSpec(name)
	if !ok {
		return false
	}
	fmt.Fprintf(w, "%s\n\n%s\n\nOutput: %s\n", spec.Usage, spec.Summary, spec.Output)
	if len(spec.Flags) > 0 {
		fmt.Fprint(w, "\nFlags:\n")
		for _, flag := range spec.Flags {
			name := flag.Name
			if flag.Default != "" {
				name += " " + flag.Default
			}
			line := fmt.Sprintf("  %-24s %s", name, flag.Description)
			if flag.Constraint != "" {
				line += "; " + flag.Constraint
			}
			fmt.Fprintln(w, line)
		}
	}
	if len(spec.AgentOps) > 0 {
		fmt.Fprintf(w, "\nAgent operations: %s\n", strings.Join(spec.AgentOps, ", "))
	}
	fmt.Fprint(w, "\nExamples:\n")
	for _, example := range spec.Examples {
		fmt.Fprintf(w, "  %s\n", example)
	}
	return true
}
