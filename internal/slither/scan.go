package slither

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "coverage": true, ".next": true, ".svelte-kit": true, ".venv": true,
	".work": true,
}

var skipSuffixes = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf", ".zip", ".gz", ".tar", ".mp4", ".mp3", ".lock", ".sum"}

type fallbackTerm struct {
	Term   string
	Weight int
	Layer  string
}

func BuildReport(ctx context.Context, opts Options) (Report, error) {
	if opts.Days <= 0 {
		opts.Days = 90
	}
	paths, discovery, skippedSignals, err := discoverFiles(ctx, opts.Repo)
	if err != nil {
		return Report{}, err
	}
	patterns, err := loadScoringPatterns(opts.Patterns)
	if err != nil {
		return Report{}, err
	}
	scoreCtx := newScoreContext(ctx, opts.Repo, paths, opts.MaxBytes, opts.Days, patterns)
	skippedSignals = append(skippedSignals, scoreCtx.skipped...)
	scorer, err := NewModelScorer(opts)
	if err != nil {
		return Report{}, err
	}
	if scorer == nil {
		skippedSignals = append(skippedSignals, "model_scoring:not_configured")
	}
	report := Report{Repo: opts.Repo, GeneratedAt: time.Now(), Days: opts.Days, PatternsSource: patterns.Source, FilesSeen: len(paths), Discovery: discovery, Model: opts.Model, BaseURL: opts.BaseURL, SkippedSignals: skippedSignals}
	for _, path := range paths {
		evidence, ok, err := inspectFile(opts.Repo, path, opts.MaxBytes, scoreCtx)
		if err != nil {
			return Report{}, err
		}
		if !ok {
			continue
		}
		if scorer != nil {
			fallbackLayers := evidence.EvidenceLayers
			scored, err := scorer.Score(ctx, evidence)
			if err != nil {
				evidence.Reasons = append(evidence.Reasons, "model_error:"+err.Error())
				evidence.EvidenceLayers = evidenceLayersForReasons(evidence.Reasons)
			} else {
				evidence = scored
				evidence.EvidenceLayers = mergeLayers(fallbackLayers, []string{"model"})
			}
		}
		finalizeEvidenceMetadata(&evidence)
		report.Rows = append(report.Rows, evidence)
	}
	sort.SliceStable(report.Rows, func(i, j int) bool {
		if report.Rows[i].SeedScore != report.Rows[j].SeedScore {
			return report.Rows[i].SeedScore > report.Rows[j].SeedScore
		}
		if report.Rows[i].FixTouches != report.Rows[j].FixTouches {
			return report.Rows[i].FixTouches > report.Rows[j].FixTouches
		}
		if report.Rows[i].Churn != report.Rows[j].Churn {
			return report.Rows[i].Churn > report.Rows[j].Churn
		}
		if report.Rows[i].TestGap != report.Rows[j].TestGap {
			return report.Rows[i].TestGap
		}
		if len(report.Rows[i].EvidenceLayers) != len(report.Rows[j].EvidenceLayers) {
			return len(report.Rows[i].EvidenceLayers) > len(report.Rows[j].EvidenceLayers)
		}
		for _, less := range []func(FileEvidence) int{
			func(row FileEvidence) int { return row.PathRisk },
			func(row FileEvidence) int { return row.UnknownsRisk },
			func(row FileEvidence) int { return row.EnvContractRisk },
			func(row FileEvidence) int { return row.WorkflowSecurityRisk },
			func(row FileEvidence) int { return row.MigrationSafetyRisk },
			func(row FileEvidence) int { return row.ContainerBuildRisk },
			func(row FileEvidence) int { return row.KubernetesSecurityRisk },
			func(row FileEvidence) int { return row.TerraformSecurityRisk },
			func(row FileEvidence) int { return row.OpenAPIContractRisk },
			func(row FileEvidence) int { return row.CORSSecurityRisk },
			func(row FileEvidence) int { return row.CookieSecurityRisk },
			func(row FileEvidence) int { return row.DependencyHealthRisk },
			func(row FileEvidence) int { return row.HotspotRisk },
			func(row FileEvidence) int { return row.CentralityRisk },
			func(row FileEvidence) int { return row.CochangeRisk },
			func(row FileEvidence) int { return row.OwnershipRisk },
			func(row FileEvidence) int { return row.FlakeRisk },
			func(row FileEvidence) int { return row.OracleRisk },
			func(row FileEvidence) int { return row.StaleMarkerRisk },
			func(row FileEvidence) int { return row.Lines },
		} {
			left := less(report.Rows[i])
			right := less(report.Rows[j])
			if left != right {
				return left > right
			}
		}
		return report.Rows[i].Path < report.Rows[j].Path
	})
	if len(report.Rows) > opts.Top {
		report.Rows = report.Rows[:opts.Top]
	}
	report.FilesScored = len(report.Rows)
	report.FirstReadQueue, report.ReviewPlan = BuildReviewPlan(report.Rows)
	return report, nil
}

