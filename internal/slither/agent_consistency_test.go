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

func TestAgentContextRejectsPostReadFilesystemChange(t *testing.T) {
	repo := agentTestRepo(t)
	report, err := BuildReport(context.Background(), agentReportOptions(repo))
	if err != nil {
		t.Fatal(err)
	}
	previous := contextDescriptorReader
	contextDescriptorReader = func(reader io.Reader, limit int64) (string, bool, bool, error) {
		text, ok, truncated, err := previous(reader, limit)
		if writeErr := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package fixture\n// TODO: replaced after context read\nfunc Changed() {}\n"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return text, ok, truncated, err
	}
	t.Cleanup(func() { contextDescriptorReader = previous })
	input := `{"schema":"slither.agent/v1","id":"context","op":"context","target_ids":["` + report.Rows[0].ID + `"],"budget_bytes":4096}`
	responses, err := runAgentTest(t, repo, input)
	if err != nil {
		t.Fatal(err)
	}
	if code := agentResponseCode(responses[0]); code != "stale_evidence" {
		t.Fatalf("context result = %#v, want stale_evidence", responses[0])
	}
}

func TestAgentContextRejectsPostReadGitChange(t *testing.T) {
	repo, _ := agentGitTestRepo(t, []byte("package fixture\n// TODO: inspect\nfunc Main() {}\n"))
	report, err := BuildReport(context.Background(), agentReportOptions(repo))
	if err != nil {
		t.Fatal(err)
	}
	previous := contextDescriptorReader
	contextDescriptorReader = func(reader io.Reader, limit int64) (string, bool, bool, error) {
		text, ok, truncated, err := previous(reader, limit)
		if writeErr := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package fixture\n// TODO: changed during context\nfunc Changed() {}\n"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return text, ok, truncated, err
	}
	t.Cleanup(func() { contextDescriptorReader = previous })
	input := `{"schema":"slither.agent/v1","id":"context","op":"context","target_ids":["` + report.Rows[0].ID + `"],"budget_bytes":4096}`
	responses, err := runAgentTest(t, repo, input)
	if err != nil {
		t.Fatal(err)
	}
	if code := agentResponseCode(responses[0]); code != "stale_evidence" {
		t.Fatalf("git context result = %#v, want stale_evidence", responses[0])
	}
}

func TestAgentFeedbackChecksSnapshotAndPreservesCancellation(t *testing.T) {
	repo := agentTestRepo(t)
	snapshots := agentSnapshots{repo: repo}
	report, err := snapshots.current(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	fields := agentFeedbackFields(report)
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package fixture\n// TODO: stale feedback\nfunc Changed() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = agentFeedback(context.Background(), &snapshots, report, outcomeWriterFunc(func(context.Context, Report, outcomeFeedback) error {
		called = true
		return nil
	}), fields)
	if !errors.Is(err, errAgentStaleEvidence) || called {
		t.Fatalf("stale feedback = err=%v called=%t", err, called)
	}

	// A newly current feedback request must preserve a writer's cancellation so
	// the protocol emits canceled and stops rather than feedback_write_failed.
	report, err = BuildReport(context.Background(), agentReportOptions(repo))
	if err != nil {
		t.Fatal(err)
	}
	input := `{"schema":"slither.agent/v1","id":"feedback","op":"feedback","report_id":"` + report.ReportID + `","evidence_id":"` + report.Rows[0].EvidenceID + `","verdict":"confirmed","files_opened":0,"tool_calls":0,"review_ms":0}`
	var stdout bytes.Buffer
	err = runAgentProtocol(context.Background(), repo, outcomeWriterFunc(func(context.Context, Report, outcomeFeedback) error {
		return context.Canceled
	}), bufio.NewReader(strings.NewReader(input)), &stdout)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writer cancellation = %v", err)
	}
	if code := agentResponseCode(decodeAgentResponses(t, stdout.Bytes())[0]); code != "canceled" {
		t.Fatalf("writer cancellation response = %q", code)
	}
}

func agentFeedbackFields(report Report) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"report_id":    json.RawMessage(`"` + report.ReportID + `"`),
		"evidence_id":  json.RawMessage(`"` + report.Rows[0].EvidenceID + `"`),
		"verdict":      json.RawMessage(`"confirmed"`),
		"files_opened": json.RawMessage(`0`),
		"tool_calls":   json.RawMessage(`0`),
		"review_ms":    json.RawMessage(`0`),
	}
}
