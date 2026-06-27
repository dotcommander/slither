package slither

import (
	"context"
	"os"
	"sort"
	"testing"
)

// scoutGroundTruth is a labeled precision set captured 2026-06-27 by manually
// verifying slither's top-ranked files against the reliquary source. Each file
// slither scored highly was read and classified:
//
//	confirmed = a real defect worth fixing
//	likely    = a minor guard worth considering
//	noise     = heuristic misfire (the flagged reason does not hold in code)
//
// Used to measure whether scorer changes suppress known noise while keeping the
// real concern ranked.
// Run with: SLITHER_EVAL_REPO=<reliquary checkout> go test -run TestScoutPrecisionReliquary -v ./internal/slither/
var scoutGroundTruth = []struct {
	Path    string
	Verdict string
}{
	{"primitives/vectors/pq/pq.go", "confirmed"},
	{"tools/gokart/verify.go", "likely"},
	{"tools/gokart/new_flow.go", "likely"},
	{"tools/gokart/add.go", "noise"},
	{"tools/gokart/add_flow.go", "noise"},
	{"storage/sqlite/sqlite.go", "noise"},
	{"storage/clustering/category_seed.go", "noise"},
	{"storage/postgres/pool.go", "noise"},
	{"pipeline/retrieval/fixture.go", "noise"},
	{"embed/hashing.go", "noise"},
	{"pipeline/retrieval/scorer.go", "noise"},
	{"pipeline/retrieval/tune.go", "noise"},
	{"platform/fs/fs.go", "noise"},
	{"pipeline/chunking/semantic_merge.go", "noise"},
	{"primitives/dedup/hasher.go", "noise"},
	{"contracts/workflow/workflow.go", "noise"},
	{"pipeline/ingest/pipeline.go", "noise"},
	{"pipeline/clustering/tfidf.go", "noise"},
	{"primitives/vectors/clustering/silhouette.go", "noise"},
	{"primitives/vectors/clustering/greedy.go", "noise"},
	{"primitives/vectors/clustering/hac.go", "noise"},
	{"primitives/vectors/clustering/service.go", "noise"},
	{"primitives/support/hash/hash.go", "noise"},
	{"pipeline/document/document.go", "noise"},
	{"graph/graphbuild/graphbuild.go", "noise"},
	{"primitives/textutil/locate.go", "noise"},
}

func TestScoutPrecisionReliquary(t *testing.T) {
	repo := os.Getenv("SLITHER_EVAL_REPO")
	if repo == "" {
		t.Skip("set SLITHER_EVAL_REPO to a reliquary checkout to run the scout precision eval")
	}

	// MaxBytes is required: BuildReport does not default it (the CLI's resolveReportOptions does).
	// MaxBytes==0 makes LimitReader read 0 bytes, so every file is skipped and 0 rows are returned.
	report, err := BuildReport(context.Background(), Options{Repo: repo, Top: 80, Cull: true, MaxBytes: 500_000, Days: 90})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	// Rank rows by the documented order: score desc, then seed score desc.
	rows := make([]FileEvidence, len(report.Rows))
	copy(rows, report.Rows)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		return rows[i].SeedScore > rows[j].SeedScore
	})
	rankByPath := make(map[string]int, len(rows))
	scoreByPath := make(map[string]int, len(rows))
	for i, r := range rows {
		rankByPath[r.Path] = i
		scoreByPath[r.Path] = r.Score
	}

	const topK = 15
	var (
		found, missing               int
		noiseInTopK, confirmedInTopK int
		confirmedRanks               []int
	)
	for _, gt := range scoutGroundTruth {
		rank, ok := rankByPath[gt.Path]
		if !ok {
			missing++
			t.Logf("  [not ranked] %-48s verdict=%s", gt.Path, gt.Verdict)
			continue
		}
		found++
		inTopK := rank < topK
		switch gt.Verdict {
		case "noise":
			if inTopK {
				noiseInTopK++
			}
		case "confirmed":
			confirmedRanks = append(confirmedRanks, rank)
			if inTopK {
				confirmedInTopK++
			}
		}
		t.Logf("  rank=%3d score=%d verdict=%-9s %s", rank, scoreByPath[gt.Path], gt.Verdict, gt.Path)
	}

	// Distinct scores in the top-K is a saturation indicator (1 == fully saturated).
	distinct := map[int]struct{}{}
	for i := 0; i < topK && i < len(rows); i++ {
		distinct[rows[i].Score] = struct{}{}
	}

	t.Logf("SCOUT-EVAL summary: labeled=%d found=%d missing=%d | noise_in_top%d=%d confirmed_in_top%d=%d | distinct_scores_in_top%d=%d total_rows=%d",
		len(scoutGroundTruth), found, missing, topK, noiseInTopK, topK, confirmedInTopK, topK, len(distinct), len(rows))

	// Recall guard: the one confirmed concern must stay present in the ranking.
	// Threshold-based noise assertions are added once the IDF/calibration passes land.
	if len(confirmedRanks) == 0 {
		t.Errorf("confirmed concern primitives/vectors/pq/pq.go not present in ranked rows — tuning must not bury it")
	}
}
