package slither

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxGitHistoryBytes int64 = 16 << 20

var errGitOutputLimit = errors.New("git output exceeded limit")

type scoreContext struct {
	files        []string
	churn        map[string]churnStats
	fixTouches   map[string]int
	cochange     map[string]cochangeInfo
	ownership    map[string]ownershipInfo
	staleMarkers map[string]staleMarkerInfo
	incomingRefs map[string]int
	// packageLevelRefs marks files whose incoming refs were attributed by
	// the ownerless-package fallback: the count is real package fan-in, but
	// the file itself was chosen alphabetically, not as a resolved hub.
	packageLevelRefs map[string]bool
	localImports     []localImportEdge
	documentedEnv    map[string]bool
	patterns         scoringPatterns
	skipped          []string
}

func newScoreContext(ctx context.Context, repo string, files []string, maxBytes int64, days int, asOf time.Time, patterns scoringPatterns) scoreContext {
	incomingRefs, localImports, packageLevelRefs := localImportGraph(repo, files, maxBytes)
	scoreCtx := scoreContext{
		files:            files,
		fixTouches:       map[string]int{},
		incomingRefs:     incomingRefs,
		packageLevelRefs: packageLevelRefs,
		localImports:     localImports,
		documentedEnv:    documentedEnvVars(repo, files, maxBytes),
		patterns:         patterns,
	}
	historyWindow := gitHistoryWindow(days, asOf)
	churn, skip := churnStatsByFile(ctx, repo, historyWindow)
	scoreCtx.churn = churn
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "churn:"+skip)
	}
	fixTouches, skip := bugfixTouchesByFile(ctx, repo, historyWindow)
	scoreCtx.fixTouches = fixTouches
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "bugfix_density:"+skip)
	}
	cochange, skip := cochangeByFile(ctx, repo, historyWindow)
	scoreCtx.cochange = cochange
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "cochange:"+skip)
	}
	ownership, skip := ownershipByFile(ctx, repo, historyWindow)
	scoreCtx.ownership = ownership
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "ownership:"+skip)
	}
	staleMarkers, skip := staleMarkersByFile(ctx, repo, files, maxBytes, asOf)
	scoreCtx.staleMarkers = staleMarkers
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "stale_markers:"+skip)
	}
	return scoreCtx
}

// localImportEdge retains the already-resolved local edge used for centrality.
// It is report-internal context only: scoring continues to consume incomingRefs.
type localImportEdge struct {
	Importer string
	Imported string
	Kind     string
}

type cochangeInfo struct {
	PartnerCount int
	MaxJaccard   float64
}

type ownershipInfo struct {
	AuthorCount int
	Touches     int
	TopShare    float64
}

type staleMarkerInfo struct {
	StaleCount int
	OldestDays int
}

type gitHistoryBounds struct {
	Since string
	Until string
}

func gitHistoryWindow(days int, asOf time.Time) gitHistoryBounds {
	asOf = asOf.UTC()
	return gitHistoryBounds{
		Since: asOf.AddDate(0, 0, -days).Format(time.RFC3339),
		Until: asOf.Format(time.RFC3339),
	}
}

func (bounds gitHistoryBounds) args() []string {
	return []string{"--since=" + bounds.Since, "--until=" + bounds.Until}
}

// churnStats decomposes per-file churn so review pressure can be separated
// from file size: Total is raw numstat additions+deletions over the window,
// AfterCreate excludes the churn contributed by the file's creation commit
// (equal to Total when the creation happened before the window or cannot be
// identified), and Touches counts commits that touched the file.
type churnStats struct {
	Total       int
	AfterCreate int
	Touches     int
}

