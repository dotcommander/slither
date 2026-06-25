package slither

import (
	"path/filepath"
	"strings"
)

func BuildCullLedger(report Report) CullLedger {
	ledger := CullLedger{
		RunLabel:       "slither_cull_ledger",
		Repo:           report.Repo,
		GeneratedAt:    report.GeneratedAt,
		RowsConsidered: len(report.Rows),
		StopReason:     "deterministic cull complete; premium review should start with kept_for_premium",
		SkippedSignals: report.SkippedSignals,
		KeptForPremium: CullBucket{Examples: []CullEntry{}},
		Alternates:     CullBucket{Examples: []CullEntry{}},
		Generated:      CullBucket{Examples: []CullEntry{}},
		TestOnly:       CullBucket{Examples: []CullEntry{}},
		LowSignal:      CullBucket{Examples: []CullEntry{}},
		Duplicate:      CullBucket{Examples: []CullEntry{}},
		NeedsEvidence:  CullBucket{Examples: []CullEntry{}},
	}
	seenSurfaces := map[string]string{}
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
			entry.Reason = "strong multi-layer seed"
			addCullEntry(&ledger.KeptForPremium, entry, 0)
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
	return ledger
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
		row.CochangeRisk > 0 ||
		row.OwnershipRisk > 0 ||
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
