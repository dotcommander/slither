package slither

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutcomeDerivesTypedRecordAndAppendsOwnerOnly(t *testing.T) {
	previous := currentTime
	currentTime = func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 6, time.UTC) }
	t.Cleanup(func() { currentTime = previous })
	report := outcomeTestReport("a", "b")
	path := filepath.Join(t.TempDir(), "outcomes.jsonl")
	writer, err := outcomeWriterForPath(path)
	if err != nil {
		t.Fatal(err)
	}
	feedback := outcomeFeedback{ReportID: report.ReportID, EvidenceID: report.Rows[0].EvidenceID, Verdict: "confirmed", FilesOpened: 2, ToolCalls: 3, ReviewMS: 125}
	if err := writer.WriteOutcome(context.Background(), report, feedback); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := deriveOutcomeRecord(report, feedback.ReportID, feedback.EvidenceID, feedback.Verdict, feedback.FilesOpened, feedback.ToolCalls, feedback.ReviewMS)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if string(got) != string(want) {
		t.Fatalf("ledger bytes = %s, want %s", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode = %o, want 600", info.Mode().Perm())
	}
}

func TestOutcomeWriterRejectsUnsafeExistingTargets(t *testing.T) {
	report := outcomeTestReport("c", "d")
	feedback := outcomeFeedback{ReportID: report.ReportID, EvidenceID: report.Rows[0].EvidenceID, Verdict: "confirmed"}
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"group-readable", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, path string) {
			if err := os.Symlink("target", path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "outcomes.jsonl")
			test.setup(t, path)
			writer, err := outcomeWriterForPath(path)
			if err == nil {
				if err := writer.WriteOutcome(context.Background(), report, feedback); err == nil {
					t.Fatal("unsafe target was accepted")
				}
			}
			if err == nil {
				t.Fatal("unsafe target was accepted")
			}
		})
	}
}

func outcomeTestReport(reportSeed, evidenceSeed string) Report {
	report := Report{
		SchemaVersion: reportSchemaVersion,
		SourceState:   SourceState{Kind: "filesystem", TreeDigest: outcomeTestIdentity(reportSeed)},
		Parameters:    ReportParameters{Days: 1, MaxBytes: 1, Top: 1, PatternsID: outcomeTestIdentity(reportSeed)},
		Rows: []FileEvidence{{
			Path: "auth.go", EvidenceID: outcomeTestIdentity(evidenceSeed), Score: 4,
			EvidenceLayers: []string{"path-risk", "content-risk"}, Reasons: []string{"path:auth", "content:unsafe_query:1"},
			PathRisk: 3, ContentRisk: 4, ScoreProvenance: ScoreProvenance{Deterministic: 4, SelectedBy: "deterministic"},
		}},
	}
	refreshOutcomeTestReport(&report)
	return report
}

func refreshOutcomeTestReport(report *Report) { report.ReportID = reportIdentity(*report) }

func outcomeTestIdentity(seed string) string { return "sha256:" + strings.Repeat(seed, 64) }
