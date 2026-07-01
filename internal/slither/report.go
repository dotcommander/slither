package slither

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const maxDetailedMarkdownRows = 80

func RenderMarkdown(report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Slither Report\n\n")
	fmt.Fprintf(&b, "> Slither creeps like a snake through `%s`, tasting each path for cheap-model scent before striking only where the signal is strongest.\n\n", report.Repo)
	fmt.Fprintf(&b, "- Generated: `%s`\n", report.GeneratedAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&b, "- Days: `%d`\n", report.Days)
	fmt.Fprintf(&b, "- Patterns source: `%s`\n", report.PatternsSource)
	fmt.Fprintf(&b, "- Files seen: `%d`\n", report.FilesSeen)
	if report.Build.Version != "" || report.Build.Revision != "" || report.Build.GoVersion != "" {
		fmt.Fprintf(&b, "- Slither build: `%s`\n", report.Build.Summary())
	}
	if report.Discovery.Source != "" {
		fmt.Fprintf(&b, "- Discovery: source `%s`, candidates `%d`, git tracked `%d`, git untracked `%d`, filesystem files `%d`\n", report.Discovery.Source, report.Discovery.CandidateFiles, report.Discovery.GitTracked, report.Discovery.GitUntracked, report.Discovery.FilesystemFiles)
	}
	if filterSummary := reportFilterSummary(report.Filters); filterSummary != "" {
		fmt.Fprintf(&b, "- Filters: %s\n", filterSummary)
	}
	if report.FreshnessHint != "" {
		fmt.Fprintf(&b, "- Freshness: %s\n", report.FreshnessHint)
	}
	fmt.Fprintf(&b, "- Files reported: `%d`\n", len(report.Rows))
	if report.Model == "" {
		fmt.Fprintf(&b, "- Scoring: deterministic fallback\n\n")
	} else {
		fmt.Fprintf(&b, "- Scoring: wormhole model `%s` at `%s`\n\n", report.Model, report.BaseURL)
	}
	if report.CacheStats != nil {
		fmt.Fprintf(&b, "- Score cache: `%d` hits, `%d` misses\n\n", report.CacheStats.Hits, report.CacheStats.Misses)
	}
	if len(report.SkippedSignals) > 0 {
		fmt.Fprintf(&b, "- Skipped signals: `%s`\n\n", strings.Join(report.SkippedSignals, "`, `"))
	}
	writeExecutiveTriageMarkdown(&b, report)
	if len(report.WhyTop) > 0 {
		writeWhyTopMarkdown(&b, report.WhyTop)
	}
	if report.CullLedger != nil {
		writeCullLedgerMarkdown(&b, *report.CullLedger)
	}
	rankedRows := rankedMarkdownRows(report.Rows)
	fmt.Fprintf(&b, "## Ranked Files\n\n")
	if len(rankedRows) < len(report.Rows) {
		fmt.Fprintf(&b, "Generated/support, documentation, test/fixture, duplicate-surface, needs-more-evidence, low-signal, and weak-score rows are omitted here; separated rows appear below, and `--json` retains all reported evidence rows.\n\n")
	}
	fmt.Fprintf(&b, "| rank | file | score | confidence | actionability | evidence | review command | top signals | note |\n")
	fmt.Fprintf(&b, "| ---: | --- | ---: | --- | --- | --- | --- | --- | --- |\n")
	for i, row := range rankedRows {
		fmt.Fprintf(
			&b,
			"| %d | `%s` | %d | %s | %s | %s | %s | %s | %s |\n",
			i+1,
			row.Path,
			row.Score,
			cellOrDash(row.Confidence),
			cellOrDash(string(actionabilityForRow(row))),
			escapeCell(compactList(row.EvidenceLayers, 5)),
			cellOrDash(row.VerifyCmd),
			escapeCell(compactList(topReasons(row, 3), 3)),
			escapeCell(rowNote(row)),
		)
	}
	writeSeparatedRowsMarkdown(&b, "Documentation Rows", "Documentation and guide files are separated from the production-ranked code queue.", documentationMarkdownRows(report.Rows))
	writeSeparatedRowsMarkdown(&b, "Test Risk Rows", "Test and fixture files with reliability or oracle evidence are separated from the production-ranked queue.", testRiskMarkdownRows(report.Rows))
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
		case isGeneratedOrReportPath(row.Path) ||
			isDocumentationPath(row.Path) ||
			isTestPath(row.Path) ||
			isTestOnlyCull(row) ||
			(needsMoreEvidence(row) && !keepForPremium(row)) ||
			stringSliceContains(row.EvidenceLayers, "low-signal") ||
			(!keepForPremium(row) && row.Score < 3):
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

func documentationMarkdownRows(rows []FileEvidence) []FileEvidence {
	docRows := make([]FileEvidence, 0)
	for _, row := range rows {
		if isGeneratedOrReportPath(row.Path) || isTestPath(row.Path) || !isDocumentationPath(row.Path) {
			continue
		}
		docRows = append(docRows, row)
	}
	return docRows
}

func testRiskMarkdownRows(rows []FileEvidence) []FileEvidence {
	testRows := make([]FileEvidence, 0)
	for _, row := range rows {
		if isGeneratedOrReportPath(row.Path) || !isTestPath(row.Path) || isTestOnlyCull(row) {
			continue
		}
		testRows = append(testRows, row)
	}
	return testRows
}

func isDocumentationPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasPrefix(lower, "docs/") ||
		strings.HasPrefix(lower, "doc/") ||
		strings.HasSuffix(lower, ".md") ||
		strings.HasSuffix(lower, ".mdx") ||
		strings.HasSuffix(lower, ".rst")
}

