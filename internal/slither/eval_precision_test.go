package slither

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const calibrationFixturePath = "testdata/calibration_fixture.json"

type calibrationFixture struct {
	Schema             string                  `json:"schema"`
	CreatedAt          string                  `json:"created_at"`
	AsOf               string                  `json:"as_of"`
	Selection          calibrationSelection    `json:"selection"`
	LabelGate          calibrationLabelGate    `json:"label_gate"`
	Repositories       []calibrationRepository `json:"repositories"`
	SaturationScreen   calibrationScreen       `json:"saturation_screen"`
	PrivateHoldout     calibrationHoldout      `json:"private_holdout"`
	FinalQA            []calibrationFinalQA    `json:"final_qa"`
	ScoringDisposition string                  `json:"scoring_disposition"`
}

type calibrationSelection struct {
	Top                    int `json:"top"`
	Distributed            int `json:"distributed"`
	StrongestDirectRisk    int `json:"strongest_direct_risk"`
	LowScorePathHash       int `json:"low_score_path_hash"`
	ExpansionStrongestRisk int `json:"expansion_strongest_risk"`
}

type calibrationLabelGate struct {
	RequiredConfirmed         int    `json:"required_confirmed"`
	RequiredConfirmedRepos    int    `json:"required_confirmed_repositories"`
	RequiredRefuted           int    `json:"required_refuted"`
	Confirmed                 int    `json:"confirmed"`
	ConfirmedRepositories     int    `json:"confirmed_repositories"`
	Refuted                   int    `json:"refuted"`
	Passed                    bool   `json:"passed"`
	Reason                    string `json:"reason"`
	IndependentReviewComplete bool   `json:"independent_review_complete"`
}

type calibrationRepository struct {
	Name         string                   `json:"name"`
	SourceKind   string                   `json:"source_kind"`
	Head         string                   `json:"head"`
	Dirty        bool                     `json:"dirty"`
	TreeDigest   string                   `json:"tree_digest"`
	ReportID     string                   `json:"report_id"`
	ReportDigest string                   `json:"report_digest"`
	Population   []FileEvidence           `json:"population"`
	Cases        []calibrationFixtureCase `json:"cases"`
}

type calibrationFixtureCase struct {
	Phase                    string   `json:"phase"`
	Rank                     int      `json:"rank"`
	Path                     string   `json:"path"`
	ContentID                string   `json:"content_id"`
	BaselineScore            int      `json:"baseline_score"`
	SeedScore                float64  `json:"seed_score"`
	StrongestRawRisk         int      `json:"strongest_raw_risk"`
	NonLexicalIntersections  int      `json:"non_lexical_intersections"`
	EvidenceLayers           []string `json:"evidence_layers"`
	Verdict                  string   `json:"verdict"`
	Rationale                string   `json:"rationale"`
	VerificationCommands     []string `json:"verification_commands"`
	IndependentReviewVerdict string   `json:"independent_review_verdict,omitempty"`
}

type calibrationScreen struct {
	CandidateCount             int                    `json:"candidate_count"`
	SaturationEligibleCount    int                    `json:"saturation_eligible_count"`
	QualityEvaluatedCount      int                    `json:"quality_evaluated_count"`
	MetricEligibleCount        int                    `json:"metric_eligible_count"`
	QualityEligibleCount       int                    `json:"quality_eligible_count"`
	Baseline                   map[string]scoreHealth `json:"baseline"`
	SaturationEligibleMappings []calibrationMapping   `json:"saturation_eligible_mappings"`
	MetricEligibleMappings     []calibrationMapping   `json:"metric_eligible_mappings"`
	QualityEligibleMappings    []calibrationMapping   `json:"quality_eligible_mappings"`
	QualifyingIntersection     []calibrationMapping   `json:"qualifying_intersection"`
	RepositoryOrder            []string               `json:"repository_order"`
	CandidateSaturationResults []candidateSaturation  `json:"candidate_saturation_results"`
}

type scoreHealth struct {
	Top15Saturation float64 `json:"top15_saturation"`
	Score5Share     float64 `json:"score5_share"`
}

type calibrationMapping struct {
	Thresholds            []int                  `json:"thresholds"`
	CorroborationBonus    bool                   `json:"corroboration_bonus"`
	RepositoryScoreHealth map[string]scoreHealth `json:"repository_score_health"`
	Rejection             string                 `json:"rejection,omitempty"`
}

type calibrationHoldout struct {
	Head             string      `json:"head"`
	SourceKind       string      `json:"source_kind"`
	Dirty            bool        `json:"dirty"`
	TreeDigest       string      `json:"tree_digest"`
	ReportID         string      `json:"report_id"`
	ReportDigest     string      `json:"report_digest"`
	Baseline         scoreHealth `json:"baseline"`
	PathCountTracked int         `json:"path_count_tracked"`
}

type candidateSaturation struct {
	Thresholds         []int         `json:"t"`
	CorroborationBonus bool          `json:"b,omitempty"`
	Health             [4][2]float64 `json:"h"`
}

