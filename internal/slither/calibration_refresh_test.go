package slither

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const calibrationRefreshGate = "SLITHER_CALIBRATION_REFRESH"
const calibrationFixtureRelPath = "internal/slither/testdata/calibration_fixture.json"

type calibrationRefreshCase struct {
	Repository string
	Case       calibrationFixtureCase
}

func calibrationReportOptions(repo string, asOf time.Time) Options {
	return Options{
		Repo: repo, AsOf: asOf, Top: 100_000, Cull: true, NoCache: true,
		MaxBytes: 500_000, Days: 90,
	}
}

func calibrationCohortReportOptions(name, repo string, asOf time.Time) Options {
	opts := calibrationReportOptions(repo, asOf)
	if name == "slither" {
		// The derived fixture cannot participate in the population whose
		// identity it stores: replacing it would invalidate that identity.
		opts.Exclude = []string{calibrationFixtureRelPath}
	}
	return opts
}

func buildStableCalibrationReport(ctx context.Context, name, repo string, asOf time.Time) (Report, error) {
	var previous Report
	for attempt := 0; attempt < 5; attempt++ {
		current, err := buildReport(ctx, calibrationCohortReportOptions(name, repo, asOf), 1)
		if err != nil {
			return Report{}, err
		}
		if previous.ReportID != "" && current.ReportID == previous.ReportID {
			return current, nil
		}
		previous = current
	}
	return Report{}, fmt.Errorf("report identity did not stabilize after 5 fixed-time scans; last=%s", previous.ReportID)
}

func validateCalibrationSource(report Report, want calibrationRepository) error {
	if report.SourceState.Kind != want.SourceKind {
		return fmt.Errorf("source kind = %q, want %q", report.SourceState.Kind, want.SourceKind)
	}
	if report.SourceState.Head != want.Head {
		return fmt.Errorf("HEAD = %q, want %q", report.SourceState.Head, want.Head)
	}
	if report.SourceState.Dirty != want.Dirty {
		return fmt.Errorf("dirty = %v, want %v", report.SourceState.Dirty, want.Dirty)
	}
	if report.SourceState.TreeDigest != want.TreeDigest {
		return fmt.Errorf("tree digest = %q, want %q", report.SourceState.TreeDigest, want.TreeDigest)
	}
	if report.ReportID != want.ReportID {
		return fmt.Errorf("report ID = %q, want %q", report.ReportID, want.ReportID)
	}
	return nil
}

func validateCalibrationHoldoutSource(report Report, want calibrationHoldout) error {
	return validateCalibrationSource(report, calibrationRepository{
		SourceKind: want.SourceKind,
		Head:       want.Head,
		Dirty:      want.Dirty,
		TreeDigest: want.TreeDigest,
		ReportID:   want.ReportID,
	})
}