func writeSeparatedRowsMarkdown(b *strings.Builder, title, intro string, rows []FileEvidence) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n\n", title)
	fmt.Fprintf(b, "%s\n\n", intro)
	fmt.Fprintf(b, "| rank | file | score | confidence | actionability | evidence | review command | top signals |\n")
	fmt.Fprintf(b, "| ---: | --- | ---: | --- | --- | --- | --- | --- |\n")
	for i, row := range rows {
		fmt.Fprintf(
			b,
			"| %d | `%s` | %d | %s | %s | %s | %s | %s |\n",
			i+1,
			row.Path,
			row.Score,
			cellOrDash(row.Confidence),
			cellOrDash(string(actionabilityForRow(row))),
			escapeCell(compactList(row.EvidenceLayers, 5)),
			cellOrDash(row.VerifyCmd),
			escapeCell(compactList(topReasons(row, 3), 3)),
		)
	}
}

func writeExecutiveTriageMarkdown(b *strings.Builder, report Report) {
	stats := summarizeRows(report.Rows)
	rankedCount := len(rankedMarkdownRows(report.Rows))
	documentationCount := countDocumentationRows(report.Rows)
	testCount := countTestRows(report.Rows)
	generatedCount := countGeneratedRows(report.Rows)
	detailOnlyCount := len(report.Rows) - rankedCount - documentationCount - testCount - generatedCount
	if detailOnlyCount < 0 {
		detailOnlyCount = 0
	}
	fmt.Fprintf(b, "## Executive Triage\n\n")
	fmt.Fprintf(b, "- Start with: %s\n", startHere(report.Rows))
	fmt.Fprintf(b, "- Ranked production files: `%d`; separated documentation rows: `%d`; separated test/fixture rows: `%d`; generated/support rows: `%d`; detail-only weak rows: `%d`; total reported rows: `%d`\n", rankedCount, documentationCount, testCount, generatedCount, detailOnlyCount, len(report.Rows))
	fmt.Fprintf(b, "- Confidence: high `%d`, medium `%d`, low `%d`; test-gap rows: `%d`\n", stats.HighConfidence, stats.MediumConfidence, stats.LowConfidence, stats.TestGaps)
	fmt.Fprintf(b, "- History-backed rows: `%d`; import-graph-backed rows: `%d`; deterministic-only rows: `%d`\n", stats.HistoryBacked, stats.ImportGraphBacked, stats.HeuristicOnly)
	if len(stats.TopLayers) > 0 {
		fmt.Fprintf(b, "- Dominant discriminating evidence layers: `%s`\n", strings.Join(stats.TopLayers, "`, `"))
	}
	if counts := actionabilityCounts(reviewableActionabilityRows(report.Rows)); len(counts) > 0 {
		fmt.Fprintf(b, "- Actionability in ranked production rows: `%s`\n", strings.Join(counts, "`, `"))
	}
	if len(report.ReviewPlan) > 0 {
		fmt.Fprintf(b, "- Review lanes: `%s`\n", strings.Join(reviewLaneNames(report.ReviewPlan), "`, `"))
	}
	fmt.Fprintf(b, "\n")
}