func discoverFiles(ctx context.Context, repo string) ([]string, DiscoveryStats, []string, error) {
	var skippedSignals []string
	tracked, err := gitFiles(ctx, repo, "--cached")
	if err == nil {
		untracked, err := gitFiles(ctx, repo, "--others", "--exclude-standard")
		if err != nil {
			return nil, DiscoveryStats{}, nil, err
		}
		if len(tracked) > 0 || len(untracked) > 0 {
			if len(tracked) == 0 {
				skippedSignals = append(skippedSignals, "git_ls_files:no_tracked_files")
			}
			if len(untracked) > 0 {
				skippedSignals = append(skippedSignals, "git_ls_files:included_untracked:"+itoa(len(untracked)))
			}
			paths := appendGitFiles(repo, tracked, untracked)
			discovery := DiscoveryStats{
				Source:         "git",
				GitTracked:     len(tracked),
				GitUntracked:   len(untracked),
				CandidateFiles: len(paths),
			}
			return paths, discovery, skippedSignals, nil
		}
	}
	if err != nil {
		skippedSignals = append(skippedSignals, "git_ls_files:unavailable")
	} else {
		skippedSignals = append(skippedSignals, "git_ls_files:no_tracked_files")
	}

	var paths []string
	err = filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repo && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	discovery := DiscoveryStats{
		Source:          "filesystem",
		FilesystemFiles: len(paths),
		CandidateFiles:  len(paths),
	}
	return paths, discovery, skippedSignals, err
}

func gitFiles(ctx context.Context, repo string, args ...string) ([]string, error) {
	cmdArgs := append([]string{"-C", repo, "ls-files"}, args...)
	out, err := exec.CommandContext(ctx, "git", cmdArgs...).Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		if rel := strings.TrimSpace(scanner.Text()); rel != "" {
			paths = append(paths, rel)
		}
	}
	return paths, scanner.Err()
}

