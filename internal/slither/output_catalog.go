package slither

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const outputCatalogSchema = "slither.output-catalog/v1"

type outputCatalog struct {
	Schema   string          `json:"schema"`
	Surfaces []outputSurface `json:"surfaces"`
}

type outputSurface struct {
	Name            string           `json:"name"`
	Producer        string           `json:"producer"`
	Schema          string           `json:"schema,omitempty"`
	MediaType       string           `json:"media_type"`
	BestFor         string           `json:"best_for"`
	Privacy         string           `json:"privacy"`
	Limits          []string         `json:"limits,omitempty"`
	Flags           []string         `json:"flags,omitempty"`
	AgentOps        []string         `json:"agent_operations,omitempty"`
	OperationDetail []agentOperation `json:"agent_operation_operands,omitempty"`
	Errors          []string         `json:"stable_errors,omitempty"`
	Compatibility   string           `json:"compatibility"`
}

type agentOperation struct {
	Name        string   `json:"name"`
	IntendedUse string   `json:"intended_use"`
	Required    []string `json:"required_operands,omitempty"`
	Optional    []string `json:"optional_operands,omitempty"`
}

var agentProtocolErrors = []string{"invalid_request", "unsupported_schema", "unsupported_op", "request_too_large", "budget_too_small", "budget_too_large", "target_not_found", "stale_evidence", "feedback_disabled", "feedback_write_failed", "scan_failed", "canceled"}

func agentOperationCatalog() []agentOperation {
	return []agentOperation{
		{Name: "hello", IntendedUse: "discover protocol limits and feedback state"},
		{Name: "scan", IntendedUse: "rebuild the bounded repository snapshot"},
		{Name: "query", IntendedUse: "select bounded evidence summaries", Required: []string{"limit"}, Optional: []string{"target_ids", "focus"}},
		{Name: "context", IntendedUse: "produce redacted bounded source context", Required: []string{"budget_bytes"}, Optional: []string{"target_ids", "focus"}},
		{Name: "feedback", IntendedUse: "record a derived private outcome", Required: []string{"report_id", "evidence_id", "verdict", "files_opened", "tool_calls", "review_ms"}},
	}
}

func buildOutputCatalog() outputCatalog {
	return outputCatalog{Schema: outputCatalogSchema, Surfaces: []outputSurface{
		{Name: "output-catalog", Producer: "slither outputs", Schema: outputCatalogSchema, MediaType: "text/plain or application/json", BestFor: "discovering Slither output choices", Privacy: "command metadata only", Flags: commandFlagNames("outputs"), Compatibility: "new discovery surface"},
		{Name: "completion", Producer: "slither completion", MediaType: "text/x-shellscript", BestFor: "shell command and flag completion", Privacy: "command metadata only", Compatibility: "new dependency-free shell surface"},
		{Name: "report", Producer: "slither report", Schema: reportSchemaVersion, MediaType: "text/markdown or application/json", BestFor: "full ranked evidence and automation", Privacy: "contains repository paths and bounded redacted evidence", Limits: []string{"--top controls ranked production target", "--max-bytes caps each inspected file"}, Flags: commandFlagNames("report"), Compatibility: "existing v1 Markdown and JSON contracts remain unchanged"},
		{Name: "report-summary", Producer: "slither report --summary", Schema: "slither.summary/v1", MediaType: "text/markdown or application/json", BestFor: "first-read triage and compact automation", Privacy: "contains repository paths, evidence labels, and verification commands", Limits: []string{"at most 10 start-here rows", "ranking health uses fixed top_k 15"}, Flags: commandFlagNames("report"), Compatibility: "opt-in; does not alter slither.report/v1"},
		{Name: "agent", Producer: "slither agent", Schema: agentSchema, MediaType: "application/x-ndjson", BestFor: "bounded repository-aware agent operations", Privacy: "query responses omit source and excerpts; context packet is redacted", Limits: []string{"1 MiB request and context caps", "serial operations"}, Flags: commandFlagNames("agent"), AgentOps: []string{"hello", "scan", "query", "context", "feedback"}, OperationDetail: agentOperationCatalog(), Errors: agentProtocolErrors, Compatibility: "existing v1 protocol is unchanged"},
		{Name: "context", Producer: "slither agent context", Schema: "slither.context/v1", MediaType: "application/json", BestFor: "redacted bounded source context", Privacy: "redacts secrets and records omissions", Limits: []string{"maximum 1 MiB budget"}, AgentOps: []string{"context"}, Compatibility: "existing v1 packet is unchanged"},
		{Name: "outcome", Producer: "slither agent --outcomes feedback", Schema: "slither.outcome/v1", MediaType: "application/x-ndjson", BestFor: "private derived evaluation labels", Privacy: "contains no paths, source, snippets, prompts, or commands", Limits: []string{"1 MiB per record", "owner-only ledger"}, Flags: []string{"--outcomes"}, AgentOps: []string{"feedback"}, Compatibility: "existing v1 ledger fields are unchanged"},
		{Name: "eval", Producer: "slither eval --json", Schema: evalSchema, MediaType: "application/json", BestFor: "deterministic evaluation automation", Privacy: "contains no paths, IDs, timestamps, or input names", Limits: []string{"top_k fixed at 15"}, Flags: commandFlagNames("eval"), Compatibility: "existing v1 JSON bytes are unchanged"},
		{Name: "eval-markdown", Producer: "slither eval --markdown", MediaType: "text/markdown", BestFor: "human inspection of evaluation health", Privacy: "contains no paths, IDs, timestamps, or input names", Limits: []string{"top_k fixed at 15"}, Flags: commandFlagNames("eval"), Compatibility: "opt-in presentation over the unchanged eval calculation"},
		{Name: "version", Producer: "slither version --json", Schema: "slither.version/v1", MediaType: "application/json", BestFor: "build provenance automation", Privacy: "build metadata only", Flags: commandFlagNames("version"), Compatibility: "plain version and --build remain available"},
		{Name: "doctor", Producer: "slither doctor --json", Schema: "slither.doctor/v1", MediaType: "application/json", BestFor: "safe readiness checks", Privacy: "reports API-key environment presence only; no raw provider response or error", Limits: []string{"2 second loopback preflight", "1 MiB response cap"}, Flags: commandFlagNames("doctor"), Compatibility: "new read-only surface"},
	}}
}

