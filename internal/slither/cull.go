package slither

import (
	"path/filepath"
	"sort"
	"strings"
)

const maxPremiumCullSeeds = 24

type cullCandidate struct {
	row   FileEvidence
	entry CullEntry
}

func BuildCullLedger(report Report) CullLedger {
	firstReadQueue := report.FirstReadQueue
	reviewPlan := report.ReviewPlan
	if len(firstReadQueue) == 0 && len(reviewPlan) == 0 {
		firstReadQueue, reviewPlan = BuildReviewPlan(report.Rows)
	}
	ledger := CullLedger{
		RunLabel:       "slither_cull_ledger",
		Repo:           report.Repo,
		GeneratedAt:    report.GeneratedAt,
		RowsConsidered: len(report.Rows),
		StopReason:     "deterministic cull complete; premium review should start with kept_for_premium",
		SkippedSignals: report.SkippedSignals,
		FirstReadQueue: firstReadQueue,
		ReviewPlan:     reviewPlan,
		KeptForPremium: CullBucket{Examples: []CullEntry{}},
		Alternates:     CullBucket{Examples: []CullEntry{}},
		Generated:      CullBucket{Examples: []CullEntry{}},
		TestOnly:       CullBucket{Examples: []CullEntry{}},
		LowSignal:      CullBucket{Examples: []CullEntry{}},
		Duplicate:      CullBucket{Examples: []CullEntry{}},
		NeedsEvidence:  CullBucket{Examples: []CullEntry{}},
	}
	seenSurfaces := map[string]string{}
	var premiumCandidates []cullCandidate
	for _, row := range report.Rows {
		entry := cullEntry(row, "")
		surfaceKey := cullSurfaceKey(row)
		duplicateOf, isDuplicate := seenSurfaces[surfaceKey]
		switch {
		case isGeneratedOrReportPath(row.Path):
			entry.Reason = "generated, report, minified, or derived artifact"
			addCullEntry(&ledger.Generated, entry, 3)
		case isTestOnlyCull(row):
			entry.Reason = "test or fixture without release, reliability, or security evidence"
			addCullEntry(&ledger.TestOnly, entry, 3)
		case isDuplicate && row.Score < 4:
			entry.Reason = "same evidence surface represented by stronger row " + duplicateOf
			addCullEntry(&ledger.Duplicate, entry, 3)
		case keepForPremium(row):
			premiumCandidates = append(premiumCandidates, cullCandidate{row: row, entry: entry})
			seenSurfaces[surfaceKey] = row.Path
		case needsMoreEvidence(row):
			entry.Reason = "lexical or single-lane evidence needs corroboration"
			addCullEntry(&ledger.NeedsEvidence, entry, 3)
		case row.Score >= 3:
			entry.Reason = "plausible next premium target if budget remains"
			addCullEntry(&ledger.Alternates, entry, 3)
			seenSurfaces[surfaceKey] = row.Path
		default:
			entry.Reason = "low score or weak evidence intersection"
			addCullEntry(&ledger.LowSignal, entry, 3)
		}
	}
	addPremiumCandidates(&ledger, premiumCandidates)
	return ledger
}

func addPremiumCandidates(ledger *CullLedger, candidates []cullCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return premiumCandidateLess(candidates[i].row, candidates[j].row)
	})
	for i, candidate := range candidates {
		if i < maxPremiumCullSeeds {
			candidate.entry.Reason = "strong multi-layer seed"
			addCullEntry(&ledger.KeptForPremium, candidate.entry, 0)
			continue
		}
		candidate.entry.Reason = "strong seed beyond kept_for_premium cap"
		addCullEntry(&ledger.Alternates, candidate.entry, 3)
	}
}

func premiumCandidateLess(a, b FileEvidence) bool {
	for _, cmp := range []func(FileEvidence) float64{
		func(row FileEvidence) float64 { return float64(confidenceRank(effectiveConfidence(row))) },
		func(row FileEvidence) float64 { return float64(row.Score) },
		func(row FileEvidence) float64 { return row.SeedScore },
		func(row FileEvidence) float64 { return float64(evidenceIntersectionCount(row)) },
		func(row FileEvidence) float64 { return float64(rowHighRiskRank(row)) },
	} {
		left := cmp(a)
		right := cmp(b)
		if left != right {
			return left > right
		}
	}
	return a.Path < b.Path
}

func effectiveConfidence(row FileEvidence) string {
	if row.Confidence != "" {
		return row.Confidence
	}
	return confidenceForRow(row)
}

