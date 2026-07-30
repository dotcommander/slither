package slither

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAgentQueryDefaultUnionOrderAndTruncation(t *testing.T) {
	report := agentQueryReport()
	defaultResult, err := agentQuery(report, agentQueryFields(2, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if defaultResult["count"] != 2 || defaultResult["truncated"] != true {
		t.Fatalf("default result = %#v", defaultResult)
	}
	union, err := agentQuery(report, agentQueryFields(3, []string{"file:three", "file:one", "file:three"}, "beta"))
	if err != nil {
		t.Fatal(err)
	}
	summaries := union["summaries"].([]map[string]any)
	for index, want := range []string{"file:one", "file:two", "file:three"} {
		if summaries[index]["id"] != want || summaries[index]["rank"] != index+1 {
			t.Fatalf("union summary %d = %#v, want %s rank %d", index, summaries[index], want, index+1)
		}
	}
	if _, err := agentQuery(report, agentQueryFields(1, []string{"file:missing"}, "")); !errors.Is(err, ErrContextTargetNotFound) {
		t.Fatalf("unknown target error = %v", err)
	}
}

func TestAgentQueryAndContextOperandValidation(t *testing.T) {
	report := agentQueryReport()
	for _, fields := range []map[string]json.RawMessage{
		{},
		{"limit": json.RawMessage(`0`)},
		{"limit": json.RawMessage(`81`)},
		{"limit": json.RawMessage(`1.5`)},
		{"limit": json.RawMessage(`1`), "focus": json.RawMessage(`"["`)},
		{"limit": json.RawMessage(`1`), "target_ids": json.RawMessage(`[null]`)},
	} {
		if _, err := agentQuery(report, fields); !errors.Is(err, errAgentInvalidRequest) {
			t.Fatalf("query fields %#v error = %v", fields, err)
		}
	}
	if _, err := agentContextRequest(map[string]json.RawMessage{"budget_bytes": json.RawMessage(`null`)}); !errors.Is(err, errAgentInvalidRequest) {
		t.Fatalf("context operands error = %v", err)
	}
}

func agentQueryReport() Report {
	rows := []FileEvidence{
		{ID: "file:one", EvidenceID: "sha256:one", Path: "one.go", Score: 5, Summary: "alpha", EvidenceLayers: []string{"one"}, Reasons: []string{"one"}},
		{ID: "file:two", EvidenceID: "sha256:two", Path: "two.go", Score: 4, Summary: "beta", EvidenceLayers: []string{"two"}, Reasons: []string{"two"}},
		{ID: "file:three", EvidenceID: "sha256:three", Path: "three.go", Score: 3, Summary: "gamma", EvidenceLayers: []string{"three"}, Reasons: []string{"three"}},
	}
	return Report{Rows: rows}
}

func agentQueryFields(limit int, ids []string, focus string) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{"limit": json.RawMessage(string(rune('0' + limit)))}
	if ids != nil {
		encoded, _ := json.Marshal(ids)
		fields["target_ids"] = encoded
	}
	if focus != "" {
		encoded, _ := json.Marshal(focus)
		fields["focus"] = encoded
	}
	return fields
}
