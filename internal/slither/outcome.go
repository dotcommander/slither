package slither

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	outcomeSchema       = "slither.outcome/v1"
	maxOutcomeRecordLen = 1 << 20
)

// OutcomeRecord is the complete, deliberately small feedback ledger payload.
// It contains no path, source, prompt, command, or free-form user input.
type OutcomeRecord struct {
	Schema      string `json:"schema"`
	Timestamp   string `json:"timestamp"`
	ReportID    string `json:"report_id"`
	EvidenceID  string `json:"evidence_id"`
	Lane        string `json:"lane"`
	Rank        int    `json:"rank"`
	Score       int    `json:"score"`
	Verdict     string `json:"verdict"`
	FilesOpened int    `json:"files_opened"`
	ToolCalls   int    `json:"tool_calls"`
	ReviewMS    int    `json:"review_ms"`
}

func deriveOutcomeRecord(report Report, reportID, evidenceID, verdict string, filesOpened, toolCalls, reviewMS int) (OutcomeRecord, error) {
	if reportID != report.ReportID {
		return OutcomeRecord{}, errors.New("outcome report identity is stale")
	}
	if !validOutcomeVerdict(verdict) || filesOpened < 0 || toolCalls < 0 || reviewMS < 0 {
		return OutcomeRecord{}, errors.New("invalid outcome feedback")
	}
	dispositions := classifyCullDispositions(report.Rows)
	for index, row := range report.Rows {
		if row.EvidenceID == evidenceID {
			return OutcomeRecord{Schema: outcomeSchema, Timestamp: currentTime().UTC().Format(time.RFC3339Nano),
				ReportID: reportID, EvidenceID: evidenceID, Lane: string(dispositions[index].Decision), Rank: index + 1,
				Score: row.Score, Verdict: verdict, FilesOpened: filesOpened, ToolCalls: toolCalls, ReviewMS: reviewMS}, nil
		}
	}
	return OutcomeRecord{}, errors.New("outcome evidence identity is stale")
}

func validOutcomeVerdict(verdict string) bool {
	return verdict == "confirmed" || verdict == "refuted" || verdict == "unknown" || verdict == "skipped"
}

func isOutcomeLane(lane string) bool {
	switch CullDecision(lane) {
	case CullDecisionKeptForPremium, CullDecisionAlternates, CullDecisionGenerated, CullDecisionDocumentation,
		CullDecisionTestOnly, CullDecisionLowSignal, CullDecisionDuplicate, CullDecisionNeedsEvidence:
		return true
	default:
		return false
	}
}

func validateStoredOutcomeRecord(record OutcomeRecord) error {
	if record.Schema != outcomeSchema || !validOutcomeVerdict(record.Verdict) || !isOutcomeLane(record.Lane) || record.Rank <= 0 || record.Score < 1 || record.Score > 5 ||
		record.FilesOpened < 0 || record.ToolCalls < 0 || record.ReviewMS < 0 {
		return errors.New("invalid outcome record")
	}
	if _, err := time.Parse(time.RFC3339Nano, record.Timestamp); err != nil {
		return fmt.Errorf("invalid outcome timestamp: %w", err)
	}
	if !validOutcomeIdentity(record.ReportID) || !validOutcomeIdentity(record.EvidenceID) {
		return errors.New("invalid outcome identity")
	}
	return nil
}

func validOutcomeIdentity(value string) bool {
	if len(value) != len("sha256:")+64 || value[:len("sha256:")] != "sha256:" {
		return false
	}
	for _, b := range value[len("sha256:"):] {
		if !(b >= '0' && b <= '9') && !(b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func decodeOutcomeRecord(raw []byte) (OutcomeRecord, []byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return OutcomeRecord{}, nil, fmt.Errorf("decode outcome record: %w", err)
	}
	if len(fields) != 11 {
		return OutcomeRecord{}, nil, errors.New("outcome record has unexpected fields")
	}
	for _, name := range []string{"schema", "timestamp", "report_id", "evidence_id", "lane", "rank", "score", "verdict", "files_opened", "tool_calls", "review_ms"} {
		if _, ok := fields[name]; !ok {
			return OutcomeRecord{}, nil, fmt.Errorf("outcome record is missing %s", name)
		}
	}
	var record OutcomeRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return OutcomeRecord{}, nil, fmt.Errorf("decode outcome record: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return OutcomeRecord{}, nil, errors.New("outcome record has trailing data")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return OutcomeRecord{}, nil, fmt.Errorf("encode outcome record: %w", err)
	}
	return record, encoded, nil
}
