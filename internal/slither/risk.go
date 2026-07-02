package slither

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

//go:embed patterns/triage_patterns.json
var embeddedTriagePatterns []byte

type contentPattern struct {
	ID         string
	Pattern    *regexp2.Regexp
	Weight     int
	MaxMatches int
}

type scoringPatterns struct {
	PathTerms       []fallbackTerm
	ContentPatterns []contentPattern
	Source          string
}



func loadScoringPatterns(path string) (scoringPatterns, error) {
	if path == "" {
		patterns, err := parseScoringPatterns(embeddedTriagePatterns, "embedded:triage_patterns.json")
		if err != nil {
			return scoringPatterns{}, err
		}
		return patterns, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return scoringPatterns{}, fmt.Errorf("load patterns: %w", err)
	}
	patterns, err := parseScoringPatterns(data, path)
	if err != nil {
		return scoringPatterns{}, err
	}
	if abs, err := filepath.Abs(path); err == nil {
		patterns.Source = abs
	}
	return patterns, nil
}

func parseScoringPatterns(data []byte, source string) (scoringPatterns, error) {
	var raw struct {
		PathTerms []struct {
			Term   string `json:"term"`
			Weight int    `json:"weight"`
		} `json:"path_terms"`
		ContentPatterns []struct {
			ID         string `json:"id"`
			Pattern    string `json:"pattern"`
			Weight     int    `json:"weight"`
			MaxMatches int    `json:"max_matches"`
		} `json:"content_patterns"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return scoringPatterns{}, fmt.Errorf("parse patterns: %w", err)
	}
	patterns := scoringPatterns{Source: source}
	for index, item := range raw.PathTerms {
		term := strings.TrimSpace(item.Term)
		if term == "" {
			return scoringPatterns{}, fmt.Errorf("path_terms[%d].term is required", index)
		}
		if item.Weight <= 0 {
			return scoringPatterns{}, fmt.Errorf("path_terms[%d].weight must be positive", index)
		}
		patterns.PathTerms = append(patterns.PathTerms, fallbackTerm{Term: term, Weight: item.Weight})
	}
	for index, item := range raw.ContentPatterns {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = "pattern_" + itoa(index)
		}
		if strings.TrimSpace(item.Pattern) == "" {
			return scoringPatterns{}, fmt.Errorf("content_patterns[%s].pattern is required", id)
		}
		if item.Weight <= 0 {
			return scoringPatterns{}, fmt.Errorf("content_patterns[%s].weight must be positive", id)
		}
		if item.MaxMatches <= 0 {
			return scoringPatterns{}, fmt.Errorf("content_patterns[%s].max_matches must be positive", id)
		}
		compiled, err := regexp2.Compile(item.Pattern, regexp2.None)
		if err != nil {
			return scoringPatterns{}, fmt.Errorf("content_patterns[%s].pattern is invalid: %w", id, err)
		}
		compiled.MatchTimeout = 100 * time.Millisecond
		patterns.ContentPatterns = append(patterns.ContentPatterns, contentPattern{
			ID:         id,
			Pattern:    compiled,
			Weight:     item.Weight,
			MaxMatches: item.MaxMatches,
		})
	}
	if len(patterns.PathTerms) == 0 && len(patterns.ContentPatterns) == 0 {
		return scoringPatterns{}, fmt.Errorf("patterns file must define path_terms or content_patterns: %s", source)
	}
	return patterns, nil
}

func pathRisk(patterns scoringPatterns, rel string) (int, []string) {
	lower := strings.ToLower(rel)
	score := 0
	var reasons []string
	for _, term := range patterns.PathTerms {
		if pathTermMatches(lower, term.Term) {
			score += term.Weight
			reasons = append(reasons, "path:"+term.Term)
		}
	}
	return score, reasons
}

func pathTermMatches(path, term string) bool {
	if strings.Contains(term, ".") {
		return strings.Contains(path, term)
	}
	re := regexp.MustCompile(`(^|[^a-z0-9])` + regexp.QuoteMeta(term) + `([^a-z0-9]|$)`)
	return re.FindStringIndex(path) != nil
}

func contentRisk(patterns scoringPatterns, rel, text string) (int, []string) {
	score, reasons, _ := contentRiskWithLocations(patterns, rel, text)
	return score, reasons
}

func contentRiskWithLocations(patterns scoringPatterns, rel, text string) (int, []string, []EvidenceLocation) {
	if contentPatternSkipped(rel) {
		return 0, nil, nil
	}
	text = textWithoutDetectorLiterals(text)
	score := 0
	var reasons []string
	var locations []EvidenceLocation
	for _, match := range contentPatternMatches(patterns, text) {
		score += match.count * match.pattern.Weight
		reason := "content:" + match.pattern.ID + ":" + itoa(match.count)
		reasons = append(reasons, reason)
		snippet := lineSnippetAt(text, match.index)
		if isSecretPatternID(match.pattern.ID) {
			snippet = "[redacted]"
		}
		locations = append(locations, EvidenceLocation{
			Reason:  reason,
			Line:    lineNumberAt(text, match.index),
			Snippet: snippet,
		})
	}
	return score, reasons, locations
}

type contentPatternMatch struct {
	pattern contentPattern
	count   int
	index   int
}

func contentPatternMatches(patterns scoringPatterns, text string) []contentPatternMatch {
	matches := make([]contentPatternMatch, 0)
	for _, item := range patterns.ContentPatterns {
		count, index, err := countRegexp2Matches(item.Pattern, text, item.MaxMatches)
		if err != nil || count == 0 {
			continue
		}
		matches = append(matches, contentPatternMatch{pattern: item, count: count, index: index})
	}
	return matches
}

func countRegexp2Matches(pattern *regexp2.Regexp, text string, maxMatches int) (int, int, error) {
	match, err := pattern.FindStringMatch(text)
	if err != nil {
		return 0, 0, err
	}
	count := 0
	firstIndex := 0
	for match != nil && count < maxMatches {
		if count == 0 {
			firstIndex = match.Index
		}
		count++
		match, err = pattern.FindNextMatch(match)
		if err != nil {
			return count, firstIndex, err
		}
	}
	return count, firstIndex, nil
}

func lineNumberAt(text string, index int) int {
	if index < 0 {
		return 0
	}
	if index > len(text) {
		index = len(text)
	}
	return strings.Count(text[:index], "\n") + 1
}

func lineSnippetAt(text string, index int) string {
	if index < 0 || index > len(text) {
		return ""
	}
	start := strings.LastIndex(text[:index], "\n") + 1
	end := strings.Index(text[index:], "\n")
	if end < 0 {
		end = len(text)
	} else {
		end += index
	}
	return strings.TrimSpace(text[start:end])
}

func contentPatternSkipped(rel string) bool {
	switch strings.ToLower(filepathExt(rel)) {
	case ".json", ".md", ".toml", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func isSecretPatternID(id string) bool {
	switch id {
	case "hardcoded_private_key", "provider_token_literal", "credential_assignment_literal":
		return true
	default:
		return false
	}
}

func architectureSmellRisk(text, rel string, lines int) (int, int, []string) {
	if !isArchitectureSource(rel) {
		return 0, 0, nil
	}
	imports := len(regexp.MustCompile(`(?m)^\s*(import\s|from\s+\S+\s+import\s|use\s+)`).FindAllStringIndex(text, -1))
	methodPrefixes := map[string]bool{}
	for _, match := range regexp.MustCompile(`\b(validate|format|send|query|fetch|build|parse|render|handle)[A-Z_]`).FindAllStringSubmatch(text, -1) {
		methodPrefixes[strings.ToLower(match[1])] = true
	}
	caseCount := len(regexp.MustCompile(`(?m)^\s*(case\s+|default\s*:)`).FindAllStringIndex(text, -1))
	score := 0
	var reasons []string
	if lines >= 500 && imports >= 15 {
		score += 5
		reasons = append(reasons, "smell:large_import_fanout:"+itoa(imports))
	} else if imports >= 25 {
		score += 4
		reasons = append(reasons, "smell:import_fanout:"+itoa(imports))
	}
	if lines >= 500 && len(methodPrefixes) >= 4 {
		score += 4
		reasons = append(reasons, "smell:mixed_method_prefixes:"+itoa(len(methodPrefixes)))
	}
	if caseCount >= 8 {
		score += 4
		reasons = append(reasons, "smell:case_cascade:"+itoa(caseCount))
	}
	return imports, score, reasons
}

func unknownsRisk(rel, text string) (int, []string) {
	if !isArchitectureSource(rel) {
		return 0, nil
	}
	score := 0
	var reasons []string
	if count := len(regexp.MustCompile(`\bos\.(Getenv|LookupEnv)\s*\(`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "unknowns:env_assumptions:"+itoa(count))
	}
	if count := len(regexp.MustCompile(`http://(?:127\.0\.0\.1|localhost|\[::1\])|:\d{4,5}/`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "unknowns:hardcoded_runtime_endpoint:"+itoa(count))
	}
	if hasNestedLoop(text) {
		score += 2
		reasons = append(reasons, "unknowns:nested_loop_scale:1")
	}
	if hasRecursiveCall(rel, text) {
		score += 3
		reasons = append(reasons, "unknowns:recursive_control_flow:1")
	}
	if count := len(regexp.MustCompile(`\b(NewClient|sql\.Open|http\.Client|regexp\.MustCompile)\s*\(`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "unknowns:resource_factory:"+itoa(count))
	}
	if count := len(regexp.MustCompile(`(?i)\b(utils?|helpers?|common)\b`).FindAllStringIndex(rel, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "unknowns:custom_infra:"+itoa(count))
	}
	return score, reasons
}

func unknownsEvidenceLocations(rel, text string, reasons []string) []EvidenceLocation {
	if len(reasons) == 0 || !isArchitectureSource(rel) {
		return nil
	}
	var locations []EvidenceLocation
	for _, reason := range reasons {
		pattern := unknownsReasonLocationPattern(reason)
		if pattern == nil {
			continue
		}
		match := pattern.FindStringIndex(text)
		if match == nil {
			continue
		}
		locations = append(locations, EvidenceLocation{
			Reason:  reason,
			Line:    lineNumberAt(text, match[0]),
			Snippet: lineSnippetAt(text, match[0]),
		})
	}
	return locations
}

func unknownsReasonLocationPattern(reason string) *regexp.Regexp {
	switch {
	case strings.HasPrefix(reason, "unknowns:env_assumptions:"):
		return regexp.MustCompile(`\bos\.(Getenv|LookupEnv)\s*\(`)
	case strings.HasPrefix(reason, "unknowns:hardcoded_runtime_endpoint:"):
		return regexp.MustCompile(`http://(?:127\.0\.0\.1|localhost|\[::1\])|:\d{4,5}/`)
	case strings.HasPrefix(reason, "unknowns:resource_factory:"):
		return regexp.MustCompile(`\b(NewClient|sql\.Open|http\.Client|regexp\.MustCompile)\s*\(`)
	default:
		return nil
	}
}

func hasNestedLoop(text string) bool {
	loopBodyDepths := []int{}
	depth := 0
	for _, line := range strings.Split(text, "\n") {
		for len(loopBodyDepths) > 0 && depth < loopBodyDepths[len(loopBodyDepths)-1] {
			loopBodyDepths = loopBodyDepths[:len(loopBodyDepths)-1]
		}
		if regexp.MustCompile(`\bfor\b[^{]*\{`).FindStringIndex(line) != nil {
			if len(loopBodyDepths) > 0 {
				return true
			}
			loopBodyDepths = append(loopBodyDepths, depth+1)
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth < 0 {
			depth = 0
		}
		for len(loopBodyDepths) > 0 && depth < loopBodyDepths[len(loopBodyDepths)-1] {
			loopBodyDepths = loopBodyDepths[:len(loopBodyDepths)-1]
		}
	}
	return false
}

func hasRecursiveCall(rel, text string) bool {
	if strings.ToLower(filepathExt(rel)) != ".go" {
		return false
	}
	file, err := parser.ParseFile(token.NewFileSet(), rel, text, 0)
	if err != nil {
		return false
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		receiver := receiverName(fn)
		found := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if found {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				found = fun.Name == fn.Name.Name
			case *ast.SelectorExpr:
				found = receiver != "" && fun.Sel.Name == fn.Name.Name && directReceiverName(fun.X) == receiver
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

func directReceiverName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.ParenExpr:
		return directReceiverName(x.X)
	case *ast.StarExpr:
		return directReceiverName(x.X)
	default:
		return ""
	}
}

func envVarsInCode(rel, text string) []string {
	if !isArchitectureSource(rel) {
		return nil
	}
	seen := map[string]bool{}
	for _, match := range regexp.MustCompile(`\bos\.(?:Getenv|LookupEnv)\(\s*["']([A-Z][A-Z0-9_]{1,})["']`).FindAllStringSubmatch(text, -1) {
		seen[match[1]] = true
	}
	var out []string
	for env := range seen {
		out = append(out, env)
	}
	sortStrings(out)
	return out
}

func envContractRisk(envVars []string, documented map[string]bool, churn, fixTouches, pathScore, contentScore, unknownsScore int) (int, []string) {
	var missing []string
	for _, env := range envVars {
		if !documented[env] {
			missing = append(missing, env)
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}
	riskOverlap := pathScore >= 2 || contentScore >= 4 || unknownsScore >= 4
	changePressure := churn >= 120 || fixTouches > 0
	if len(missing) == 1 && !riskOverlap && !changePressure {
		return 0, nil
	}
	score := min(len(missing)*3, 6)
	reasons := []string{"env_contract:undocumented:" + itoa(len(missing)), "env_contract:vars:" + strings.Join(missing, ",")}
	if riskOverlap {
		score++
		reasons = append(reasons, "env_contract:risk_overlap")
	}
	if changePressure {
		score++
		reasons = append(reasons, "env_contract:change_pressure")
	}
	return score, reasons
}

func centralityRisk(incomingRefs, pathScore, contentScore int) (int, []string) {
	score := 0
	var reasons []string
	switch {
	case incomingRefs >= 10:
		score += 5
	case incomingRefs >= 5:
		score += 4
	case incomingRefs >= 2:
		score += 3
	}
	if score == 0 {
		return 0, nil
	}
	reasons = append(reasons, "centrality:incoming_refs:"+itoa(incomingRefs))
	if pathScore >= 3 || contentScore >= 6 {
		score++
		reasons = append(reasons, "centrality:risk_overlap")
	}
	return score, reasons
}

func cochangeRisk(info cochangeInfo, churn, fixTouches, pathScore, contentScore, centralityScore int) (int, []string) {
	if info.PartnerCount == 0 {
		return 0, nil
	}
	if info.PartnerCount == 1 && info.MaxJaccard < 0.3 {
		return 0, nil
	}
	riskContext := fixTouches > 0 || churn >= 120 || pathScore >= 3 || contentScore >= 6 || centralityScore > 0
	if !riskContext {
		return 0, nil
	}
	score := 0
	var reasons []string
	if info.PartnerCount >= 4 {
		score += 5
	} else if info.PartnerCount >= 2 {
		score += 4
	} else {
		score += 2
	}
	reasons = append(reasons, "cochange:partners:"+itoa(info.PartnerCount))
	if info.MaxJaccard >= 0.5 {
		score += 2
	} else if info.MaxJaccard >= 0.3 {
		score++
	}
	reasons = append(reasons, "cochange:max_jaccard:"+formatFloat(info.MaxJaccard))
	if fixTouches > 0 {
		score += 2
		reasons = append(reasons, "cochange:bugfix_overlap")
	}
	if churn >= 120 {
		score++
		reasons = append(reasons, "cochange:churn_overlap")
	}
	return score, reasons
}

func ownershipRisk(info ownershipInfo, churn, fixTouches, pathScore, contentScore int) (int, []string) {
	if info.Touches == 0 {
		return 0, nil
	}
	reviewPressure := fixTouches > 0 || churn >= 120 || pathScore >= 3 || contentScore >= 8
	if info.Touches < 3 || !reviewPressure {
		return 0, nil
	}
	riskContext := fixTouches > 0 || churn >= 120 || (pathScore >= 3 && contentScore >= 4) || contentScore >= 10
	if !riskContext {
		return 0, nil
	}
	score := 0
	var reasons []string
	if info.AuthorCount == 1 {
		score += 2
		reasons = append(reasons, "ownership:risky_single_author")
	} else if info.AuthorCount == 2 {
		score += 2
		reasons = append(reasons, "ownership:risky_two_authors")
	}
	if info.AuthorCount > 1 && info.TopShare >= 0.8 {
		score += 3
		reasons = append(reasons, "ownership:top_author_share:"+formatFloat(info.TopShare))
	} else if info.AuthorCount > 1 && info.TopShare >= 0.65 {
		score += 2
		reasons = append(reasons, "ownership:top_author_share:"+formatFloat(info.TopShare))
	}
	if info.Touches >= 10 {
		score += 2
		reasons = append(reasons, "ownership:concentrated_touches:"+itoa(info.Touches))
	} else if info.Touches >= 5 {
		score++
		reasons = append(reasons, "ownership:concentrated_touches:"+itoa(info.Touches))
	}
	return score, reasons
}

func staleMarkerRisk(info staleMarkerInfo, churn, fixTouches, pathScore, contentScore, ownershipScore int) (int, []string) {
	if info.StaleCount == 0 {
		return 0, nil
	}
	riskContext := fixTouches > 0 || churn >= 120 || pathScore >= 3 || contentScore >= 6 || ownershipScore > 0
	if !riskContext {
		return 0, nil
	}
	score := min(info.StaleCount*2, 6)
	reasons := []string{"stale_marker:count:" + itoa(info.StaleCount), "stale_marker:oldest_days:" + itoa(info.OldestDays)}
	if info.OldestDays >= 365 {
		score += 2
		reasons = append(reasons, "stale_marker:older_than_year")
	} else if info.OldestDays >= 180 {
		score++
		reasons = append(reasons, "stale_marker:older_than_180d")
	}
	if fixTouches > 0 {
		score++
		reasons = append(reasons, "stale_marker:bugfix_overlap")
	}
	return score, reasons
}

func dependencyHealthRisk(rel, text string) (int, []string) {
	name := strings.ToLower(filepathBase(rel))
	score := 0
	var reasons []string
	switch name {
	case "go.mod":
		if strings.Contains(text, "\nreplace ") || strings.HasPrefix(text, "replace ") {
			score += 4
			reasons = append(reasons, "dependency_health:go_module_replace")
		}
	case "package.json":
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return 2, []string{"dependency_health:unparseable_package_json"}
		}
		directCount := jsonObjectLen(parsed, "dependencies") + jsonObjectLen(parsed, "devDependencies") + jsonObjectLen(parsed, "optionalDependencies")
		if directCount > 20 {
			score += 4
			reasons = append(reasons, "dependency_health:large_dependency_tree:"+itoa(directCount))
		}
		peerCount := jsonObjectLen(parsed, "peerDependencies")
		if peerCount > 0 {
			score += min(peerCount*2, 6)
			reasons = append(reasons, "dependency_health:peer_dependency_surface:"+itoa(peerCount))
		}
		if _, ok := parsed["license"]; !ok {
			score += 2
			reasons = append(reasons, "dependency_health:missing_license")
		}
	case "composer.json":
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return 2, []string{"dependency_health:unparseable_composer_json"}
		}
		directCount := jsonObjectLen(parsed, "require") + jsonObjectLen(parsed, "require-dev")
		if directCount > 20 {
			score += 4
			reasons = append(reasons, "dependency_health:large_dependency_tree:"+itoa(directCount))
		}
		conflictCount := jsonObjectLen(parsed, "conflict")
		if conflictCount > 0 {
			score += min(conflictCount*2, 6)
			reasons = append(reasons, "dependency_health:conflict_constraints:"+itoa(conflictCount))
		}
		if _, ok := parsed["license"]; !ok {
			score += 2
			reasons = append(reasons, "dependency_health:missing_license")
		}
	case "pyproject.toml":
		directCount := countPyprojectDependencies(text)
		if directCount > 20 {
			score += 4
			reasons = append(reasons, "dependency_health:large_dependency_tree:"+itoa(directCount))
		}
		if strings.Contains(text, "[project]") && !regexpMust(`(?m)^license\s*=`).MatchString(text) {
			score += 2
			reasons = append(reasons, "dependency_health:missing_license")
		}
	case "requirements.txt", "requirements.in":
		directCount := countRequirementLines(text)
		if directCount > 20 {
			score += 4
			reasons = append(reasons, "dependency_health:large_dependency_tree:"+itoa(directCount))
		}
	}
	return score, reasons
}

func jsonObjectLen(parsed map[string]any, key string) int {
	items, ok := parsed[key].(map[string]any)
	if !ok {
		return 0
	}
	return len(items)
}

func countRequirementLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		count++
	}
	return count
}

