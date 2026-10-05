package slither

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyChurnProfile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		churn      int
		after      int
		touches    int
		fixTouches int
		want       string
	}{
		{name: "single touch is stable", churn: 400, after: 0, touches: 1, fixTouches: 0, want: ChurnProfileStable},
		{name: "born big and quiet is creation-dominated", churn: 832, after: 36, touches: 2, fixTouches: 0, want: ChurnProfileCreationDominated},
		{name: "creation within twenty percent stays creation-dominated", churn: 200, after: 40, touches: 2, fixTouches: 0, want: ChurnProfileCreationDominated},
		{name: "repeated fixes are the problem-spot shape", churn: 90, after: 60, touches: 5, fixTouches: 3, want: ChurnProfileRecurringFixes},
		{name: "one large rewrite is reworked", churn: 180, after: 140, touches: 2, fixTouches: 1, want: ChurnProfileReworked},
		{name: "steady change is evolving", churn: 80, after: 50, touches: 4, fixTouches: 1, want: ChurnProfileEvolving},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyChurnProfile(tt.churn, tt.after, tt.touches, tt.fixTouches); got != tt.want {
				t.Fatalf("classifyChurnProfile(%d, %d, %d, %d) = %q, want %q", tt.churn, tt.after, tt.touches, tt.fixTouches, got, tt.want)
			}
		})
	}
}

func TestSeedScoreUsesPostCreationChurnForPressure(t *testing.T) {
	t.Parallel()
	bornBig := FileEvidence{Path: "born.go", Lines: 400, Churn: 400, ChurnAfterCreation: 10, CommitTouches: 2}
	problemSpot := FileEvidence{Path: "spot.go", Lines: 400, Churn: 400, ChurnAfterCreation: 390, CommitTouches: 9}
	big, spot := seedScore(bornBig), seedScore(problemSpot)
	if big >= spot {
		t.Fatalf("seedScore(born-big) = %.2f >= seedScore(problem-spot) = %.2f; pressure must come from post-creation churn", big, spot)
	}
	// A file whose creation predates the window keeps full churn as pressure.
	older := FileEvidence{Path: "older.go", Lines: 400, Churn: 400, ChurnAfterCreation: 400, CommitTouches: 9}
	if olderScore := seedScore(older); olderScore < spot {
		t.Fatalf("seedScore(pre-window creation) = %.2f < %.2f; identical pressure must score identically", olderScore, spot)
	}
}

func TestChurnStatsByFileDecomposesCreation(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.com")
	runGitTestCommand(t, repo, "config", "user.name", "Test")

	writeCommit := func(name, path, body, message string) {
		t.Helper()
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitTestCommand(t, repo, "add", "--", path)
		runGitTestCommand(t, repo, "commit", "-q", "-m", message)
	}

	// born_big.go: created at 200 lines, then one 6-line fix visit.
	writeCommit("create-born", "born_big.go", strings.Repeat("line\n", 200), "feat: born big")
	writeCommit("fix-born", "born_big.go", strings.Repeat("line\n", 200)+"patch1\npatch2\npatch3\n", "fix: small correction")

	// reworked.go: created at 40 lines, then one visit replacing 60 lines.
	writeCommit("create-reworked", "reworked.go", strings.Repeat("old\n", 40), "feat: reworked base")
	writeCommit("rework", "reworked.go", strings.Repeat("new\n", 100), "refactor: rewrite in place")

	// untouched.go: created once, never touched again.
	writeCommit("create-untouched", "untouched.go", strings.Repeat("u\n", 80), "feat: stable file")

	stats, skip := churnStatsByFile(context.Background(), repo, gitHistoryWindow(90, currentTime()))
	if skip != "" {
		t.Fatalf("churnStatsByFile skip = %q, want empty", skip)
	}

	born := stats["born_big.go"]
	if born.Total != 203 || born.AfterCreate != 3 || born.Touches != 2 {
		t.Fatalf("born_big.go stats = %+v, want Total=203 AfterCreate=3 Touches=2", born)
	}
	if profile := classifyChurnProfile(born.Total, born.AfterCreate, born.Touches, 0); profile != ChurnProfileCreationDominated {
		t.Fatalf("born_big.go profile = %q, want %q", profile, ChurnProfileCreationDominated)
	}

	reworked := stats["reworked.go"]
	if reworked.Total != 180 || reworked.AfterCreate != 140 || reworked.Touches != 2 {
		t.Fatalf("reworked.go stats = %+v, want Total=180 AfterCreate=140 Touches=2", reworked)
	}
	if profile := classifyChurnProfile(reworked.Total, reworked.AfterCreate, reworked.Touches, 0); profile != ChurnProfileReworked {
		t.Fatalf("reworked.go profile = %q, want %q", profile, ChurnProfileReworked)
	}

	untouched := stats["untouched.go"]
	if untouched.Total != 80 || untouched.AfterCreate != 0 || untouched.Touches != 1 {
		t.Fatalf("untouched.go stats = %+v, want Total=80 AfterCreate=0 Touches=1", untouched)
	}
	if profile := classifyChurnProfile(untouched.Total, untouched.AfterCreate, untouched.Touches, 0); profile != ChurnProfileStable {
		t.Fatalf("untouched.go profile = %q, want %q", profile, ChurnProfileStable)
	}
}
