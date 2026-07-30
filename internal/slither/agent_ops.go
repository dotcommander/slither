package slither

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

func executeAgentRequest(ctx context.Context, snapshots *agentSnapshots, writer outcomeWriter, request agentRequest) (agentResponse, error) {
	if request.Op == "hello" {
		return agentResponse{Schema: agentSchema, ID: request.ID, OK: true, Result: map[string]any{
			"protocol":          agentSchema,
			"schemas":           []string{reportSchemaVersion, contextPacketSchema, "slither.outcome/v1", "slither.eval/v1"},
			"operations":        []string{"hello", "scan", "query", "context", "feedback"},
			"max_request_bytes": maxAgentRequestBytes,
			"max_context_bytes": maxContextPacketBytes,
			"feedback_enabled":  writer != nil,
		}}, nil
	}
	report, err := snapshots.current(ctx, request.Op == "scan")
	if err != nil {
		return agentResponse{}, err
	}
	response := agentResponse{Schema: agentSchema, ID: request.ID, OK: true, ReportID: report.ReportID, SourceState: &report.SourceState}
	switch request.Op {
	case "scan":
		ledger := BuildCullLedger(report)
		response.Result = map[string]any{
			"schema":           report.SchemaVersion,
			"parameters":       report.Parameters,
			"discovery":        report.Discovery,
			"skipped_signals":  report.SkippedSignals,
			"files_seen":       report.FilesSeen,
			"files_scored":     report.FilesScored,
			"cull_counts":      agentCullCounts(ledger),
			"first_read_queue": report.FirstReadQueue,
			"review_plan":      report.ReviewPlan,
		}
	case "query":
		result, err := agentQuery(report, request.Fields)
		if err != nil {
			return agentResponse{}, err
		}
		response.Result = result
	case "context":
		contextRequest, err := agentContextRequest(request.Fields)
		if err != nil {
			return agentResponse{}, err
		}
		packet, err := BuildContextPacket(ctx, report, contextRequest)
		if err != nil {
			return agentResponse{}, err
		}
		if err := snapshots.verify(ctx, report); err != nil {
			return agentResponse{}, err
		}
		response.Result = packet
	case "feedback":
		if err := agentFeedback(ctx, snapshots, report, writer, request.Fields); err != nil {
			return agentResponse{}, err
		}
		response.Result = map[string]bool{"recorded": true}
	}
	return response, nil
}

func agentCullCounts(ledger CullLedger) map[string]int {
	return map[string]int{
		"kept_for_premium":           ledger.KeptForPremium.Count,
		"alternates":                 ledger.Alternates.Count,
		"culled_generated_or_report": ledger.Generated.Count,
		"culled_documentation":       ledger.Documentation.Count,
		"culled_test_only":           ledger.TestOnly.Count,
		"culled_low_signal":          ledger.LowSignal.Count,
		"culled_duplicate_surface":   ledger.Duplicate.Count,
		"needs_more_evidence":        ledger.NeedsEvidence.Count,
	}
}

func agentQuery(report Report, fields map[string]json.RawMessage) (map[string]any, error) {
	var limit int
	if err := agentDecodeInt(fields, "limit", &limit, true); err != nil || limit < 1 || limit > defaultTop {
		return nil, errAgentInvalidRequest
	}
	ids, focus, err := agentSelectors(fields)
	if err != nil {
		return nil, errAgentInvalidRequest
	}
	focusRE, err := compileFocus(focus)
	if err != nil {
		return nil, errAgentInvalidRequest
	}
	selected := make(map[int]bool)
	rowsWithCull := rowsWithCullDispositions(report.Rows)
	byID := make(map[string]int, len(rowsWithCull))
	for index, row := range rowsWithCull {
		byID[row.ID] = index
	}
	for _, id := range ids {
		index, ok := byID[id]
		if !ok {
			return nil, ErrContextTargetNotFound
		}
		selected[index] = true
	}
	if focusRE != nil {
		for index, row := range rowsWithCull {
			if rowMatchesFocus(row, focusRE) {
				selected[index] = true
			}
		}
	}
	if len(ids) == 0 && focus == "" {
		for index := range rowsWithCull {
			selected[index] = true
		}
	}
	rows := make([]map[string]any, 0, limit)
	matched := 0
	for index, row := range rowsWithCull {
		if !selected[index] {
			continue
		}
		matched++
		if len(rows) < limit {
			rows = append(rows, agentRowSummary(index+1, row))
		}
	}
	return map[string]any{"count": len(rows), "truncated": matched > len(rows), "summaries": rows}, nil
}