func churnStatsByFile(ctx context.Context, repo string, bounds gitHistoryBounds) (map[string]churnStats, string) {
	creation := fileCreationCommits(ctx, repo)
	args := append(bounds.args(), "--numstat", "--format=%H")
	out, err := runGitOutput(ctx, repo, append([]string{"log"}, args...)...)
	if err != nil {
		return map[string]churnStats{}, gitSkipReason(err)
	}
	if out == "" {
		return map[string]churnStats{}, "no recent git history"
	}
	stats := map[string]churnStats{}
	commit := ""
	for _, line := range strings.Split(out, "\n") {
		if hash, ok := commitHashLine(line); ok {
			commit = hash
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || parts[0] == "-" || parts[1] == "-" {
			continue
		}
		added, errA := strconv.Atoi(parts[0])
		deleted, errD := strconv.Atoi(parts[1])
		if errA != nil || errD != nil {
			continue
		}
		path := numstatPath(parts[2])
		s := stats[path]
		s.Total += added + deleted
		s.Touches++
		if commit != "" && commit == creation[path] {
			// Creation churn stays in Total but never counts as pressure.
		} else {
			s.AfterCreate += added + deleted
		}
		stats[path] = s
	}
	return stats, ""
}

// fileCreationCommits maps each path to the newest commit that added it over
// full history. Files whose creation cannot be identified (renames, files
// predating detection) are absent; their churn then counts entirely as
// post-creation pressure, which is the conservative direction for review.
func fileCreationCommits(ctx context.Context, repo string) map[string]string {
	out, err := runGitOutput(ctx, repo, "log", "--diff-filter=A", "--name-only", "--format=%H")
	if err != nil || out == "" {
		return map[string]string{}
	}
	creation := map[string]string{}
	commit := ""
	for _, line := range strings.Split(out, "\n") {
		if hash, ok := commitHashLine(line); ok {
			commit = hash
			continue
		}
		path := strings.TrimSpace(line)
		if path == "" || commit == "" {
			continue
		}
		if _, seen := creation[path]; !seen {
			creation[path] = commit
		}
	}
	return creation
}

func commitHashLine(line string) (string, bool) {
	if len(line) < 40 || len(line) > 64 {
		return "", false
	}
	for _, r := range line {
		if !isHexDigit(r) {
			return "", false
		}
	}
	return line, true
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

func numstatPath(field string) string {
	if !strings.Contains(field, " => ") && !strings.Contains(field, "{") {
		// Plain path without rename.
		return field
	}
	// Simple rename: "old/path => new/path"
	if strings.Contains(field, " => ") && !strings.Contains(field, "{") {
		return strings.TrimSpace(field[strings.LastIndex(field, " => ")+4:])
	}
	// Brace/partial rename: "prefix/{old => new}/suffix"
	beforeBrace := field[:strings.Index(field, "{")]
	afterBrace := field[strings.Index(field, "}")+1:]
	inside := field[strings.Index(field, "{")+1 : strings.Index(field, "}")]
	parts := strings.SplitN(inside, " => ", 2)
	right := ""
	if len(parts) == 2 {
		right = strings.TrimSpace(parts[1])
	}
	result := beforeBrace + right + afterBrace
	// Collapse any doubled slashes from an empty segment.
	return strings.ReplaceAll(result, "//", "/")
}

// bugfixSubjectPattern matches bug-fix subjects on word boundaries so words
// merely containing a keyword ("prefixes", "fixtures", "suffix") never count.
var bugfixSubjectPattern = regexp.MustCompile(`(?i)\b(fix(es|ed|ing)?|bug(s)?|bugfix(es)?|hotfix(es)?|regressions?|crash(es|ed|ing)?|panic(s|ked)?|broken)\b`)

func bugfixTouchesByFile(ctx context.Context, repo string, bounds gitHistoryBounds) (map[string]int, string) {
	args := append(bounds.args(), "--format=format:__SLITHER_SUBJECT__%s", "--name-only")
	out, err := runGitOutput(ctx, repo, append([]string{"log"}, args...)...)
	if err != nil {
		return map[string]int{}, gitSkipReason(err)
	}
	if out == "" {
		return map[string]int{}, "no recent git history"
	}
	commits := 0
	touches := map[string]int{}
	isFix := false
	for _, line := range strings.Split(out, "\n") {
		if subject, ok := strings.CutPrefix(line, "__SLITHER_SUBJECT__"); ok {
			commits++
			isFix = bugfixSubjectPattern.MatchString(subject)
			continue
		}
		rel := strings.TrimSpace(line)
		if rel == "" || !isFix {
			continue
		}
		touches[rel]++
	}
	if commits < 30 {
		return map[string]int{}, "insufficient commit count:" + itoa(commits)
	}
	return touches, ""
}

func ownershipByFile(ctx context.Context, repo string, bounds gitHistoryBounds) (map[string]ownershipInfo, string) {
	args := append(bounds.args(), "--format=format:__SLITHER_AUTHOR__%ae", "--name-only")
	out, err := runGitOutput(ctx, repo, append([]string{"log"}, args...)...)
	if err != nil {
		return map[string]ownershipInfo{}, gitSkipReason(err)
	}
	if out == "" {
		return map[string]ownershipInfo{}, "no recent git history"
	}
	authorsByFile := map[string]map[string]int{}
	currentAuthor := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "__SLITHER_AUTHOR__") {
			currentAuthor = strings.TrimSpace(strings.TrimPrefix(line, "__SLITHER_AUTHOR__"))
			if currentAuthor == "" {
				currentAuthor = "unknown"
			}
			continue
		}
		rel := strings.TrimSpace(line)
		if rel == "" || currentAuthor == "" {
			continue
		}
		if authorsByFile[rel] == nil {
			authorsByFile[rel] = map[string]int{}
		}
		authorsByFile[rel][currentAuthor]++
	}
	if len(authorsByFile) == 0 {
		return map[string]ownershipInfo{}, "no file author history"
	}
	ownership := map[string]ownershipInfo{}
	for rel, authors := range authorsByFile {
		touches := 0
		topTouches := 0
		for _, count := range authors {
			touches += count
			if count > topTouches {
				topTouches = count
			}
		}
		if touches > 0 {
			ownership[rel] = ownershipInfo{AuthorCount: len(authors), Touches: touches, TopShare: float64(topTouches) / float64(touches)}
		}
	}
	return ownership, ""
}