type calibrationFinalQA struct {
	Name                 string         `json:"name"`
	ReportID             string         `json:"report_id"`
	AggregateID          string         `json:"aggregate_id"`
	Rows                 int            `json:"rows"`
	Distribution         [5]int         `json:"distribution"`
	Health               scoreHealth    `json:"health"`
	Cull                 map[string]int `json:"cull"`
	ConfirmedTop15Recall *float64       `json:"confirmed_top15_recall,omitempty"`
	RefutedTop15Count    *int           `json:"refuted_top15_count,omitempty"`
	RankChanged          int            `json:"rank_changed"`
	CommonRows           int            `json:"common_rows"`
	Top15Overlap         int            `json:"top15_overlap"`
}

func loadCalibrationFixture(t *testing.T) (calibrationFixture, []byte) {
	t.Helper()
	data, err := os.ReadFile(calibrationFixturePath)
	if err != nil {
		t.Fatalf("read calibration fixture: %v", err)
	}
	var fixture calibrationFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse calibration fixture: %v", err)
	}
	return fixture, data
}

func TestCalibrationFixtureContractAndEvidenceGate(t *testing.T) {
	fixture, data := loadCalibrationFixture(t)
	assertCalibrationFixtureKeySets(t, data)
	if fixture.Schema != "slither.calibration/v1" {
		t.Fatalf("schema = %q", fixture.Schema)
	}
	asOf, err := time.Parse(time.RFC3339, fixture.AsOf)
	if err != nil || fixture.AsOf != asOf.UTC().Format(time.RFC3339) || fixture.CreatedAt != asOf.Format(time.DateOnly) {
		t.Fatalf("fixture time provenance = created_at:%q as_of:%q parse:%v", fixture.CreatedAt, fixture.AsOf, err)
	}
	if len(fixture.FinalQA) != 4 {
		t.Fatalf("final QA rows = %d, want 4", len(fixture.FinalQA))
	}
	for _, qa := range fixture.FinalQA {
		if qa.Name == "slither" {
			if qa.ReportID != "" {
				t.Fatalf("self-referential Slither report ID = %q, want empty", qa.ReportID)
			}
		} else if !validOutcomeIdentity(qa.ReportID) {
			t.Fatalf("%s final QA report ID = %q, want canonical sha256 identity", qa.Name, qa.ReportID)
		}
		if want := finalQAAggregateID(qa); qa.AggregateID != want {
			t.Fatalf("%s final QA aggregate ID = %q, want %q", qa.Name, qa.AggregateID, want)
		}
	}
	for _, forbidden := range [][]byte{[]byte(`"excerpt"`), []byte(`/Users/`), []byte(`clickmojo`)} {
		if bytes.Contains(bytes.ToLower(data), bytes.ToLower(forbidden)) {
			t.Fatalf("fixture contains forbidden private or excerpt material %q", forbidden)
		}
	}
	if len(fixture.Repositories) != 3 {
		t.Fatalf("repositories = %d, want 3 public repositories", len(fixture.Repositories))
	}

	confirmed, refuted := 0, 0
	confirmedRepos := map[string]bool{}
	for _, repo := range fixture.Repositories {
		if repo.Name == "" || repo.SourceKind != "git" || repo.Head == "" || !validOutcomeIdentity(repo.TreeDigest) ||
			!validOutcomeIdentity(repo.ReportID) || !validOutcomeIdentity(repo.ReportDigest) {
			t.Fatalf("incomplete repository provenance: %#v", repo)
		}
		if repo.Name != "slither" && repo.Dirty {
			t.Fatalf("%s public calibration repository must be clean", repo.Name)
		}
		if len(repo.Cases) != 60 {
			t.Fatalf("%s cases = %d, want initial 40 plus expansion 20", repo.Name, len(repo.Cases))
		}
		if len(repo.Population) == 0 {
			t.Fatalf("%s has no self-contained score population", repo.Name)
		}
		if want := calibrationPopulationDigest(repo.Population); repo.ReportDigest != want {
			t.Fatalf("%s population digest = %q, want %q", repo.Name, repo.ReportDigest, want)
		}
		phaseCounts := map[string]int{}
		paths := map[string]bool{}
		contentByPath := make(map[string]string, len(repo.Population))
		for _, row := range repo.Population {
			contentByPath[row.Path] = row.ContentID
		}
		repoRefuted, independentlyReviewedRefuted := 0, 0
		for _, item := range repo.Cases {
			if item.Rank <= 0 || item.Path == "" || filepath.IsAbs(item.Path) || paths[item.Path] || !validOutcomeIdentity(item.ContentID) {
				t.Fatalf("%s invalid or duplicate case: %#v", repo.Name, item)
			}
			if contentByPath[item.Path] != item.ContentID {
				t.Fatalf("%s case %s content ID = %q, population = %q",
					repo.Name, item.Path, item.ContentID, contentByPath[item.Path])
			}
			paths[item.Path] = true
			phaseCounts[item.Phase]++
			if item.BaselineScore < 1 || item.BaselineScore > 5 || item.StrongestRawRisk < 0 || item.NonLexicalIntersections < 0 {
				t.Fatalf("%s invalid score inputs: %#v", repo.Name, item)
			}
			if item.Rationale == "" || len(item.VerificationCommands) == 0 {
				t.Fatalf("%s missing review evidence: %#v", repo.Name, item)
			}
			switch item.Verdict {
			case "confirmed":
				confirmed++
				confirmedRepos[repo.Name] = true
				if item.IndependentReviewVerdict != "confirmed" {
					t.Fatalf("%s confirmed case lacks agreeing independent review: %#v", repo.Name, item)
				}
			case "refuted":
				refuted++
				repoRefuted++
				if item.IndependentReviewVerdict == "refuted" {
					independentlyReviewedRefuted++
				}
			case "unknown", "skipped":
			default:
				t.Fatalf("%s invalid verdict %q for %s", repo.Name, item.Verdict, item.Path)
			}
		}
		wantPhases := map[string]int{
			"top":         fixture.Selection.Top,
			"distributed": fixture.Selection.Distributed,
			"direct-risk": fixture.Selection.StrongestDirectRisk,
			"path-hash":   fixture.Selection.LowScorePathHash,
			"expansion":   fixture.Selection.ExpansionStrongestRisk,
		}
		for phase, want := range wantPhases {
			if phaseCounts[phase] != want {
				t.Fatalf("%s phase %s = %d, want %d", repo.Name, phase, phaseCounts[phase], want)
			}
		}
		if independentlyReviewedRefuted < (repoRefuted+4)/5 {
			t.Fatalf("%s independently reviewed refuted cases = %d of %d, want at least 20%%",
				repo.Name, independentlyReviewedRefuted, repoRefuted)
		}
	}

	if confirmed != fixture.LabelGate.Confirmed || refuted != fixture.LabelGate.Refuted || len(confirmedRepos) != fixture.LabelGate.ConfirmedRepositories {
		t.Fatalf("fixture label totals = confirmed:%d repos:%d refuted:%d, gate = %#v", confirmed, len(confirmedRepos), refuted, fixture.LabelGate)
	}
	passed := confirmed >= fixture.LabelGate.RequiredConfirmed &&
		len(confirmedRepos) >= fixture.LabelGate.RequiredConfirmedRepos &&
		refuted >= fixture.LabelGate.RequiredRefuted
	if passed != fixture.LabelGate.Passed {
		t.Fatalf("computed label gate passed=%v, fixture passed=%v", passed, fixture.LabelGate.Passed)
	}
	if !fixture.LabelGate.Passed || fixture.LabelGate.Reason == "" || !fixture.LabelGate.IndependentReviewComplete {
		t.Fatalf("evidence gate is not completely recorded: %#v", fixture.LabelGate)
	}
	if fixture.ScoringDisposition != "retain-current-scoring" {
		t.Fatalf("scoring disposition = %q", fixture.ScoringDisposition)
	}
	if fixture.PrivateHoldout.SourceKind != "git" || fixture.PrivateHoldout.Dirty ||
		!validOutcomeIdentity(fixture.PrivateHoldout.TreeDigest) ||
		!validOutcomeIdentity(fixture.PrivateHoldout.ReportID) {
		t.Fatalf("private holdout provenance is incomplete: %#v", fixture.PrivateHoldout)
	}
}