func agentRowSummary(rank int, row FileEvidence) map[string]any {
	layers := append([]string(nil), row.EvidenceLayers...)
	reasons := append([]string(nil), row.Reasons...)
	if len(layers) > 8 {
		layers = layers[:8]
	}
	if len(reasons) > 8 {
		reasons = reasons[:8]
	}
	return map[string]any{
		"rank":             rank,
		"id":               row.ID,
		"evidence_id":      row.EvidenceID,
		"path":             row.Path,
		"score":            row.Score,
		"score_provenance": row.ScoreProvenance,
		"evidence_class":   row.EvidenceClass,
		"confidence":       row.Confidence,
		"actionability":    row.Actionability,
		"caveat":           row.Caveat,
		"cull_decision":    row.CullDecision,
		"cull_reason":      row.CullReason,
		"verify_cmd":       row.VerifyCmd,
		"evidence_layers":  layers,
		"reasons":          reasons,
	}
}

func agentContextRequest(fields map[string]json.RawMessage) (ContextPacketRequest, error) {
	ids, focus, err := agentSelectors(fields)
	if err != nil {
		return ContextPacketRequest{}, errAgentInvalidRequest
	}
	var budget int
	if err := agentDecodeInt(fields, "budget_bytes", &budget, true); err != nil {
		return ContextPacketRequest{}, errAgentInvalidRequest
	}
	return ContextPacketRequest{TargetIDs: ids, Focus: focus, BudgetBytes: budget}, nil
}

func agentSelectors(fields map[string]json.RawMessage) ([]string, string, error) {
	var ids []string
	if raw, ok := fields["target_ids"]; ok {
		if string(raw) == "null" || json.Unmarshal(raw, &ids) != nil {
			return nil, "", errors.New("invalid target ids")
		}
		for _, id := range ids {
			if id == "" {
				return nil, "", errors.New("empty target id")
			}
		}
	}
	var focus string
	if err := decodeAgentString(fields, "focus", &focus, false); err != nil {
		return nil, "", err
	}
	return ids, focus, nil
}

func agentDecodeInt(fields map[string]json.RawMessage, name string, destination *int, required bool) error {
	raw, ok := fields[name]
	if !ok {
		if required {
			return errors.New("missing integer")
		}
		return nil
	}
	if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, destination) != nil {
		return errors.New("invalid integer")
	}
	return nil
}

func agentFeedback(ctx context.Context, snapshots *agentSnapshots, report Report, writer outcomeWriter, fields map[string]json.RawMessage) error {
	if writer == nil {
		return errAgentFeedbackDisabled
	}
	var input struct {
		ReportID    string `json:"report_id"`
		EvidenceID  string `json:"evidence_id"`
		Verdict     string `json:"verdict"`
		FilesOpened int    `json:"files_opened"`
		ToolCalls   int    `json:"tool_calls"`
		ReviewMS    int    `json:"review_ms"`
	}
	for name, destination := range map[string]*string{"report_id": &input.ReportID, "evidence_id": &input.EvidenceID, "verdict": &input.Verdict} {
		if err := decodeAgentString(fields, name, destination, true); err != nil {
			return errAgentInvalidRequest
		}
	}
	for name, destination := range map[string]*int{"files_opened": &input.FilesOpened, "tool_calls": &input.ToolCalls, "review_ms": &input.ReviewMS} {
		if err := agentDecodeInt(fields, name, destination, true); err != nil || *destination < 0 {
			return errAgentInvalidRequest
		}
	}
	if !agentVerdictPattern.MatchString(input.Verdict) {
		return errAgentInvalidRequest
	}
	if input.ReportID != report.ReportID {
		return errAgentStaleEvidence
	}
	validEvidence := false
	for _, row := range report.Rows {
		if row.EvidenceID == input.EvidenceID {
			validEvidence = true
			break
		}
	}
	if !validEvidence {
		return errAgentStaleEvidence
	}
	if err := snapshots.verify(ctx, report); err != nil {
		return err
	}
	if err := writer.WriteOutcome(ctx, report, outcomeFeedback(input)); err != nil {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		return fmt.Errorf("%w: %v", errAgentFeedbackWrite, err)
	}
	return nil
}

var agentVerdictPattern = regexp.MustCompile(`^(confirmed|refuted|unknown|skipped)$`)