func cochangeByFile(ctx context.Context, repo string, bounds gitHistoryBounds) (map[string]cochangeInfo, string) {
	args := append(bounds.args(), "--format=format:__SLITHER_COMMIT__", "--name-only")
	out, err := runGitOutput(ctx, repo, append([]string{"log"}, args...)...)
	if err != nil {
		return map[string]cochangeInfo{}, gitSkipReason(err)
	}
	if out == "" {
		return map[string]cochangeInfo{}, "no recent git history"
	}
	fileCommits := map[string]int{}
	pairCounts := map[[2]string]int{}
	currentFiles := []string{}
	commitsUsed := 0
	flush := func(files []string) {
		unique := uniqueIncludedFiles(files)
		if len(unique) < 2 || len(unique) > 15 {
			return
		}
		commitsUsed++
		for _, rel := range unique {
			fileCommits[rel]++
		}
		for i, rel := range unique {
			for _, other := range unique[i+1:] {
				pairCounts[[2]string{rel, other}]++
			}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "__SLITHER_COMMIT__") {
			flush(currentFiles)
			currentFiles = nil
			continue
		}
		if rel := strings.TrimSpace(line); rel != "" {
			currentFiles = append(currentFiles, rel)
		}
	}
	flush(currentFiles)
	if commitsUsed == 0 {
		return map[string]cochangeInfo{}, "no bounded multi-file commits"
	}
	edges := map[string][]float64{}
	for pair, count := range pairCounts {
		if count < 2 {
			continue
		}
		a, b := pair[0], pair[1]
		denominator := fileCommits[a] + fileCommits[b] - count
		if denominator <= 0 {
			continue
		}
		jaccard := float64(count) / float64(denominator)
		if jaccard < 0.2 {
			continue
		}
		edges[a] = append(edges[a], jaccard)
		edges[b] = append(edges[b], jaccard)
	}
	cochange := map[string]cochangeInfo{}
	for rel, weights := range edges {
		maxWeight := 0.0
		for _, weight := range weights {
			if weight > maxWeight {
				maxWeight = weight
			}
		}
		cochange[rel] = cochangeInfo{PartnerCount: len(weights), MaxJaccard: maxWeight}
	}
	return cochange, ""
}

func uniqueIncludedFiles(files []string) []string {
	seen := map[string]bool{}
	for _, rel := range files {
		rel = strings.TrimSpace(rel)
		lower := strings.ToLower(rel)
		if rel == "" || strings.HasPrefix(lower, ".github/") || strings.HasPrefix(lower, ".vscode/") {
			continue
		}
		if shouldSkip(rel) {
			continue
		}
		seen[rel] = true
	}
	var unique []string
	for rel := range seen {
		unique = append(unique, rel)
	}
	sort.Strings(unique)
	return unique
}

