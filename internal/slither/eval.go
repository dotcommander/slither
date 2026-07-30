package slither

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	evalSchema = "slither.eval/v1"
	evalTopK   = 15
)

type evalResult struct {
	Schema      string          `json:"schema"`
	Reports     int             `json:"reports"`
	Rows        int             `json:"rows"`
	Outcomes    evalOutcomes    `json:"outcomes"`
	ContextCost evalContextCost `json:"context_cost"`
	Calibration evalCalibration `json:"calibration"`
	Warnings    evalWarnings    `json:"warnings"`
}

type evalOutcomes struct {
	Confirmed int `json:"confirmed"`
	Refuted   int `json:"refuted"`
	Unknown   int `json:"unknown"`
	Skipped   int `json:"skipped"`
}

type evalContextCost struct {
	FilesOpened int `json:"files_opened"`
	ToolCalls   int `json:"tool_calls"`
	ReviewMS    int `json:"review_ms"`
}

type evalCalibration struct {
	Labeled              int     `json:"labeled"`
	Found                int     `json:"found"`
	Missing              int     `json:"missing"`
	TopK                 int     `json:"top_k"`
	NoiseInTopK          int     `json:"noise_in_top_k"`
	ConfirmedInTopK      int     `json:"confirmed_in_top_k"`
	DistinctScoresInTopK int     `json:"distinct_scores_in_top_k"`
	TotalRows            int     `json:"total_rows"`
	NoiseTopKRate        float64 `json:"noise_top_k_rate"`
	ConfirmedTopKRecall  float64 `json:"confirmed_top_k_recall"`
	ConfirmedFoundRecall float64 `json:"confirmed_found_recall"`
	LabeledCoverage      float64 `json:"labeled_coverage"`
	TopKScoreSaturation  float64 `json:"top_k_score_saturation"`
	topKSlots            int
	foundConfirmed       int
	missingConfirmed     int
}

type evalWarnings struct {
	PartialTrailingRecords int `json:"partial_trailing_records"`
	UnmatchedOutcomes      int `json:"unmatched_outcomes"`
}

type evalReport struct {
	ID   string
	Rows []evalRow
}

type evalRow struct {
	Row  FileEvidence
	Lane string
}

func evaluateOutcomeFiles(ctx context.Context, outcomePath string, reportPaths []string) (evalResult, error) {
	if err := ctx.Err(); err != nil {
		return evalResult{}, err
	}
	reports := make(map[string]evalReport, len(reportPaths))
	result := evalResult{Schema: evalSchema, Reports: len(reportPaths), Calibration: evalCalibration{TopK: evalTopK}}
	for _, path := range reportPaths {
		report, err := readEvalReport(path)
		if err != nil {
			return evalResult{}, err
		}
		if _, exists := reports[report.ID]; exists {
			return evalResult{}, fmt.Errorf("duplicate report identity %s", report.ID)
		}
		reports[report.ID] = report
		result.Rows += len(report.Rows)
		result.Calibration.TotalRows += len(report.Rows)
		scores := make([]int, len(report.Rows))
		for index, row := range report.Rows {
			scores[index] = row.Row.Score
		}
		result.Calibration.DistinctScoresInTopK += scoreHealthForTopK(scores, evalTopK).DistinctScores
	}
	partialTrailingRecords := 0
	if err := streamOutcomeLedger(ctx, outcomePath, &partialTrailingRecords, func(record OutcomeRecord) error {
		return applyOutcomeRecord(&result, reports, record)
	}); err != nil {
		return evalResult{}, err
	}
	result.Warnings.PartialTrailingRecords = partialTrailingRecords
	totalSlots := 0
	for _, report := range reports {
		totalSlots += min(evalTopK, len(report.Rows))
	}
	result.Calibration.NoiseTopKRate = evaluationRatio(result.Calibration.NoiseInTopK, totalSlots)
	result.Calibration.ConfirmedTopKRecall = evaluationRatio(result.Calibration.ConfirmedInTopK, result.Calibration.confirmedTotal())
	result.Calibration.ConfirmedFoundRecall = evaluationRatio(result.Calibration.confirmedFound(), result.Calibration.confirmedTotal())
	result.Calibration.LabeledCoverage = evaluationRatio(result.Calibration.Found, result.Calibration.Labeled)
	result.Calibration.TopKScoreSaturation = scoreSaturation(result.Calibration.DistinctScoresInTopK, totalSlots)
	result.Calibration.topKSlots = totalSlots
	return result, nil
}

func (calibration evalCalibration) confirmedTotal() int {
	return calibration.confirmedFound() + calibration.missingConfirmed
}
func (calibration evalCalibration) confirmedFound() int { return calibration.foundConfirmed }