func refreshCalibrationFixture(
	ctx context.Context,
	prior calibrationFixture,
	roots map[string]string,
	asOf time.Time,
	outputPath string,
) (calibrationFixture, []calibrationRefreshCase, error) {
	if os.Getenv(calibrationRefreshGate) != "1" {
		return calibrationFixture{}, nil, fmt.Errorf("%s=1 is required", calibrationRefreshGate)
	}
	if outputPath == "" {
		return calibrationFixture{}, nil, errors.New("temporary output path is required")
	}
	checkedIn, err := filepath.Abs(calibrationFixturePath)
	if err != nil {
		return calibrationFixture{}, nil, fmt.Errorf("resolve checked-in fixture: %w", err)
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		return calibrationFixture{}, nil, fmt.Errorf("resolve temporary output: %w", err)
	}
	if outputPath == checkedIn {
		return calibrationFixture{}, nil, errors.New("refresh output must not be the checked-in fixture")
	}
	if err := rejectCalibrationOutputAlias(checkedIn, outputPath); err != nil {
		return calibrationFixture{}, nil, err
	}
	if asOf.IsZero() {
		return calibrationFixture{}, nil, errors.New("fixed as_of is required")
	}
	asOf = asOf.UTC()

	fixture := calibrationFixture{
		Schema:             "slither.calibration/v1",
		CreatedAt:          asOf.Format(time.DateOnly),
		AsOf:               asOf.Format(time.RFC3339),
		Selection:          prior.Selection,
		ScoringDisposition: "retain-current-scoring",
	}
	if fixture.Selection.Top == 0 {
		fixture.Selection = calibrationSelection{
			Top: 15, Distributed: 10, StrongestDirectRisk: 10,
			LowScorePathHash: 5, ExpansionStrongestRisk: 20,
		}
	}

	reports := make(map[string]Report, 4)
	var newCases []calibrationRefreshCase
	for _, name := range []string{"slither", "repomap", "reliquary"} {
		root := roots[name]
		if root == "" {
			return calibrationFixture{}, nil, fmt.Errorf("%s repository root is required", name)
		}
		report, err := buildStableCalibrationReport(ctx, name, root, asOf)
		if err != nil {
			return calibrationFixture{}, nil, fmt.Errorf("scan %s: %w", name, err)
		}
		if report.SourceState.Kind != "git" {
			return calibrationFixture{}, nil, fmt.Errorf("%s source kind = %q, want git", name, report.SourceState.Kind)
		}
		if name == "slither" {
			if !report.SourceState.Dirty {
				return calibrationFixture{}, nil, errors.New("slither prerequisite snapshot must be dirty")
			}
		} else if report.SourceState.Dirty {
			return calibrationFixture{}, nil, fmt.Errorf("%s calibration clone must be clean", name)
		}
		reports[name] = report
		priorLabels := calibrationPriorLabelsForReport(prior, name, report)
		population := sanitizeCalibrationPopulation(report.Rows)
		repository := calibrationRepository{
			Name:         name,
			SourceKind:   report.SourceState.Kind,
			Head:         report.SourceState.Head,
			Dirty:        report.SourceState.Dirty,
			TreeDigest:   report.SourceState.TreeDigest,
			ReportID:     report.ReportID,
			ReportDigest: calibrationPopulationDigest(population),
			Population:   population,
		}
		selected, err := selectCalibrationCases(population, fixture.Selection)
		if err != nil {
			return calibrationFixture{}, nil, fmt.Errorf("select %s cases: %w", name, err)
		}
		for i := range selected {
			key := calibrationLabelKey(name, selected[i].Path, selected[i].ContentID)
			if label, ok := priorLabels[key]; ok {
				copyCalibrationLabel(&selected[i], label)
				continue
			}
			selected[i].Verdict = "unknown"
			selected[i].Rationale = "Pending independent review after fixed-time Git-aware reselection."
			selected[i].VerificationCommands = []string{"inspect " + selected[i].Path}
			newCases = append(newCases, calibrationRefreshCase{Repository: name, Case: selected[i]})
		}
		repository.Cases = selected
		fixture.Repositories = append(fixture.Repositories, repository)
	}

	holdoutRoot := roots["holdout"]
	if holdoutRoot == "" {
		return calibrationFixture{}, nil, errors.New("holdout repository root is required")
	}
	holdoutReport, err := buildStableCalibrationReport(ctx, "holdout", holdoutRoot, asOf)
	if err != nil {
		return calibrationFixture{}, nil, fmt.Errorf("scan holdout: %w", err)
	}
	if holdoutReport.SourceState.Kind != "git" || holdoutReport.SourceState.Dirty {
		return calibrationFixture{}, nil, fmt.Errorf("holdout must be a clean Git source, got kind=%q dirty=%v",
			holdoutReport.SourceState.Kind, holdoutReport.SourceState.Dirty)
	}
	reports["holdout"] = holdoutReport
	fixture.PrivateHoldout = calibrationHoldout{
		Head:         holdoutReport.SourceState.Head,
		SourceKind:   holdoutReport.SourceState.Kind,
		Dirty:        holdoutReport.SourceState.Dirty,
		TreeDigest:   holdoutReport.SourceState.TreeDigest,
		ReportID:     holdoutReport.ReportID,
		ReportDigest: calibrationPopulationDigest(sanitizeCalibrationPopulation(holdoutReport.Rows)),
		Baseline:     calibrationPopulationHealth(holdoutReport.Rows),
	}

	fixture.LabelGate = calibrationLabelGateFor(fixture.Repositories)
	fixture.SaturationScreen = calibrationCandidateScreen(fixture, holdoutReport.Rows)
	fixture.FinalQA = calibrationFinalQAForRefresh(fixture, prior, reports)

	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return calibrationFixture{}, nil, fmt.Errorf("marshal refreshed fixture: %w", err)
	}
	data = append(data, '\n')
	if bytes.Contains(bytes.ToLower(data), []byte(`"excerpt"`)) ||
		bytes.Contains(data, []byte(`/Users/`)) ||
		bytes.Contains(bytes.ToLower(data), []byte(`clickmojo`)) {
		return calibrationFixture{}, nil, errors.New("refreshed fixture failed privacy constraints")
	}
	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return calibrationFixture{}, nil, fmt.Errorf("create temporary fixture without replacement: %w", err)
	}
	if _, err := output.Write(data); err != nil {
		_ = output.Close()
		return calibrationFixture{}, nil, fmt.Errorf("write temporary fixture: %w", err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return calibrationFixture{}, nil, fmt.Errorf("sync temporary fixture: %w", err)
	}
	if err := output.Close(); err != nil {
		return calibrationFixture{}, nil, fmt.Errorf("close temporary fixture: %w", err)
	}
	return fixture, newCases, nil
}

