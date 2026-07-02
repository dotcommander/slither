package slither

import (
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxPremiumCullSeeds = 24

	cullReasonGenerated       = "generated, report, minified, or derived artifact"
	cullReasonTestOnly        = "test or fixture separated from the production premium queue"
	cullReasonDocumentation   = "documentation or guide separated from the production premium queue"
	cullReasonDuplicatePrefix = "same evidence surface represented by stronger row "
	cullReasonNeedsEvidence   = "lexical or single-lane evidence needs corroboration"
	cullReasonAlternate       = "plausible next premium target if budget remains"
	cullReasonLowSignal       = "low score or weak evidence intersection"
	cullReasonPremiumKept     = "strong multi-layer seed"
	cullReasonPremiumOverflow = "strong seed beyond kept_for_premium cap"
)

type cullCandidate struct {
	index int
	row   FileEvidence
}

type cullDisposition struct {
	Decision CullDecision
	Reason   string
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
		Documentation:  CullBucket{Examples: []CullEntry{}},
		TestOnly:       CullBucket{Examples: []CullEntry{}},
		LowSignal:      CullBucket{Examples: []CullEntry{}},
		Duplicate:      CullBucket{Examples: []CullEntry{}},
		NeedsEvidence:  CullBucket{Examples: []CullEntry{}},
	}
	dispositions := classifyCullDispositions(report.Rows)
	for i, disposition := range dispositions {
		if disposition.Decision == CullDecisionKeptForPremium || disposition.Reason == cullReasonPremiumOverflow {
			continue
		}
		addCullDisposition(&ledger, disposition.Decision, cullEntry(report.Rows[i], disposition.Decision, disposition.Reason))
	}
	addPremiumDispositionEntries(&ledger, report.Rows, dispositions)
	return ledger
}

func classifyCullDispositions(rows []FileEvidence) []cullDisposition {
	dispositions := make([]cullDisposition, len(rows))
	seenSurfaces := map[string]string{}
	var premiumCandidates []cullCandidate
	for i, row := range rows {
		surfaceKey := cullSurfaceKey(row)
		duplicateOf, isDuplicate := seenSurfaces[surfaceKey]
		switch {
		case isGeneratedOrReportPath(row.Path):
			dispositions[i] = cullDisposition{Decision: CullDecisionGenerated, Reason: cullReasonGenerated}
		case isTestPath(row.Path):
			dispositions[i] = cullDisposition{Decision: CullDecisionTestOnly, Reason: cullReasonTestOnly}
		case isDocumentationPath(row.Path):
			dispositions[i] = cullDisposition{Decision: CullDecisionDocumentation, Reason: cullReasonDocumentation}
		case isDuplicate && row.Score < 4:
			dispositions[i] = cullDisposition{Decision: CullDecisionDuplicate, Reason: cullReasonDuplicatePrefix + duplicateOf}
		case keepForPremium(row):
			premiumCandidates = append(premiumCandidates, cullCandidate{index: i, row: row})
			seenSurfaces[surfaceKey] = row.Path
		case needsMoreEvidence(row):
			dispositions[i] = cullDisposition{Decision: CullDecisionNeedsEvidence, Reason: cullReasonNeedsEvidence}
		case row.Score >= 3:
			dispositions[i] = cullDisposition{Decision: CullDecisionAlternates, Reason: cullReasonAlternate}
			seenSurfaces[surfaceKey] = row.Path
		default:
			dispositions[i] = cullDisposition{Decision: CullDecisionLowSignal, Reason: cullReasonLowSignal}
		}
	}
	applyPremiumCandidateCap(dispositions, premiumCandidates)
	return dispositions
}

func applyPremiumCandidateCap(dispositions []cullDisposition, candidates []cullCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return premiumCandidateLess(candidates[i].row, candidates[j].row)
	})
	for i, candidate := range candidates {
		if i < maxPremiumCullSeeds {
			dispositions[candidate.index] = cullDisposition{Decision: CullDecisionKeptForPremium, Reason: cullReasonPremiumKept}
			continue
		}
		dispositions[candidate.index] = cullDisposition{Decision: CullDecisionAlternates, Reason: cullReasonPremiumOverflow}
	}
}

