package slither

import (
	"errors"
	"path/filepath"
)

var (
	ErrUnsupportedActionability = errors.New("context packet actionability is unsupported")
	ErrInvalidFalsifier         = errors.New("context packet falsifier operands are invalid")
)

type FalsifierOperation string

const (
	FalsifierOperationRead      FalsifierOperation = "read"
	FalsifierOperationSearch    FalsifierOperation = "search"
	FalsifierOperationInspect   FalsifierOperation = "inspect"
	FalsifierOperationRunVerify FalsifierOperation = "run-verify"
)

type Falsifier struct {
	Operation FalsifierOperation `json:"operation"`
	Path      string             `json:"path,omitempty"`
	Query     string             `json:"query,omitempty"`
	Subject   string             `json:"subject,omitempty"`
	Command   string             `json:"command,omitempty"`
}

func (f Falsifier) Validate() error {
	switch f.Operation {
	case FalsifierOperationRead:
		if safeContextRelativePath(f.Path) && f.Query == "" && f.Subject == "" && f.Command == "" {
			return nil
		}
	case FalsifierOperationSearch:
		if f.Query != "" && (f.Path == "" || safeContextRelativePath(f.Path)) && f.Subject == "" && f.Command == "" {
			return nil
		}
	case FalsifierOperationInspect:
		if f.Subject != "" && f.Path == "" && f.Query == "" && f.Command == "" {
			return nil
		}
	case FalsifierOperationRunVerify:
		if f.Command != "" && f.Path == "" && f.Query == "" && f.Subject == "" {
			return nil
		}
	}
	return ErrInvalidFalsifier
}

type ProofObligation struct {
	ID            string      `json:"id"`
	TargetID      string      `json:"target_id"`
	Hypothesis    string      `json:"hypothesis"`
	Support       []string    `json:"support"`
	Falsifiers    []Falsifier `json:"falsifiers"`
	PromotionGate string      `json:"promotion_gate"`
	Verify        []string    `json:"verify"`
}

// deterministicContextRow removes post-scan model state before deriving the
// packet's classification. Report rows remain untouched: this projection exists
// only so a packet has one deterministic actionability/proof contract.
func deterministicContextRow(row FileEvidence) (FileEvidence, error) {
	if !supportedActionability(row.Actionability) {
		return FileEvidence{}, ErrUnsupportedActionability
	}
	projected := row
	projected.Score = contextDeterministicScore(row)
	projected.ScoreProvenance = ScoreProvenance{Deterministic: projected.Score, SelectedBy: "deterministic"}
	projected.Reasons = deterministicReasons(row.Reasons)
	projected.EvidenceLayers = deterministicLayers(row.EvidenceLayers)
	projected.Actionability = ""
	projected.Actionability = actionabilityForRow(projected)
	projected.Caveat = caveatForRow(projected)
	return projected, nil
}

// contextDeterministicScore preserves a real deterministic zero once scoring
// has recorded provenance. Legacy rows without provenance retain the existing
// row.Score fallback used elsewhere in the package.
func contextDeterministicScore(row FileEvidence) int {
	if row.ScoreProvenance.SelectedBy != "" {
		return row.ScoreProvenance.Deterministic
	}
	return deterministicScore(row)
}

func supportedActionability(actionability Actionability) bool {
	switch actionability {
	case ActionabilityLikelyDefect,
		ActionabilityHighRiskInspect,
		ActionabilityDependencyReview,
		ActionabilityVerifyFirst,
		ActionabilityInspect,
		ActionabilityHotspot:
		return true
	default:
		return false
	}
}

