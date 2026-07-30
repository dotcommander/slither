package slither

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

const (
	contextPacketSchema   = "slither.context/v1"
	maxContextPacketBytes = 1 << 20
)

var (
	ErrContextBudgetTooSmall = errors.New("context packet budget is below mandatory envelope")
	ErrContextBudgetTooLarge = errors.New("context packet budget exceeds maximum")
	ErrContextTargetNotFound = errors.New("context packet target not found")
	ErrContextUnsafeTarget   = errors.New("context packet target is not safe to read")
	ErrContextInvalidFocus   = errors.New("context packet focus is invalid")
)

type ContextBudgetError struct {
	Budget int
	Min    int
	Max    int
	Err    error
}

func (e *ContextBudgetError) Error() string {
	return fmt.Sprintf("context packet budget %d outside [%d,%d]: %v", e.Budget, e.Min, e.Max, e.Err)
}

func (e *ContextBudgetError) Unwrap() error { return e.Err }

type ContextTargetError struct {
	TargetID string
	Err      error
}

func (e *ContextTargetError) Error() string {
	return fmt.Sprintf("context packet target %q: %v", e.TargetID, e.Err)
}

func (e *ContextTargetError) Unwrap() error { return e.Err }

// ContextPacketRequest is intentionally repository-free: the packet is bound
// to the report's scanned repository. IDs and focus compose as a stable union.
type ContextPacketRequest struct {
	TargetIDs   []string
	Focus       string
	BudgetBytes int
}

type ContextPacket struct {
	Schema      string                `json:"schema"`
	ReportID    string                `json:"report_id"`
	BudgetBytes int                   `json:"budget_bytes"`
	UsedBytes   int                   `json:"used_bytes"`
	Targets     []ContextPacketTarget `json:"targets"`
	Omissions   []string              `json:"omissions"`
}

type ContextPacketTarget struct {
	ID                string             `json:"id"`
	EvidenceID        string             `json:"evidence_id"`
	Path              string             `json:"path"`
	Actionability     Actionability      `json:"actionability"`
	Caveat            string             `json:"caveat"`
	EvidenceLocations []EvidenceLocation `json:"evidence_locations"`
	ProofObligation   ProofObligation    `json:"proof_obligation"`
	Resolution        string             `json:"resolution"`
	Components        []ContextComponent `json:"components"`
}

type ContextComponent struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Path string `json:"path"`
	Text string `json:"text"`
}

type contextComponentPlan struct {
	target   int
	targetID string
	kind     string
	row      FileEvidence
}

type contextRead struct {
	text      string
	truncated bool
	err       error
}

// BuildContextPacket returns a deterministic, report-bound capsule. It uses
// scan-retained rows and import edges only; it never discovers or rescans the
// repository tree.
func BuildContextPacket(ctx context.Context, report Report, request ContextPacketRequest) (ContextPacket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.BudgetBytes <= 0 {
		return ContextPacket{}, &ContextBudgetError{Budget: request.BudgetBytes, Max: maxContextPacketBytes, Err: ErrContextBudgetTooSmall}
	}
	if request.BudgetBytes > maxContextPacketBytes {
		return ContextPacket{}, &ContextBudgetError{Budget: request.BudgetBytes, Max: maxContextPacketBytes, Err: ErrContextBudgetTooLarge}
	}
	if err := ctx.Err(); err != nil {
		return ContextPacket{}, err
	}

	selected, err := selectContextRows(report.Rows, request)
	if err != nil {
		return ContextPacket{}, err
	}
	allRows := report.contextRows
	if len(allRows) == 0 {
		allRows = report.Rows
	}
	plans := planContextComponents(selected, allRows, report.contextEdges)
	packet, err := contextPacketEnvelope(report, request.BudgetBytes, selected)
	if err != nil {
		return ContextPacket{}, err
	}
	packet.Omissions = contextPlanOmissions(plans)
	minimum, err := contextPacketMinimum(packet)
	if err != nil {
		return ContextPacket{}, err
	}
	if request.BudgetBytes < minimum {
		return ContextPacket{}, &ContextBudgetError{Budget: request.BudgetBytes, Min: minimum, Max: maxContextPacketBytes, Err: ErrContextBudgetTooSmall}
	}
	if len(plans) == 0 {
		return finalizeContextPacket(packet)
	}

	maxRead := contextReadLimit(report.Parameters.MaxBytes, request.BudgetBytes, plans)
	root, err := contextRootOpener(report.Repo)
	if err != nil {
		return ContextPacket{}, fmt.Errorf("open context repository: %w", ErrContextUnsafeTarget)
	}
	defer root.Close()
	reads := make(map[string]contextRead)
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return ContextPacket{}, err
		}
		if _, ok := reads[plan.row.Path]; ok {
			continue
		}
		if stringSliceContains(plan.row.EvidenceLayers, "secret-risk") {
			reads[plan.row.Path] = contextRead{}
			continue
		}
		read, err := readContextSource(ctx, root, plan.row.Path, maxRead)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ContextPacket{}, ctxErr
			}
			if plan.kind == "target" {
				return ContextPacket{}, &ContextTargetError{TargetID: plan.row.ID, Err: ErrContextUnsafeTarget}
			}
			reads[plan.row.Path] = contextRead{err: err}
			continue
		}
		reads[plan.row.Path] = read
	}

	for _, plan := range plans {
		read := reads[plan.row.Path]
		if stringSliceContains(plan.row.EvidenceLayers, "secret-risk") {
			packet.Omissions = replaceComponentOmissions(packet.Omissions, plan, nil)
			packet = addContextComponent(packet, plan, redactedContextComponent(plan), nil)
			continue
		}
		baseOmissions := contextComponentOmissions(plan, read)
		packet.Omissions = replaceComponentOmissions(packet.Omissions, plan, baseOmissions)
		if read.err != nil {
			continue
		}
		component, resolution := contextComponentFor(plan, read.text)
		if plan.kind == "target" {
			packet.Targets[plan.target].Resolution = resolution
		}
		packet = addContextComponent(packet, plan, component, baseOmissions)
	}
	return finalizeContextPacket(packet)
}