func countDocumentationRows(rows []FileEvidence) int {
	count := 0
	for _, row := range rows {
		if isDocumentationPath(row.Path) && !isTestPath(row.Path) && !isGeneratedOrReportPath(row.Path) {
			count++
		}
	}
	return count
}

func countTestRows(rows []FileEvidence) int {
	count := 0
	for _, row := range rows {
		if isTestPath(row.Path) && !isGeneratedOrReportPath(row.Path) {
			count++
		}
	}
	return count
}

func countGeneratedRows(rows []FileEvidence) int {
	count := 0
	for _, row := range rows {
		if isGeneratedOrReportPath(row.Path) {
			count++
		}
	}
	return count
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
	startRows := rankedMarkdownRows(rows)
	if len(startRows) == 0 {
		startRows = rows
	}
	row := startRows[0]
	signals := compactList(topSignalLabels(row, 3), 3)
	if signals == "" {
		signals = compactList(row.EvidenceLayers, 3)
	}
	return fmt.Sprintf("`%s` (score `%d`, %s)", row.Path, row.Score, escapeCell(signals))
}

func topSignalLabels(row FileEvidence, limit int) []string {
	var labels []string
	add := func(label string) {
		if !stringSliceContains(labels, label) {
			labels = append(labels, label)
		}
	}
	for _, reason := range topReasons(row, len(row.Reasons)) {
		switch {
		case strings.HasPrefix(reason, "cochange:bugfix_overlap"), strings.HasPrefix(reason, "bugfix_touches:"):
			add("bug-fix history")
		case strings.HasPrefix(reason, "hotspot:"):
			add("hotspot complexity")
		case strings.HasPrefix(reason, "ownership:"):
			add("concentrated ownership")
		case strings.HasPrefix(reason, "content:rate_limit_boundary"):
			add("rate-limit boundary")
		case strings.HasPrefix(reason, "content:resource_factory"):
			add("resource lifecycle")
		case strings.HasPrefix(reason, "content:reliability_policy_boundary"):
			add("reliability boundary")
		case strings.HasPrefix(reason, "content:audit_correctness_drift_metric"):
			add("correctness metric")
		case strings.HasPrefix(reason, "dependency_health:"):
			add("dependency policy")
		case strings.HasPrefix(reason, "path:"):
			add("sensitive path")
		case strings.HasPrefix(reason, "unknowns:"):
			add("needs invariant review")
		}
		if len(labels) == limit {
			return labels
		}
	}
	if len(labels) > 0 {
		return labels
	}
	return topReasons(row, limit)
}

func reviewLaneNames(plan []ReviewLane) []string {
	names := make([]string, 0, len(plan))
	for _, lane := range plan {
		names = append(names, lane.Lane)
	}
	return names
}

func reportFilterSummary(filters ReportFilters) string {
	var parts []string
	if filters.Focus != "" {
		parts = append(parts, "focus `"+escapeCell(filters.Focus)+"`")
	}
	if len(filters.Include) > 0 {
		parts = append(parts, "include `"+escapeCell(strings.Join(filters.Include, "`, `"))+"`")
	}
	if len(filters.Exclude) > 0 {
		parts = append(parts, "exclude `"+escapeCell(strings.Join(filters.Exclude, "`, `"))+"`")
	}
	if filters.Inventory != "" {
		parts = append(parts, "inventory `"+escapeCell(filters.Inventory)+"`")
	}
	return strings.Join(parts, "; ")
}

