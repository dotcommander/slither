package slither

import (
	"encoding/json"
	"fmt"
	"strings"
)

const summarySchema = "slither.summary/v1"

type reportSummary struct {
	Schema         string               `json:"schema"`
	ReportID       string               `json:"report_id"`
	SourceState    SourceState          `json:"source_state"`
	Parameters     ReportParameters     `json:"parameters"`
	Discovery      DiscoveryStats       `json:"discovery"`
	Selection      summarySelection     `json:"selection"`
	Confidence     map[string]int       `json:"confidence_counts"`
	Actionability  map[string]int       `json:"actionability_counts"`
	ReviewLanes    []string             `json:"review_lanes"`
	SkippedSignals []string             `json:"skipped_signals,omitempty"`
	StartHere      []summaryStartRow    `json:"start_here"`
	RankingHealth  summaryRankingHealth `json:"ranking_health"`
	ScoringHealth  summaryScoringHealth `json:"scoring_health"`
	Cull           *summaryCull         `json:"cull,omitempty"`
}

type summarySelection struct {
	FilesSeen        int `json:"files_seen"`
	FilesScored      int `json:"files_scored"`
	RowsReported     int `json:"rows_reported"`
	RankedProduction int `json:"ranked_production"`
}
type summaryStartRow struct {
	Rank          int           `json:"rank"`
	ID            string        `json:"id"`
	EvidenceID    string        `json:"evidence_id"`
	Path          string        `json:"path"`
	Score         int           `json:"score"`
	SeedScore     float64       `json:"seed_score"`
	Confidence    string        `json:"confidence"`
	Actionability Actionability `json:"actionability"`
	Caveat        string        `json:"caveat,omitempty"`
	Evidence      []string      `json:"evidence,omitempty"`
	VerifyCmd     string        `json:"verify_cmd,omitempty"`
}
type summaryRankingHealth struct {
	TopK               int            `json:"top_k"`
	Slots              int            `json:"slots"`
	DistinctTopKScores int            `json:"distinct_top_k_scores"`
	Saturation         float64        `json:"saturation"`
	TopScore           int            `json:"top_score"`
	TiedRowCount       int            `json:"tied_row_count"`
	ScoreCounts        map[string]int `json:"score_counts"`
}
type summaryScoringHealth struct {
	ConfiguredMode          string      `json:"configured_mode"`
	SelectionMode           string      `json:"selection_mode"`
	ActualModelSelections   int         `json:"actual_model_selections"`
	DeterministicSelections int         `json:"deterministic_selections"`
	ModelErrorRows          int         `json:"model_error_rows"`
	FallbackOrMixed         bool        `json:"fallback_or_mixed"`
	Cache                   *CacheStats `json:"cache,omitempty"`
}
type summaryCull struct {
	Rows              int            `json:"rows"`
	Kept              int            `json:"kept"`
	Alternates        int            `json:"alternates"`
	Culled            int            `json:"culled"`
	NeedsEvidence     int            `json:"needs_evidence"`
	KeptRate          float64        `json:"kept_rate"`
	AlternateRate     float64        `json:"alternate_rate"`
	CulledRate        float64        `json:"culled_rate"`
	NeedsEvidenceRate float64        `json:"needs_evidence_rate"`
	Counts            map[string]int `json:"counts"`
}

func BuildReportSummary(report Report) reportSummary {
	effectiveRows := report.Rows
	if report.CullLedger != nil {
		effectiveRows = rowsWithCullDispositions(report.Rows)
	}
	summary := reportSummary{Schema: summarySchema, ReportID: report.ReportID, SourceState: report.SourceState, Parameters: report.Parameters, Discovery: report.Discovery, Selection: summarySelection{FilesSeen: report.FilesSeen, FilesScored: report.FilesScored, RowsReported: len(report.Rows), RankedProduction: len(rankedMarkdownRows(report.Rows))}, Confidence: map[string]int{}, Actionability: map[string]int{}, ReviewLanes: reviewLaneNames(report.ReviewPlan), SkippedSignals: append([]string(nil), report.SkippedSignals...), StartHere: summaryStartRows(effectiveRows), RankingHealth: buildSummaryRankingHealth(report.Rows), ScoringHealth: buildSummaryScoringHealth(report)}
	for _, row := range effectiveRows {
		summary.Confidence[cellOrDash(row.Confidence)]++
		summary.Actionability[string(actionabilityForRow(row))]++
	}
	if report.CullLedger != nil {
		summary.Cull = buildSummaryCull(*report.CullLedger)
	}
	return summary
}

func summaryStartRows(rows []FileEvidence) []summaryStartRow {
	selected := rankedMarkdownRows(rows)
	if len(selected) == 0 {
		selected = rows
	}
	if len(selected) > 10 {
		selected = selected[:10]
	}
	out := make([]summaryStartRow, 0, len(selected))
	for index, row := range selected {
		out = append(out, summaryStartRow{Rank: index + 1, ID: row.ID, EvidenceID: row.EvidenceID, Path: row.Path, Score: row.Score, SeedScore: row.SeedScore, Confidence: row.Confidence, Actionability: actionabilityForRow(row), Caveat: row.Caveat, Evidence: append([]string(nil), row.EvidenceLayers...), VerifyCmd: row.VerifyCmd})
	}
	return out
}