func assertCalibrationFixtureKeySets(t *testing.T, data []byte) {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode fixture object: %v", err)
	}
	assertStringKeySet(t, root, []string{
		"schema", "created_at", "as_of", "selection", "label_gate", "repositories",
		"saturation_screen", "private_holdout", "final_qa", "scoring_disposition",
	})
	var repositories []map[string]json.RawMessage
	if err := json.Unmarshal(root["repositories"], &repositories); err != nil {
		t.Fatalf("decode fixture repositories: %v", err)
	}
	for _, repository := range repositories {
		assertStringKeySet(t, repository, []string{
			"name", "source_kind", "head", "dirty", "tree_digest", "report_id",
			"report_digest", "population", "cases",
		})
		var cases []map[string]json.RawMessage
		if err := json.Unmarshal(repository["cases"], &cases); err != nil {
			t.Fatalf("decode fixture cases: %v", err)
		}
		for _, item := range cases {
			keys := []string{
				"phase", "rank", "path", "content_id", "baseline_score", "seed_score",
				"strongest_raw_risk", "non_lexical_intersections", "evidence_layers",
				"verdict", "rationale", "verification_commands",
			}
			if _, ok := item["independent_review_verdict"]; ok {
				keys = append(keys, "independent_review_verdict")
			}
			assertStringKeySet(t, item, keys)
		}
	}
	var holdout map[string]json.RawMessage
	if err := json.Unmarshal(root["private_holdout"], &holdout); err != nil {
		t.Fatalf("decode private holdout: %v", err)
	}
	assertStringKeySet(t, holdout, []string{
		"head", "source_kind", "dirty", "tree_digest", "report_id", "report_digest",
		"baseline", "path_count_tracked",
	})
}

func assertStringKeySet(t *testing.T, object map[string]json.RawMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("JSON keys = %v, want %v", got, want)
	}
}