func rejectCalibrationOutputAlias(checkedIn, outputPath string) error {
	outputInfo, err := os.Lstat(outputPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect temporary output: %w", err)
	}
	if outputInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("refresh output must not be a symlink")
	}
	checkedInfo, err := os.Stat(checkedIn)
	if err != nil {
		return fmt.Errorf("inspect checked-in fixture: %w", err)
	}
	resolvedOutput, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("inspect resolved temporary output: %w", err)
	}
	if os.SameFile(checkedInfo, resolvedOutput) {
		return errors.New("refresh output must not alias the checked-in fixture")
	}
	return errors.New("refresh output must not replace an existing file")
}

func sanitizeCalibrationPopulation(rows []FileEvidence) []FileEvidence {
	population := make([]FileEvidence, len(rows))
	for i, row := range rows {
		row.ID = ""
		row.EvidenceID = ""
		row.EvidenceLocations = nil
		row.Summary = ""
		row.Excerpt = ""
		population[i] = row
	}
	return population
}

func calibrationPopulationDigest(population []FileEvidence) string {
	return sha256Identity("slither.calibration-population/v1", population)
}

func calibrationPriorLabelsForReport(fixture calibrationFixture, repository string, report Report) map[string]calibrationFixtureCase {
	labels := make(map[string]calibrationFixtureCase)
	for _, repo := range fixture.Repositories {
		if repo.Name != repository {
			continue
		}
		currentContent := map[string]string{}
		if !repo.Dirty && !report.SourceState.Dirty && repo.Head == report.SourceState.Head {
			for _, row := range report.Rows {
				currentContent[row.Path] = row.ContentID
			}
		}
		for _, item := range repo.Cases {
			contentID := item.ContentID
			if contentID == "" {
				contentID = currentContent[item.Path]
			}
			if contentID == "" {
				continue
			}
			labels[calibrationLabelKey(repo.Name, item.Path, contentID)] = item
		}
	}
	return labels
}

func calibrationLabelKey(repository, path, contentID string) string {
	return repository + "\x00" + path + "\x00" + contentID
}

func copyCalibrationLabel(dst *calibrationFixtureCase, src calibrationFixtureCase) {
	dst.Verdict = src.Verdict
	dst.Rationale = src.Rationale
	dst.VerificationCommands = append([]string(nil), src.VerificationCommands...)
	dst.IndependentReviewVerdict = src.IndependentReviewVerdict
}

