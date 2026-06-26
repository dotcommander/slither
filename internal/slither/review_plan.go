package slither

import (
	"path/filepath"
	"slices"
	"strings"
)

type reviewLaneRule struct {
	group  string
	lane   string
	reason string
	match  func(FileEvidence) bool
}

var reviewLaneRules = []reviewLaneRule{
	{group: "user-surface", lane: "cli-ux", reason: "entrypoints, docs, SDK, or output-facing files need command and report checks", match: isUserSurfaceRow},
	{group: "writes-and-persistence", lane: "data-integrity", reason: "migration, store, or stateful artifact signals need integrity checks", match: isDataIntegrityRow},
	{group: "network-and-api-effects", lane: "api-contracts", reason: "API, OpenAPI, CORS, cookie, or serialization signals need contract checks", match: isAPIContractRow},
	{group: "subprocess-and-exit", lane: "error-handling", reason: "subprocess, shell, or process-exit signals need error and timeout checks", match: isErrorHandlingRow},
	{group: "dependency-policy", lane: "dependency-policy", reason: "dependency manifests and replacement policy need review", match: func(row FileEvidence) bool { return row.DependencyHealthRisk > 0 }},
	{group: "secret-and-crypto", lane: "security", reason: "secret, workflow, infrastructure, or credential-like signals need security review", match: isSecurityRow},
	{group: "lifecycle-concurrency", lane: "lifecycle-concurrency", reason: "concurrency, context, or flaky-test signals need lifecycle checks", match: isLifecycleRow},
	{group: "resource-bounds", lane: "performance", reason: "large, hot, or unbounded-resource signals need bounds checks", match: isResourceBoundsRow},
	{group: "test-risk", lane: "test-risk", reason: "test gaps and weak test oracles need coverage checks", match: isTestRiskRow},
	{group: "coupling-hotspots", lane: "coupling", reason: "centrality, cochange, or ownership concentration need blast-radius checks", match: isCouplingRow},
	{group: "ranked-risk-packets", lane: "architecture", reason: "high-ranked multi-layer files need architecture review", match: func(row FileEvidence) bool { return row.Score >= 4 && evidenceIntersectionCount(row) >= 2 }},
}

type reviewLanePlan struct {
	gates  []string
	verify []string
}

var reviewLanePlans = map[string]reviewLanePlan{
	"cli-ux":                {gates: []string{"flag parsing", "help-text accuracy", "exit codes", "output formatting"}, verify: []string{"go build ./..."}},
	"api-contracts":         {gates: []string{"JSON schema stability", "encoder/decoder round-trip", "backward compatibility"}, verify: []string{"go test ./..."}},
	"data-integrity":        {gates: []string{"write atomicity", "read-after-write", "rollback on error"}, verify: []string{"go test ./..."}},
	"error-handling":        {gates: []string{"subprocess timeout", "stderr capture", "exit-code propagation", "error wrapping"}, verify: []string{"go vet ./...", "go test ./..."}},
	"dependency-policy":     {gates: []string{"dependency necessity", "version pinning", "replacement justification"}, verify: []string{"go list -m all"}},
	"security":              {gates: []string{"secret handling", "privilege scope", "credential logging"}},
	"lifecycle-concurrency": {gates: []string{"context propagation", "goroutine ownership", "shutdown cleanup"}, verify: []string{"go test -race ./..."}},
	"performance":           {gates: []string{"bounded reads", "resource limits", "hot path evidence"}, verify: []string{"go test ./..."}},
	"test-risk":             {gates: []string{"nearby coverage", "assertion strength", "flake controls"}, verify: []string{"go test ./..."}},
	"coupling":              {gates: []string{"fan-in blast radius", "cochange partners", "ownership concentration"}},
	"architecture":          {gates: []string{"central dependency blast radius", "layer boundary", "change sequencing"}, verify: []string{"go build ./..."}},
}

var reviewLanePriority = map[string]int{
	"cli-ux":                0,
	"api-contracts":         1,
	"data-integrity":        2,
	"error-handling":        3,
	"dependency-policy":     4,
	"security":              5,
	"lifecycle-concurrency": 6,
	"performance":           7,
	"test-risk":             8,
	"coupling":              9,
	"architecture":          10,
}

func finalizeEvidenceMetadata(row *FileEvidence) {
	row.ID = "slither:file:" + slug(row.Path)
	row.EvidenceClass = evidenceClassForRow(*row)
	row.Confidence = confidenceForRow(*row)
	row.Caveat = caveatForRow(*row)
	row.VerifyCmd = verifyCmdForPath(row.Path)
	if row.Excerpt != "" && int64(len(row.Excerpt)) < row.Bytes {
		row.OmittedReason = "excerpt truncated to report summary"
	}
}