func proofObligationForRow(row FileEvidence) (ProofObligation, error) {
	obligation := ProofObligation{
		TargetID: row.ID,
		Support:  uniqueStrings(deterministicReasons(row.Reasons)),
		Verify:   []string{},
	}
	if obligation.Support == nil {
		obligation.Support = []string{}
	}

	readTarget := Falsifier{Operation: FalsifierOperationRead, Path: row.Path}
	switch row.Actionability {
	case ActionabilityLikelyDefect:
		obligation.Hypothesis = "Likely-defect hypothesis"
		obligation.Falsifiers = []Falsifier{readTarget, {Operation: FalsifierOperationInspect, Subject: "callers_or_witness"}}
		obligation.PromotionGate = "Source witness plus caller, focused test, or runtime witness"
		appendVerifyFalsifier(&obligation, row.VerifyCmd)
	case ActionabilityHighRiskInspect:
		obligation.Hypothesis = "Invariant-risk hypothesis"
		obligation.Falsifiers = []Falsifier{readTarget, {Operation: FalsifierOperationInspect, Subject: "invariant_or_runtime_witness"}}
		obligation.PromotionGate = "Invariant-specific test or runtime witness"
		appendVerifyFalsifier(&obligation, row.VerifyCmd)
	case ActionabilityDependencyReview:
		obligation.Hypothesis = "Dependency-policy hypothesis"
		obligation.Falsifiers = []Falsifier{readTarget, {Operation: FalsifierOperationSearch, Query: "(replace|override|resolution)"}, {Operation: FalsifierOperationInspect, Subject: "replacement_policy"}}
		obligation.PromotionGate = "Manifest and replacement-policy inspection"
		appendVerifyFalsifier(&obligation, row.VerifyCmd)
	case ActionabilityHotspot:
		obligation.Hypothesis = "Hotspot-not-defect hypothesis"
		obligation.Falsifiers = []Falsifier{readTarget, {Operation: FalsifierOperationInspect, Subject: "callers"}, {Operation: FalsifierOperationInspect, Subject: "ownership_or_blast_radius"}}
		obligation.PromotionGate = "Caller, ownership, or blast-radius confirmation; never defect promotion"
		appendVerifyFalsifier(&obligation, row.VerifyCmd)
	case ActionabilityVerifyFirst:
		obligation.Hypothesis = "Classification-first hypothesis"
		obligation.Falsifiers = []Falsifier{readTarget, {Operation: FalsifierOperationSearch, Query: filepath.Base(row.Path)}, {Operation: FalsifierOperationInspect, Subject: "row_context"}}
		obligation.PromotionGate = "Classify fixture, generated, documentation, or model-error context before promotion"
		appendVerifyFalsifier(&obligation, row.VerifyCmd)
	case ActionabilityInspect:
		obligation.Hypothesis = "Corroboration-required hypothesis"
		obligation.Falsifiers = []Falsifier{readTarget, {Operation: FalsifierOperationInspect, Subject: "structural_or_history_edge"}}
		obligation.PromotionGate = "Live source plus one corroborating structural or history edge"
		appendVerifyFalsifier(&obligation, row.VerifyCmd)
	default:
		return ProofObligation{}, ErrUnsupportedActionability
	}

	if isTestPath(row.Path) || isGeneratedOrReportPath(row.Path) || isDocumentationPath(row.Path) {
		obligation.PromotionGate += "; Production source or runtime witness required before promotion"
		obligation.Falsifiers = append(obligation.Falsifiers, Falsifier{Operation: FalsifierOperationInspect, Subject: "production_witness"})
	}
	for _, falsifier := range obligation.Falsifiers {
		if err := falsifier.Validate(); err != nil {
			return ProofObligation{}, err
		}
	}
	obligation.ID = proofObligationIdentity(row.EvidenceID, obligation)
	return obligation, nil
}

func appendVerifyFalsifier(obligation *ProofObligation, command string) {
	if command == "" {
		return
	}
	obligation.Verify = []string{command}
	obligation.Falsifiers = append(obligation.Falsifiers, Falsifier{Operation: FalsifierOperationRunVerify, Command: command})
}

func proofObligationIdentity(evidenceID string, obligation ProofObligation) string {
	return sha256Identity("slither.proof-obligation/v1", struct {
		EvidenceID string      `json:"evidence_id"`
		TargetID   string      `json:"target_id"`
		Hypothesis string      `json:"hypothesis"`
		Support    []string    `json:"support"`
		Falsifiers []Falsifier `json:"falsifiers"`
		Gate       string      `json:"promotion_gate"`
		Verify     []string    `json:"verify"`
	}{
		EvidenceID: evidenceID,
		TargetID:   obligation.TargetID,
		Hypothesis: obligation.Hypothesis,
		Support:    obligation.Support,
		Falsifiers: obligation.Falsifiers,
		Gate:       obligation.PromotionGate,
		Verify:     obligation.Verify,
	})
}