func staleMarkersByFile(ctx context.Context, repo string, files []string, maxBytes int64, asOf time.Time) (map[string]staleMarkerInfo, string) {
	markers := [][2]string{}
	for _, path := range files {
		if len(markers) >= 200 {
			break
		}
		rel, ok := relPath(repo, path)
		if !ok || shouldSkip(rel) || !isArchitectureSource(rel) {
			continue
		}
		text, ok, _ := readTextPrefix(path, maxBytes)
		if !ok {
			continue
		}
		for lineNumber, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if !(strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*")) {
				continue
			}
			if regexpMust(`\b(TODO|FIXME|HACK|XXX)\b`).FindStringIndex(trimmed) != nil {
				markers = append(markers, [2]string{rel, itoa(lineNumber + 1)})
			}
		}
	}
	if len(markers) == 0 {
		return map[string]staleMarkerInfo{}, ""
	}
	today := asOf
	stale := map[string]staleMarkerInfo{}
	blameFailures := 0
	blameFailureReason := ""
	for _, marker := range markers {
		rel, line := marker[0], marker[1]
		out, err := runGitOutput(ctx, repo, "blame", "--line-porcelain", "-L", line+","+line, "--", rel)
		if err != nil {
			blameFailures++
			if blameFailureReason == "" {
				blameFailureReason = gitSkipReason(err)
			}
			continue
		}
		if out == "" {
			blameFailures++
			continue
		}
		match := regexpMust(`(?m)^author-time\s+(\d+)$`).FindStringSubmatch(out)
		if len(match) < 2 {
			blameFailures++
			continue
		}
		seconds, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			blameFailures++
			continue
		}
		ageDays := int(today.Sub(time.Unix(seconds, 0)).Hours() / 24)
		if ageDays < 180 {
			continue
		}
		info := stale[rel]
		info.StaleCount++
		if ageDays > info.OldestDays {
			info.OldestDays = ageDays
		}
		stale[rel] = info
	}
	if blameFailures > 0 {
		if blameFailureReason != "" {
			return stale, "marker blame " + blameFailureReason + ":" + itoa(blameFailures)
		}
		return stale, "marker blame unavailable:" + itoa(blameFailures)
	}
	return stale, ""
}

func localImportGraph(repo string, files []string, maxBytes int64) (map[string]int, []localImportEdge, map[string]bool) {
	source := map[string]string{}
	noExt := map[string]string{}
	goPackageFiles := map[string][]string{}
	for _, path := range files {
		rel, ok := relPath(repo, path)
		if !ok || shouldSkip(rel) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(rel))
		if ext != ".go" && ext != ".py" && ext != ".js" && ext != ".jsx" && ext != ".ts" && ext != ".tsx" {
			continue
		}
		source[rel] = path
		if ext == ".go" {
			if !isTestFile(rel) {
				dir := filepath.Dir(rel)
				if dir == "." {
					dir = ""
				}
				goPackageFiles[dir] = append(goPackageFiles[dir], rel)
			}
		} else {
			noExt[strings.TrimSuffix(rel, ext)] = rel
			if filepath.Base(rel) == "index"+ext || filepath.Base(rel) == "__init__"+ext {
				noExt[filepath.Dir(rel)] = rel
			}
		}
	}
	counts := map[string]int{}
	packageLevel := map[string]bool{}
	for rel := range source {
		counts[rel] = 0
	}
	modulePath := goModulePath(repo)
	var edges []localImportEdge
	for importer, path := range source {
		text, ok, _ := readTextPrefix(path, maxBytes)
		if !ok {
			continue
		}
		resolved := map[string]string{}
		for _, match := range regexp.MustCompile(`\bfrom\s+["']([^"']+)["']|\bimport\s*\(\s*["']([^"']+)["']\s*\)|\brequire\s*\(\s*["']([^"']+)["']\s*\)|(?m)^\s*import\s+["']([^"']+)["']|(?m)^\s*import\s+(?:\w+\s+|[._]\s+)?["']([^"']+)["']`).FindAllStringSubmatch(text, -1) {
			spec := firstNonEmpty(match[1:])
			if target := resolveLocalImport(importer, spec, counts, noExt); target != "" && target != importer {
				resolved[target] = "local_module"
			}
		}
		for _, spec := range goImportSpecs(text) {
			targets, fallback := resolveGoImportPackage(importer, spec, modulePath, goPackageFiles)
			for _, target := range targets {
				if target != importer {
					resolved[target] = "go_package"
					if fallback {
						packageLevel[target] = true
					}
				}
			}
		}
		for target, kind := range resolved {
			counts[target]++
			edges = append(edges, localImportEdge{Importer: importer, Imported: target, Kind: kind})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Importer != edges[j].Importer {
			return edges[i].Importer < edges[j].Importer
		}
		if edges[i].Imported != edges[j].Imported {
			return edges[i].Imported < edges[j].Imported
		}
		return edges[i].Kind < edges[j].Kind
	})
	return counts, edges, packageLevel
}