func BuildReviewPlan(rows []FileEvidence) ([]ReviewQueue, []ReviewLane) {
	grouped := map[string]*ReviewQueue{}
	for _, row := range rows {
		for _, rule := range reviewLaneRules {
			if !rule.match(row) {
				continue
			}
			queue := grouped[rule.group]
			if queue == nil {
				queue = &ReviewQueue{
					ID:            "slither:queue:" + slug(rule.group),
					Group:         rule.group,
					Lane:          rule.lane,
					EvidenceClass: evidenceClassForRow(row),
					Confidence:    confidenceForRow(row),
				}
				grouped[rule.group] = queue
			}
			queue.Files = appendUnique(queue.Files, row.Path)
			queue.Reasons = appendUnique(queue.Reasons, rule.reason)
			queue.EvidenceClass = strongestEvidenceClass(queue.EvidenceClass, evidenceClassForRow(row))
			queue.Confidence = strongestConfidence(queue.Confidence, confidenceForRow(row))
			if queue.Caveat == "" {
				queue.Caveat = caveatForRow(row)
			}
		}
	}

	queue := make([]ReviewQueue, 0, len(grouped))
	for _, item := range grouped {
		slices.Sort(item.Files)
		slices.Sort(item.Reasons)
		if total := len(item.Files); total > 12 {
			item.Files = item.Files[:12]
			item.OmittedReason = "showing 12 of " + itoa(total) + " files; truncated by review-plan cap"
		}
		queue = append(queue, *item)
	}
	slices.SortFunc(queue, func(a, b ReviewQueue) int {
		ap, bp := reviewLanePriority[a.Lane], reviewLanePriority[b.Lane]
		if ap != bp {
			return ap - bp
		}
		return strings.Compare(a.Group, b.Group)
	})

	return queue, buildReviewLanes(queue)
}

func buildReviewLanes(queue []ReviewQueue) []ReviewLane {
	type laneAcc struct {
		files         []string
		why           []string
		evidenceClass string
		confidence    string
		caveat        string
	}
	lanes := map[string]*laneAcc{}
	for _, group := range queue {
		acc := lanes[group.Lane]
		if acc == nil {
			acc = &laneAcc{}
			lanes[group.Lane] = acc
		}
		acc.files = append(acc.files, group.Files...)
		acc.why = append(acc.why, group.Reasons...)
		acc.evidenceClass = strongestEvidenceClass(acc.evidenceClass, group.EvidenceClass)
		acc.confidence = strongestConfidence(acc.confidence, group.Confidence)
		if acc.caveat == "" {
			acc.caveat = group.Caveat
		}
	}

	out := make([]ReviewLane, 0, len(lanes))
	for lane, acc := range lanes {
		files := dedupeSorted(acc.files)
		why := dedupeSorted(acc.why)
		omitted := ""
		if total := len(files); total > 12 {
			files = files[:12]
			omitted = "showing 12 of " + itoa(total) + " files; truncated by review-plan cap"
		}
		plan := reviewLanePlans[lane]
		gates := plan.gates
		if gates == nil {
			gates = []string{}
		}
		verify := plan.verify
		if verify == nil {
			verify = []string{}
		}
		out = append(out, ReviewLane{
			ID:            "slither:review:" + slug(lane),
			Lane:          lane,
			Group:         lane,
			EvidenceClass: acc.evidenceClass,
			Confidence:    acc.confidence,
			Files:         files,
			Caveat:        acc.caveat,
			Gates:         gates,
			Verify:        verify,
			Why:           why,
			OmittedReason: omitted,
		})
	}
	slices.SortFunc(out, func(a, b ReviewLane) int {
		ap, bp := reviewLanePriority[a.Lane], reviewLanePriority[b.Lane]
		if ap != bp {
			return ap - bp
		}
		return strings.Compare(a.Lane, b.Lane)
	})
	return out
}

func evidenceClassForRow(row FileEvidence) string {
	switch {
	case stringSliceContains(row.EvidenceLayers, "model"):
		return "model"
	case row.FixTouches > 0 || row.Churn > 0 || row.CochangeRisk > 0 || row.OwnershipRisk > 0 || row.StaleMarkerRisk > 0:
		return "git_history"
	case row.IncomingRefs > 0 || row.CentralityRisk > 0:
		return "import_graph"
	default:
		return "heuristic"
	}
}

func confidenceForRow(row FileEvidence) string {
	switch {
	case stringSliceContains(row.EvidenceLayers, "model-error") || stringSliceContains(row.EvidenceLayers, "low-signal"):
		return "low"
	case isGeneratedOrReportPath(row.Path) || isTestOnlyCull(row) || needsMoreEvidence(row):
		return "low"
	case stringSliceContains(row.EvidenceLayers, "model"):
		return "high"
	case strongDeterministicConfidence(row):
		return "high"
	case keepForPremium(row) || row.Score >= 3 || evidenceIntersectionCount(row) >= 2:
		return "medium"
	default:
		return "low"
	}
}