func appendGitFiles(repo string, groups ...[]string) []string {
	seen := map[string]bool{}
	var paths []string
	for _, group := range groups {
		for _, rel := range group {
			path := filepath.Join(repo, rel)
			if seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

func inspectFile(repo, path string, maxBytes int64, scoreCtx scoreContext) (FileEvidence, bool, error) {
	rel, err := filepath.Rel(repo, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return FileEvidence{}, false, nil
	}
	if shouldSkip(rel) {
		return FileEvidence{}, false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return FileEvidence{}, false, fmt.Errorf("stat %s: %w", rel, err)
	}
	text, ok, err := readTextPrefix(path, maxBytes)
	if err != nil || !ok {
		return FileEvidence{}, ok, err
	}
	e := FileEvidence{Path: filepath.ToSlash(rel), Bytes: info.Size(), Lines: strings.Count(text, "\n") + 1, Excerpt: firstSentence(text)}
	scoreFile(repo, text, &e, scoreCtx)
	e.EvidenceLayers = evidenceLayersForReasons(e.Reasons)
	e.Summary = e.Excerpt
	return e, true, nil
}

func shouldSkip(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		if skipDirs[part] {
			return true
		}
	}
	lower := strings.ToLower(rel)
	for _, suffix := range skipSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func readTextPrefix(path string, maxBytes int64) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 || strings.Contains(string(data[:min(len(data), 4096)]), "\x00") || !utf8.Valid(data) {
		return "", false, nil
	}
	return string(data), true, nil
}

func fallbackScore(path, text string, bytes int64) (int, []string) {
	e := FileEvidence{Path: path, Bytes: bytes, Lines: strings.Count(text, "\n") + 1}
	scoreFile("", text, &e, scoreContext{})
	return e.Score, e.Reasons
}

func scoreFile(repo, text string, e *FileEvidence, scoreCtx scoreContext) {
	e.Churn = scoreCtx.churn[e.Path]
	e.FixTouches = scoreCtx.fixTouches[e.Path]
	e.IncomingRefs = scoreCtx.incomingRefs[e.Path]
	e.Markers = len(regexpMust(`\b(TODO|FIXME|HACK|XXX)\b`).FindAllStringIndex(text, -1))

	var reasons []string
	e.PathRisk, reasons = pathRisk(scoreCtx.patterns, e.Path)
	e.Reasons = append(e.Reasons, reasons...)
	e.ContentRisk, reasons = contentRisk(scoreCtx.patterns, e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.Imports, e.SmellRisk, reasons = architectureSmellRisk(text, e.Path, e.Lines)
	e.Reasons = append(e.Reasons, reasons...)
	e.UnknownsRisk, reasons = unknownsRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	envVars := envVarsInCode(e.Path, text)
	e.EnvContractRisk, reasons = envContractRisk(envVars, scoreCtx.documentedEnv, e.Churn, e.FixTouches, e.PathRisk, e.ContentRisk, e.UnknownsRisk)
	e.Reasons = append(e.Reasons, reasons...)
	e.WorkflowSecurityRisk, reasons = workflowSecurityRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.MigrationSafetyRisk, reasons = migrationSafetyRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.ContainerBuildRisk, reasons = containerBuildRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.KubernetesSecurityRisk, reasons = kubernetesSecurityRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.TerraformSecurityRisk, reasons = terraformSecurityRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.OpenAPIContractRisk, reasons = openAPIContractRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.CORSSecurityRisk, reasons = corsSecurityRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.CookieSecurityRisk, reasons = cookieSecurityRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.DependencyHealthRisk, reasons = dependencyHealthRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.SDKDXRisk, reasons = sdkDXRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.CentralityRisk, reasons = centralityRisk(e.IncomingRefs, e.PathRisk, e.ContentRisk)
	e.Reasons = append(e.Reasons, reasons...)
	e.CochangeRisk, reasons = cochangeRisk(scoreCtx.cochange[e.Path], e.Churn, e.FixTouches, e.PathRisk, e.ContentRisk, e.CentralityRisk)
	e.Reasons = append(e.Reasons, reasons...)
	e.OwnershipRisk, reasons = ownershipRisk(scoreCtx.ownership[e.Path], e.Churn, e.FixTouches, e.PathRisk, e.ContentRisk)
	e.Reasons = append(e.Reasons, reasons...)
	e.FlakeRisk, reasons = testFlakeRisk(e.Path, text)
	e.Reasons = append(e.Reasons, reasons...)
	e.OracleRisk, reasons = testOracleRisk(e.Path, text, e.Lines)
	e.Reasons = append(e.Reasons, reasons...)
	e.StaleMarkerRisk, reasons = staleMarkerRisk(scoreCtx.staleMarkers[e.Path], e.Churn, e.FixTouches, e.PathRisk, e.ContentRisk, e.OwnershipRisk)
	e.Reasons = append(e.Reasons, reasons...)
	e.HotspotRisk, reasons = hotspotRisk(branchComplexity(text, e.Path), e.Churn, e.FixTouches, e.IncomingRefs)
	e.Reasons = append(e.Reasons, reasons...)
	if repo != "" && e.Lines >= 80 && !isTestFile(e.Path) && isArchitectureSource(e.Path) && !hasNearbyTest(repo, e.Path) {
		e.TestGap = true
		e.Reasons = append(e.Reasons, "test_gap:no nearby test")
	}
	if e.Lines >= 300 {
		e.Reasons = append(e.Reasons, "size:"+itoa(e.Lines)+" lines")
	}
	if e.Markers > 0 {
		e.Reasons = append(e.Reasons, "markers:"+itoa(e.Markers))
	}
	if e.Churn > 0 {
		e.Reasons = append(e.Reasons, "churn:"+itoa(e.Churn))
	}
	if e.FixTouches > 0 {
		e.Reasons = append(e.Reasons, "bugfix_touches:"+itoa(e.FixTouches))
	}
	e.EvidenceLayers = evidenceLayersForReasons(e.Reasons)
	e.SeedScore = seedScore(*e)
	e.Score = qualitativeScore(*e)
	if len(e.Reasons) == 0 {
		e.Reasons = append(e.Reasons, "low-signal")
		e.EvidenceLayers = evidenceLayersForReasons(e.Reasons)
	}
}

func textWithoutDetectorLiterals(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "fallbackcontentterms") ||
			strings.Contains(lower, "fallbackpathterms") ||
			strings.Contains(lower, "pattern(") ||
			strings.Contains(lower, "regexp.mustcompile") ||
			strings.Contains(line, "Term:") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func evidenceLayersForReasons(reasons []string) []string {
	layers := make([]string, 0, 4)
	for _, reason := range reasons {
		switch {
		case strings.HasPrefix(reason, "path:"):
			layers = appendLayer(layers, "path-risk")
		case strings.HasPrefix(reason, "content:hardcoded_private_key") ||
			strings.HasPrefix(reason, "content:provider_token_literal") ||
			strings.HasPrefix(reason, "content:credential_assignment_literal"):
			layers = appendLayer(layers, "secret-risk")
		case strings.HasPrefix(reason, "content:"):
			layers = appendLayer(layers, "content-risk")
		case strings.HasPrefix(reason, "size:"):
			layers = appendLayer(layers, "size")
		case strings.HasPrefix(reason, "markers:"):
			layers = appendLayer(layers, "work-marker")
		case strings.HasPrefix(reason, "churn:"):
			layers = appendLayer(layers, "churn")
		case strings.HasPrefix(reason, "bugfix_touches:"):
			layers = appendLayer(layers, "bugfix-history")
		case strings.HasPrefix(reason, "smell:"):
			layers = appendLayer(layers, "architecture-smell")
		case strings.HasPrefix(reason, "hotspot:"):
			layers = appendLayer(layers, "hotspot")
		case strings.HasPrefix(reason, "unknowns:"):
			layers = appendLayer(layers, "unknowns")
		case strings.HasPrefix(reason, "env_contract:"):
			layers = appendLayer(layers, "env-contract")
		case strings.HasPrefix(reason, "workflow_security:"):
			layers = appendLayer(layers, "workflow-security")
		case strings.HasPrefix(reason, "migration_safety:"):
			layers = appendLayer(layers, "migration-safety")
		case strings.HasPrefix(reason, "container_build:"):
			layers = appendLayer(layers, "container-build")
		case strings.HasPrefix(reason, "kubernetes_security:"):
			layers = appendLayer(layers, "kubernetes-security")
		case strings.HasPrefix(reason, "terraform_security:"):
			layers = appendLayer(layers, "terraform-security")
		case strings.HasPrefix(reason, "openapi_contract:"):
			layers = appendLayer(layers, "openapi-contract")
		case strings.HasPrefix(reason, "cors_security:"):
			layers = appendLayer(layers, "cors-security")
		case strings.HasPrefix(reason, "cookie_security:"):
			layers = appendLayer(layers, "cookie-security")
		case strings.HasPrefix(reason, "dependency_health:"):
			layers = appendLayer(layers, "dependency-health")
		case strings.HasPrefix(reason, "sdk_dx:"):
			layers = appendLayer(layers, "sdk-dx")
		case strings.HasPrefix(reason, "centrality:"):
			layers = appendLayer(layers, "centrality")
		case strings.HasPrefix(reason, "cochange:"):
			layers = appendLayer(layers, "cochange")
		case strings.HasPrefix(reason, "ownership:"):
			layers = appendLayer(layers, "ownership")
		case strings.HasPrefix(reason, "flake:"):
			layers = appendLayer(layers, "flake-risk")
		case strings.HasPrefix(reason, "oracle:"):
			layers = appendLayer(layers, "oracle-risk")
		case strings.HasPrefix(reason, "stale_marker:"):
			layers = appendLayer(layers, "stale-marker")
		case strings.HasPrefix(reason, "test_gap:"):
			layers = appendLayer(layers, "test-void")
		case strings.HasPrefix(reason, "model_error:"):
			layers = appendLayer(layers, "model-error")
		case reason == "low-signal":
			layers = appendLayer(layers, "low-signal")
		}
	}
	return layers
}

func mergeLayers(groups ...[]string) []string {
	var merged []string
	for _, group := range groups {
		for _, layer := range group {
			merged = appendLayer(merged, layer)
		}
	}
	return merged
}

func appendLayer(layers []string, layer string) []string {
	for _, existing := range layers {
		if existing == layer {
			return layers
		}
	}
	return append(layers, layer)
}

func firstSentence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 180 {
		return text[:180] + "..."
	}
	return text
}
