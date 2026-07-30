package slither

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxAgentRequestBytes = 1 << 20

type agentRequest struct {
	Schema string
	ID     string
	Op     string
	Fields map[string]json.RawMessage
}

type agentResponse struct {
	Schema      string       `json:"schema"`
	ID          string       `json:"id"`
	OK          bool         `json:"ok"`
	Result      any          `json:"result,omitempty"`
	ReportID    string       `json:"report_id,omitempty"`
	SourceState *SourceState `json:"source_state,omitempty"`
	Error       *agentError  `json:"error,omitempty"`
}

type agentError struct {
	Code string `json:"code"`
}

func runAgentProtocol(ctx context.Context, repo string, writer outcomeWriter, input *bufio.Reader, output io.Writer) error {
	snapshots := agentSnapshots{repo: repo}
	for {
		line, oversized, err := readAgentRecord(input)
		if err == io.EOF && len(line) == 0 && !oversized {
			return nil
		}
		if err != nil && err != io.EOF {
			return fmt.Errorf("read agent request: %w", err)
		}
		if oversized {
			if writeErr := writeAgentError(output, "", "request_too_large"); writeErr != nil {
				return writeErr
			}
			if err == io.EOF {
				return nil
			}
			continue
		}
		request, code := decodeAgentRequest(line)
		if code != "" {
			if writeErr := writeAgentError(output, request.ID, code); writeErr != nil {
				return writeErr
			}
			if err == io.EOF {
				return nil
			}
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			if writeErr := writeAgentError(output, request.ID, "canceled"); writeErr != nil {
				return writeErr
			}
			return ctxErr
		}
		response, opErr := executeAgentRequest(ctx, &snapshots, writer, request)
		if opErr != nil {
			code = agentErrorCode(opErr)
			if writeErr := writeAgentError(output, request.ID, code); writeErr != nil {
				return writeErr
			}
			if errors.Is(opErr, context.Canceled) {
				return context.Canceled
			}
		} else if writeErr := writeAgentResponse(output, response); writeErr != nil {
			return writeErr
		}
		if err == io.EOF {
			return nil
		}
	}
}

func readAgentRecord(reader *bufio.Reader) ([]byte, bool, error) {
	var record []byte
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if fragment[len(fragment)-1] == '\n' {
				fragment = fragment[:len(fragment)-1]
				if len(fragment) > 0 && fragment[len(fragment)-1] == '\r' {
					fragment = fragment[:len(fragment)-1]
				}
			}
			if !oversized && len(record)+len(fragment) <= maxAgentRequestBytes {
				record = append(record, fragment...)
			} else {
				oversized = true
			}
		}
		switch err {
		case nil:
			return record, oversized, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			return record, oversized, io.EOF
		default:
			return nil, false, err
		}
	}
}

func decodeAgentRequest(line []byte) (agentRequest, string) {
	request := agentRequest{Fields: map[string]json.RawMessage{}}
	decoder := json.NewDecoder(bytes.NewReader(line))
	if err := decoder.Decode(&request.Fields); err != nil || request.Fields == nil {
		return request, "invalid_request"
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return request, "invalid_request"
	}
	if raw, ok := request.Fields["id"]; ok {
		_ = json.Unmarshal(raw, &request.ID)
		if !agentValidRequestID(request.ID) {
			request.ID = ""
		}
	}
	if err := decodeAgentString(request.Fields, "schema", &request.Schema, true); err != nil {
		return request, "invalid_request"
	}
	if err := decodeAgentString(request.Fields, "id", &request.ID, true); err != nil || !agentValidRequestID(request.ID) {
		request.ID = ""
		return request, "invalid_request"
	}
	if request.Schema != agentSchema {
		return request, "unsupported_schema"
	}
	if err := decodeAgentString(request.Fields, "op", &request.Op, true); err != nil {
		return request, "invalid_request"
	}
	if !agentOperationKnown(request.Op) {
		return request, "unsupported_op"
	}
	if !agentRequestFieldsAllowed(request.Fields, request.Op) {
		return request, "invalid_request"
	}
	return request, ""
}

func decodeAgentString(fields map[string]json.RawMessage, name string, destination *string, required bool) error {
	raw, ok := fields[name]
	if !ok {
		if required {
			return errors.New("missing field")
		}
		return nil
	}
	if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, destination) != nil {
		return errors.New("invalid string")
	}
	return nil
}

func agentOperationKnown(op string) bool {
	return op == "hello" || op == "scan" || op == "query" || op == "context" || op == "feedback"
}

func agentRequestFieldsAllowed(fields map[string]json.RawMessage, op string) bool {
	allowed := map[string]bool{"schema": true, "id": true, "op": true}
	switch op {
	case "query":
		allowed["target_ids"], allowed["focus"], allowed["limit"] = true, true, true
	case "context":
		allowed["target_ids"], allowed["focus"], allowed["budget_bytes"] = true, true, true
	case "feedback":
		for _, name := range []string{"report_id", "evidence_id", "verdict", "files_opened", "tool_calls", "review_ms"} {
			allowed[name] = true
		}
	}
	for name := range fields {
		if !allowed[name] {
			return false
		}
	}
	if op == "query" {
		_, ok := fields["limit"]
		return ok
	}
	if op == "context" {
		_, ok := fields["budget_bytes"]
		return ok
	}
	if op == "feedback" {
		for name := range allowed {
			if name == "schema" || name == "id" || name == "op" {
				continue
			}
			if _, ok := fields[name]; !ok {
				return false
			}
		}
	}
	return true
}

func agentValidRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, value := range []byte(id) {
		if value < 0x20 || value > 0x7e {
			return false
		}
	}
	return true
}

func writeAgentError(output io.Writer, id, code string) error {
	return writeAgentResponse(output, agentResponse{Schema: agentSchema, ID: id, OK: false, Error: &agentError{Code: code}})
}

func writeAgentResponse(output io.Writer, response agentResponse) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode agent response: %w", err)
	}
	encoded = append(encoded, '\n')
	if written, err := output.Write(encoded); err != nil {
		return fmt.Errorf("write agent response: %w", err)
	} else if written != len(encoded) {
		return fmt.Errorf("write agent response: %w", io.ErrShortWrite)
	}
	return nil
}

func agentErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrContextBudgetTooSmall):
		return "budget_too_small"
	case errors.Is(err, ErrContextBudgetTooLarge):
		return "budget_too_large"
	case errors.Is(err, ErrContextTargetNotFound):
		return "target_not_found"
	case errors.Is(err, ErrContextUnsafeTarget), errors.Is(err, errAgentStaleEvidence):
		return "stale_evidence"
	case errors.Is(err, errAgentFeedbackDisabled):
		return "feedback_disabled"
	case errors.Is(err, errAgentFeedbackWrite):
		return "feedback_write_failed"
	case errors.Is(err, ErrContextInvalidFocus), errors.Is(err, errAgentInvalidRequest):
		return "invalid_request"
	default:
		return "scan_failed"
	}
}

var (
	errAgentInvalidRequest   = errors.New("invalid agent request")
	errAgentStaleEvidence    = errors.New("stale agent evidence")
	errAgentFeedbackDisabled = errors.New("agent feedback is disabled")
	errAgentFeedbackWrite    = errors.New("agent feedback write failed")
	errAgentSnapshotChanged  = errors.New("agent snapshot changed during rebuild")
)
