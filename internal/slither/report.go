package slither

import (
	"encoding/json"
	"fmt"
	"sort"
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
	writeExecutiveTriageMarkdown(&b, report)
	if report.CullLedger != nil {
		writeCullLedgerMarkdown(&b, *report.CullLedger)
	}
	rankedRows := rankedMarkdownRows(report.Rows)
	fmt.Fprintf(&b, "## Ranked Files\n\n")
	if len(rankedRows) < len(report.Rows) {
		fmt.Fprintf(&b, "Culled generated, test-only, duplicate-surface, and needs-more-evidence rows are omitted here; use the cull ledger or `--json` for the full evidence set.\n\n")
	}
	fmt.Fprintf(&b, "| rank | file | score | confidence | evidence | review command | top signals | note |\n")
	fmt.Fprintf(&b, "| ---: | --- | ---: | --- | --- | --- | --- | --- |\n")
	for i, row := range rankedRows {
		fmt.Fprintf(
			&b,
			"| %d | `%s` | %d | %s | %s | %s | %s | %s |\n",
			i+1,
			row.Path,
			row.Score,
			cellOrDash(row.Confidence),
			escapeCell(compactList(row.EvidenceLayers, 5)),
			cellOrDash(row.VerifyCmd),
			escapeCell(compactList(topReasons(row, 3), 3)),
			escapeCell(rowNote(row)),
		)
	}
	writeDetailedSignalsMarkdown(&b, report.Rows)
	if len(report.ReviewPlan) > 0 {
		writeReviewPlanMarkdown(&b, report.ReviewPlan)
	}
	return b.String()
}

func rankedMarkdownRows(rows []FileEvidence) []FileEvidence {
	rankedRows := make([]FileEvidence, 0, len(rows))
	seenSurfaces := map[string]string{}
	for _, row := range rows {
		surfaceKey := cullSurfaceKey(row)
		_, isDuplicate := seenSurfaces[surfaceKey]
		switch {
		case isGeneratedOrReportPath(row.Path) || isTestOnlyCull(row) || needsMoreEvidence(row):
			continue
		case isDuplicate && row.Score < 4:
			continue
		}
		rankedRows = append(rankedRows, row)
		if keepForPremium(row) || row.Score >= 3 {
			seenSurfaces[surfaceKey] = row.Path
		}
	}
	return rankedRows
}

func writeExecutiveTriageMarkdown(b *strings.Builder, report Report) {
	stats := summarizeRows(report.Rows)
	fmt.Fprintf(b, "## Executive Triage\n\n")
	fmt.Fprintf(b, "- Start with: %s\n", startHere(report.Rows))
	fmt.Fprintf(b, "- Confidence: high `%d`, medium `%d`, low `%d`; test-gap rows: `%d`\n", stats.HighConfidence, stats.MediumConfidence, stats.LowConfidence, stats.TestGaps)
	fmt.Fprintf(b, "- History-backed rows: `%d`; import-graph-backed rows: `%d`; deterministic-only rows: `%d`\n", stats.HistoryBacked, stats.ImportGraphBacked, stats.HeuristicOnly)
	if len(stats.TopLayers) > 0 {
		fmt.Fprintf(b, "- Dominant discriminating evidence layers: `%s`\n", strings.Join(stats.TopLayers, "`, `"))
	}
	if len(report.ReviewPlan) > 0 {
		fmt.Fprintf(b, "- Review lanes: `%s`\n", strings.Join(reviewLaneNames(report.ReviewPlan), "`, `"))
	}
	fmt.Fprintf(b, "\n")
}

type rowSummaryStats struct {
	HighConfidence    int
	MediumConfidence  int
	LowConfidence     int
	TestGaps          int
	HistoryBacked     int
	ImportGraphBacked int
	HeuristicOnly     int
	TopLayers         []string
}

func summarizeRows(rows []FileEvidence) rowSummaryStats {
	var stats rowSummaryStats
	layerCounts := map[string]int{}
	for _, row := range rows {
		switch row.Confidence {
		case "high":
			stats.HighConfidence++
		case "medium":
			stats.MediumConfidence++
		case "low":
			stats.LowConfidence++
		}
		if row.TestGap {
			stats.TestGaps++
		}
		switch row.EvidenceClass {
		case "git_history":
			stats.HistoryBacked++
		case "import_graph":
			stats.ImportGraphBacked++
		case "heuristic":
			stats.HeuristicOnly++
		}
		for _, layer := range row.EvidenceLayers {
			layerCounts[layer]++
		}
	}
	stats.TopLayers = topLayerNames(layerCounts, len(rows), 5)
	return stats
}