func goModulePath(repo string) string {
	text, ok, truncated, err := sourcePrefixReader(filepath.Join(repo, "go.mod"), maxRepoManifestBytes)
	if err != nil || !ok || truncated {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`)
		}
	}
	return ""
}

func goImportSpecs(text string) []string {
	matches := regexp.MustCompile(`(?m)^\s*import\s+(?:\w+\s+|[._]\s+)?["']([^"']+)["']|(?ms)^\s*import\s*\((.*?)\)`).FindAllStringSubmatch(text, -1)
	seen := map[string]bool{}
	var specs []string
	for _, match := range matches {
		if match[1] != "" {
			if !seen[match[1]] {
				seen[match[1]] = true
				specs = append(specs, match[1])
			}
			continue
		}
		for _, lineMatch := range regexp.MustCompile(`(?:^|\n)\s*(?:\w+\s+|[._]\s+)?["']([^"']+)["']`).FindAllStringSubmatch(match[2], -1) {
			spec := lineMatch[1]
			if !seen[spec] {
				seen[spec] = true
				specs = append(specs, spec)
			}
		}
	}
	return specs
}

func resolveGoImportPackage(importer, spec, modulePath string, goPackageFiles map[string][]string) ([]string, bool) {
	if modulePath == "" {
		return nil, false
	}
	if spec == modulePath {
		return goPackageOwnerFiles("", goPackageFiles[""])
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(spec, prefix) {
		return nil, false
	}
	dir := strings.TrimPrefix(spec, prefix)
	targets := goPackageFiles[dir]
	if len(targets) == 0 {
		return nil, false
	}
	importerDir := filepath.Dir(filepath.ToSlash(importer))
	if importerDir == "." {
		importerDir = ""
	}
	if importerDir == dir {
		return nil, false
	}
	return goPackageOwnerFiles(dir, targets)
}

// goPackageOwnerFiles picks at most two owner files for a package import. A
// named owner match (pkg.go, types.go, interfaces.go, ...) resolves the hub;
// when no preferred name exists the alphabetically first file absorbs the
// package fan-in and the caller must label that attribution package-level.
func goPackageOwnerFiles(dir string, files []string) ([]string, bool) {
	if len(files) <= 1 {
		return files, false
	}
	base := filepath.Base(dir)
	singular := strings.TrimSuffix(base, "s")
	preferredNames := []string{
		base + ".go",
		singular + ".go",
		"types.go",
		"interfaces.go",
		"interface.go",
		"client.go",
		"server.go",
		"handlers.go",
		"handler.go",
		"config.go",
	}
	byName := map[string]string{}
	for _, rel := range files {
		byName[filepath.Base(rel)] = rel
	}
	var owners []string
	for _, name := range preferredNames {
		if rel := byName[name]; rel != "" {
			owners = append(owners, rel)
		}
		if len(owners) == 2 {
			return owners, false
		}
	}
	if len(owners) > 0 {
		return owners, false
	}
	sort.Strings(files)
	return files[:1], true
}

func resolveLocalImport(importer, spec string, counts map[string]int, noExt map[string]string) string {
	if strings.HasPrefix(spec, ".") {
		base := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(importer), spec)))
		for _, candidate := range []string{base, base + ".ts", base + ".tsx", base + ".js", base + ".jsx", base + "/index.ts", base + "/index.tsx", base + "/index.js", base + "/index.jsx"} {
			if _, ok := counts[candidate]; ok {
				return candidate
			}
			if rel := noExt[candidate]; rel != "" {
				return rel
			}
		}
	}
	base := strings.ReplaceAll(spec, ".", "/")
	for _, candidate := range []string{base + ".py", filepath.ToSlash(filepath.Join(filepath.Dir(importer), base+".py"))} {
		if _, ok := counts[candidate]; ok {
			return candidate
		}
	}
	return noExt[base]
}

func documentedEnvVars(repo string, files []string, maxBytes int64) map[string]bool {
	documented := map[string]bool{}
	for _, path := range files {
		rel, ok := relPath(repo, path)
		if !ok || shouldSkip(rel) || !isEnvContractDocPath(rel) {
			continue
		}
		text, ok, _ := readTextPrefix(path, maxBytes)
		if !ok {
			continue
		}
		for _, env := range regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`).FindAllString(text, -1) {
			documented[env] = true
		}
	}
	return documented
}

