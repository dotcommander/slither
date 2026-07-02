package slither

import (
	"encoding/json"
	"os"
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

func finalizeEvidenceMetadata(repo string, row *FileEvidence) {
	row.ID = "slither:file:" + slug(row.Path)
	row.EvidenceClass = evidenceClassForRow(*row)
	row.Confidence = confidenceForRow(*row)
	row.Actionability = actionabilityForRow(*row)
	row.Caveat = caveatForRow(*row)
	row.VerifyCmd = verifyCmdForPathInRepo(repo, row.Path)
	if row.Excerpt != "" && int64(len(row.Excerpt)) < row.Bytes {
		row.OmittedReason = "excerpt truncated to report summary"
	}
}

func BuildReviewPlan(rows []FileEvidence) ([]ReviewQueue, []ReviewLane) {
	return BuildReviewPlanForRepo("", rows)
}

func BuildReviewPlanForRepo(repo string, rows []FileEvidence) ([]ReviewQueue, []ReviewLane) {
	grouped := map[string]*ReviewQueue{}
	rowByPath := map[string]FileEvidence{}
	rankByPath := map[string]int{}
	for _, row := range rows {
		if _, ok := rowByPath[row.Path]; !ok {
			rowByPath[row.Path] = row
			rankByPath[row.Path] = len(rankByPath)
		}
		if !includeInReviewPlan(row) {
			continue
		}
		for _, rule := range reviewLaneRules {
			if !reviewRuleAppliesToRow(rule, row) {
				continue
			}
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
		sortReviewFiles(item.Files, item.Lane, rowByPath, rankByPath)
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

	return queue, buildReviewLanes(repo, queue, rowByPath)
}

func BuildDataIntegrityInventoryForRepo(repo string, rows []FileEvidence) ([]ReviewQueue, []ReviewLane) {
	rowByPath := map[string]FileEvidence{}
	rankByPath := map[string]int{}
	queue := ReviewQueue{
		ID:    "slither:queue:writes-and-persistence",
		Group: "writes-and-persistence",
		Lane:  "data-integrity",
		Reasons: []string{
			"migration, store, or stateful artifact signals need integrity checks",
		},
	}
	for _, row := range rows {
		if !isDataIntegrityRow(row) {
			continue
		}
		if _, ok := rowByPath[row.Path]; !ok {
			rowByPath[row.Path] = row
			rankByPath[row.Path] = len(rankByPath)
		}
		queue.Files = appendUnique(queue.Files, row.Path)
		queue.EvidenceClass = strongestEvidenceClass(queue.EvidenceClass, evidenceClassForRow(row))
		queue.Confidence = strongestConfidence(queue.Confidence, confidenceForRow(row))
		if queue.Caveat == "" {
			queue.Caveat = caveatForRow(row)
		}
	}
	if len(queue.Files) == 0 {
		return nil, nil
	}
	sortReviewFiles(queue.Files, queue.Lane, rowByPath, rankByPath)
	if total := len(queue.Files); total > 12 {
		queue.Files = queue.Files[:12]
		queue.OmittedReason = "showing 12 of " + itoa(total) + " files; truncated by review-plan cap"
	}
	queue.Reasons = dedupeSorted(queue.Reasons)
	queues := []ReviewQueue{queue}
	return queues, buildReviewLanes(repo, queues, rowByPath)
}

func sortReviewFiles(files []string, lane string, rowByPath map[string]FileEvidence, rankByPath map[string]int) {
	slices.SortFunc(files, func(a, b string) int {
		left := reviewLaneFilePriority(lane, rowByPath[a])
		right := reviewLaneFilePriority(lane, rowByPath[b])
		if left != right {
			return right - left
		}
		return rankByPath[a] - rankByPath[b]
	})
}

func reviewLaneFilePriority(lane string, row FileEvidence) int {
	switch lane {
	case "api-contracts":
		return row.OpenAPIContractRisk*10 + row.CORSSecurityRisk*8 + row.CookieSecurityRisk*8 + row.SDKDXRisk*4
	case "data-integrity":
		return row.MigrationSafetyRisk*10 + row.StaleMarkerRisk*6 + row.EnvContractRisk*4 + row.PathRisk
	case "error-handling":
		return reasonCount(row, "shell_boundary")*6 + reasonCount(row, "process_exit")*6 + reasonCount(row, "error_context_dropped")*4
	case "lifecycle-concurrency":
		return row.FlakeRisk*6 + reasonCount(row, "background_context")*4 + reasonCount(row, "concurrent")*4 // contract-tested: see TestReviewReasonNeedlesMatchLivePatternCatalog
	case "performance":
		return row.HotspotRisk*4 + row.Lines/100 + reasonCount(row, "read_all_or_global_growth")*4
	case "test-risk":
		return row.FlakeRisk*6 + row.OracleRisk*6 + boolScore(row.TestGap)*4
	case "coupling":
		return row.CentralityRisk*6 + row.CochangeRisk*4 + row.OwnershipRisk*3
	default:
		return 0
	}
}

func reasonCount(row FileEvidence, needle string) int {
	count := 0
	for _, reason := range row.Reasons {
		if strings.Contains(reason, needle) {
			count++
		}
	}
	return count
}

func boolScore(value bool) int {
	if value {
		return 1
	}
	return 0
}

func actionabilityForRow(row FileEvidence) Actionability {
	if row.Actionability != "" {
		return row.Actionability
	}
	if isGeneratedOrReportPath(row.Path) ||
		isDocumentationPath(row.Path) ||
		isTestOnlyCull(row) ||
		isDetectorFixtureRow(row) ||
		stringSliceContains(row.EvidenceLayers, "low-signal") ||
		stringSliceContains(row.EvidenceLayers, "model-error") ||
		(needsMoreEvidence(row) && !keepForPremium(row)) {
		return ActionabilityVerifyFirst
	}
	if row.DependencyHealthRisk > 0 {
		return ActionabilityDependencyReview
	}
	if likelyDefectRow(row) {
		return ActionabilityLikelyDefect
	}
	if highRiskInspectRow(row) {
		return ActionabilityHighRiskInspect
	}
	if row.Score < 3 {
		if rowHasHotspotActionabilitySignal(row) {
			return ActionabilityHotspot
		}
		return ActionabilityVerifyFirst
	}
	if keepForPremium(row) || row.Score >= 4 || evidenceIntersectionCount(row) >= 2 {
		return ActionabilityInspect
	}
	if rowHasHotspotActionabilitySignal(row) {
		return ActionabilityHotspot
	}
	return ActionabilityVerifyFirst
}

func likelyDefectRow(row FileEvidence) bool {
	if row.Score < 4 || evidenceIntersectionCount(row) < 2 {
		return false
	}
	for _, reason := range row.Reasons {
		if highRiskContentReason(reason) {
			return true
		}
	}
	return false
}

func highRiskInspectRow(row FileEvidence) bool {
	return row.Score >= 4 && evidenceIntersectionCount(row) >= 2 && rowHasHighRiskInspectSignal(row)
}

func rowHasHotspotActionabilitySignal(row FileEvidence) bool {
	return row.HotspotRisk > 0 || row.CentralityRisk > 0 || row.CochangeRisk > 0 || row.OwnershipRisk > 0 || row.SmellRisk > 0
}

func rowHasHighRiskInspectSignal(row FileEvidence) bool {
	return row.WorkflowSecurityRisk > 0 ||
		row.MigrationSafetyRisk > 0 ||
		row.ContainerBuildRisk > 0 ||
		row.KubernetesSecurityRisk > 0 ||
		row.TerraformSecurityRisk > 0 ||
		row.OpenAPIContractRisk > 0 ||
		row.CORSSecurityRisk > 0 ||
		row.CookieSecurityRisk > 0 ||
		row.FlakeRisk > 0 ||
		row.OracleRisk > 0 ||
		row.StaleMarkerRisk > 0
}

func highRiskContentReason(reason string) bool {
	for _, prefix := range []string{
		"content:open_redirect_",
		"content:csrf_",
		"content:idor_",
		"content:mass_assignment_",
		"content:nosql_",
		"content:ssrf_",
		"content:unsafe_",
		"content:xxe_",
		"content:prototype_pollution_",
		"content:graphql_schema_",
		"content:websocket_allow_all_",
		"content:path_traversal_",
		"content:upload_user_filename_",
		"content:hardcoded_private_key",
		"content:provider_token_literal",
		"content:credential_assignment_literal",
	} {
		if strings.HasPrefix(reason, prefix) {
			return true
		}
	}
	return false
}

func includeInReviewPlan(row FileEvidence) bool {
	if isGeneratedOrReportPath(row.Path) || isDocumentationPath(row.Path) {
		return false
	}
	if isTestPath(row.Path) {
		return isTestRiskRow(row)
	}
	if stringSliceContains(row.EvidenceLayers, "low-signal") {
		return false
	}
	if needsMoreEvidence(row) && !keepForPremium(row) {
		return false
	}
	return keepForPremium(row) || row.Score >= 3
}

func reviewRuleAppliesToRow(rule reviewLaneRule, row FileEvidence) bool {
	if isTestPath(row.Path) {
		return rule.group == "test-risk"
	}
	return true
}

func buildReviewLanes(repo string, queue []ReviewQueue, rowByPath map[string]FileEvidence) []ReviewLane {
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
		files := dedupePreserveOrder(acc.files)
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
		verify := reviewLaneDefaultVerify(repo, lane, plan.verify)
		if verify == nil {
			verify = []string{}
		}
		verify = reviewLaneVerifyCommands(verify, files, rowByPath)
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

func reviewLaneVerifyCommands(base []string, files []string, rowByPath map[string]FileEvidence) []string {
	out := append([]string{}, base...)
	for _, file := range files {
		row := rowByPath[file]
		if row.VerifyCmd == "" {
			continue
		}
		out = appendUnique(out, row.VerifyCmd)
	}
	return out
}

func reviewLaneDefaultVerify(repo, lane string, defaults []string) []string {
	if repo == "" {
		return defaults
	}
	profile := verificationProfileForRepo(repo)
	if profile == "" || profile == "go" {
		return defaults
	}
	out := make([]string, 0, len(defaults))
	for _, cmd := range defaults {
		out = appendUnique(out, translateGenericVerifyCmd(repo, profile, cmd))
	}
	return out
}

func translateGenericVerifyCmd(repo, profile, cmd string) string {
	switch profile {
	case "php":
		switch cmd {
		case "go test ./...", "go test -race ./...":
			if composerHasScript(repo, "test") {
				return "composer test"
			}
			if repoFileExists(repo, "phpunit.xml") {
				return "vendor/bin/phpunit"
			}
			return "composer validate --strict"
		case "go build ./...":
			if packageHasScript(repo, "build") {
				return "npm run build"
			}
			return "composer validate --strict"
		case "go vet ./...", "go list -m all":
			return "composer validate --strict"
		default:
			return cmd
		}
	case "node":
		switch cmd {
		case "go test ./...", "go test -race ./...", "go build ./...", "go vet ./...", "go list -m all":
			if packageHasScript(repo, "test") {
				return "npm test"
			}
			if packageHasScript(repo, "build") {
				return "npm run build"
			}
		}
	}
	return cmd
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
	if isDetectorFixtureRow(row) {
		return "test file contains multiple detector-like snippets; treat content/security hits as fixture or coverage evidence before product risk"
	}
	if stringSliceContains(row.EvidenceLayers, "model-error") {
		return "model scoring failed; deterministic fallback evidence retained"
	}
	if needsMoreEvidence(row) && !keepForPremium(row) {
		return "single-lane or lexical evidence needs corroboration before high-cost review"
	}
	if stringSliceContains(row.EvidenceLayers, "low-signal") {
		return "low-signal row retained for completeness"
	}
	return ""
}

func isDetectorFixtureRow(row FileEvidence) bool {
	return isTestPath(row.Path) && contentDetectorReasonCount(row) >= 3
}

func contentDetectorReasonCount(row FileEvidence) int {
	count := 0
	for _, reason := range row.Reasons {
		if strings.HasPrefix(reason, "content:") {
			count++
		}
	}
	return count
}

func verifyCmdForPath(path string) string {
	return verifyCmdForPathInRepo("", path)
}

func verifyCmdForPathInRepo(repo, path string) string {
	rel := filepath.ToSlash(path)
	if isGeneratedOrReportPath(rel) {
		return ""
	}
	if cmd := commandDocsVerifyCmd(repo, rel); cmd != "" {
		return cmd
	}
	if cmd := phpVerifyCmd(repo, rel); cmd != "" {
		return cmd
	}
	switch {
	case isEnvExamplePath(rel):
		return translateGenericVerifyCmd(repo, verificationProfileForRepo(repo), "go test ./...")
	case rel == "go.mod" || rel == "go.sum":
		return "go list -m all"
	case strings.HasSuffix(rel, ".go"):
		return goVerifyCmdForPath(repo, rel)
	case isShellScriptPath(rel):
		return "bash -n " + rel
	case isRepoConfigPath(rel) && verificationProfileForRepo(repo) == "go":
		return goVerifyCmdForPath(repo, rel)
	case strings.HasPrefix(rel, "docs/") || strings.EqualFold(filepath.Base(rel), "README.md"):
		return translateGenericVerifyCmd(repo, verificationProfileForRepo(repo), "go test ./...")
	case strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/"):
		return "go test ./..."
	case strings.HasSuffix(strings.ToLower(rel), ".sql"):
		return "psql \"$TEST_DATABASE_URL\" -v ON_ERROR_STOP=1 -f " + rel
	case isJavaScriptSourcePath(rel):
		return jsVerifyCmd(repo, rel)
	case rel == "package.json" || rel == "package-lock.json":
		if packageHasScript(repo, "build") {
			return "npm run build"
		}
	default:
		return ""
	}
	return ""
}

func isEnvExamplePath(rel string) bool {
	name := strings.ToLower(filepath.Base(filepath.ToSlash(rel)))
	return name == ".env" || strings.HasPrefix(name, ".env.") || strings.HasSuffix(name, ".env") || strings.Contains(name, "env.example")
}

func phpVerifyCmd(repo, rel string) string {
	if rel == "composer.json" || rel == "composer.lock" {
		if repoFileExists(repo, "composer.json") {
			return "composer validate --strict"
		}
	}
	if strings.HasSuffix(strings.ToLower(rel), ".php") {
		return "php -l " + rel
	}
	return ""
}

func goVerifyCmdForPath(repo, rel string) string {
	dir := nearestGoPackageDir(repo, rel)
	if dir == "." {
		return "go test ./..."
	}
	return "go test ./" + dir + "/..."
}

func nearestGoPackageDir(repo, rel string) string {
	dir := filepath.Dir(filepath.ToSlash(rel))
	if dir == "." {
		return "."
	}
	if repo == "" {
		return dir
	}
	for {
		if directoryHasGoFiles(repo, dir) {
			return dir
		}
		if dir == "." {
			return "."
		}
		dir = filepath.Dir(dir)
	}
}

func directoryHasGoFiles(repo, relDir string) bool {
	entries, err := os.ReadDir(filepath.Join(repo, relDir))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".go") {
			return true
		}
	}
	return false
}

func isShellScriptPath(rel string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	return strings.HasSuffix(lower, ".sh") || strings.HasPrefix(lower, "scripts/") || strings.HasPrefix(lower, "runs/")
}

func isRepoConfigPath(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".yaml", ".yml", ".toml", ".json":
		return true
	default:
		return false
	}
}

func commandDocsVerifyCmd(repo, rel string) string {
	if repo == "" {
		return ""
	}
	if rel != "docs/commands.md" && !strings.Contains(rel, "docs_refresh_commands") {
		return ""
	}
	if _, err := os.Stat(filepath.Join(repo, "cmd", "mytree", "docs_refresh_commands.go")); err != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(repo, "cmd", "mytree", "main.go")); err != nil {
		return ""
	}
	return "go test ./cmd/mytree/... && go build -o mytree ./cmd/mytree && ./mytree docs refresh-commands --check"
}

func isJavaScriptSourcePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".js", ".jsx", ".ts", ".tsx", ".svelte":
		return true
	default:
		return false
	}
}

func jsVerifyCmd(repo, rel string) string {
	if cmd := packageVerifyCmd(repo, rel); cmd != "" {
		return cmd
	}
	return "bun build --no-bundle --outfile /tmp/slither-bun-check.js " + rel
}

func verificationProfileForRepo(repo string) string {
	switch {
	case repoFileExists(repo, "composer.json"):
		return "php"
	case repoFileExists(repo, "go.mod"):
		return "go"
	case repoFileExists(repo, "package.json"):
		return "node"
	default:
		return ""
	}
}

func repoFileExists(repo, rel string) bool {
	if repo == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(repo, rel))
	return err == nil
}

func composerHasScript(repo, name string) bool {
	if repo == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(repo, "composer.json"))
	if err != nil {
		return false
	}
	var composer struct {
		Scripts map[string]any `json:"scripts"`
	}
	if err := json.Unmarshal(data, &composer); err != nil {
		return false
	}
	_, ok := composer.Scripts[name]
	return ok
}

func packageHasScript(repo, name string) bool {
	if repo == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false
	}
	return pkg.Scripts[name] != ""
}

func packageVerifyCmd(repo, rel string) string {
	if repo == "" {
		return ""
	}
	pkgDir, scripts := nearestPackageScripts(repo, rel)
	if pkgDir == "" {
		return ""
	}
	script := preferredPackageScript(scripts)
	if script == "" {
		return ""
	}
	displayDir := filepath.ToSlash(pkgDir)
	if displayDir == "." {
		if packageUsesBun(filepath.Join(repo, pkgDir)) {
			return "bun run " + script
		}
		return "npm run " + script
	}
	if packageUsesBun(filepath.Join(repo, pkgDir)) {
		return "bun --cwd " + displayDir + " run " + script
	}
	return "npm --prefix " + displayDir + " run " + script
}