func buildSummaryRankingHealth(rows []FileEvidence) summaryRankingHealth {
	health := summaryRankingHealth{TopK: evalTopK, ScoreCounts: map[string]int{}}
	if len(rows) > 0 {
		health.TopScore = rows[0].Score
	}
	scores := make([]int, 0, len(rows))
	for _, row := range rows {
		scores = append(scores, row.Score)
		if row.Score >= 1 && row.Score <= 5 {
			health.ScoreCounts[itoa(row.Score)]++
		}
		if row.Score == health.TopScore {
			health.TiedRowCount++
		}
	}
	top := scoreHealthForTopK(scores, evalTopK)
	health.Slots = top.Slots
	health.DistinctTopKScores = top.DistinctScores
	health.Saturation = top.Saturation
	for score := 1; score <= 5; score++ {
		if _, ok := health.ScoreCounts[itoa(score)]; !ok {
			health.ScoreCounts[itoa(score)] = 0
		}
	}
	return health
}

func buildSummaryScoringHealth(report Report) summaryScoringHealth {
	health := summaryScoringHealth{ConfiguredMode: "deterministic", SelectionMode: "deterministic", Cache: report.CacheStats}
	if report.Model != "" {
		health.ConfiguredMode = "model"
	}
	for _, row := range report.Rows {
		if row.ScoreProvenance.SelectedBy == "model" {
			health.ActualModelSelections++
		} else {
			health.DeterministicSelections++
		}
		if stringSliceContains(row.EvidenceLayers, "model-error") {
			health.ModelErrorRows++
		}
	}
	health.FallbackOrMixed = health.ModelErrorRows > 0 || (health.ActualModelSelections > 0 && health.DeterministicSelections > 0)
	switch {
	case health.ModelErrorRows > 0 && health.ActualModelSelections > 0:
		health.SelectionMode = "mixed_fallback"
	case health.ModelErrorRows > 0:
		health.SelectionMode = "fallback"
	case health.ActualModelSelections > 0 && health.DeterministicSelections > 0:
		health.SelectionMode = "mixed"
	case health.ActualModelSelections > 0:
		health.SelectionMode = "model"
	}
	return health
}

func buildSummaryCull(ledger CullLedger) *summaryCull {
	counts := map[string]int{string(CullDecisionKeptForPremium): ledger.KeptForPremium.Count, string(CullDecisionAlternates): ledger.Alternates.Count, string(CullDecisionGenerated): ledger.Generated.Count, string(CullDecisionDocumentation): ledger.Documentation.Count, string(CullDecisionTestOnly): ledger.TestOnly.Count, string(CullDecisionLowSignal): ledger.LowSignal.Count, string(CullDecisionDuplicate): ledger.Duplicate.Count, string(CullDecisionNeedsEvidence): ledger.NeedsEvidence.Count}
	culled := ledger.Generated.Count + ledger.Documentation.Count + ledger.TestOnly.Count + ledger.LowSignal.Count + ledger.Duplicate.Count
	result := &summaryCull{Rows: ledger.RowsConsidered, Kept: ledger.KeptForPremium.Count, Alternates: ledger.Alternates.Count, Culled: culled, NeedsEvidence: ledger.NeedsEvidence.Count, Counts: counts}
	if result.Rows > 0 {
		n := float64(result.Rows)
		result.KeptRate = float64(result.Kept) / n
		result.AlternateRate = float64(result.Alternates) / n
		result.CulledRate = float64(result.Culled) / n
		result.NeedsEvidenceRate = float64(result.NeedsEvidence) / n
	}
	return result
}

func RenderSummaryJSON(summary reportSummary) ([]byte, error) { return json.Marshal(summary) }

func RenderSummaryMarkdown(summary reportSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Slither Summary\n\n- Report: `%s`\n- Source: `%s`; discovery candidates: `%d`\n- Selection: `%d` rows, `%d` ranked production files\n- Ranking health (top-%d of all reported rows, separated test/doc included): slots `%d`, distinct scores `%d`, saturation `%.2f`\n- Scoring: configured `%s`; actual `%s`; model selections `%d`; deterministic selections `%d`; model-error rows `%d`\n", summary.ReportID, summary.SourceState.Kind, summary.Discovery.CandidateFiles, summary.Selection.RowsReported, summary.Selection.RankedProduction, summary.RankingHealth.TopK, summary.RankingHealth.Slots, summary.RankingHealth.DistinctTopKScores, summary.RankingHealth.Saturation, summary.ScoringHealth.ConfiguredMode, summary.ScoringHealth.SelectionMode, summary.ScoringHealth.ActualModelSelections, summary.ScoringHealth.DeterministicSelections, summary.ScoringHealth.ModelErrorRows)
	if len(summary.ReviewLanes) > 0 {
		fmt.Fprintf(&b, "- Review lanes: `%s`\n", strings.Join(summary.ReviewLanes, "`, `"))
	}
	if summary.Cull != nil {
		fmt.Fprintf(&b, "- Cull: kept `%d` (%.2f), alternates `%d` (%.2f), culled `%d` (%.2f), needs evidence `%d` (%.2f)\n", summary.Cull.Kept, summary.Cull.KeptRate, summary.Cull.Alternates, summary.Cull.AlternateRate, summary.Cull.Culled, summary.Cull.CulledRate, summary.Cull.NeedsEvidence, summary.Cull.NeedsEvidenceRate)
	}
	fmt.Fprint(&b, "\n## Start Here\n\n| rank | file | score | confidence | actionability | evidence | verify |\n| ---: | --- | ---: | --- | --- | --- | --- |\n")
	for _, row := range summary.StartHere {
		fmt.Fprintf(&b, "| %d | %s | %d | %s | %s | %s | %s |\n", row.Rank, markdownCodeCell(row.Path), row.Score, cellOrDash(row.Confidence), row.Actionability, escapeCell(compactList(row.Evidence, 5)), cellOrDash(row.VerifyCmd))
	}
	if len(summary.SkippedSignals) > 0 {
		fmt.Fprintf(&b, "\nSkipped signals: `%s`\n", strings.Join(summary.SkippedSignals, "`, `"))
	}
	return b.String()
}