func countPyprojectDependencies(text string) int {
	count := 0
	inDependencyArray := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "dependencies") || strings.Contains(trimmed, "dependencies = [") {
			inDependencyArray = true
		}
		if inDependencyArray && regexpMust(`^["'][A-Za-z0-9_.-]+`).MatchString(trimmed) {
			count++
		}
		if inDependencyArray && strings.Contains(trimmed, "]") {
			inDependencyArray = false
		}
	}
	return count
}

func testFlakeRisk(rel, text string) (int, []string) {
	if !isTestFile(rel) {
		return 0, nil
	}
	score := 0
	var reasons []string
	if count := len(regexp.MustCompile(`\btime\.Sleep\s*\(`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "flake:fixed_wait:"+itoa(count))
	}
	if count := len(regexp.MustCompile(`\b(rand\.|time\.Now\s*\(|httptest\.|http\.Get\s*\()`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "flake:nondeterministic_or_io:"+itoa(count))
	}
	return score, reasons
}

func testOracleRisk(rel, text string, lines int) (int, []string) {
	if !isTestFile(rel) {
		return 0, nil
	}
	testCases := len(regexp.MustCompile(`\bfunc\s+Test\w+|\.Run\s*\(`).FindAllStringIndex(text, -1))
	assertions := len(regexp.MustCompile(`\b(t\.Fatal|t\.Error|t\.Fail|if\s+.*!=|if\s+.*==|assert\.|require\.)`).FindAllStringIndex(text, -1))
	if testCases >= 3 && assertions == 0 {
		return 6, []string{"oracle:no_assertions"}
	}
	if lines >= 200 && assertions < 3 {
		return 4, []string{"oracle:large_weak_test"}
	}
	return 0, nil
}