func evaluationRatio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func applyOutcomeRecord(result *evalResult, reports map[string]evalReport, record OutcomeRecord) error {
	report, ok := reports[record.ReportID]
	if !ok {
		return addUnmatchedOutcome(result, record)
	}
	rowIndex := -1
	for index, candidate := range report.Rows {
		if candidate.Row.EvidenceID == record.EvidenceID {
			rowIndex = index
			break
		}
	}
	if rowIndex < 0 {
		return addUnmatchedOutcome(result, record)
	}
	row := report.Rows[rowIndex]
	if record.Rank != rowIndex+1 || record.Score != row.Row.Score || record.Lane != row.Lane {
		return errors.New("outcome integrity mismatch")
	}
	switch record.Verdict {
	case "confirmed":
		result.Outcomes.Confirmed++
		result.Calibration.Labeled++
		result.Calibration.Found++
		result.Calibration.foundConfirmed++
		if record.Rank <= evalTopK {
			result.Calibration.ConfirmedInTopK++
		}
	case "refuted":
		result.Outcomes.Refuted++
		result.Calibration.Labeled++
		result.Calibration.Found++
		if record.Rank <= evalTopK {
			result.Calibration.NoiseInTopK++
		}
	case "unknown":
		result.Outcomes.Unknown++
	case "skipped":
		result.Outcomes.Skipped++
	}
	result.ContextCost.FilesOpened += record.FilesOpened
	result.ContextCost.ToolCalls += record.ToolCalls
	result.ContextCost.ReviewMS += record.ReviewMS
	return nil
}

func addUnmatchedOutcome(result *evalResult, record OutcomeRecord) error {
	result.Warnings.UnmatchedOutcomes++
	if record.Verdict == "confirmed" || record.Verdict == "refuted" {
		result.Calibration.Labeled++
		result.Calibration.Missing++
		if record.Verdict == "confirmed" {
			result.Calibration.missingConfirmed++
		}
	}
	return nil
}

func readEvalReport(path string) (evalReport, error) {
	file, err := os.Open(path)
	if err != nil {
		return evalReport{}, fmt.Errorf("open report %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	var envelope struct {
		SchemaVersion string           `json:"schema_version"`
		ReportID      string           `json:"report_id"`
		SourceState   SourceState      `json:"source_state"`
		Parameters    ReportParameters `json:"parameters"`
		Rows          json.RawMessage  `json:"rows"`
	}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&envelope); err != nil {
		return evalReport{}, fmt.Errorf("decode report %s: %w", filepath.Base(path), err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return evalReport{}, fmt.Errorf("decode report %s: trailing data", filepath.Base(path))
	}
	if envelope.SchemaVersion != reportSchemaVersion || !validOutcomeIdentity(envelope.ReportID) || len(envelope.Rows) == 0 || string(envelope.Rows) == "null" {
		return evalReport{}, fmt.Errorf("invalid report %s", filepath.Base(path))
	}
	var rawRows []json.RawMessage
	if err := json.Unmarshal(envelope.Rows, &rawRows); err != nil || rawRows == nil {
		return evalReport{}, fmt.Errorf("invalid report rows in %s", filepath.Base(path))
	}
	report := evalReport{ID: envelope.ReportID, Rows: make([]evalRow, 0, len(rawRows))}
	identityReport := Report{SchemaVersion: envelope.SchemaVersion, SourceState: envelope.SourceState, Parameters: envelope.Parameters, Rows: make([]FileEvidence, 0, len(rawRows))}
	seen := make(map[string]bool, len(rawRows))
	for index, raw := range rawRows {
		var required struct {
			EvidenceID string `json:"evidence_id"`
			Score      *int   `json:"score"`
		}
		var row FileEvidence
		if err := json.Unmarshal(raw, &required); err != nil || json.Unmarshal(raw, &row) != nil || !validOutcomeIdentity(required.EvidenceID) || required.Score == nil || *required.Score < 1 || *required.Score > 5 || seen[required.EvidenceID] || !validEvalProvenance(row) {
			return evalReport{}, fmt.Errorf("invalid report row %d in %s", index+1, filepath.Base(path))
		}
		seen[required.EvidenceID] = true
		report.Rows = append(report.Rows, evalRow{Row: row})
		identityReport.Rows = append(identityReport.Rows, row)
	}
	if reportIdentity(identityReport) != envelope.ReportID {
		return evalReport{}, fmt.Errorf("report identity mismatch in %s", filepath.Base(path))
	}
	cullRows := make([]FileEvidence, len(report.Rows))
	for index, row := range report.Rows {
		cullRows[index] = row.Row
	}
	for index, disposition := range classifyCullDispositions(cullRows) {
		report.Rows[index].Lane = string(disposition.Decision)
	}
	return report, nil
}

func validEvalProvenance(row FileEvidence) bool {
	provenance := row.ScoreProvenance
	if provenance.Deterministic < 1 || provenance.Deterministic > 5 {
		return false
	}
	switch provenance.SelectedBy {
	case "deterministic":
		return provenance.Model == nil && row.Score == provenance.Deterministic
	case "model":
		return provenance.Model != nil && *provenance.Model == row.Score
	default:
		return false
	}
}