func TestCalibrationFixtureRecordsBlockedCandidateEvaluation(t *testing.T) {
	fixture, _ := loadCalibrationFixture(t)
	const candidateCount = 2 * 1001 // C(14,4), with and without corroboration.
	if fixture.SaturationScreen.CandidateCount != candidateCount ||
		fixture.SaturationScreen.QualityEvaluatedCount != candidateCount ||
		fixture.SaturationScreen.SaturationEligibleCount != len(fixture.SaturationScreen.SaturationEligibleMappings) ||
		len(fixture.SaturationScreen.RepositoryOrder) != 4 ||
		len(fixture.SaturationScreen.CandidateSaturationResults) != candidateCount {
		t.Fatalf("candidate screen = %#v", fixture.SaturationScreen)
	}
	if fixture.PrivateHoldout.PathCountTracked != 0 || fixture.PrivateHoldout.Dirty {
		t.Fatalf("private holdout leaked tracked paths or was not clean: %#v", fixture.PrivateHoldout)
	}
	for name, health := range fixture.SaturationScreen.Baseline {
		if health.Top15Saturation < 0 || health.Top15Saturation > 1 || health.Score5Share < 0 || health.Score5Share > 1 {
			t.Fatalf("%s invalid baseline health: %#v", name, health)
		}
	}
	candidates := calibrationCandidates()
	if len(candidates) != candidateCount {
		t.Fatalf("candidate family = %d, want %d", len(candidates), candidateCount)
	}
	expectedCandidates := map[string]bool{}
	for _, candidate := range candidates {
		key := calibrationMappingKey(candidate)
		if expectedCandidates[key] {
			t.Fatalf("duplicate generated candidate %s", key)
		}
		expectedCandidates[key] = true
	}
	var reproducedSaturationEligible []calibrationMapping
	seenResults := map[string]bool{}
	publicRepositories := make(map[string]calibrationRepository, len(fixture.Repositories))
	for _, repo := range fixture.Repositories {
		publicRepositories[repo.Name] = repo
	}
	for _, result := range fixture.SaturationScreen.CandidateSaturationResults {
		mapping := calibrationMapping{Thresholds: result.Thresholds, CorroborationBonus: result.CorroborationBonus}
		key := calibrationMappingKey(mapping)
		if !expectedCandidates[key] || seenResults[key] {
			t.Fatalf("missing-family or duplicate saturation result %s", key)
		}
		seenResults[key] = true
		eligible := true
		for i, health := range result.Health {
			name := fixture.SaturationScreen.RepositoryOrder[i]
			if repo, ok := publicRepositories[name]; ok {
				want := calibrationCandidateHealth(repo.Population, mapping)
				if health != [2]float64{want.Top15Saturation, want.Score5Share} {
					t.Fatalf("%s candidate %s health = %v, want %#v", name, key, health, want)
				}
			}
			if health[0] > 0.80 || (name == "holdout" && health[1] > 0.70) {
				eligible = false
			}
		}
		if eligible {
			reproducedSaturationEligible = append(reproducedSaturationEligible, mapping)
		}
	}
	if len(seenResults) != len(expectedCandidates) ||
		!sameCalibrationMappings(reproducedSaturationEligible, fixture.SaturationScreen.SaturationEligibleMappings) {
		t.Fatalf("reproduced saturation mappings = %#v, fixture = %#v", reproducedSaturationEligible, fixture.SaturationScreen.SaturationEligibleMappings)
	}
	for _, mapping := range fixture.SaturationScreen.SaturationEligibleMappings {
		if len(mapping.Thresholds) != 4 || mapping.CorroborationBonus || mapping.Rejection == "" {
			t.Fatalf("unexpected saturation-only mapping: %#v", mapping)
		}
		for name, health := range mapping.RepositoryScoreHealth {
			if health.Top15Saturation > 0.80 {
				t.Fatalf("%s mapping %v saturation = %.3f", name, mapping.Thresholds, health.Top15Saturation)
			}
		}
	}

	metricEligible := fixtureQualityEligibleMappings(fixture, false)
	if len(metricEligible) != fixture.SaturationScreen.MetricEligibleCount ||
		!sameCalibrationMappings(metricEligible, fixture.SaturationScreen.MetricEligibleMappings) {
		t.Fatalf("reproduced metric mappings = %#v, fixture = %#v", metricEligible, fixture.SaturationScreen.MetricEligibleMappings)
	}
	qualityEligible := fixtureQualityEligibleMappings(fixture, true)
	if len(qualityEligible) != fixture.SaturationScreen.QualityEligibleCount ||
		!sameCalibrationMappings(qualityEligible, fixture.SaturationScreen.QualityEligibleMappings) {
		t.Fatalf("reproduced quality mappings = %#v, fixture = %#v", qualityEligible, fixture.SaturationScreen.QualityEligibleMappings)
	}
	saturationEligible := map[string]bool{}
	for _, mapping := range fixture.SaturationScreen.SaturationEligibleMappings {
		saturationEligible[calibrationMappingKey(mapping)] = true
	}
	var reproducedIntersection []calibrationMapping
	for _, mapping := range qualityEligible {
		if saturationEligible[calibrationMappingKey(mapping)] {
			reproducedIntersection = append(reproducedIntersection, mapping)
		}
	}
	if !sameCalibrationMappings(reproducedIntersection, fixture.SaturationScreen.QualifyingIntersection) {
		t.Fatalf("qualifying intersection = %#v, fixture = %#v",
			reproducedIntersection, fixture.SaturationScreen.QualifyingIntersection)
	}
}