func contextPacketMinimum(packet ContextPacket) (int, error) {
	candidate := packet
	candidate.BudgetBytes = 0
	for range 16 {
		encoded, _, err := encodeContextPacket(candidate)
		if err != nil {
			return 0, err
		}
		if candidate.BudgetBytes == encoded.UsedBytes {
			return encoded.UsedBytes, nil
		}
		candidate.BudgetBytes = encoded.UsedBytes
		candidate.UsedBytes = 0
	}
	return 0, errors.New("context packet minimum did not converge")
}

func contextPacketEnvelope(report Report, budget int, rows []FileEvidence) (ContextPacket, error) {
	packet := ContextPacket{Schema: contextPacketSchema, ReportID: report.ReportID, BudgetBytes: budget, Targets: make([]ContextPacketTarget, len(rows)), Omissions: []string{}}
	for index, row := range rows {
		projected, err := deterministicContextRow(row)
		if err != nil {
			return ContextPacket{}, err
		}
		proof, err := proofObligationForRow(projected)
		if err != nil {
			return ContextPacket{}, err
		}
		packet.Targets[index] = ContextPacketTarget{ID: row.ID, EvidenceID: row.EvidenceID, Path: row.Path, Actionability: projected.Actionability, Caveat: projected.Caveat, EvidenceLocations: scrubContextEvidenceLocations(projected), ProofObligation: proof, Components: []ContextComponent{}}
		if stringSliceContains(row.EvidenceLayers, "secret-risk") {
			packet.Targets[index].Resolution = "redacted"
			packet.Targets[index].Components = []ContextComponent{{Kind: "target", ID: row.ID, Path: row.Path, Text: "[redacted: secret-risk evidence]"}}
		} else if strings.EqualFold(filepath.Ext(row.Path), ".go") {
			packet.Targets[index].Resolution = "go_declaration"
		} else {
			packet.Targets[index].Resolution = "lexical"
		}
	}
	return packet, nil
}

func scrubContextEvidenceLocations(row FileEvidence) []EvidenceLocation {
	out := append([]EvidenceLocation(nil), row.EvidenceLocations...)
	for index := range out {
		if stringSliceContains(row.EvidenceLayers, "secret-risk") {
			out[index].Snippet = "[redacted]"
			continue
		}
		out[index].Snippet = scrubOutputSecrets(out[index].Snippet)
	}
	return out
}