func confidenceRank(confidence string) int {
	switch confidence {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func rowHighRiskRank(row FileEvidence) int {
	if rowHasHighRiskSignal(row) {
		return 1
	}
	return 0
}

func addCullEntry(bucket *CullBucket, entry CullEntry, maxExamples int) {
	bucket.Count++
	if maxExamples <= 0 || len(bucket.Examples) < maxExamples {
		bucket.Examples = append(bucket.Examples, entry)
	}
}

func cullEntry(row FileEvidence, reason string) CullEntry {
	return CullEntry{
		Path:                          row.Path,
		Score:                         row.Score,
		EvidenceClass:                 row.EvidenceClass,
		Confidence:                    row.Confidence,
		Caveat:                        row.Caveat,
		VerifyCmd:                     row.VerifyCmd,
		EvidenceLayers:                row.EvidenceLayers,
		StrongestEvidenceIntersection: strongestEvidenceIntersection(row),
		Reason:                        reason,
	}
}

func keepForPremium(row FileEvidence) bool {
	if row.Score < 4 {
		return false
	}
	return evidenceIntersectionCount(row) >= 2 || rowHasHighRiskSignal(row)
}

func needsMoreEvidence(row FileEvidence) bool {
	if row.Score < 3 {
		return false
	}
	if len(row.EvidenceLayers) <= 1 {
		return true
	}
	for _, layer := range row.EvidenceLayers {
		if !isLexicalLayer(layer) {
			return false
		}
	}
	return true
}

func evidenceIntersectionCount(row FileEvidence) int {
	count := 0
	for _, layer := range row.EvidenceLayers {
		if !isLexicalLayer(layer) && layer != "low-signal" {
			count++
		}
	}
	if row.PathRisk > 0 {
		count++
	}
	if row.ContentRisk > 0 {
		count++
	}
	return count
}

func strongestEvidenceIntersection(row FileEvidence) string {
	var layers []string
	for _, layer := range row.EvidenceLayers {
		if layer == "low-signal" {
			continue
		}
		layers = append(layers, layer)
		if len(layers) == 3 {
			break
		}
	}
	if len(layers) == 0 {
		return "unknown"
	}
	return strings.Join(layers, " + ")
}

func rowHasHighRiskSignal(row FileEvidence) bool {
	return row.WorkflowSecurityRisk > 0 ||
		row.MigrationSafetyRisk > 0 ||
		row.ContainerBuildRisk > 0 ||
		row.KubernetesSecurityRisk > 0 ||
		row.TerraformSecurityRisk > 0 ||
		row.OpenAPIContractRisk > 0 ||
		row.CORSSecurityRisk > 0 ||
		row.CookieSecurityRisk > 0 ||
		row.DependencyHealthRisk > 0 ||
		row.FlakeRisk > 0 ||
		row.OracleRisk > 0 ||
		row.StaleMarkerRisk > 0
}

func isLexicalLayer(layer string) bool {
	switch layer {
	case "path-risk", "content-risk", "work-marker", "secret-risk":
		return true
	default:
		return false
	}
}

func isGeneratedOrReportPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(lower, "generated") ||
		strings.Contains(lower, "/gen/") ||
		strings.Contains(lower, ".gen.") ||
		strings.Contains(lower, ".generated.") ||
		strings.HasSuffix(lower, ".pb.go") ||
		strings.HasSuffix(lower, ".min.js") ||
		strings.HasSuffix(lower, ".bundle.js") ||
		(strings.HasPrefix(lower, "docs/") && strings.HasSuffix(lower, ".html")) ||
		(strings.HasPrefix(lower, "docs/") && strings.HasSuffix(lower, ".json")) ||
		(strings.HasPrefix(lower, "docs/") && strings.Contains(lower, "report")) ||
		(strings.HasPrefix(lower, "docs/") && strings.Contains(lower, "scoreboard")) ||
		(strings.HasPrefix(lower, "docs/") && strings.Contains(lower, "benchmark")) ||
		strings.Contains(lower, "slither-report") ||
		strings.Contains(lower, "triage-report") ||
		strings.Contains(lower, "/reports/")
}

func isTestOnlyCull(row FileEvidence) bool {
	lower := strings.ToLower(filepath.ToSlash(row.Path))
	isTestPath := strings.Contains(lower, "/test/") ||
		strings.Contains(lower, "/tests/") ||
		strings.Contains(lower, "/fixtures/") ||
		strings.Contains(lower, "/testdata/") ||
		strings.HasPrefix(lower, "test/") ||
		strings.HasPrefix(lower, "tests/") ||
		strings.HasPrefix(lower, "fixtures/") ||
		strings.HasPrefix(lower, "testdata/") ||
		strings.HasSuffix(lower, "_test.go") ||
		strings.HasSuffix(lower, ".test.ts") ||
		strings.HasSuffix(lower, ".test.js") ||
		strings.HasSuffix(lower, ".spec.ts") ||
		strings.HasSuffix(lower, ".spec.js")
	if !isTestPath {
		return false
	}
	return row.FlakeRisk == 0 && row.OracleRisk == 0 && !rowHasHighRiskSignal(row)
}

func cullSurfaceKey(row FileEvidence) string {
	dir := filepath.Dir(filepath.ToSlash(row.Path))
	layers := "none"
	if len(row.EvidenceLayers) > 0 {
		limit := min(len(row.EvidenceLayers), 2)
		layers = strings.Join(row.EvidenceLayers[:limit], "+")
	}
	return dir + "|" + layers
}