func calibrationCandidateHealth(population []FileEvidence, candidate calibrationMapping) scoreHealth {
	rows := append([]FileEvidence(nil), population...)
	for i := range rows {
		rows[i].Score = calibrationCandidateScore(rows[i], candidate)
	}
	sortReportRows(rows)
	topScores := make([]int, 0, min(15, len(rows)))
	score5 := 0
	for i, row := range rows {
		if i < 15 {
			topScores = append(topScores, row.Score)
		}
		if row.Score == 5 {
			score5++
		}
	}
	health := scoreHealthForTopK(topScores, 15)
	return scoreHealth{
		Top15Saturation: health.Saturation,
		Score5Share:     ratioForCalibration(score5, len(rows)),
	}
}

func TestCalibrationFixtureSelectionReproduces(t *testing.T) {
	fixture, _ := loadCalibrationFixture(t)
	distributedRanks := []int{16, 23, 30, 37, 44, 52, 59, 66, 73, 80}
	for _, repo := range fixture.Repositories {
		selected := map[string]bool{}
		var expected []calibrationFixtureCase
		add := func(phase string, row FileEvidence, rank int) bool {
			if selected[row.Path] {
				return false
			}
			selected[row.Path] = true
			expected = append(expected, calibrationFixtureCase{Phase: phase, Path: row.Path, Rank: rank})
			return true
		}
		for rank := 1; rank <= 15; rank++ {
			add("top", repo.Population[rank-1], rank)
		}
		for _, rank := range distributedRanks {
			add("distributed", repo.Population[rank-1], rank)
		}
		byRisk := append([]FileEvidence(nil), repo.Population...)
		rankByPath := map[string]int{}
		for i, row := range repo.Population {
			rankByPath[row.Path] = i + 1
		}
		sort.SliceStable(byRisk, func(i, j int) bool {
			left, right := strongestRawRisk(byRisk[i]), strongestRawRisk(byRisk[j])
			if left != right {
				return left > right
			}
			return rankByPath[byRisk[i].Path] < rankByPath[byRisk[j].Path]
		})
		for _, row := range byRisk {
			if phaseCount(expected, "direct-risk") == 10 {
				break
			}
			add("direct-risk", row, rankByPath[row.Path])
		}
		byHash := append([]FileEvidence(nil), repo.Population...)
		sort.Slice(byHash, func(i, j int) bool {
			left, right := sha256.Sum256([]byte(byHash[i].Path)), sha256.Sum256([]byte(byHash[j].Path))
			return bytes.Compare(left[:], right[:]) < 0
		})
		for _, row := range byHash {
			if phaseCount(expected, "path-hash") == 5 {
				break
			}
			if row.Score >= 1 && row.Score <= 3 {
				add("path-hash", row, rankByPath[row.Path])
			}
		}
		for _, row := range byRisk {
			if phaseCount(expected, "expansion") == 20 {
				break
			}
			add("expansion", row, rankByPath[row.Path])
		}
		if len(expected) != len(repo.Cases) {
			t.Fatalf("%s reproduced cases = %d, fixture = %d", repo.Name, len(expected), len(repo.Cases))
		}
		for i := range expected {
			got := repo.Cases[i]
			if got.Phase != expected[i].Phase || got.Path != expected[i].Path || got.Rank != expected[i].Rank {
				t.Fatalf("%s case %d = %s rank %d %s, want %s rank %d %s",
					repo.Name, i, got.Phase, got.Rank, got.Path, expected[i].Phase, expected[i].Rank, expected[i].Path)
			}
		}
	}
}