func selectContextRows(rows []FileEvidence, request ContextPacketRequest) ([]FileEvidence, error) {
	focus, err := compileFocus(request.Focus)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContextInvalidFocus, err)
	}
	byID := make(map[string]int, len(rows))
	for index, row := range rows {
		byID[row.ID] = index
	}
	selected := make(map[int]bool)
	for _, id := range request.TargetIDs {
		index, ok := byID[id]
		if !ok {
			return nil, &ContextTargetError{TargetID: id, Err: ErrContextTargetNotFound}
		}
		selected[index] = true
	}
	if focus != nil {
		for index, row := range rows {
			if rowMatchesFocus(row, focus) {
				selected[index] = true
			}
		}
	}
	if len(request.TargetIDs) == 0 && focus == nil {
		for index, row := range rows {
			if keepForPremium(row) {
				selected[index] = true
			}
		}
	}
	out := make([]FileEvidence, 0, len(selected))
	for index, row := range rows {
		if selected[index] {
			out = append(out, row)
		}
	}
	return out, nil
}

func planContextComponents(targets, allRows []FileEvidence, edges []localImportEdge) []contextComponentPlan {
	byPath := make(map[string]FileEvidence, len(allRows))
	for _, row := range allRows {
		byPath[row.Path] = row
	}
	var plans []contextComponentPlan
	for targetIndex, target := range targets {
		if stringSliceContains(target.EvidenceLayers, "secret-risk") {
			continue
		}
		plans = append(plans, contextComponentPlan{target: targetIndex, targetID: target.ID, kind: "target", row: target})
		var importers, imported []FileEvidence
		for _, edge := range edges {
			if edge.Imported == target.Path {
				if row, ok := byPath[edge.Importer]; ok {
					importers = append(importers, row)
				}
			}
			if edge.Importer == target.Path {
				if row, ok := byPath[edge.Imported]; ok {
					imported = append(imported, row)
				}
			}
		}
		plans = append(plans, contextPlansForRows(targetIndex, target.ID, "importer", importers)...)
		plans = append(plans, contextPlansForRows(targetIndex, target.ID, "imported", imported)...)
		dir := filepath.Dir(target.Path)
		var tests, support []FileEvidence
		for _, row := range allRows {
			if row.Path == target.Path {
				continue
			}
			if isTestFile(row.Path) && filepath.Dir(row.Path) == dir {
				tests = append(tests, row)
				continue
			}
			if contextSupportRow(target, row) {
				support = append(support, row)
			}
		}
		plans = append(plans, contextPlansForRows(targetIndex, target.ID, "test", tests)...)
		plans = append(plans, contextPlansForRows(targetIndex, target.ID, "support", support)...)
	}
	return plans
}

func contextPlansForRows(target int, targetID, kind string, rows []FileEvidence) []contextComponentPlan {
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Path] = true
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	byPath := make(map[string]FileEvidence, len(rows))
	for _, row := range rows {
		byPath[row.Path] = row
	}
	out := make([]contextComponentPlan, 0, len(paths))
	for _, path := range paths {
		out = append(out, contextComponentPlan{target: target, targetID: targetID, kind: kind, row: byPath[path]})
	}
	return out
}

func contextSupportRow(target, row FileEvidence) bool {
	if !(isDocumentationPath(row.Path) || contextManifestPath(row.Path)) {
		return false
	}
	targetDir := filepath.Dir(target.Path)
	rowDir := filepath.Dir(row.Path)
	return rowDir == targetDir || rowDir == "."
}

func contextManifestPath(path string) bool {
	switch filepath.Base(strings.ToLower(path)) {
	case "go.mod", "package.json", "composer.json", "cargo.toml", "pyproject.toml", "requirements.txt":
		return true
	default:
		return false
	}
}

func contextPlanOmissions(plans []contextComponentPlan) []string {
	var out []string
	for _, plan := range plans {
		out = append(out, contextSourceTruncatedOmission(plan), contextBudgetOmission(plan))
	}
	sort.Strings(out)
	return out
}

func contextReadLimit(reportMax int64, budget int, plans []contextComponentPlan) int64 {
	limit := int64(budget)
	if reportMax > 0 && reportMax < limit {
		limit = reportMax
	}
	if limit > maxContextPacketBytes {
		limit = maxContextPacketBytes
	}
	seen := map[string]bool{}
	for _, plan := range plans {
		seen[plan.row.Path] = true
	}
	if len(seen) > 0 {
		limit /= int64(len(seen))
	}
	if limit < 1 {
		return 1
	}
	return limit
}

