package slither

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestAgentFramingExactLimitAndGoldenShapes(t *testing.T) {
	exact := agentExactQueryRecord(maxAgentRequestBytes)
	line, oversized, err := readAgentRecord(bufio.NewReader(strings.NewReader(exact + "\r\n")))
	if err != nil || oversized || len(line) != maxAgentRequestBytes {
		t.Fatalf("exact record = bytes:%d oversized:%t err:%v", len(line), oversized, err)
	}
	if _, code := decodeAgentRequest(line); code != "" {
		t.Fatalf("exact record code = %q", code)
	}
	repo := agentTestRepo(t)
	var stdout bytes.Buffer
	input := `{"schema":"slither.agent/v1","id":"hello","op":"hello"}` + "\n" + `{"schema":"slither.agent/v1","id":"bad","op":"hello","limit":1}`
	if err := runAgentProtocol(context.Background(), repo, nil, bufio.NewReader(strings.NewReader(input)), &stdout); err != nil {
		t.Fatal(err)
	}
	want := "{\"schema\":\"slither.agent/v1\",\"id\":\"hello\",\"ok\":true,\"result\":{\"feedback_enabled\":false,\"max_context_bytes\":1048576,\"max_request_bytes\":1048576,\"operations\":[\"hello\",\"scan\",\"query\",\"context\",\"feedback\"],\"protocol\":\"slither.agent/v1\",\"schemas\":[\"slither.report/v1\",\"slither.context/v1\",\"slither.outcome/v1\",\"slither.eval/v1\"]}}\n" +
		"{\"schema\":\"slither.agent/v1\",\"id\":\"bad\",\"ok\":false,\"error\":{\"code\":\"invalid_request\"}}\n"
	if got := stdout.String(); got != want {
		t.Fatalf("response golden mismatch (-want +got):\n- %s+ %s", want, got)
	}
}

func TestAgentFramingRecoveryAndFatalInput(t *testing.T) {
	repo := agentTestRepo(t)
	input := strings.Join([]string{
		`{"schema":"slither.agent/v2","id":"schema","op":"hello"}`,
		`{"schema":"slither.agent/v1","id":"op","op":"other"}`,
		`{"schema":"slither.agent/v1","id":"json","op":`,
		strings.Repeat("x", maxAgentRequestBytes+1),
		`{"schema":"slither.agent/v1","id":"crlf","op":"hello"}`,
	}, "\r\n")
	responses, err := runAgentTest(t, repo, input)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []string{"unsupported_schema", "unsupported_op", "invalid_request", "request_too_large", ""} {
		if got := agentResponseCode(responses[index]); got != want {
			t.Fatalf("response %d code = %q, want %q", index, got, want)
		}
	}
	if err := runAgentProtocol(context.Background(), repo, nil, bufio.NewReader(agentFailingReader{}), io.Discard); err == nil {
		t.Fatal("fatal input error was swallowed")
	}
}

func agentExactQueryRecord(size int) string {
	prefix := `{"schema":"slither.agent/v1","id":"limit","op":"query","focus":"`
	suffix := `","limit":1}`
	return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
}

type agentFailingReader struct{}

func (agentFailingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