func isEnvContractDocPath(rel string) bool {
	lower := strings.ToLower(rel)
	name := filepath.Base(lower)
	if strings.HasPrefix(name, ".env") {
		return true
	}
	switch filepath.Ext(lower) {
	case ".env", ".example", ".md", ".txt", ".yaml", ".yml", ".toml", ".json":
	default:
		return false
	}
	for _, hint := range []string{".env", "env.example", "environment", "readme", "docs", "config", "settings", "example", "sample"} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func runGit(ctx context.Context, repo string, args ...string) string {
	out, _ := runGitOutput(ctx, repo, args...)
	return out
}

func runGitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	return runGitOutputLimit(ctx, repo, maxGitHistoryBytes, args...)
}

func runGitOutputLimit(ctx context.Context, repo string, limit int64, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("git stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start git: %w", err)
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	if readErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", fmt.Errorf("read git output: %w", readErr)
	}
	if int64(len(out)) > limit {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", fmt.Errorf("%w: %d bytes", errGitOutputLimit, limit)
	}
	err = cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git command: %w", err)
	}
	return string(out), nil
}

func gitSkipReason(err error) string {
	switch {
	case errors.Is(err, errGitOutputLimit):
		return "output_limit_exceeded:" + itoa(int(maxGitHistoryBytes))
	case errors.Is(err, context.Canceled):
		return "command_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "command_deadline_exceeded"
	default:
		return "command_failed"
	}
}

func relPath(repo, path string) (string, bool) {
	rel, err := filepath.Rel(repo, path)
	if err != nil || isParentTraversal(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func isArchitectureSource(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".php", ".rb", ".java", ".cs":
		return true
	default:
		return false
	}
}

func isTestFile(rel string) bool {
	lower := strings.ToLower(rel)
	name := filepath.Base(rel)
	ext := strings.ToLower(filepath.Ext(rel))
	if ext == ".go" {
		return strings.HasSuffix(name, "_test.go")
	}
	if ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" {
		return strings.Contains(name, ".test.") || strings.Contains(name, ".spec.")
	}
	if ext == ".sh" || ext == ".bash" || ext == ".zsh" {
		return strings.HasSuffix(name, "_test.sh") || strings.HasSuffix(name, "_test.bash") || strings.HasSuffix(name, "_test.zsh")
	}
	if ext == ".py" {
		return strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test.py") || strings.Contains(lower, "/tests/")
	}
	return strings.Contains(lower, "/test/") || strings.Contains(lower, "/tests/")
}

// isExamplesPath reports whether a file lives under an examples/example
// directory segment. Demos are executable documentation: the user-surface
// lane already reviews them, and demanding a nearby test for a quickstart
// adds structural noise rather than risk.
func isExamplesPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "examples" || part == "example" {
			return true
		}
	}
	return false
}

func hasNearbyTest(repo, rel string) bool {
	if isTestFile(rel) {
		return true
	}
	ext := strings.ToLower(filepath.Ext(rel))
	stem := strings.TrimSuffix(filepath.Base(rel), ext)
	dir := filepath.Join(repo, filepath.Dir(rel))
	switch ext {
	case ".go":
		return fileExists(filepath.Join(dir, stem+"_test.go")) || dirHasSuffixFile(dir, "_test.go")
	case ".py":
		return fileExists(filepath.Join(dir, "test_"+stem+".py")) || fileExists(filepath.Join(dir, stem+"_test.py"))
	case ".ts", ".tsx", ".js", ".jsx":
		return fileExists(filepath.Join(dir, stem+".test"+ext)) || fileExists(filepath.Join(dir, stem+".spec"+ext))
	default:
		return true
	}
}

func dirHasSuffixFile(dir, suffix string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func filepathBase(path string) string {
	return filepath.Base(path)
}

func filepathExt(path string) string {
	return filepath.Ext(path)
}

func sortStrings(items []string) {
	sort.Strings(items)
}

func firstNonEmpty(items []string) string {
	for _, item := range items {
		if item != "" {
			return item
		}
	}
	return ""
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func regexpMust(expr string) *regexp.Regexp {
	return regexp.MustCompile(expr)
}