func reviewableActionabilityRows(rows []FileEvidence) []FileEvidence {
	return rankedMarkdownRows(rows)
}

func actionabilityCounts(rows []FileEvidence) []string {
	counts := map[Actionability]int{}
	for _, row := range rows {
		counts[actionabilityForRow(row)]++
	}
	order := []Actionability{
		ActionabilityLikelyDefect,
		ActionabilityHighRiskInspect,
		ActionabilityDependencyReview,
		ActionabilityInspect,
		ActionabilityHotspot,
		ActionabilityVerifyFirst,
	}
	out := make([]string, 0, len(order))
	for _, actionability := range order {
		if counts[actionability] > 0 {
			out = append(out, fmt.Sprintf("%s:%d", actionability, counts[actionability]))
		}
	}
	return out
}

func writeDetailedSignalsMarkdown(b *strings.Builder, rows []FileEvidence) {
	fmt.Fprintf(b, "\n## Detailed Signals\n\n")
	if len(rows) > maxDetailedMarkdownRows {
		fmt.Fprintf(b, "Showing the first `%d` of `%d` rows; full per-risk fields remain available in `--json` output.\n\n", maxDetailedMarkdownRows, len(rows))
		rows = rows[:maxDetailedMarkdownRows]
	} else {
		fmt.Fprintf(b, "Raw per-risk fields remain available in `--json` output; this table keeps the Markdown reviewable.\n\n")
	}
	fmt.Fprintf(b, "| rank | file | seed_score | class | actionability | churn | fix_touches | lines | key risk fields | test_gap | reasons |\n")
	fmt.Fprintf(b, "| ---: | --- | ---: | --- | --- | ---: | ---: | ---: | --- | --- | --- |\n")
	for i, row := range rows {
		fmt.Fprintf(
			b,
			"| %d | `%s` | %.2f | %s | %s | %d | %d | %d | %s | %t | %s |\n",
			i+1,
			row.Path,
			row.SeedScore,
			cellOrDash(row.EvidenceClass),
			cellOrDash(string(actionabilityForRow(row))),
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
	if len(row.Reasons) == 0 {
		return nil
	}
	reasons := append([]string(nil), row.Reasons...)
	sort.SliceStable(reasons, func(i, j int) bool {
		return markdownReasonPriority(reasons[i]) > markdownReasonPriority(reasons[j])
	})
	if len(reasons) <= limit {
		return reasons
	}
	return reasons[:limit]
}

func markdownReasonPriority(reason string) int {
	switch {
	case strings.HasPrefix(reason, "content:go_bool_mode_flag_param:"):
		return 0
	case strings.HasPrefix(reason, "content:lint_suppression:"):
		return 0
	default:
		return 1
	}
}

func rowNote(row FileEvidence) string {
	if row.Caveat != "" {
		return row.Caveat
	}
	if stringSliceContains(row.EvidenceLayers, "model") && row.Summary != "" {
		return truncateCell(row.Summary, 120)
	}
	intersection := strongestEvidenceIntersection(row)
	if intersection != "" && intersection != "unknown" {
		return "review " + intersection
	}
	return "review deterministic signal"
}

func buildWhyTopEntries(rows []FileEvidence, limit int) []WhyTopEntry {
	if limit <= 0 {
		return nil
	}
	rankedRows := rankedMarkdownRows(rows)
	if len(rankedRows) > limit {
		rankedRows = rankedRows[:limit]
	}
	entries := make([]WhyTopEntry, 0, len(rankedRows))
	for i, row := range rankedRows {
		entries = append(entries, WhyTopEntry{
			Rank:          i + 1,
			Path:          row.Path,
			Score:         row.Score,
			Confidence:    row.Confidence,
			Actionability: actionabilityForRow(row),
			Evidence:      append([]string(nil), row.EvidenceLayers...),
			Reasons:       topReasons(row, 5),
			VerifyCmd:     row.VerifyCmd,
			Note:          rowNote(row),
		})
	}
	return entries
}

func writeWhyTopMarkdown(b *strings.Builder, entries []WhyTopEntry) {
	fmt.Fprintf(b, "## Why Top %d\n\n", len(entries))
	fmt.Fprintf(b, "| rank | file | why | verify |\n")
	fmt.Fprintf(b, "| ---: | --- | --- | --- |\n")
	for _, entry := range entries {
		why := compactList(append(entry.Evidence, entry.Reasons...), 6)
		if entry.Note != "" {
			why = strings.TrimSpace(why + "; " + entry.Note)
		}
		fmt.Fprintf(b, "| %d | `%s` | %s | %s |\n", entry.Rank, entry.Path, escapeCell(why), cellOrDash(entry.VerifyCmd))
	}
	fmt.Fprintf(b, "\n")
}

func RenderJSON(report Report) ([]byte, error) {
	rows := report.Rows
	if report.CullLedger != nil {
		rows = rowsWithCullDispositions(rows)
	}
	payload := reportEnvelope{
		RunLabel:       "slither_report",
		Repo:           report.Repo,
		GeneratedAt:    report.GeneratedAt,
		Days:           report.Days,
		PatternsSource: report.PatternsSource,
		Build:          report.Build,
		FilesSeen:      report.FilesSeen,
		FilesReported:  len(report.Rows),
		RowCount:       len(report.Rows),
		Discovery:      report.Discovery,
		Model:          report.Model,
		BaseURL:        report.BaseURL,
		SkippedSignals: report.SkippedSignals,
		Filters:        report.Filters,
		Rows:           rows,
		WhyTop:         report.WhyTop,
		FreshnessHint:  report.FreshnessHint,
		FirstReadQueue: report.FirstReadQueue,
		ReviewPlan:     report.ReviewPlan,
		CullLedger:     report.CullLedger,
		CacheStats:     report.CacheStats,
	}
	return json.MarshalIndent(payload, "", "  ")
}

type reportEnvelope struct {
	RunLabel       string         `json:"run_label"`
	Repo           string         `json:"repo"`
	GeneratedAt    time.Time      `json:"generated_at"`
	Days           int            `json:"days"`
	PatternsSource string         `json:"patterns_source"`
	Build          BuildInfo      `json:"build,omitempty"`
	FilesSeen      int            `json:"files_seen"`
	FilesReported  int            `json:"files_reported"`
	RowCount       int            `json:"row_count"`
	Discovery      DiscoveryStats `json:"discovery"`
	Model          string         `json:"model,omitempty"`
	BaseURL        string         `json:"base_url,omitempty"`
	SkippedSignals []string       `json:"skipped_signals,omitempty"`
	Filters        ReportFilters  `json:"filters,omitempty"`
	Rows           []FileEvidence `json:"rows"`
	WhyTop         []WhyTopEntry  `json:"why_top,omitempty"`
	FreshnessHint  string         `json:"freshness_hint,omitempty"`
	FirstReadQueue []ReviewQueue  `json:"first_read_queue,omitempty"`
	ReviewPlan     []ReviewLane   `json:"review_plan,omitempty"`
	CullLedger     *CullLedger    `json:"cull_ledger,omitempty"`
	CacheStats     *CacheStats    `json:"cache_stats,omitempty"`
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
	writeCullBucketMarkdown(b, "culled_documentation", ledger.Documentation)
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
	fmt.Fprintf(b, "| file | score | confidence | actionability | verify | strongest_evidence_intersection | reason |\n")
	fmt.Fprintf(b, "| --- | ---: | --- | --- | --- | --- | --- |\n")
	for _, entry := range bucket.Examples {
		fmt.Fprintf(
			b,
			"| `%s` | %d | %s | %s | %s | %s | %s |\n",
			entry.Path,
			entry.Score,
			cellOrDash(entry.Confidence),
			cellOrDash(string(entry.Actionability)),
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