func readContextSource(ctx context.Context, root contextRoot, rel string, limit int64) (contextRead, error) {
	if err := ctx.Err(); err != nil {
		return contextRead{}, err
	}
	if !safeContextRelativePath(rel) || shouldSkip(rel) {
		return contextRead{}, ErrContextUnsafeTarget
	}
	file, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return contextRead{}, ErrContextUnsafeTarget
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return contextRead{}, ErrContextUnsafeTarget
	}
	if err := ctx.Err(); err != nil {
		return contextRead{}, err
	}
	text, ok, truncated, err := contextDescriptorReader(file, limit)
	if err != nil {
		return contextRead{}, err
	}
	if err := ctx.Err(); err != nil {
		return contextRead{}, err
	}
	if !ok {
		return contextRead{}, ErrContextUnsafeTarget
	}
	return contextRead{text: text, truncated: truncated}, nil
}

func contextComponentFor(plan contextComponentPlan, text string) (ContextComponent, string) {
	if plan.kind != "target" {
		return ContextComponent{Kind: plan.kind, ID: plan.row.ID, Path: plan.row.Path, Text: scrubOutputSecrets(text)}, "lexical"
	}
	if strings.EqualFold(filepath.Ext(plan.row.Path), ".go") {
		if declaration, ok := goDeclarationSpan(plan.row, text); ok {
			return ContextComponent{Kind: "target", ID: plan.row.ID, Path: plan.row.Path, Text: scrubOutputSecrets(declaration)}, "go_declaration"
		}
	}
	return ContextComponent{Kind: "target", ID: plan.row.ID, Path: plan.row.Path, Text: scrubOutputSecrets(lexicalEvidenceSpan(plan.row, text))}, "lexical"
}

func redactedContextComponent(plan contextComponentPlan) ContextComponent {
	return ContextComponent{Kind: plan.kind, ID: plan.row.ID, Path: plan.row.Path, Text: "[redacted: secret-risk evidence]"}
}

func goDeclarationSpan(row FileEvidence, text string) (string, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, row.Path, text, 0)
	if err != nil || len(file.Decls) == 0 {
		return "", false
	}
	line := firstEvidenceLine(row)
	if line <= 0 {
		return "", false
	}
	chosenIndex := -1
	for index, decl := range file.Decls {
		start := fset.PositionFor(decl.Pos(), false).Line
		end := fset.PositionFor(decl.End(), false).Line
		if start <= line && line <= end {
			chosenIndex = index
			break
		}
	}
	if chosenIndex < 0 {
		return "", false
	}
	chosen := file.Decls[chosenIndex]
	start := fset.PositionFor(chosen.Pos(), false).Line
	end := fset.PositionFor(chosen.End(), false).Line
	return sourceLines(text, start, end), true
}

func firstEvidenceLine(row FileEvidence) int {
	for _, location := range row.EvidenceLocations {
		if location.Line > 0 {
			return location.Line
		}
	}
	return 0
}

func lexicalEvidenceSpan(row FileEvidence, text string) string {
	line := firstEvidenceLine(row)
	if line <= 0 {
		line = 1
	}
	return sourceLines(text, line-8, line+8)
}

