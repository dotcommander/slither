package slither

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type scoreContext struct {
	files         []string
	churn         map[string]int
	fixTouches    map[string]int
	cochange      map[string]cochangeInfo
	ownership     map[string]ownershipInfo
	staleMarkers  map[string]staleMarkerInfo
	incomingRefs  map[string]int
	documentedEnv map[string]bool
	patterns      scoringPatterns
	skipped       []string
}

func newScoreContext(ctx context.Context, repo string, files []string, maxBytes int64, days int, patterns scoringPatterns) scoreContext {
	scoreCtx := scoreContext{
		files:         files,
		churn:         churnByFile(ctx, repo, days),
		fixTouches:    map[string]int{},
		incomingRefs:  localImportCounts(repo, files, maxBytes),
		documentedEnv: documentedEnvVars(repo, files, maxBytes),
		patterns:      patterns,
	}
	fixTouches, skip := bugfixTouchesByFile(ctx, repo, days)
	scoreCtx.fixTouches = fixTouches
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "bugfix_density:"+skip)
	}
	cochange, skip := cochangeByFile(ctx, repo, days)
	scoreCtx.cochange = cochange
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "cochange:"+skip)
	}
	ownership, skip := ownershipByFile(ctx, repo, days)
	scoreCtx.ownership = ownership
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "ownership:"+skip)
	}
	staleMarkers, skip := staleMarkersByFile(ctx, repo, files, maxBytes)
	scoreCtx.staleMarkers = staleMarkers
	if skip != "" {
		scoreCtx.skipped = append(scoreCtx.skipped, "stale_markers:"+skip)
	}
	return scoreCtx
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

func churnByFile(ctx context.Context, repo string, days int) map[string]int {
	out := runGit(ctx, repo, "log", "--since="+itoa(days)+" days ago", "--numstat", "--format=")
	if out == "" {
		return map[string]int{}
	}
	churn := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || parts[0] == "-" || parts[1] == "-" {
			continue
		}
		added, errA := strconv.Atoi(parts[0])
		deleted, errD := strconv.Atoi(parts[1])
		if errA != nil || errD != nil {
			continue
		}
		churn[parts[2]] += added + deleted
	}
	return churn
}

func bugfixTouchesByFile(ctx context.Context, repo string, days int) (map[string]int, string) {
	history := runGit(ctx, repo, "log", "--since="+itoa(days)+" days ago", "--pretty=format:%H")
	if history == "" {
		return map[string]int{}, "no recent git history"
	}
	commits := strings.Fields(history)
	if len(commits) < 30 {
		return map[string]int{}, "insufficient commit count:" + itoa(len(commits))
	}
	out := runGit(ctx, repo, "log", "--since="+itoa(days)+" days ago", "--extended-regexp", "--grep=fix|bug|regression|crash|panic|broken", "-i", "--name-only", "--pretty=format:")
	if out == "" {
		return map[string]int{}, ""
	}
	touches := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		rel := strings.TrimSpace(line)
		if rel != "" {
			touches[rel]++
		}
	}
	return touches, ""
}

func ownershipByFile(ctx context.Context, repo string, days int) (map[string]ownershipInfo, string) {
	out := runGit(ctx, repo, "log", "--since="+itoa(days)+" days ago", "--format=format:__SLITHER_AUTHOR__%ae", "--name-only")
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

func cochangeByFile(ctx context.Context, repo string, days int) (map[string]cochangeInfo, string) {
	out := runGit(ctx, repo, "log", "--since="+itoa(days)+" days ago", "--format=format:__SLITHER_COMMIT__", "--name-only")
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

func staleMarkersByFile(ctx context.Context, repo string, files []string, maxBytes int64) (map[string]staleMarkerInfo, string) {
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
	today := time.Now()
	stale := map[string]staleMarkerInfo{}
	blameFailures := 0
	for _, marker := range markers {
		rel, line := marker[0], marker[1]
		out := runGit(ctx, repo, "blame", "--line-porcelain", "-L", line+","+line, "--", rel)
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
	if blameFailures > 0 && len(stale) == 0 {
		return map[string]staleMarkerInfo{}, "marker blame unavailable:" + itoa(blameFailures)
	}
	return stale, ""
}

func localImportCounts(repo string, files []string, maxBytes int64) map[string]int {
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
	for rel := range source {
		counts[rel] = 0
	}
	modulePath := goModulePath(repo)
	for importer, path := range source {
		text, ok, _ := readTextPrefix(path, maxBytes)
		if !ok {
			continue
		}
		resolved := map[string]bool{}
		for _, match := range regexp.MustCompile(`\bfrom\s+["']([^"']+)["']|\bimport\s*\(\s*["']([^"']+)["']\s*\)|\brequire\s*\(\s*["']([^"']+)["']\s*\)|(?m)^\s*import\s+["']([^"']+)["']|(?m)^\s*import\s+(?:\w+\s+|[._]\s+)?["']([^"']+)["']`).FindAllStringSubmatch(text, -1) {
			spec := firstNonEmpty(match[1:])
			if target := resolveLocalImport(importer, spec, counts, noExt); target != "" && target != importer {
				resolved[target] = true
			}
		}
		for _, spec := range goImportSpecs(text) {
			for _, target := range resolveGoImportPackage(importer, spec, modulePath, goPackageFiles) {
				if target != importer {
					resolved[target] = true
				}
			}
		}
		for target := range resolved {
			counts[target]++
		}
	}
	return counts
}

func goModulePath(repo string) string {
	data, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
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

func resolveGoImportPackage(importer, spec, modulePath string, goPackageFiles map[string][]string) []string {
	if modulePath == "" {
		return nil
	}
	if spec == modulePath {
		return goPackageFiles[""]
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(spec, prefix) {
		return nil
	}
	dir := strings.TrimPrefix(spec, prefix)
	targets := goPackageFiles[dir]
	if len(targets) == 0 {
		return nil
	}
	importerDir := filepath.Dir(filepath.ToSlash(importer))
	if importerDir == "." {
		importerDir = ""
	}
	if importerDir == dir {
		return nil
	}
	return goPackageOwnerFiles(dir, targets)
}

func goPackageOwnerFiles(dir string, files []string) []string {
	if len(files) <= 1 {
		return files
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
			return owners
		}
	}
	if len(owners) > 0 {
		return owners
	}
	sort.Strings(files)
	return files[:1]
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
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func relPath(repo, path string) (string, bool) {
	rel, err := filepath.Rel(repo, path)
	if err != nil || strings.HasPrefix(rel, "..") {
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
	if ext == ".py" {
		return strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test.py") || strings.Contains(lower, "/tests/")
	}
	return strings.Contains(lower, "/test/") || strings.Contains(lower, "/tests/")
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
