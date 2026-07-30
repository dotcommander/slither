package slither

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentProtocolFramingBoundaries(t *testing.T) {
	repo := agentTestRepo(t)
	oversized := strings.Repeat("x", maxAgentRequestBytes+1)
	input := `{"schema":"slither.agent/v1","id":"bad-1","op":"hello","extra":true}` + "\r\n" + oversized + "\n" + `{"schema":"slither.agent/v1","id":"hello-1","op":"hello"}`
	responses, err := runAgentTest(t, repo, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 3 {
		t.Fatalf("responses = %d, want 3", len(responses))
	}
	if code := agentResponseCode(responses[0]); code != "invalid_request" {
		t.Fatalf("first error = %q", code)
	}
	if code := agentResponseCode(responses[1]); code != "request_too_large" {
		t.Fatalf("oversized error = %q", code)
	}
	if ok, _ := responses[2]["ok"].(bool); !ok {
		t.Fatalf("hello response = %#v", responses[2])
	}
}

func TestAgentOperationsAndOfflineContract(t *testing.T) {
	repo := agentTestRepo(t)
	report, err := BuildReport(context.Background(), agentReportOptions(repo))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) == 0 {
		t.Fatal("fixture report has no rows")
	}
	configDir := filepath.Join(t.TempDir(), "config")
	previousConfigDir := userConfigDir
	userConfigDir = func() (string, error) { return configDir, nil }
	t.Cleanup(func() { userConfigDir = previousConfigDir })
	input := strings.Join([]string{
		`{"schema":"slither.agent/v1","id":"hello","op":"hello"}`,
		`{"schema":"slither.agent/v1","id":"scan","op":"scan"}`,
		`{"schema":"slither.agent/v1","id":"query","op":"query","target_ids":["` + report.Rows[0].ID + `"],"focus":"TODO","limit":2}`,
		`{"schema":"slither.agent/v1","id":"context","op":"context","target_ids":["` + report.Rows[0].ID + `"],"budget_bytes":1}`,
	}, "\n")
	responses, err := runAgentTest(t, repo, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent created config state: %v", err)
	}
	if got := responses[0]["result"].(map[string]any)["feedback_enabled"]; got != false {
		t.Fatalf("feedback enabled = %#v, want false", got)
	}
	scan := responses[1]
	if scan["report_id"] != report.ReportID {
		t.Fatalf("scan report id = %v, want %s", scan["report_id"], report.ReportID)
	}
	query := responses[2]["result"].(map[string]any)
	if query["count"] != float64(1) || query["truncated"] != false {
		t.Fatalf("query result = %#v", query)
	}
	if code := agentResponseCode(responses[3]); code != "budget_too_small" {
		t.Fatalf("context code = %q", code)
	}
}

func TestAgentRecoveryAndFeedbackSeam(t *testing.T) {
	repo := agentTestRepo(t)
	report, err := BuildReport(context.Background(), agentReportOptions(repo))
	if err != nil {
		t.Fatal(err)
	}
	var feedback outcomeFeedback
	writer := outcomeWriterFunc(func(_ context.Context, gotReport Report, record outcomeFeedback) error {
		if gotReport.ReportID != report.ReportID {
			t.Fatalf("feedback report = %q, want %q", gotReport.ReportID, report.ReportID)
		}
		feedback = record
		return nil
	})
	input := strings.Join([]string{
		`{"schema":"slither.agent/v1","id":"wrong","op":"missing"}`,
		`{"schema":"slither.agent/v1","id":"feedback","op":"feedback","report_id":"` + report.ReportID + `","evidence_id":"` + report.Rows[0].EvidenceID + `","verdict":"confirmed","files_opened":0,"tool_calls":1,"review_ms":2}`,
	}, "\n")
	var stdout bytes.Buffer
	if err := runAgentProtocol(context.Background(), repo, writer, bufio.NewReader(strings.NewReader(input)), &stdout); err != nil {
		t.Fatal(err)
	}
	responses := decodeAgentResponses(t, stdout.Bytes())
	if code := agentResponseCode(responses[0]); code != "unsupported_op" {
		t.Fatalf("recovery error = %q", code)
	}
	if got := responses[1]["result"].(map[string]any)["recorded"]; got != true {
		t.Fatalf("feedback response = %#v", responses[1])
	}
	if feedback.EvidenceID != report.Rows[0].EvidenceID || feedback.Verdict != "confirmed" || feedback.ToolCalls != 1 || feedback.ReviewMS != 2 {
		t.Fatalf("feedback seam = %#v", feedback)
	}
}