func TestCalibrationCandidateBoundariesMonotonicityAndCorroboration(t *testing.T) {
	candidate := calibrationMapping{Thresholds: []int{4, 8, 12, 15}}
	for _, test := range []struct {
		raw  int
		want int
	}{
		{raw: 0, want: 1},
		{raw: 3, want: 1},
		{raw: 4, want: 2},
		{raw: 7, want: 2},
		{raw: 8, want: 3},
		{raw: 12, want: 4},
		{raw: 15, want: 5},
		{raw: 100, want: 5},
	} {
		row := FileEvidence{ContentRisk: test.raw}
		if got := calibrationCandidateScore(row, candidate); got != test.want {
			t.Fatalf("raw %d score = %d, want %d", test.raw, got, test.want)
		}
	}
	previous := 0
	for raw := 0; raw <= 100; raw++ {
		score := calibrationCandidateScore(FileEvidence{ContentRisk: raw}, candidate)
		if score < previous || score < 1 || score > 5 {
			t.Fatalf("raw %d score = %d after %d", raw, score, previous)
		}
		previous = score
	}

	withBonus := candidate
	withBonus.CorroborationBonus = true
	lexicalOnly := FileEvidence{ContentRisk: 4, PathRisk: 1, EvidenceLayers: []string{"content-risk", "path-risk"}}
	if got := calibrationCandidateScore(lexicalOnly, withBonus); got != 2 {
		t.Fatalf("lexical-only corroboration score = %d, want 2", got)
	}
	corroborated := lexicalOnly
	corroborated.EvidenceLayers = []string{"content-risk", "centrality", "cochange"}
	if got := calibrationCandidateScore(corroborated, withBonus); got != 3 {
		t.Fatalf("non-lexical corroboration score = %d, want 3", got)
	}

	rows := []FileEvidence{
		{Path: "seed-low.go", ContentRisk: 4, SeedScore: 1},
		{Path: "score-high.go", ContentRisk: 8, SeedScore: 0},
		{Path: "seed-high.go", ContentRisk: 4, SeedScore: 2},
	}
	for i := range rows {
		rows[i].Score = calibrationCandidateScore(rows[i], candidate)
	}
	sortReportRows(rows)
	if rows[0].Path != "score-high.go" || rows[1].Path != "seed-high.go" || rows[2].Path != "seed-low.go" {
		t.Fatalf("score-first stable ordering = %v", []string{rows[0].Path, rows[1].Path, rows[2].Path})
	}
}

func phaseCount(cases []calibrationFixtureCase, phase string) int {
	count := 0
	for _, item := range cases {
		if item.Phase == phase {
			count++
		}
	}
	return count
}

func fixtureQualityEligibleMappings(fixture calibrationFixture, requireReviewLanes bool) []calibrationMapping {
	baseline := make(map[string]calibrationQuality, len(fixture.Repositories))
	for _, repo := range fixture.Repositories {
		baseline[repo.Name] = calibrationQualityFor(repo, nil)
	}
	var eligible []calibrationMapping
	for _, candidate := range calibrationCandidates() {
		passed := true
		for _, repo := range fixture.Repositories {
			current := calibrationQualityFor(repo, &candidate)
			before := baseline[repo.Name]
			if current.ConfirmedTop15Recall < before.ConfirmedTop15Recall ||
				current.ConfirmedMeanReciprocalRank < before.ConfirmedMeanReciprocalRank ||
				current.RefutedTop15Count > before.RefutedTop15Count ||
				(requireReviewLanes && !current.ConfirmedProductionInReviewLanes) {
				passed = false
				break
			}
		}
		if passed {
			eligible = append(eligible, candidate)
		}
	}
	return eligible
}

type calibrationQuality struct {
	ConfirmedTop15Recall             float64
	ConfirmedMeanReciprocalRank      float64
	RefutedTop15Count                int
	ConfirmedProductionInReviewLanes bool
}

func calibrationQualityFor(repo calibrationRepository, candidate *calibrationMapping) calibrationQuality {
	rows := append([]FileEvidence(nil), repo.Population...)
	if candidate != nil {
		for i := range rows {
			rows[i].Score = calibrationCandidateScore(rows[i], *candidate)
		}
	}
	sortReportRows(rows)
	ranks := make(map[string]int, len(rows))
	for i, row := range rows {
		ranks[row.Path] = i + 1
	}

	confirmed, confirmedTop15, reciprocalRank, refutedTop15 := 0, 0, 0.0, 0
	confirmedProduction := map[string]bool{}
	for _, item := range repo.Cases {
		rank := ranks[item.Path]
		switch item.Verdict {
		case "confirmed":
			confirmed++
			if rank <= 15 {
				confirmedTop15++
			}
			reciprocalRank += 1 / float64(rank)
			if !isGeneratedOrReportPath(item.Path) && !isDocumentationPath(item.Path) && !isTestPath(item.Path) {
				confirmedProduction[item.Path] = true
			}
		case "refuted":
			if rank <= 15 {
				refutedTop15++
			}
		}
	}

	dispositions := classifyCullDispositions(rows)
	for i, disposition := range dispositions {
		if !confirmedProduction[rows[i].Path] {
			continue
		}
		if disposition.Decision == CullDecisionKeptForPremium || disposition.Decision == CullDecisionAlternates {
			delete(confirmedProduction, rows[i].Path)
		}
	}
	quality := calibrationQuality{
		RefutedTop15Count:                refutedTop15,
		ConfirmedProductionInReviewLanes: len(confirmedProduction) == 0,
	}
	if confirmed > 0 {
		quality.ConfirmedTop15Recall = float64(confirmedTop15) / float64(confirmed)
		quality.ConfirmedMeanReciprocalRank = reciprocalRank / float64(confirmed)
	}
	return quality
}

func calibrationCandidateScore(row FileEvidence, candidate calibrationMapping) int {
	score := 1
	strongest := strongestRawRisk(row)
	for _, threshold := range candidate.Thresholds {
		if strongest >= threshold {
			score++
		}
	}
	if candidate.CorroborationBonus && nonLexicalIntersectionCount(row) >= 2 {
		score++
	}
	return min(score, 5)
}

