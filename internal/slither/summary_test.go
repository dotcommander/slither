package slither

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// TestSummaryRankingHealthLabelsAllRowsBasis reproduces the observed summary
// confusion: when separated test rows dominate the top score band, ranking
// health reports a single distinct score (near-total saturation) while the
// Start Here production queue still discriminates across several scores. The
// rendered line must name its basis so the two no longer appear to contradict.
func TestSummaryRankingHealthLabelsAllRowsBasis(t *testing.T) {
	t.Parallel()

	rows := make([]FileEvidence, 0, 17)
	rows = append(rows, FileEvidence{
		Path:           "internal/storage/store.go",
		Score:          5,
		Confidence:     "high",
		ContentRisk:    12,
		EvidenceLayers: []string{"content-risk", "hotspot"},
		Reasons:        []string{"content:stateful_store"},
	})
	for i := 0; i < 14; i++ {
		rows = append(rows, FileEvidence{
			Path:           "internal/storage/store_case" + itoa(i) + "_test.go",
			Score:          5,
			Confidence:     "high",
			ContentRisk:    12,
			FlakeRisk:      5,
			EvidenceLayers: []string{"content-risk", "flake-risk", "hotspot"},
			Reasons:        []string{"flake:nondeterministic_or_io:2"},
		})
	}
	rows = append(rows,
		FileEvidence{
			Path:           "internal/api/handler.go",
			Score:          4,
			Confidence:     "medium",
			ContentRisk:    6,
			EvidenceLayers: []string{"content-risk", "churn"},
			Reasons:        []string{"content:api_contract_boundary"},
		},
		FileEvidence{
			Path:           "internal/api/util.go",
			Score:          3,
			Confidence:     "medium",
			Churn:          9,
			EvidenceLayers: []string{"churn"},
			Reasons:        []string{"churn:9"},
		},
	)
	report := Report{Repo: "/repo", FilesSeen: len(rows), Rows: rows}

	summary := BuildReportSummary(report)

	// Independent oracle from docs/usage.md: distinct scores among the first
	// 15 rows in report order, saturation = 1 - distinct/slots.
	distinct := map[int]struct{}{}
	for _, row := range rows[:15] {
		distinct[row.Score] = struct{}{}
	}
	wantDistinct := len(distinct)
	wantSaturation := 1 - float64(wantDistinct)/15
	if summary.RankingHealth.Slots != 15 || summary.RankingHealth.DistinctTopKScores != wantDistinct {
		t.Fatalf("ranking health = slots %d distinct %d, want 15/%d", summary.RankingHealth.Slots, summary.RankingHealth.DistinctTopKScores, wantDistinct)
	}
	if math.Abs(summary.RankingHealth.Saturation-wantSaturation) > 1e-9 {
		t.Fatalf("saturation = %v, want %v", summary.RankingHealth.Saturation, wantSaturation)
	}

	// The fixture's discriminating property: the Start Here production queue
	// spans more distinct scores than the all-rows top band.
	startDistinct := map[int]struct{}{}
	for _, row := range summary.StartHere {
		startDistinct[row.Score] = struct{}{}
	}
	if len(startDistinct) <= wantDistinct {
		t.Fatalf("fixture lost its discriminating property: start-here distinct %d <= all-rows top-15 distinct %d", len(startDistinct), wantDistinct)
	}

	markdown := RenderSummaryMarkdown(summary)
	wantLine := "Ranking health (top-15 of all reported rows, separated test/doc included): slots `15`, distinct scores `" + itoa(wantDistinct) + "`, saturation `" + fmt.Sprintf("%.2f", wantSaturation) + "`"
	if !strings.Contains(markdown, wantLine) {
		t.Fatalf("summary ranking health line missing all-rows basis label or values:\nwant substring: %s\nmarkdown:\n%s", wantLine, markdown)
	}
	if strings.Contains(markdown, "- Ranking health: top-") {
		t.Fatalf("stale unlabeled ranking health line still rendered:\n%s", markdown)
	}
}
