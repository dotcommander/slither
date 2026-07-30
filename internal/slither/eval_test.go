package slither

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestEvalStreamsMultiReportOutcomesAndPartialTail(t *testing.T) {
	dir := t.TempDir()
	first := outcomeTestReport("1", "2")
	second := outcomeTestReport("3", "4")
	second.Rows[0].Path, second.Rows[0].Score = "docs/guide.md", 3
	second.Rows[0].ScoreProvenance = ScoreProvenance{Deterministic: 3, SelectedBy: "deterministic"}
	refreshOutcomeTestReport(&second)
	firstPath, secondPath := filepath.Join(dir, "first.json"), filepath.Join(dir, "second.json")
	writeEvalReport(t, firstPath, first)
	writeEvalReport(t, secondPath, second)
	confirmed, err := deriveOutcomeRecord(first, first.ReportID, first.Rows[0].EvidenceID, "confirmed", 2, 3, 125)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := deriveOutcomeRecord(second, second.ReportID, second.Rows[0].EvidenceID, "unknown", 1, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	unmatched := confirmed
	unmatched.ReportID, unmatched.EvidenceID, unmatched.Verdict = outcomeTestIdentity("5"), outcomeTestIdentity("6"), "refuted"
	outcomesPath := filepath.Join(dir, "outcomes.jsonl")
	writeOutcomeLedger(t, outcomesPath, confirmed, unknown, unmatched)
	if err := os.WriteFile(outcomesPath, append(readTestFile(t, outcomesPath), []byte(`{"schema":"slither.outcome/v1"`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	before := fileHash(t, outcomesPath)
	result, err := evaluateOutcomeFiles(context.Background(), outcomesPath, []string{firstPath, secondPath})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reports != 2 || result.Rows != 2 || result.Outcomes.Confirmed != 1 || result.Outcomes.Unknown != 1 || result.ContextCost.ReviewMS != 175 {
		t.Fatalf("evaluation = %#v", result)
	}
	if result.Calibration.Labeled != 2 || result.Calibration.Found != 1 || result.Calibration.Missing != 1 || result.Calibration.ConfirmedTopKRecall != 1 || result.Calibration.LabeledCoverage != 0.5 {
		t.Fatalf("calibration = %#v", result.Calibration)
	}
	if result.Warnings.PartialTrailingRecords != 1 || result.Warnings.UnmatchedOutcomes != 1 {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	if after := fileHash(t, outcomesPath); after != before {
		t.Fatal("evaluation modified outcome input")
	}
	var stdout bytes.Buffer
	if err := runEval(context.Background(), []string{"--outcomes", outcomesPath, "--report", firstPath, "--report", secondPath, "--json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	assertFixtureBytes(t, "eval-v1.json", stdout.Bytes())
	outputPath := filepath.Join(dir, "eval.json")
	if err := runEval(context.Background(), []string{"--outcomes", outcomesPath, "--report", firstPath, "--report", secondPath, "--json", "--out", outputPath}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, outputPath); string(got) != stdout.String() {
		t.Fatalf("file output differs from stdout\nfile=%s\nstdout=%s", got, stdout.String())
	}
}

func TestEvalRejectsOutputAliasesAndReportTampering(t *testing.T) {
	dir := t.TempDir()
	report := outcomeTestReport("9", "a")
	reportPath := filepath.Join(dir, "report.json")
	writeEvalReport(t, reportPath, report)
	record, err := deriveOutcomeRecord(report, report.ReportID, report.Rows[0].EvidenceID, "confirmed", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := filepath.Join(dir, "outcomes.jsonl")
	writeOutcomeLedger(t, outcomes, record)
	if err := rejectEvalOutputAlias(outcomes, []string{outcomes, reportPath}); err == nil {
		t.Fatal("outcome output alias accepted")
	}
	link := filepath.Join(dir, "report-link.json")
	if err := os.Link(reportPath, link); err != nil {
		t.Fatal(err)
	}
	if err := rejectEvalOutputAlias(link, []string{outcomes, reportPath}); err == nil {
		t.Fatal("hard-link output alias accepted")
	}
	data := readTestFile(t, reportPath)
	data = bytes.Replace(data, []byte(`"score": 4`), []byte(`"score": 5`), 1)
	if err := os.WriteFile(reportPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEvalReport(reportPath); err == nil {
		t.Fatal("tampered report was accepted")
	}
	report = outcomeTestReport("d", "e")
	report.Rows = append(report.Rows, FileEvidence{Path: "second.go", EvidenceID: outcomeTestIdentity("f"), Score: 3, ScoreProvenance: ScoreProvenance{Deterministic: 3, SelectedBy: "deterministic"}})
	refreshOutcomeTestReport(&report)
	writeEvalReport(t, reportPath, report)
	var envelope map[string]any
	if err := json.Unmarshal(readTestFile(t, reportPath), &envelope); err != nil {
		t.Fatal(err)
	}
	rows := envelope["rows"].([]any)
	rows[0], rows[1] = rows[1], rows[0]
	reordered, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, reordered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEvalReport(reportPath); err == nil {
		t.Fatal("reordered report was accepted")
	}
}

func TestEvalTrailingRecordAndLineLimits(t *testing.T) {
	dir := t.TempDir()
	report := outcomeTestReport("b", "c")
	reportPath := filepath.Join(dir, "report.json")
	writeEvalReport(t, reportPath, report)
	record, err := deriveOutcomeRecord(report, report.ReportID, report.Rows[0].EvidenceID, "confirmed", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		tail string
		ok   bool
	}{
		{"partial", `{"schema":"`, true},
		{"complete malformed", `{"schema":}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcomes := filepath.Join(t.TempDir(), "outcomes.jsonl")
			writeOutcomeLedger(t, outcomes, record)
			if err := os.WriteFile(outcomes, append(readTestFile(t, outcomes), []byte(test.tail)...), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := evaluateOutcomeFiles(context.Background(), outcomes, []string{reportPath})
			if (err == nil) != test.ok {
				t.Fatalf("error = %v, ok = %t", err, test.ok)
			}
		})
	}
	exact := bytes.Repeat([]byte("x"), maxOutcomeRecordLen-1)
	line, _, oversized, err := readOutcomeLine(bufio.NewReader(bytes.NewReader(append(exact, '\n'))))
	if err != nil || oversized || len(line) != len(exact) {
		t.Fatalf("exact cap = len=%d oversized=%t err=%v", len(line), oversized, err)
	}
	_, _, oversized, _ = readOutcomeLine(bufio.NewReader(bytes.NewReader(append(exact, 'x', '\n'))))
	if !oversized {
		t.Fatal("oversized record was accepted")
	}
}

func TestEvalZeroDenominatorAndStrictCLI(t *testing.T) {
	dir := t.TempDir()
	report := Report{SchemaVersion: reportSchemaVersion, SourceState: SourceState{Kind: "filesystem", TreeDigest: outcomeTestIdentity("1")}, Parameters: ReportParameters{Days: 1, MaxBytes: 1, Top: 1, PatternsID: outcomeTestIdentity("2")}, Rows: []FileEvidence{}}
	report.ReportID = reportIdentity(report)
	reportPath := filepath.Join(dir, "empty.json")
	writeEvalReport(t, reportPath, report)
	outcomes := filepath.Join(dir, "outcomes.jsonl")
	if err := os.WriteFile(outcomes, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := evaluateOutcomeFiles(context.Background(), outcomes, []string{reportPath})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 0 || result.Calibration.NoiseTopKRate != 0 || result.Calibration.TopKScoreSaturation != 1 {
		t.Fatalf("zero result = %#v", result)
	}
	for _, args := range [][]string{{"--json"}, {"--outcomes", outcomes, "--report", reportPath}, {"--outcomes", outcomes, "--outcomes", outcomes, "--report", reportPath, "--json"}, {"--outcomes", outcomes, "--report", reportPath, "--json", "extra"}} {
		if _, err := resolveEvalOptions(args); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
}

func TestEvalRejectsInvalidUnmatchedScoreAndShortStdout(t *testing.T) {
	dir := t.TempDir()
	report := outcomeTestReport("a", "b")
	reportPath := filepath.Join(dir, "report.json")
	writeEvalReport(t, reportPath, report)
	record, err := deriveOutcomeRecord(report, report.ReportID, report.Rows[0].EvidenceID, "confirmed", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := filepath.Join(dir, "outcomes.jsonl")
	invalid := record
	invalid.ReportID, invalid.EvidenceID, invalid.Score = outcomeTestIdentity("c"), outcomeTestIdentity("d"), 0
	writeOutcomeLedger(t, outcomes, invalid)
	if _, err := evaluateOutcomeFiles(context.Background(), outcomes, []string{reportPath}); err == nil {
		t.Fatal("invalid unmatched score was accepted")
	}
	writeOutcomeLedger(t, outcomes, record)
	if err := runEval(context.Background(), []string{"--outcomes", outcomes, "--report", reportPath, "--json"}, shortEvalWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short stdout write = %v", err)
	}
}

type shortEvalWriter struct{}

func (shortEvalWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestEvalRejectsDuplicateReportIdentityAndIntegrityMismatch(t *testing.T) {
	dir := t.TempDir()
	report := outcomeTestReport("7", "8")
	first, second := filepath.Join(dir, "one.json"), filepath.Join(dir, "two.json")
	writeEvalReport(t, first, report)
	writeEvalReport(t, second, report)
	outcomes := filepath.Join(dir, "outcomes.jsonl")
	record, err := deriveOutcomeRecord(report, report.ReportID, report.Rows[0].EvidenceID, "confirmed", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeOutcomeLedger(t, outcomes, record)
	if _, err := evaluateOutcomeFiles(context.Background(), outcomes, []string{first, second}); err == nil {
		t.Fatal("duplicate report identity was accepted")
	}
	record.Score++
	writeOutcomeLedger(t, outcomes, record)
	if _, err := evaluateOutcomeFiles(context.Background(), outcomes, []string{first}); err == nil {
		t.Fatal("integrity mismatch was accepted")
	}
}

func writeEvalReport(t *testing.T, path string, report Report) {
	t.Helper()
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeOutcomeLedger(t *testing.T, path string, records ...OutcomeRecord) {
	t.Helper()
	var data []byte
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fileHash(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	return sha256.Sum256(readTestFile(t, path))
}