func topLayerNames(counts map[string]int, totalRows, limit int) []string {
	type layerCount struct {
		name  string
		count int
	}
	layers := make([]layerCount, 0, len(counts))
	for name, count := range counts {
		if totalRows > 0 && count == totalRows {
			continue
		}
		layers = append(layers, layerCount{name: name, count: count})
	}
	sort.Slice(layers, func(i, j int) bool {
		if layers[i].count != layers[j].count {
			return layers[i].count > layers[j].count
		}
		return layers[i].name < layers[j].name
	})
	if len(layers) > limit {
		layers = layers[:limit]
	}
	out := make([]string, 0, len(layers))
	for _, layer := range layers {
		out = append(out, fmt.Sprintf("%s:%d", layer.name, layer.count))
	}
	return out
}

func startHere(rows []FileEvidence) string {
	if len(rows) == 0 {
		return "no reportable rows"
	}
	row := rows[0]
	signals := compactList(topReasons(row, 3), 3)
	if signals == "" {
		signals = compactList(row.EvidenceLayers, 3)
	}
	return fmt.Sprintf("`%s` (score `%d`, %s)", row.Path, row.Score, escapeCell(signals))
}

func reviewLaneNames(plan []ReviewLane) []string {
	names := make([]string, 0, len(plan))
	for _, lane := range plan {
		names = append(names, lane.Lane)
	}
	return names
}

func writeDetailedSignalsMarkdown(b *strings.Builder, rows []FileEvidence) {
	fmt.Fprintf(b, "\n## Detailed Signals\n\n")
	fmt.Fprintf(b, "Raw per-risk fields remain available in `--json` output; this table keeps the Markdown reviewable.\n\n")
	fmt.Fprintf(b, "| rank | file | seed_score | class | churn | fix_touches | lines | key risk fields | test_gap | reasons |\n")
	fmt.Fprintf(b, "| ---: | --- | ---: | --- | ---: | ---: | ---: | --- | --- | --- |\n")
	for i, row := range rows {
		fmt.Fprintf(
			b,
			"| %d | `%s` | %.2f | %s | %d | %d | %d | %s | %t | %s |\n",
			i+1,
			row.Path,
			row.SeedScore,
			cellOrDash(row.EvidenceClass),
			row.Churn,
			row.FixTouches,
			row.Lines,
			escapeCell(compactList(keyRiskFields(row), 4)),
			row.TestGap,
			escapeCell(compactList(topReasons(row, 3), 3)),
		)
	}
}

func keyRiskFields(row FileEvidence) []string {
	candidates := []struct {
		name  string
		value int
	}{
		{"path_risk", row.PathRisk},
		{"content_risk", row.ContentRisk},
		{"smell_risk", row.SmellRisk},
		{"hotspot_risk", row.HotspotRisk},
		{"sdk_dx_risk", row.SDKDXRisk},
		{"unknowns_risk", row.UnknownsRisk},
		{"env_contract_risk", row.EnvContractRisk},
		{"workflow_security_risk", row.WorkflowSecurityRisk},
		{"migration_safety_risk", row.MigrationSafetyRisk},
		{"container_build_risk", row.ContainerBuildRisk},
		{"kubernetes_security_risk", row.KubernetesSecurityRisk},
		{"terraform_security_risk", row.TerraformSecurityRisk},
		{"openapi_contract_risk", row.OpenAPIContractRisk},
		{"cors_security_risk", row.CORSSecurityRisk},
		{"cookie_security_risk", row.CookieSecurityRisk},
		{"dependency_health_risk", row.DependencyHealthRisk},
		{"centrality_risk", row.CentralityRisk},
		{"cochange_risk", row.CochangeRisk},
		{"ownership_risk", row.OwnershipRisk},
		{"flake_risk", row.FlakeRisk},
		{"oracle_risk", row.OracleRisk},
		{"stale_marker_risk", row.StaleMarkerRisk},
	}
	var fields []string
	for _, candidate := range candidates {
		if candidate.value > 0 {
			fields = append(fields, fmt.Sprintf("%s=%d", candidate.name, candidate.value))
		}
	}
	return fields
}

