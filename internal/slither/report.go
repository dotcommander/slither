package slither

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func RenderMarkdown(report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Slither Report\n\n")
	fmt.Fprintf(&b, "> Slither creeps like a snake through `%s`, tasting each path for cheap-model scent before striking only where the signal is strongest.\n\n", report.Repo)
	fmt.Fprintf(&b, "- Generated: `%s`\n", report.GeneratedAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&b, "- Days: `%d`\n", report.Days)
	fmt.Fprintf(&b, "- Patterns source: `%s`\n", report.PatternsSource)
	fmt.Fprintf(&b, "- Files seen: `%d`\n", report.FilesSeen)
	if report.Discovery.Source != "" {
		fmt.Fprintf(&b, "- Discovery: source `%s`, candidates `%d`, git tracked `%d`, git untracked `%d`, filesystem files `%d`\n", report.Discovery.Source, report.Discovery.CandidateFiles, report.Discovery.GitTracked, report.Discovery.GitUntracked, report.Discovery.FilesystemFiles)
	}
	fmt.Fprintf(&b, "- Files reported: `%d`\n", len(report.Rows))
	if report.Model == "" {
		fmt.Fprintf(&b, "- Scoring: deterministic fallback\n\n")
	} else {
		fmt.Fprintf(&b, "- Scoring: wormhole model `%s` at `%s`\n\n", report.Model, report.BaseURL)
	}
	if len(report.SkippedSignals) > 0 {
		fmt.Fprintf(&b, "- Skipped signals: `%s`\n\n", strings.Join(report.SkippedSignals, "`, `"))
	}
	fmt.Fprintf(&b, "| rank | file | score | seed_score | layers | churn | fix_touches | lines | imports | incoming_refs | smell_risk | hotspot_risk | sdk_dx_risk | unknowns_risk | env_contract_risk | workflow_security_risk | migration_safety_risk | container_build_risk | kubernetes_security_risk | terraform_security_risk | openapi_contract_risk | cors_security_risk | cookie_security_risk | dependency_health_risk | centrality_risk | cochange_risk | ownership_risk | flake_risk | oracle_risk | stale_marker_risk | test_gap | path_risk | content_risk | markers | reasons | summary |\n")
	fmt.Fprintf(&b, "| ---: | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | ---: | --- | --- |\n")
	for i, row := range report.Rows {
		layers := "none"
		if len(row.EvidenceLayers) > 0 {
			layers = strings.Join(row.EvidenceLayers, ", ")
		}
		fmt.Fprintf(
			&b,
			"| %d | `%s` | %d | %.2f | %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %t | %d | %d | %d | %s | %s |\n",
			i+1,
			row.Path,
			row.Score,
			row.SeedScore,
			escapeCell(layers),
			row.Churn,
			row.FixTouches,
			row.Lines,
			row.Imports,
			row.IncomingRefs,
			row.SmellRisk,
			row.HotspotRisk,
			row.SDKDXRisk,
			row.UnknownsRisk,
			row.EnvContractRisk,
			row.WorkflowSecurityRisk,
			row.MigrationSafetyRisk,
			row.ContainerBuildRisk,
			row.KubernetesSecurityRisk,
			row.TerraformSecurityRisk,
			row.OpenAPIContractRisk,
			row.CORSSecurityRisk,
			row.CookieSecurityRisk,
			row.DependencyHealthRisk,
			row.CentralityRisk,
			row.CochangeRisk,
			row.OwnershipRisk,
			row.FlakeRisk,
			row.OracleRisk,
			row.StaleMarkerRisk,
			row.TestGap,
			row.PathRisk,
			row.ContentRisk,
			row.Markers,
			escapeCell(strings.Join(row.Reasons, ", ")),
			escapeCell(row.Summary),
		)
	}
	if report.CullLedger != nil {
		fmt.Fprintf(&b, "\n## Cheap-Model Cull Ledger\n\n")
		fmt.Fprintf(&b, "- Stop reason: `%s`\n", report.CullLedger.StopReason)
		fmt.Fprintf(&b, "- Rows considered: `%d`\n\n", report.CullLedger.RowsConsidered)
		writeCullBucketMarkdown(&b, "kept_for_premium", report.CullLedger.KeptForPremium)
		writeCullBucketMarkdown(&b, "alternates", report.CullLedger.Alternates)
		writeCullBucketMarkdown(&b, "culled_generated_or_report", report.CullLedger.Generated)
		writeCullBucketMarkdown(&b, "culled_test_only", report.CullLedger.TestOnly)
		writeCullBucketMarkdown(&b, "culled_low_signal", report.CullLedger.LowSignal)
		writeCullBucketMarkdown(&b, "culled_duplicate_surface", report.CullLedger.Duplicate)
		writeCullBucketMarkdown(&b, "needs_more_evidence", report.CullLedger.NeedsEvidence)
	}
	return b.String()
}

func RenderJSON(report Report) ([]byte, error) {
	payload := reportEnvelope{
		RunLabel:       "slither_report",
		Repo:           report.Repo,
		GeneratedAt:    report.GeneratedAt,
		Days:           report.Days,
		PatternsSource: report.PatternsSource,
		FilesSeen:      report.FilesSeen,
		FilesReported:  len(report.Rows),
		RowCount:       len(report.Rows),
		Discovery:      report.Discovery,
		Model:          report.Model,
		BaseURL:        report.BaseURL,
		SkippedSignals: report.SkippedSignals,
		Rows:           report.Rows,
		CullLedger:     report.CullLedger,
	}
	return json.MarshalIndent(payload, "", "  ")
}

type reportEnvelope struct {
	RunLabel       string         `json:"run_label"`
	Repo           string         `json:"repo"`
	GeneratedAt    time.Time      `json:"generated_at"`
	Days           int            `json:"days"`
	PatternsSource string         `json:"patterns_source"`
	FilesSeen      int            `json:"files_seen"`
	FilesReported  int            `json:"files_reported"`
	RowCount       int            `json:"row_count"`
	Discovery      DiscoveryStats `json:"discovery"`
	Model          string         `json:"model,omitempty"`
	BaseURL        string         `json:"base_url,omitempty"`
	SkippedSignals []string       `json:"skipped_signals,omitempty"`
	Rows           []FileEvidence `json:"rows"`
	CullLedger     *CullLedger    `json:"cull_ledger,omitempty"`
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func writeCullBucketMarkdown(b *strings.Builder, name string, bucket CullBucket) {
	fmt.Fprintf(b, "### `%s`\n\n", name)
	fmt.Fprintf(b, "- Count: `%d`\n\n", bucket.Count)
	if len(bucket.Examples) == 0 {
		return
	}
	fmt.Fprintf(b, "| file | score | strongest_evidence_intersection | reason |\n")
	fmt.Fprintf(b, "| --- | ---: | --- | --- |\n")
	for _, entry := range bucket.Examples {
		fmt.Fprintf(
			b,
			"| `%s` | %d | %s | %s |\n",
			entry.Path,
			entry.Score,
			escapeCell(entry.StrongestEvidenceIntersection),
			escapeCell(entry.Reason),
		)
	}
	fmt.Fprintf(b, "\n")
}