func branchComplexity(text, rel string) int {
	if !isArchitectureSource(rel) {
		return 0
	}
	return len(regexp.MustCompile(`\b(if|else\s+if|for|while|case|catch|except|switch|select|match)\b|&&|\|\||\?`).FindAllStringIndex(text, -1))
}

func hotspotRisk(complexity, churn, fixTouches, incomingRefs int) (int, []string) {
	score := 0
	var reasons []string
	if complexity >= 40 && (fixTouches > 0 || churn >= 120 || incomingRefs >= 5) {
		score += 4
		reasons = append(reasons, "hotspot:complexity_with_pressure:"+itoa(complexity))
	}
	if complexity >= 25 && fixTouches > 0 {
		score += 2
		reasons = append(reasons, "hotspot:bugfix_complexity")
	}
	if complexity >= 60 {
		score += 3
		reasons = append(reasons, "hotspot:high_complexity_static:"+itoa(complexity))
	}
	return score, reasons
}

func seedScore(row FileEvidence) float64 {
	artifactPenalty := 0.0
	if isGeneratedOrReportPath(row.Path) {
		artifactPenalty = 1.25
	} else if isTestOnlyCull(row) {
		artifactPenalty = 0.75
	}
	sizeComponent := math.Min(float64(row.Lines)/300.0, 5)
	churnComponent := math.Min(float64(row.Churn)/120.0, 5)
	fixComponent := math.Min(float64(row.FixTouches), 5)
	markerComponent := math.Min(float64(row.Markers), 5)
	riskComponent := math.Min(float64(row.PathRisk)/3.0, 5)
	contentComponent := math.Min(float64(row.ContentRisk)/3.0, 5)
	smellComponent := math.Min(float64(row.SmellRisk)/3.0, 5)
	hotspotComponent := math.Min(float64(row.HotspotRisk)/3.0, 5)
	sdkComponent := math.Min(float64(row.SDKDXRisk)/3.0, 5)
	unknownsComponent := math.Min(float64(row.UnknownsRisk)/3.0, 5)
	envComponent := math.Min(float64(row.EnvContractRisk)/3.0, 5)
	workflowComponent := math.Min(float64(row.WorkflowSecurityRisk)/3.0, 5)
	migrationComponent := math.Min(float64(row.MigrationSafetyRisk)/3.0, 5)
	containerComponent := math.Min(float64(row.ContainerBuildRisk)/3.0, 5)
	kubernetesComponent := math.Min(float64(row.KubernetesSecurityRisk)/3.0, 5)
	terraformComponent := math.Min(float64(row.TerraformSecurityRisk)/3.0, 5)
	openAPIComponent := math.Min(float64(row.OpenAPIContractRisk)/3.0, 5)
	corsComponent := math.Min(float64(row.CORSSecurityRisk)/3.0, 5)
	cookieComponent := math.Min(float64(row.CookieSecurityRisk)/3.0, 5)
	dependencyComponent := math.Min(float64(row.DependencyHealthRisk)/3.0, 5)
	centralityComponent := math.Min(float64(row.CentralityRisk)/3.0, 5)
	cochangeComponent := math.Min(float64(row.CochangeRisk)/3.0, 5)
	ownershipComponent := math.Min(float64(row.OwnershipRisk)/3.0, 5)
	flakeComponent := math.Min(float64(row.FlakeRisk)/3.0, 5)
	oracleComponent := math.Min(float64(row.OracleRisk)/3.0, 5)
	staleMarkerComponent := math.Min(float64(row.StaleMarkerRisk)/3.0, 5)
	layerComponent := math.Min(float64(len(row.EvidenceLayers)), 5)
	total := 0.18*churnComponent +
		0.15*fixComponent +
		0.18*riskComponent +
		0.18*contentComponent +
		0.08*smellComponent +
		0.05*hotspotComponent +
		0.04*sdkComponent +
		0.02*unknownsComponent +
		0.04*envComponent +
		0.05*workflowComponent +
		0.05*migrationComponent +
		0.04*containerComponent +
		0.05*kubernetesComponent +
		0.05*terraformComponent +
		0.04*openAPIComponent +
		0.04*corsComponent +
		0.04*cookieComponent +
		0.03*dependencyComponent +
		0.05*centralityComponent +
		0.05*cochangeComponent +
		0.04*ownershipComponent +
		0.04*flakeComponent +
		0.04*oracleComponent +
		0.04*staleMarkerComponent +
		0.10*sizeComponent +
		0.05*markerComponent +
		0.02*layerComponent
	return math.Round(math.Max(total-artifactPenalty, 0)*100) / 100
}

func qualitativeScore(row FileEvidence) int {
	maxComponent := 0
	for _, value := range []int{
		row.PathRisk,
		row.ContentRisk,
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
	} {
		if value > maxComponent {
			maxComponent = value
		}
	}
	score := int(math.Ceil(float64(maxComponent) / 3.0))
	if len(row.EvidenceLayers) >= 2 {
		score++
	}
	if score < 1 {
		return 1
	}
	if score > 5 {
		return 5
	}
	return score
}
