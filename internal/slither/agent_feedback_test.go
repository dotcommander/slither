package slither

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAgentFeedbackDisabledWriteFailureAndContextMappings(t *testing.T) {
	repo := agentTestRepo(t)
	report, err := BuildReport(context.Background(), agentReportOptions(repo))
	if err != nil {
		t.Fatal(err)
	}
	feedback := `{"schema":"slither.agent/v1","id":"feedback","op":"feedback","report_id":"` + report.ReportID + `","evidence_id":"` + report.Rows[0].EvidenceID + `","verdict":"confirmed","files_opened":0,"tool_calls":0,"review_ms":0}`
	for _, test := range []struct {
		name   string
		writer outcomeWriter
		want   string
	}{
		{"disabled", nil, "feedback_disabled"},
		{"write failure", outcomeWriterFunc(func(context.Context, Report, outcomeFeedback) error { return errors.New("write failed") }), "feedback_write_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			if err := runAgentProtocol(context.Background(), repo, test.writer, bufio.NewReader(strings.NewReader(feedback)), &stdout); err != nil {
				t.Fatal(err)
			}
			if got := agentResponseCode(decodeAgentResponses(t, stdout.Bytes())[0]); got != test.want {
				t.Fatalf("feedback code = %q, want %q", got, test.want)
			}
		})
	}
	if got := agentErrorCode(ErrContextInvalidFocus); got != "invalid_request" {
		t.Fatalf("invalid focus code = %q", got)
	}
	if got := agentErrorCode(ErrContextUnsafeTarget); got != "stale_evidence" {
		t.Fatalf("unsafe target code = %q", got)
	}
}