func strongestRawRisk(row FileEvidence) int {
	strongest := 0
	for _, risk := range []int{
		row.PathRisk, row.ContentRisk, row.SmellRisk, row.HotspotRisk,
		row.SDKDXRisk, row.UnknownsRisk, row.EnvContractRisk, row.WorkflowSecurityRisk,
		row.MigrationSafetyRisk, row.ContainerBuildRisk, row.KubernetesSecurityRisk,
		row.TerraformSecurityRisk, row.OpenAPIContractRisk, row.CORSSecurityRisk,
		row.CookieSecurityRisk, row.DependencyHealthRisk, row.CentralityRisk,
		row.CochangeRisk, row.OwnershipRisk, row.FlakeRisk, row.OracleRisk,
		row.StaleMarkerRisk,
	} {
		strongest = max(strongest, risk)
	}
	return strongest
}

func nonLexicalIntersectionCount(row FileEvidence) int {
	count := 0
	for _, layer := range row.EvidenceLayers {
		if !isLexicalLayer(layer) && layer != "low-signal" {
			count++
		}
	}
	return count
}

func calibrationCandidates() []calibrationMapping {
	pool := []int{2, 3, 4, 6, 8, 10, 12, 15, 18, 24, 30, 36, 48, 60}
	candidates := make([]calibrationMapping, 0, 2002)
	for a := 0; a < len(pool)-3; a++ {
		for b := a + 1; b < len(pool)-2; b++ {
			for c := b + 1; c < len(pool)-1; c++ {
				for d := c + 1; d < len(pool); d++ {
					thresholds := []int{pool[a], pool[b], pool[c], pool[d]}
					for _, bonus := range []bool{false, true} {
						candidates = append(candidates, calibrationMapping{
							Thresholds:         append([]int(nil), thresholds...),
							CorroborationBonus: bonus,
						})
					}
				}
			}
		}
	}
	return candidates
}

func sameCalibrationMappings(left, right []calibrationMapping) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if calibrationMappingKey(left[i]) != calibrationMappingKey(right[i]) {
			return false
		}
	}
	return true
}

func calibrationMappingKey(mapping calibrationMapping) string {
	parts := make([]string, 0, len(mapping.Thresholds)+1)
	for _, threshold := range mapping.Thresholds {
		parts = append(parts, itoa(threshold))
	}
	parts = append(parts, map[bool]string{false: "no-bonus", true: "bonus"}[mapping.CorroborationBonus])
	return strings.Join(parts, ",")
}

func TestCalibrationFixtureLiveRefresh(t *testing.T) {
	fixture, _ := loadCalibrationFixture(t)
	asOf, err := time.Parse(time.RFC3339, fixture.AsOf)
	if err != nil {
		t.Fatalf("parse calibration as_of: %v", err)
	}
	roots := map[string]string{
		"slither":   os.Getenv("SLITHER_CALIBRATION_SLITHER_REPO"),
		"repomap":   os.Getenv("SLITHER_CALIBRATION_REPOMAP_REPO"),
		"reliquary": os.Getenv("SLITHER_CALIBRATION_RELIQUARY_REPO"),
		"holdout":   os.Getenv("SLITHER_CALIBRATION_HOLDOUT_REPO"),
	}
	for _, root := range roots {
		if root == "" {
			t.Skip("set all SLITHER_CALIBRATION_*_REPO variables to verify the frozen cohort against live public sources")
		}
	}
	reports := make(map[string]Report, len(roots))
	for _, repo := range fixture.Repositories {
		root := roots[repo.Name]
		report, err := buildStableCalibrationReport(context.Background(), repo.Name, root, asOf)
		if err != nil {
			t.Fatalf("%s rebuild calibration report: %v", repo.Name, err)
		}
		reports[repo.Name] = report
		if err := validateCalibrationSource(report, repo); err != nil {
			t.Fatalf("%s provenance: %v", repo.Name, err)
		}
		rows := make(map[string]FileEvidence, len(report.Rows))
		for _, row := range report.Rows {
			rows[row.Path] = row
		}
		for _, item := range repo.Cases {
			row, ok := rows[item.Path]
			if !ok || row.ContentID != item.ContentID {
				t.Fatalf("%s case %s content ID = %q present=%v, want %q",
					repo.Name, item.Path, row.ContentID, ok, item.ContentID)
			}
		}
	}
	holdout, err := buildStableCalibrationReport(context.Background(), "holdout", roots["holdout"], asOf)
	if err != nil {
		t.Fatalf("holdout rebuild calibration report: %v", err)
	}
	reports["holdout"] = holdout
	if err := validateCalibrationHoldoutSource(holdout, fixture.PrivateHoldout); err != nil {
		t.Fatalf("holdout provenance: %v", err)
	}
	for _, want := range fixture.FinalQA {
		report := reports[want.Name]
		t.Logf("%s final QA report identity %s", want.Name, report.ReportID)
		got := finalQAAggregates(report)
		if (want.Name != "slither" && got.ReportID != want.ReportID) ||
			got.Rows != want.Rows || got.Distribution != want.Distribution ||
			got.Health != want.Health || !equalIntMap(got.Cull, want.Cull) {
			t.Fatalf("%s final QA aggregate mismatch:\ngot  %#v\nwant %#v", want.Name, got, want)
		}
	}
	if outputPath := os.Getenv("SLITHER_CALIBRATION_REFRESH_OUT"); outputPath != "" {
		refreshed, newCases, err := refreshCalibrationFixture(context.Background(), fixture, roots, asOf, outputPath)
		if err != nil {
			t.Fatalf("write gated refresh: %v", err)
		}
		t.Logf("wrote gated refresh to %s with %d newly selected cases; label gate passed=%v",
			outputPath, len(newCases), refreshed.LabelGate.Passed)
	}
}

