package slither

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaleMarkersBatchPreservesSelectedLineAgesAndFailures(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-q")
	runGitTestCommand(t, repo, "config", "user.email", "slither@example.test")
	runGitTestCommand(t, repo, "config", "user.name", "Slither Test")
	path := filepath.Join(repo, "markers.go")
	write := func(contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTestCommand(t, repo, "add", "--", "markers.go")
	}
	write("package sample\n// TODO: old\n// ordinary comment\n// FIXME: old too\n")
	commitAt(t, repo, "2023-01-01T00:00:00Z", "old markers")
	write("package sample\n// TODO: old\n// ordinary comment\n// FIXME: old too\n// HACK: recent\n")
	commitAt(t, repo, "2023-12-15T00:00:00Z", "recent marker")
	untracked := filepath.Join(repo, "untracked.go")
	if err := os.WriteFile(untracked, []byte("// TODO: missing\n// FIXME: missing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	stale, skip := staleMarkersByFile(context.Background(), repo, []string{path, untracked}, 1024, asOf)
	if got := stale["markers.go"]; got != (staleMarkerInfo{StaleCount: 2, OldestDays: 365}) {
		t.Fatalf("stale = %#v, want two old markers with 365-day age", got)
	}
	if _, ok := stale["untracked.go"]; ok {
		t.Fatalf("untracked file contributed stale markers: %#v", stale)
	}
	if skip != "marker blame command_failed:2" {
		t.Fatalf("skip = %q, want two failed marker lines", skip)
	}
}

func TestEvalIndexedEvidencePreservesRankAndUnmatchedBehavior(t *testing.T) {
	t.Parallel()
	report := outcomeTestReport("1", "2")
	report.Rows = append(report.Rows, FileEvidence{Path: "second.go", EvidenceID: outcomeTestIdentity("3"), Score: 3, ScoreProvenance: ScoreProvenance{Deterministic: 3, SelectedBy: "deterministic"}})
	refreshOutcomeTestReport(&report)
	path := filepath.Join(t.TempDir(), "report.json")
	writeEvalReport(t, path, report)
	loaded, err := readEvalReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if index, ok := loaded.rowByEvidence[report.Rows[1].EvidenceID]; !ok || index != 1 {
		t.Fatalf("second evidence index = %d, present = %t", index, ok)
	}
	result := evalResult{}
	second := OutcomeRecord{ReportID: loaded.ID, EvidenceID: report.Rows[1].EvidenceID, Rank: 2, Score: loaded.Rows[1].Row.Score, Lane: loaded.Rows[1].Lane, Verdict: "confirmed"}
	reports := map[string]evalReport{loaded.ID: loaded}
	if err := applyOutcomeRecord(&result, reports, second); err != nil || result.Outcomes.Confirmed != 1 {
		t.Fatalf("second row: result=%#v err=%v", result, err)
	}
	wrong := second
	wrong.Rank = 1
	if err := applyOutcomeRecord(&result, reports, wrong); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("wrong rank accepted: %v", err)
	}
	missing := second
	missing.EvidenceID = outcomeTestIdentity("4")
	if err := applyOutcomeRecord(&result, reports, missing); err != nil || result.Warnings.UnmatchedOutcomes != 1 || result.Calibration.Missing != 1 {
		t.Fatalf("missing evidence: result=%#v err=%v", result, err)
	}
}