func selectCalibrationCases(population []FileEvidence, selection calibrationSelection) ([]calibrationFixtureCase, error) {
	requiredMaxRank := 80
	if len(population) < requiredMaxRank {
		return nil, fmt.Errorf("population has %d rows, want at least %d", len(population), requiredMaxRank)
	}
	distributedRanks := []int{16, 23, 30, 37, 44, 52, 59, 66, 73, 80}
	selected := map[string]bool{}
	var cases []calibrationFixtureCase
	add := func(phase string, row FileEvidence, rank int) bool {
		if selected[row.Path] {
			return false
		}
		selected[row.Path] = true
		cases = append(cases, calibrationFixtureCase{
			Phase:                   phase,
			Rank:                    rank,
			Path:                    row.Path,
			ContentID:               row.ContentID,
			BaselineScore:           row.Score,
			SeedScore:               row.SeedScore,
			StrongestRawRisk:        strongestRawRisk(row),
			NonLexicalIntersections: nonLexicalIntersectionCount(row),
			EvidenceLayers:          append([]string(nil), row.EvidenceLayers...),
		})
		return true
	}
	for rank := 1; rank <= selection.Top; rank++ {
		add("top", population[rank-1], rank)
	}
	for _, rank := range distributedRanks[:selection.Distributed] {
		add("distributed", population[rank-1], rank)
	}
	rankByPath := make(map[string]int, len(population))
	for i, row := range population {
		rankByPath[row.Path] = i + 1
	}
	byRisk := append([]FileEvidence(nil), population...)
	sort.SliceStable(byRisk, func(i, j int) bool {
		left, right := strongestRawRisk(byRisk[i]), strongestRawRisk(byRisk[j])
		if left != right {
			return left > right
		}
		return rankByPath[byRisk[i].Path] < rankByPath[byRisk[j].Path]
	})
	for _, row := range byRisk {
		if phaseCount(cases, "direct-risk") == selection.StrongestDirectRisk {
			break
		}
		add("direct-risk", row, rankByPath[row.Path])
	}
	byHash := append([]FileEvidence(nil), population...)
	sort.Slice(byHash, func(i, j int) bool {
		left, right := sha256.Sum256([]byte(byHash[i].Path)), sha256.Sum256([]byte(byHash[j].Path))
		return bytes.Compare(left[:], right[:]) < 0
	})
	for _, row := range byHash {
		if phaseCount(cases, "path-hash") == selection.LowScorePathHash {
			break
		}
		if row.Score >= 1 && row.Score <= 3 {
			add("path-hash", row, rankByPath[row.Path])
		}
	}
	for _, row := range byRisk {
		if phaseCount(cases, "expansion") == selection.ExpansionStrongestRisk {
			break
		}
		add("expansion", row, rankByPath[row.Path])
	}
	want := selection.Top + selection.Distributed + selection.StrongestDirectRisk +
		selection.LowScorePathHash + selection.ExpansionStrongestRisk
	if len(cases) != want {
		return nil, fmt.Errorf("selected %d cases, want %d", len(cases), want)
	}
	return cases, nil
}