func addPremiumDispositionEntries(ledger *CullLedger, rows []FileEvidence, dispositions []cullDisposition) {
	var candidates []cullCandidate
	for i, disposition := range dispositions {
		if disposition.Decision != CullDecisionKeptForPremium && disposition.Reason != cullReasonPremiumOverflow {
			continue
		}
		candidates = append(candidates, cullCandidate{index: i, row: rows[i]})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return premiumCandidateLess(candidates[i].row, candidates[j].row)
	})
	for _, candidate := range candidates {
		disposition := dispositions[candidate.index]
		addCullDisposition(ledger, disposition.Decision, cullEntry(candidate.row, disposition.Decision, disposition.Reason))
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

func addCullDisposition(ledger *CullLedger, decision CullDecision, entry CullEntry) {
	switch decision {
	case CullDecisionKeptForPremium:
		addCullEntry(&ledger.KeptForPremium, entry, 0)
	case CullDecisionAlternates:
		addCullEntry(&ledger.Alternates, entry, 3)
	case CullDecisionGenerated:
		addCullEntry(&ledger.Generated, entry, 3)
	case CullDecisionDocumentation:
		addCullEntry(&ledger.Documentation, entry, 3)
	case CullDecisionTestOnly:
		addCullEntry(&ledger.TestOnly, entry, 3)
	case CullDecisionDuplicate:
		addCullEntry(&ledger.Duplicate, entry, 3)
	case CullDecisionNeedsEvidence:
		addCullEntry(&ledger.NeedsEvidence, entry, 3)
	default:
		addCullEntry(&ledger.LowSignal, entry, 3)
	}
}

func rowsWithCullDispositions(rows []FileEvidence) []FileEvidence {
	out := make([]FileEvidence, len(rows))
	copy(out, rows)
	for i, disposition := range classifyCullDispositions(rows) {
		out[i].CullDecision = disposition.Decision
		out[i].CullReason = disposition.Reason
		out[i].Actionability = actionabilityForCullDisposition(out[i], disposition.Decision)
	}
	return out
}

func actionabilityForCullDisposition(row FileEvidence, decision CullDecision) Actionability {
	switch decision {
	case CullDecisionKeptForPremium, CullDecisionAlternates:
		return actionabilityForRow(row)
	default:
		return ActionabilityVerifyFirst
	}
}

func addCullEntry(bucket *CullBucket, entry CullEntry, maxExamples int) {
	bucket.Count++
	if maxExamples <= 0 || len(bucket.Examples) < maxExamples {
		bucket.Examples = append(bucket.Examples, entry)
	}
}

func cullEntry(row FileEvidence, decision CullDecision, reason string) CullEntry {
	return CullEntry{
		Path:                          row.Path,
		Score:                         row.Score,
		EvidenceClass:                 row.EvidenceClass,
		Confidence:                    row.Confidence,
		Actionability:                 actionabilityForCullDisposition(row, decision),
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
		strings.HasSuffix(lower, ".gitignore") ||
		strings.HasSuffix(lower, "/triage_patterns.json") ||
		lower == "triage_patterns.json" ||
		strings.HasPrefix(lower, ".work/") ||
		strings.HasPrefix(lower, "prototypes/") ||
		strings.HasPrefix(lower, "stubs/") ||
		strings.Contains(lower, "/web-cache-") ||
		(strings.HasPrefix(lower, "data/") && strings.HasSuffix(lower, ".html") && strings.Contains(lower, "/cache")) ||
		(strings.HasPrefix(lower, "docs/") && strings.HasSuffix(lower, ".html")) ||
		(strings.HasPrefix(lower, "docs/") && strings.HasSuffix(lower, ".json")) ||
		(strings.HasPrefix(lower, "docs/") && strings.Contains(lower, "report")) ||
		(strings.HasPrefix(lower, "docs/") && strings.Contains(lower, "scoreboard")) ||
		(strings.HasPrefix(lower, "docs/") && strings.Contains(lower, "benchmark")) ||
		strings.Contains(lower, "slither-cull") ||
		strings.Contains(lower, "slither-report") ||
		strings.Contains(lower, "triage-report") ||
		strings.Contains(lower, "/reports/")
}

func isTestOnlyCull(row FileEvidence) bool {
	if !isTestPath(row.Path) {
		return false
	}
	return row.FlakeRisk == 0 && row.OracleRisk == 0 && !rowHasHighRiskSignal(row)
}

func isTestPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(lower, "/test/") ||
		strings.Contains(lower, "/tests/") ||
		strings.Contains(lower, "/fixtures/") ||
		strings.Contains(lower, "/testdata/") ||
		strings.Contains(lower, "testdata") ||
		strings.Contains(lower, "fixture") ||
		strings.HasPrefix(lower, "test/") ||
		strings.HasPrefix(lower, "tests/") ||
		strings.HasPrefix(lower, "fixtures/") ||
		strings.HasPrefix(lower, "testdata/") ||
		strings.HasSuffix(lower, "_test.go") ||
		strings.HasSuffix(lower, ".test.ts") ||
		strings.HasSuffix(lower, ".test.js") ||
		strings.HasSuffix(lower, ".spec.ts") ||
		strings.HasSuffix(lower, ".spec.js")
}

func cullSurfaceKey(row FileEvidence) string {
	path := filepath.ToSlash(row.Path)
	dir := filepath.Dir(path)
	layers := "none"
	if len(row.EvidenceLayers) > 0 {
		limit := min(len(row.EvidenceLayers), 2)
		layers = strings.Join(row.EvidenceLayers[:limit], "+")
	}
	if pathReason := firstReasonWithPrefix(row.Reasons, "path:"); pathReason != "" {
		layers += "+" + pathReason
	}
	if dir == "." {
		ext := filepath.Ext(path)
		if ext == "" {
			ext = filepath.Base(path)
		}
		return dir + "|" + ext + "|" + layers
	}
	return dir + "|" + layers
}

func firstReasonWithPrefix(reasons []string, prefix string) string {
	for _, reason := range reasons {
		if strings.HasPrefix(reason, prefix) {
			return reason
		}
	}
	return ""
}