func strongDeterministicConfidence(row FileEvidence) bool {
	if row.Score < 5 || evidenceIntersectionCount(row) < 3 {
		return false
	}
	return rowHasHighRiskSignal(row) ||
		row.HotspotRisk >= 4 ||
		row.UnknownsRisk >= 5 ||
		row.ContentRisk >= 15 ||
		row.SmellRisk >= 4
}

func caveatForRow(row FileEvidence) string {
	if stringSliceContains(row.EvidenceLayers, "model-error") {
		return "model scoring failed; deterministic fallback evidence retained"
	}
	if needsMoreEvidence(row) {
		return "single-lane or lexical evidence needs corroboration before high-cost review"
	}
	if stringSliceContains(row.EvidenceLayers, "low-signal") {
		return "low-signal row retained for completeness"
	}
	return ""
}

func verifyCmdForPath(path string) string {
	rel := filepath.ToSlash(path)
	switch {
	case rel == "go.mod" || rel == "go.sum":
		return "go list -m all"
	case strings.HasSuffix(rel, ".go"):
		dir := filepath.Dir(rel)
		if dir == "." {
			return "go test ./..."
		}
		return "go test ./" + dir + "/..."
	case strings.HasPrefix(rel, "docs/") || strings.EqualFold(filepath.Base(rel), "README.md"):
		return "go test ./..."
	case strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/"):
		return "go test ./..."
	default:
		return ""
	}
}

func isUserSurfaceRow(row FileEvidence) bool {
	lower := strings.ToLower(filepath.ToSlash(row.Path))
	return strings.HasPrefix(lower, "cmd/") ||
		strings.HasPrefix(lower, "docs/") ||
		strings.Contains(lower, "readme") ||
		row.SDKDXRisk > 0 ||
		strings.Contains(lower, "report") ||
		strings.Contains(lower, "cli")
}

func isDataIntegrityRow(row FileEvidence) bool {
	return row.MigrationSafetyRisk > 0 ||
		row.StaleMarkerRisk > 0 ||
		row.EnvContractRisk > 0 ||
		stringSliceContains(row.EvidenceLayers, "churn") ||
		stringSliceContains(row.EvidenceLayers, "bugfix-history")
}

func isAPIContractRow(row FileEvidence) bool {
	return row.OpenAPIContractRisk > 0 ||
		row.CORSSecurityRisk > 0 ||
		row.CookieSecurityRisk > 0 ||
		row.SDKDXRisk > 0 ||
		strings.Contains(strings.ToLower(row.Path), "schema") ||
		strings.Contains(strings.ToLower(row.Path), "types.")
}

func isErrorHandlingRow(row FileEvidence) bool {
	for _, reason := range row.Reasons {
		if strings.Contains(reason, "shell_boundary") ||
			strings.Contains(reason, "process_exit") ||
			strings.Contains(reason, "error_context_dropped") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(row.Path), "cli") || strings.Contains(strings.ToLower(row.Path), "main.go")
}

func isSecurityRow(row FileEvidence) bool {
	return row.WorkflowSecurityRisk > 0 ||
		row.ContainerBuildRisk > 0 ||
		row.KubernetesSecurityRisk > 0 ||
		row.TerraformSecurityRisk > 0 ||
		row.CORSSecurityRisk > 0 ||
		row.CookieSecurityRisk > 0 ||
		stringSliceContains(row.EvidenceLayers, "secret-risk")
}

func isLifecycleRow(row FileEvidence) bool {
	if row.FlakeRisk > 0 {
		return true
	}
	for _, reason := range row.Reasons {
		if strings.Contains(reason, "background_context") || strings.Contains(reason, "concurrency") {
			return true
		}
	}
	return false
}

func isResourceBoundsRow(row FileEvidence) bool {
	return row.HotspotRisk > 0 ||
		row.Lines >= 300 ||
		strings.Contains(strings.Join(row.Reasons, " "), "read_all_or_global_growth")
}

func isTestRiskRow(row FileEvidence) bool {
	return row.TestGap || row.OracleRisk > 0 || row.FlakeRisk > 0
}

func isCouplingRow(row FileEvidence) bool {
	return row.CentralityRisk > 0 || row.CochangeRisk > 0 || row.OwnershipRisk > 0
}

func strongestEvidenceClass(a, b string) string {
	order := map[string]int{"": 0, "heuristic": 1, "model": 2, "git_history": 3, "import_graph": 4}
	if order[b] > order[a] {
		return b
	}
	return a
}

func strongestConfidence(a, b string) string {
	order := map[string]int{"": 0, "low": 1, "medium": 2, "high": 3}
	if order[b] > order[a] {
		return b
	}
	return a
}

func appendUnique(items []string, item string) []string {
	if item == "" {
		return items
	}
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

func stringSliceContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func dedupeSorted(items []string) []string {
	var out []string
	for _, item := range items {
		out = appendUnique(out, item)
	}
	slices.Sort(out)
	return out
}

func slug(value string) string {
	value = strings.ToLower(filepath.ToSlash(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