func calibrationLabelGateFor(repositories []calibrationRepository) calibrationLabelGate {
	gate := calibrationLabelGate{
		RequiredConfirmed: 6, RequiredConfirmedRepos: 2, RequiredRefuted: 24,
		IndependentReviewComplete: true,
	}
	confirmedRepos := map[string]bool{}
	for _, repo := range repositories {
		refuted, reviewedRefuted := 0, 0
		for _, item := range repo.Cases {
			switch item.Verdict {
			case "confirmed":
				gate.Confirmed++
				confirmedRepos[repo.Name] = true
				if item.IndependentReviewVerdict != "confirmed" {
					gate.IndependentReviewComplete = false
				}
			case "refuted":
				gate.Refuted++
				refuted++
				if item.IndependentReviewVerdict == "refuted" {
					reviewedRefuted++
				}
			}
		}
		if reviewedRefuted < (refuted+4)/5 {
			gate.IndependentReviewComplete = false
		}
	}
	gate.ConfirmedRepositories = len(confirmedRepos)
	gate.Passed = gate.Confirmed >= gate.RequiredConfirmed &&
		gate.ConfirmedRepositories >= gate.RequiredConfirmedRepos &&
		gate.Refuted >= gate.RequiredRefuted &&
		gate.IndependentReviewComplete
	if gate.Passed {
		gate.Reason = "Fixed-time Git-aware reviewed cohort satisfied the label and independent-review gates."
	} else {
		gate.Reason = "Fixed-time Git-aware cohort requires more reviewed evidence; thresholds were not weakened."
	}
	return gate
}

func calibrationCandidateScreen(fixture calibrationFixture, holdoutRows []FileEvidence) calibrationScreen {
	screen := calibrationScreen{
		CandidateCount:         len(calibrationCandidates()),
		QualityEvaluatedCount:  len(calibrationCandidates()),
		Baseline:               map[string]scoreHealth{},
		RepositoryOrder:        []string{"slither", "repomap", "reliquary", "holdout"},
		QualifyingIntersection: []calibrationMapping{},
	}
	for _, repo := range fixture.Repositories {
		screen.Baseline[repo.Name] = calibrationPopulationHealth(repo.Population)
	}
	screen.Baseline["holdout"] = calibrationPopulationHealth(holdoutRows)
	repositories := map[string][]FileEvidence{}
	for _, repo := range fixture.Repositories {
		repositories[repo.Name] = repo.Population
	}
	repositories["holdout"] = holdoutRows
	for _, candidate := range calibrationCandidates() {
		result := candidateSaturation{
			Thresholds: append([]int(nil), candidate.Thresholds...), CorroborationBonus: candidate.CorroborationBonus,
		}
		eligible := true
		healthByName := map[string]scoreHealth{}
		for i, name := range screen.RepositoryOrder {
			health := calibrationCandidateHealth(repositories[name], candidate)
			healthByName[name] = health
			result.Health[i] = [2]float64{health.Top15Saturation, health.Score5Share}
			if health.Top15Saturation > 0.80 || (name == "holdout" && health.Score5Share > 0.70) {
				eligible = false
			}
		}
		screen.CandidateSaturationResults = append(screen.CandidateSaturationResults, result)
		if eligible {
			candidate.RepositoryScoreHealth = healthByName
			candidate.Rejection = "failed reviewed quality gate"
			screen.SaturationEligibleMappings = append(screen.SaturationEligibleMappings, candidate)
		}
	}
	screen.SaturationEligibleCount = len(screen.SaturationEligibleMappings)
	screen.MetricEligibleMappings = fixtureQualityEligibleMappings(fixture, false)
	screen.MetricEligibleCount = len(screen.MetricEligibleMappings)
	screen.QualityEligibleMappings = fixtureQualityEligibleMappings(fixture, true)
	screen.QualityEligibleCount = len(screen.QualityEligibleMappings)
	metricEligible := make(map[string]bool, len(screen.MetricEligibleMappings))
	for _, mapping := range screen.MetricEligibleMappings {
		metricEligible[calibrationMappingKey(mapping)] = true
	}
	qualityEligible := make(map[string]bool, len(screen.QualityEligibleMappings))
	for _, mapping := range screen.QualityEligibleMappings {
		qualityEligible[calibrationMappingKey(mapping)] = true
	}
	for i := range screen.SaturationEligibleMappings {
		key := calibrationMappingKey(screen.SaturationEligibleMappings[i])
		switch {
		case qualityEligible[key]:
			screen.SaturationEligibleMappings[i].Rejection = "qualifies; current scoring retained by policy"
		case metricEligible[key]:
			screen.SaturationEligibleMappings[i].Rejection = "failed confirmed production review-lane gate"
		default:
			screen.SaturationEligibleMappings[i].Rejection = "failed reviewed quality metrics"
		}
	}
	saturationEligible := make(map[string]bool, len(screen.SaturationEligibleMappings))
	for _, mapping := range screen.SaturationEligibleMappings {
		saturationEligible[calibrationMappingKey(mapping)] = true
	}
	for _, mapping := range screen.QualityEligibleMappings {
		if saturationEligible[calibrationMappingKey(mapping)] {
			screen.QualifyingIntersection = append(screen.QualifyingIntersection, mapping)
		}
	}
	return screen
}