func TestAgentEOFAndSnapshotInvalidation(t *testing.T) {
	repo := agentTestRepo(t)
	responses, err := runAgentTest(t, repo, `{"schema":"slither.agent/v1","id":"eof","op":"hello"}`)
	if err != nil || len(responses) != 1 {
		t.Fatalf("final EOF = responses=%d err=%v", len(responses), err)
	}
	snapshots := agentSnapshots{repo: repo}
	first, err := snapshots.current(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package fixture\n// TODO: changed\nfunc Changed() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := snapshots.current(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReportID == second.ReportID {
		t.Fatal("filesystem snapshot was reused after source change")
	}
}

func TestAgentHelpAndOutcomesSeam(t *testing.T) {
	var stdout strings.Builder
	if err := RunWithIO(context.Background(), []string{"agent", "--help"}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "slither agent [repo] [--outcomes path]") {
		t.Fatalf("agent missing from help: %s", stdout.String())
	}
	repo := agentTestRepo(t)
	outcomes := filepath.Join(t.TempDir(), "outcomes.jsonl")
	responses, err := runAgentTestArgs(t, []string{"agent", repo, "--outcomes", outcomes}, `{"schema":"slither.agent/v1","id":"hello","op":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := responses[0]["result"].(map[string]any)["feedback_enabled"]; got != true {
		t.Fatalf("outcomes seam feedback enabled = %#v", got)
	}
	if info, err := os.Stat(outcomes); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("outcomes ledger initialization = info=%v err=%v", info, err)
	}
}

func TestAgentCancellationAndFatalOutput(t *testing.T) {
	repo := agentTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer
	err := RunWithIO(ctx, []string{"agent", repo}, strings.NewReader(`{"schema":"slither.agent/v1","id":"cancel","op":"hello"}`), &stdout, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
	responses := decodeAgentResponses(t, stdout.Bytes())
	if code := agentResponseCode(responses[0]); code != "canceled" {
		t.Fatalf("canceled response = %q", code)
	}
	err = RunWithIO(context.Background(), []string{"agent", repo}, strings.NewReader(`{"schema":"slither.agent/v1","id":"hello","op":"hello"}`), failingAgentWriter{}, io.Discard)
	if err == nil {
		t.Fatal("stdout failure was not fatal")
	}
}

type failingAgentWriter struct{}

func (failingAgentWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func agentTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package fixture\n// TODO: inspect authorization\nfunc Main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo
}

func agentReportOptions(repo string) Options {
	return Options{Repo: repo, Top: defaultTop, MaxBytes: defaultMaxBytes, Days: defaultDays, NoCache: true}
}

func runAgentTest(t *testing.T, repo, input string) ([]map[string]any, error) {
	t.Helper()
	return runAgentTestArgs(t, []string{"agent", repo}, input)
}

func runAgentTestArgs(t *testing.T, args []string, input string) ([]map[string]any, error) {
	t.Helper()
	var stdout bytes.Buffer
	err := RunWithIO(context.Background(), args, strings.NewReader(input), &stdout, io.Discard)
	if err != nil {
		return nil, err
	}
	return decodeAgentResponses(t, stdout.Bytes()), nil
}

func decodeAgentResponses(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n"))
	responses := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var response map[string]any
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

func agentResponseCode(response map[string]any) string {
	errorBody, _ := response["error"].(map[string]any)
	code, _ := errorBody["code"].(string)
	return code
}