func TestScoringContractFinalQAReadback(t *testing.T) {
	fixture, _ := loadCalibrationFixture(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "scoring-contract.md"))
	if err != nil {
		t.Fatalf("read scoring contract: %v", err)
	}
	document := string(data)
	for _, qa := range fixture.FinalQA {
		confirmed := "unlabeled"
		refuted := "unlabeled"
		if qa.ConfirmedTop15Recall != nil {
			confirmed = fmt.Sprintf("%.3f", *qa.ConfirmedTop15Recall)
		}
		if qa.RefutedTop15Count != nil {
			refuted = itoa(*qa.RefutedTop15Count)
		}
		if qa.Name == "slither" && qa.ConfirmedTop15Recall == nil {
			confirmed = "no confirmed labels"
		}
		line := fmt.Sprintf("| %s | %d / %d / %d / %d / %d | %.3f | %.3f | %d / %d | %s | %s | %d / %d |",
			displayQAName(qa.Name), qa.Distribution[0], qa.Distribution[1], qa.Distribution[2], qa.Distribution[3], qa.Distribution[4],
			qa.Health.Top15Saturation, qa.Health.Score5Share, qa.Cull["kept"], qa.Cull["alternates"],
			confirmed, refuted, qa.RankChanged, qa.CommonRows)
		if !strings.Contains(document, line) {
			t.Fatalf("scoring contract missing fixture-derived QA row:\n%s", line)
		}
	}
}

func finalQAAggregates(report Report) calibrationFinalQA {
	if report.CullLedger == nil {
		ledger := BuildCullLedger(report)
		report.CullLedger = &ledger
	}
	qa := calibrationFinalQA{
		ReportID: report.ReportID,
		Rows:     len(report.Rows),
		Cull:     map[string]int{},
	}
	for _, row := range report.Rows {
		if row.Score >= 1 && row.Score <= 5 {
			qa.Distribution[row.Score-1]++
		}
	}
	scores := make([]int, 0, min(15, len(report.Rows)))
	score5 := 0
	for i, row := range report.Rows {
		if i < 15 {
			scores = append(scores, row.Score)
		}
		if row.Score == 5 {
			score5++
		}
	}
	health := scoreHealthForTopK(scores, 15)
	qa.Health = scoreHealth{
		Top15Saturation: health.Saturation,
		Score5Share:     ratioForCalibration(score5, len(report.Rows)),
	}
	if report.CullLedger != nil {
		qa.Cull["kept"] = report.CullLedger.KeptForPremium.Count
		qa.Cull["alternates"] = report.CullLedger.Alternates.Count
		qa.Cull["generated"] = report.CullLedger.Generated.Count
		qa.Cull["documentation"] = report.CullLedger.Documentation.Count
		qa.Cull["test_only"] = report.CullLedger.TestOnly.Count
		qa.Cull["low_signal"] = report.CullLedger.LowSignal.Count
		qa.Cull["duplicate"] = report.CullLedger.Duplicate.Count
		qa.Cull["needs_evidence"] = report.CullLedger.NeedsEvidence.Count
	}
	return qa
}

func finalQAAggregateID(qa calibrationFinalQA) string {
	return sha256Identity("slither.calibration-final-qa/v1", struct {
		Name                 string         `json:"name"`
		Rows                 int            `json:"rows"`
		Distribution         [5]int         `json:"distribution"`
		Health               scoreHealth    `json:"health"`
		Cull                 map[string]int `json:"cull"`
		ConfirmedTop15Recall *float64       `json:"confirmed_top15_recall,omitempty"`
		RefutedTop15Count    *int           `json:"refuted_top15_count,omitempty"`
		RankChanged          int            `json:"rank_changed"`
		CommonRows           int            `json:"common_rows"`
		Top15Overlap         int            `json:"top15_overlap"`
	}{
		Name:                 qa.Name,
		Rows:                 qa.Rows,
		Distribution:         qa.Distribution,
		Health:               qa.Health,
		Cull:                 qa.Cull,
		ConfirmedTop15Recall: qa.ConfirmedTop15Recall,
		RefutedTop15Count:    qa.RefutedTop15Count,
		RankChanged:          qa.RankChanged,
		CommonRows:           qa.CommonRows,
		Top15Overlap:         qa.Top15Overlap,
	})
}

func ratioForCalibration(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func equalIntMap(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func displayQAName(name string) string {
	if name == "holdout" {
		return "private holdout"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