func calibrationFinalQAForRefresh(fixture, prior calibrationFixture, reports map[string]Report) []calibrationFinalQA {
	priorRows := make(map[string][]FileEvidence)
	for _, repo := range prior.Repositories {
		priorRows[repo.Name] = repo.Population
	}
	var result []calibrationFinalQA
	for _, name := range []string{"slither", "repomap", "reliquary", "holdout"} {
		report := reports[name]
		qa := finalQAAggregates(report)
		qa.Name = name
		if name == "slither" {
			qa.ReportID = ""
		}
		if name != "holdout" {
			var cases []calibrationFixtureCase
			for _, repo := range fixture.Repositories {
				if repo.Name == name {
					cases = repo.Cases
					break
				}
			}
			qa.ConfirmedTop15Recall, qa.RefutedTop15Count = calibrationTop15Labels(cases)
		}
		qa.RankChanged, qa.CommonRows, qa.Top15Overlap = calibrationRankComparison(priorRows[name], report.Rows)
		if name == "holdout" {
			qa.RankChanged = 0
			qa.CommonRows = len(report.Rows)
			qa.Top15Overlap = min(15, len(report.Rows))
		}
		qa.AggregateID = finalQAAggregateID(qa)
		result = append(result, qa)
	}
	return result
}

func calibrationPopulationHealth(rows []FileEvidence) scoreHealth {
	scores := make([]int, 0, min(15, len(rows)))
	score5 := 0
	for i, row := range rows {
		if i < 15 {
			scores = append(scores, row.Score)
		}
		if row.Score == 5 {
			score5++
		}
	}
	health := scoreHealthForTopK(scores, 15)
	return scoreHealth{
		Top15Saturation: health.Saturation,
		Score5Share:     ratioForCalibration(score5, len(rows)),
	}
}

func calibrationTop15Labels(cases []calibrationFixtureCase) (*float64, *int) {
	confirmed, confirmedTop15, refutedTop15 := 0, 0, 0
	for _, item := range cases {
		switch item.Verdict {
		case "confirmed":
			confirmed++
			if item.Rank <= 15 {
				confirmedTop15++
			}
		case "refuted":
			if item.Rank <= 15 {
				refutedTop15++
			}
		}
	}
	var recall *float64
	if confirmed > 0 {
		value := float64(confirmedTop15) / float64(confirmed)
		recall = &value
	}
	return recall, &refutedTop15
}

func calibrationRankComparison(previous, current []FileEvidence) (changed, common, top15Overlap int) {
	previousRanks := make(map[string]int, len(previous))
	previousTop15 := map[string]bool{}
	for i, row := range previous {
		previousRanks[row.Path] = i + 1
		if i < 15 {
			previousTop15[row.Path] = true
		}
	}
	for i, row := range current {
		rank, ok := previousRanks[row.Path]
		if !ok {
			continue
		}
		common++
		if rank != i+1 {
			changed++
		}
		if i < 15 && previousTop15[row.Path] {
			top15Overlap++
		}
	}
	return changed, common, top15Overlap
}