func runOutputs(args []string, stdout io.Writer) error {
	jsonOutput := false
	surface := ""
	for _, arg := range args {
		switch arg {
		case "--json":
			if jsonOutput {
				return usageError("outputs", errors.New("outputs accepts --json at most once"))
			}
			jsonOutput = true
		case "--help", "-h", "help":
			printCommandHelp(stdout, "outputs")
			return nil
		default:
			if strings.HasPrefix(arg, "-") || surface != "" {
				return usageError("outputs", errors.New("outputs accepts at most one surface and optional --json"))
			}
			surface = arg
		}
	}
	catalog := buildOutputCatalog()
	if surface != "" {
		found := false
		for _, item := range catalog.Surfaces {
			if item.Name == surface {
				catalog.Surfaces = []outputSurface{item}
				found = true
				break
			}
		}
		if !found {
			return usageError("outputs", fmt.Errorf("unknown output surface %q", surface))
		}
	}
	if jsonOutput {
		data, err := json.Marshal(catalog)
		if err != nil {
			return fmt.Errorf("encode output catalog: %w", err)
		}
		_, err = fmt.Fprintln(stdout, string(data))
		return err
	}
	for _, item := range catalog.Surfaces {
		fmt.Fprintf(stdout, "%s\n  producer: %s\n  output: %s %s\n  best for: %s\n  privacy: %s\n", item.Name, item.Producer, item.MediaType, item.Schema, item.BestFor, item.Privacy)
		if len(item.Flags) > 0 {
			fmt.Fprintf(stdout, "  flags: %s\n", strings.Join(item.Flags, ", "))
		}
		if len(item.AgentOps) > 0 {
			fmt.Fprintf(stdout, "  agent operations: %s\n", strings.Join(item.AgentOps, ", "))
		}
		if len(item.OperationDetail) > 0 {
			for _, operation := range item.OperationDetail {
				fmt.Fprintf(stdout, "  operation %s: %s", operation.Name, operation.IntendedUse)
				if len(operation.Required) > 0 {
					fmt.Fprintf(stdout, "; required: %s", strings.Join(operation.Required, ", "))
				}
				if len(operation.Optional) > 0 {
					fmt.Fprintf(stdout, "; optional: %s", strings.Join(operation.Optional, ", "))
				}
				fmt.Fprintln(stdout)
			}
		}
		if len(item.Limits) > 0 {
			fmt.Fprintf(stdout, "  limits: %s\n", strings.Join(item.Limits, "; "))
		}
		if len(item.Errors) > 0 {
			fmt.Fprintf(stdout, "  stable errors: %s\n", strings.Join(item.Errors, ", "))
		}
		fmt.Fprintf(stdout, "  compatibility: %s\n", item.Compatibility)
	}
	return nil
}