func sourceLines(text string, start, end int) string {
	lines := strings.Split(text, "\n")
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end || len(lines) == 0 {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

func contextComponentOmissions(plan contextComponentPlan, read contextRead) []string {
	if read.err != nil {
		return []string{contextUnavailableOmission(plan)}
	}
	if read.truncated {
		return []string{contextSourceTruncatedOmission(plan), contextBudgetOmission(plan)}
	}
	return []string{contextBudgetOmission(plan)}
}

func contextBudgetOmission(plan contextComponentPlan) string {
	return "target:" + plan.targetID + ":component:" + plan.kind + ":" + plan.row.ID + ":budget"
}

func contextSourceTruncatedOmission(plan contextComponentPlan) string {
	return "target:" + plan.targetID + ":component:" + plan.kind + ":" + plan.row.ID + ":source_truncated"
}

func contextUnavailableOmission(plan contextComponentPlan) string {
	return "target:" + plan.targetID + ":component:" + plan.kind + ":" + plan.row.ID + ":unavailable"
}

func replaceComponentOmissions(omissions []string, plan contextComponentPlan, replacements []string) []string {
	prefix := "target:" + plan.targetID + ":component:" + plan.kind + ":" + plan.row.ID + ":"
	out := make([]string, 0, len(omissions)+len(replacements))
	for _, omission := range omissions {
		if !strings.HasPrefix(omission, prefix) {
			out = append(out, omission)
		}
	}
	out = append(out, replacements...)
	sort.Strings(out)
	return out
}

func removeContextOmission(omissions []string, wanted string) []string {
	out := make([]string, 0, len(omissions))
	for _, omission := range omissions {
		if omission != wanted {
			out = append(out, omission)
		}
	}
	return out
}

func addContextComponent(packet ContextPacket, plan contextComponentPlan, component ContextComponent, omissions []string) ContextPacket {
	full := packet
	full.Targets = cloneContextTargets(packet.Targets)
	full.Targets[plan.target].Components = append(full.Targets[plan.target].Components, component)
	full.Omissions = removeContextOmission(full.Omissions, contextBudgetOmission(plan))
	if fitted, ok := contextPacketFits(full); ok {
		return fitted
	}
	if stringSliceContains(plan.row.EvidenceLayers, "secret-risk") {
		// A secret marker is atomic. A prefix could obscure the fact that the
		// source was deliberately redacted, so retain either the full marker or
		// one explicit budget omission.
		packet.Omissions = replaceComponentOmissions(packet.Omissions, plan, []string{contextBudgetOmission(plan)})
		return packet
	}

	trimmed := component
	trimmed.Text = largestContextComponentPrefix(packet, plan, component, omissions)
	if trimmed.Text == "" && component.Text != "" {
		return packet
	}
	packet.Targets = cloneContextTargets(packet.Targets)
	packet.Targets[plan.target].Components = append(packet.Targets[plan.target].Components, trimmed)
	return packet
}

func largestContextComponentPrefix(packet ContextPacket, plan contextComponentPlan, component ContextComponent, omissions []string) string {
	boundaries := []int{0}
	for offset := range component.Text {
		if offset > 0 {
			boundaries = append(boundaries, offset)
		}
	}
	if boundaries[len(boundaries)-1] != len(component.Text) {
		boundaries = append(boundaries, len(component.Text))
	}
	low, high := 0, len(boundaries)-1
	for low < high {
		middle := low + (high-low+1)/2
		candidate := packet
		candidate.Targets = cloneContextTargets(packet.Targets)
		candidate.Targets[plan.target].Components = append(candidate.Targets[plan.target].Components, ContextComponent{Kind: component.Kind, ID: component.ID, Path: component.Path, Text: component.Text[:boundaries[middle]]})
		if _, ok := contextPacketFits(candidate); ok {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return component.Text[:boundaries[low]]
}

func contextPacketFits(packet ContextPacket) (ContextPacket, bool) {
	packet, _, err := encodeContextPacket(packet)
	return packet, err == nil && packet.UsedBytes <= packet.BudgetBytes
}

func cloneContextTargets(targets []ContextPacketTarget) []ContextPacketTarget {
	out := append([]ContextPacketTarget(nil), targets...)
	for index := range out {
		out[index].EvidenceLocations = append([]EvidenceLocation(nil), out[index].EvidenceLocations...)
		out[index].Components = append([]ContextComponent(nil), out[index].Components...)
	}
	return out
}

func finalizeContextPacket(packet ContextPacket) (ContextPacket, error) {
	returned, _, err := encodeContextPacket(packet)
	if err != nil {
		return ContextPacket{}, err
	}
	if returned.UsedBytes > returned.BudgetBytes {
		return ContextPacket{}, &ContextBudgetError{Budget: returned.BudgetBytes, Min: returned.UsedBytes, Max: maxContextPacketBytes, Err: ErrContextBudgetTooSmall}
	}
	return returned, nil
}

func safeContextRelativePath(rel string) bool {
	if rel == "" || strings.ContainsRune(rel, '\x00') {
		return false
	}
	converted := filepath.FromSlash(rel)
	if filepath.IsAbs(converted) || filepath.Clean(converted) != converted {
		return false
	}
	return !isParentTraversal(converted)
}

func encodeContextPacket(packet ContextPacket) (ContextPacket, []byte, error) {
	sort.Strings(packet.Omissions)
	for range 16 {
		encoded, err := json.Marshal(packet)
		if err != nil {
			return ContextPacket{}, nil, fmt.Errorf("marshal context packet: %w", err)
		}
		if packet.UsedBytes == len(encoded) {
			return packet, encoded, nil
		}
		packet.UsedBytes = len(encoded)
	}
	return ContextPacket{}, nil, errors.New("context packet byte accounting did not converge")
}