func TestCalibrationReliquaryArchiveRejectedAndDetachedGitClonePasses(t *testing.T) {
	asOf := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	origin := t.TempDir()
	runGitTestCommand(t, origin, "init", "-q")
	runGitTestCommand(t, origin, "config", "user.email", "slither@example.test")
	runGitTestCommand(t, origin, "config", "user.name", "Slither Test")
	if err := os.WriteFile(filepath.Join(origin, "reliquary.go"), []byte("package reliquary\nfunc Open() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, origin, "add", "--", "reliquary.go")
	commitAt(t, origin, "2023-12-01T00:00:00Z", "add reliquary source")

	clone := filepath.Join(t.TempDir(), "reliquary")
	runCalibrationClone(t, origin, clone)
	runGitTestCommand(t, clone, "checkout", "-q", "--detach", "HEAD")
	gitReport, err := buildStableCalibrationReport(context.Background(), "reliquary", clone, asOf)
	if err != nil {
		t.Fatal(err)
	}
	want := calibrationRepository{
		SourceKind: "git", Head: gitReport.SourceState.Head, Dirty: false,
		TreeDigest: gitReport.SourceState.TreeDigest, ReportID: gitReport.ReportID,
	}
	if err := validateCalibrationSource(gitReport, want); err != nil {
		t.Fatalf("detached Git clone rejected: %v", err)
	}

	archive := t.TempDir()
	if err := os.WriteFile(filepath.Join(archive, "reliquary.go"), []byte("package reliquary\nfunc Open() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archiveReport, err := buildStableCalibrationReport(context.Background(), "reliquary", archive, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCalibrationSource(archiveReport, want); err == nil || !strings.Contains(err.Error(), "source kind") {
		t.Fatalf("source-only archive validation error = %v, want source-kind rejection", err)
	}
}

func TestCalibrationFixedTimeReportIdentityStable(t *testing.T) {
	root := os.Getenv("SLITHER_CALIBRATION_IDENTITY_REPO")
	if root == "" {
		t.Skip("set SLITHER_CALIBRATION_IDENTITY_REPO to verify a live fixed-time cohort source")
	}
	asOf := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	first, err := buildReport(context.Background(), calibrationReportOptions(root, asOf), 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildReport(context.Background(), calibrationReportOptions(root, asOf), 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReportID != second.ReportID {
		t.Fatalf("fixed-time serial report IDs differ: %s != %s", first.ReportID, second.ReportID)
	}
}

func TestCalibrationSlitherFixtureDoesNotSelfInvalidate(t *testing.T) {
	root := t.TempDir()
	runGitTestCommand(t, root, "init", "-q")
	runGitTestCommand(t, root, "config", "user.email", "slither@example.test")
	runGitTestCommand(t, root, "config", "user.name", "Slither Test")
	writeCalibrationTestFile(t, root, "main.go", "package main\n")
	writeCalibrationTestFile(t, root, calibrationFixtureRelPath, "{\"generation\":1}\n")
	runGitTestCommand(t, root, "add", "--", "main.go", calibrationFixtureRelPath)
	commitAt(t, root, "2026-07-01T00:00:00Z", "add calibration inputs")
	writeCalibrationTestFile(t, root, "main.go", "package main\n// prerequisite dirty snapshot\n")

	asOf := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	first, err := buildStableCalibrationReport(context.Background(), "slither", root, asOf)
	if err != nil {
		t.Fatal(err)
	}
	writeCalibrationTestFile(t, root, calibrationFixtureRelPath, "{\"generation\":2}\n")
	second, err := buildStableCalibrationReport(context.Background(), "slither", root, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceState.TreeDigest != second.SourceState.TreeDigest || first.ReportID != second.ReportID {
		t.Fatalf("derived fixture changed cohort identity: tree %s != %s or report %s != %s",
			first.SourceState.TreeDigest, second.SourceState.TreeDigest, first.ReportID, second.ReportID)
	}
	for _, row := range second.Rows {
		if row.Path == calibrationFixtureRelPath {
			t.Fatalf("derived fixture %q must not appear in Slither calibration population", row.Path)
		}
	}
}

func TestCalibrationRefreshRequiresGateAndTemporaryOutput(t *testing.T) {
	previous, hadPrevious := os.LookupEnv(calibrationRefreshGate)
	t.Cleanup(func() {
		if hadPrevious {
			_ = os.Setenv(calibrationRefreshGate, previous)
		} else {
			_ = os.Unsetenv(calibrationRefreshGate)
		}
	})
	_ = os.Unsetenv(calibrationRefreshGate)
	_, _, err := refreshCalibrationFixture(context.Background(), calibrationFixture{}, nil, time.Now(), filepath.Join(t.TempDir(), "fixture.json"))
	if err == nil || !strings.Contains(err.Error(), calibrationRefreshGate) {
		t.Fatalf("ungated refresh error = %v", err)
	}
	if err := os.Setenv(calibrationRefreshGate, "1"); err != nil {
		t.Fatal(err)
	}
	_, _, err = refreshCalibrationFixture(context.Background(), calibrationFixture{}, nil, time.Now(), calibrationFixturePath)
	if err == nil || !strings.Contains(err.Error(), "must not be the checked-in fixture") {
		t.Fatalf("checked-in target error = %v", err)
	}
	for _, test := range []struct {
		name string
		link func(string, string) error
		want string
	}{
		{name: "symlink", link: os.Symlink, want: "must not be a symlink"},
		{name: "hardlink", link: os.Link, want: "must not alias"},
	} {
		t.Run(test.name, func(t *testing.T) {
			alias := filepath.Join(t.TempDir(), "fixture.json")
			if err := test.link(calibrationFixturePath, alias); err != nil {
				t.Fatal(err)
			}
			_, _, err := refreshCalibrationFixture(context.Background(), calibrationFixture{}, nil, time.Now(), alias)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("alias error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCalibrationFixtureGenerate(t *testing.T) {
	if os.Getenv(calibrationRefreshGate) != "1" {
		t.Skip("set SLITHER_CALIBRATION_REFRESH=1 to generate a temporary calibration fixture")
	}
	outputPath := os.Getenv("SLITHER_CALIBRATION_REFRESH_OUT")
	asOfText := os.Getenv("SLITHER_CALIBRATION_AS_OF")
	if outputPath == "" || asOfText == "" {
		t.Fatal("SLITHER_CALIBRATION_REFRESH_OUT and SLITHER_CALIBRATION_AS_OF are required")
	}
	asOf, err := time.Parse(time.RFC3339, asOfText)
	if err != nil {
		t.Fatalf("parse SLITHER_CALIBRATION_AS_OF: %v", err)
	}
	roots := map[string]string{
		"slither":   os.Getenv("SLITHER_CALIBRATION_SLITHER_REPO"),
		"repomap":   os.Getenv("SLITHER_CALIBRATION_REPOMAP_REPO"),
		"reliquary": os.Getenv("SLITHER_CALIBRATION_RELIQUARY_REPO"),
		"holdout":   os.Getenv("SLITHER_CALIBRATION_HOLDOUT_REPO"),
	}
	prior, _ := loadCalibrationFixture(t)
	if priorPath := os.Getenv("SLITHER_CALIBRATION_PRIOR"); priorPath != "" {
		data, err := os.ReadFile(priorPath)
		if err != nil {
			t.Fatalf("read SLITHER_CALIBRATION_PRIOR: %v", err)
		}
		if err := json.Unmarshal(data, &prior); err != nil {
			t.Fatalf("parse SLITHER_CALIBRATION_PRIOR: %v", err)
		}
	}
	refreshed, newCases, err := refreshCalibrationFixture(context.Background(), prior, roots, asOf, outputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range newCases {
		t.Logf("new case %s %s %s", item.Repository, item.Case.Phase, item.Case.Path)
	}
	t.Logf("wrote %s: new_cases=%d label_gate_passed=%v", outputPath, len(newCases), refreshed.LabelGate.Passed)
}

func writeCalibrationTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCalibrationClone(t *testing.T, source, destination string) {
	t.Helper()
	cmd := exec.Command("git", "clone", "-q", "--no-local", source, destination)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone calibration source: %v: %s", err, output)
	}
}