func topReasons(row FileEvidence, limit int) []string {
	if len(row.Reasons) <= limit {
		return row.Reasons
	}
	return row.Reasons[:limit]
}

func rowNote(row FileEvidence) string {
	if row.Caveat != "" {
		return row.Caveat
	}
	if stringSliceContains(row.EvidenceLayers, "model") && row.Summary != "" {
		return truncateCell(row.Summary, 120)
	}
	return "deterministic signals; inspect file"
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
		FirstReadQueue: report.FirstReadQueue,
		ReviewPlan:     report.ReviewPlan,
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
	FirstReadQueue []ReviewQueue  `json:"first_read_queue,omitempty"`
	ReviewPlan     []ReviewLane   `json:"review_plan,omitempty"`
	CullLedger     *CullLedger    `json:"cull_ledger,omitempty"`
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func compactList(items []string, limit int) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:limit], ", ") + fmt.Sprintf(", +%d more", len(items)-limit)
}

func truncateCell(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	if limit <= 1 {
		return s[:limit]
	}
	return strings.TrimSpace(s[:limit-1]) + "..."
}

func cellOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return escapeCell(s)
}

func writeCullLedgerMarkdown(b *strings.Builder, ledger CullLedger) {
	fmt.Fprintf(b, "## Cheap-Model Cull Ledger\n\n")
	fmt.Fprintf(b, "- Stop reason: `%s`\n", ledger.StopReason)
	fmt.Fprintf(b, "- Rows considered: `%d`\n\n", ledger.RowsConsidered)
	writeCullBucketMarkdown(b, "kept_for_premium", ledger.KeptForPremium)
	writeCullBucketMarkdown(b, "alternates", ledger.Alternates)
	writeCullBucketMarkdown(b, "culled_generated_or_report", ledger.Generated)
	writeCullBucketMarkdown(b, "culled_test_only", ledger.TestOnly)
	writeCullBucketMarkdown(b, "culled_low_signal", ledger.LowSignal)
	writeCullBucketMarkdown(b, "culled_duplicate_surface", ledger.Duplicate)
	writeCullBucketMarkdown(b, "needs_more_evidence", ledger.NeedsEvidence)
}

func writeCullBucketMarkdown(b *strings.Builder, name string, bucket CullBucket) {
	fmt.Fprintf(b, "### `%s`\n\n", name)
	fmt.Fprintf(b, "- Count: `%d`\n\n", bucket.Count)
	if len(bucket.Examples) == 0 {
		return
	}
	fmt.Fprintf(b, "| file | score | confidence | verify | strongest_evidence_intersection | reason |\n")
	fmt.Fprintf(b, "| --- | ---: | --- | --- | --- | --- |\n")
	for _, entry := range bucket.Examples {
		fmt.Fprintf(
			b,
			"| `%s` | %d | %s | %s | %s | %s |\n",
			entry.Path,
			entry.Score,
			cellOrDash(entry.Confidence),
			cellOrDash(entry.VerifyCmd),
			escapeCell(entry.StrongestEvidenceIntersection),
			escapeCell(entry.Reason),
		)
	}
	fmt.Fprintf(b, "\n")
}

func writeReviewPlanMarkdown(b *strings.Builder, plan []ReviewLane) {
	fmt.Fprintf(b, "\n## Review Plan\n\n")
	fmt.Fprintf(b, "| lane | files | top files | omitted | gates | verify | why |\n")
	fmt.Fprintf(b, "| --- | ---: | --- | ---: | --- | --- | --- |\n")
	for _, lane := range plan {
		topFiles, omitted := compactFiles(lane.Files, 4)
		fmt.Fprintf(
			b,
			"| `%s` | %d | %s | %d | %s | %s | %s |\n",
			lane.Lane,
			len(lane.Files),
			escapeCell(compactList(topFiles, 4)),
			omitted,
			escapeCell(compactList(lane.Gates, 3)),
			escapeCell(compactList(lane.Verify, 2)),
			escapeCell(compactList(lane.Why, 2)),
		)
	}
}

func compactFiles(files []string, limit int) ([]string, int) {
	if len(files) <= limit {
		return files, 0
	}
	return files[:limit], len(files) - limit
}