func nearestPackageScripts(repo, rel string) (string, map[string]string) {
	dir := filepath.Dir(filepath.ToSlash(rel))
	if dir == "." {
		dir = ""
	}
	for {
		pkgPath := filepath.Join(repo, filepath.FromSlash(dir), "package.json")
		data, err := os.ReadFile(pkgPath)
		if err == nil {
			var pkg struct {
				Scripts map[string]string `json:"scripts"`
			}
			if json.Unmarshal(data, &pkg) == nil && len(pkg.Scripts) > 0 {
				if dir == "" {
					return ".", pkg.Scripts
				}
				return dir, pkg.Scripts
			}
		}
		if dir == "" || dir == "." {
			return "", nil
		}
		dir = filepath.Dir(dir)
		if dir == "." {
			dir = ""
		}
	}
}

func preferredPackageScript(scripts map[string]string) string {
	for _, name := range []string{"typecheck", "check", "test", "build"} {
		if _, ok := scripts[name]; ok {
			return name
		}
	}
	return ""
}

func packageUsesBun(absDir string) bool {
	if _, err := os.Stat(filepath.Join(absDir, "bun.lock")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(absDir, "bun.lockb")); err == nil {
		return true
	}
	return false
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
	lower := strings.ToLower(filepath.ToSlash(row.Path))
	return row.MigrationSafetyRisk > 0 ||
		row.StaleMarkerRisk > 0 ||
		row.EnvContractRisk > 0 ||
		strings.Contains(lower, "database") ||
		strings.Contains(lower, "repository") ||
		strings.Contains(lower, "migration") ||
		strings.Contains(lower, "storage") ||
		strings.Contains(lower, "cache")
}

func isAPIContractRow(row FileEvidence) bool {
	lower := strings.ToLower(filepath.ToSlash(row.Path))
	return row.OpenAPIContractRisk > 0 ||
		row.CORSSecurityRisk > 0 ||
		row.CookieSecurityRisk > 0 ||
		row.SDKDXRisk > 0 ||
		strings.Contains(lower, "openapi") ||
		strings.Contains(lower, "swagger") ||
		((strings.Contains(lower, "/api/") || strings.Contains(lower, "/web/") || strings.Contains(lower, "handler")) &&
			(strings.Contains(lower, "schema") || strings.Contains(lower, "types.")))
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
		if strings.Contains(reason, "background_context") || strings.Contains(reason, "concurrent") { // contract-tested: see TestReviewReasonNeedlesMatchLivePatternCatalog
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

func dedupePreserveOrder(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
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
