package slither

import "time"

type Options struct {
	Repo           string
	Out            string
	AsOf           time.Time
	Top            int
	MaxBytes       int64
	Days           int
	Patterns       string
	Focus          string
	Include        []string
	Exclude        []string
	WhyTop         int
	Inventory      string
	Model          string
	BaseURL        string
	APIKeyEnv      string
	Local          bool
	JSON           bool
	Summary        bool
	Cull           bool
	FallbackModels []string
	NoCache        bool
}

type FileEvidence struct {
	ID                     string             `json:"id,omitempty"`
	ContentID              string             `json:"content_id,omitempty"`
	EvidenceID             string             `json:"evidence_id,omitempty"`
	ScoreProvenance        ScoreProvenance    `json:"score_provenance,omitzero"`
	Path                   string             `json:"path"`
	EvidenceClass          string             `json:"evidence_class,omitempty"`
	Confidence             string             `json:"confidence,omitempty"`
	Actionability          Actionability      `json:"actionability,omitempty"`
	Caveat                 string             `json:"caveat,omitempty"`
	VerifyCmd              string             `json:"verify_cmd,omitempty"`
	OmittedReason          string             `json:"omitted_reason,omitempty"`
	Bytes                  int64              `json:"bytes"`
	Lines                  int                `json:"lines"`
	Score                  int                `json:"score"`
	SeedScore              float64            `json:"seed_score"`
	Churn                  int                `json:"churn"`
	FixTouches             int                `json:"fix_touches"`
	Markers                int                `json:"markers"`
	Imports                int                `json:"imports"`
	IncomingRefs           int                `json:"incoming_refs"`
	SmellRisk              int                `json:"smell_risk"`
	HotspotRisk            int                `json:"hotspot_risk"`
	SDKDXRisk              int                `json:"sdk_dx_risk"`
	UnknownsRisk           int                `json:"unknowns_risk"`
	EnvContractRisk        int                `json:"env_contract_risk"`
	WorkflowSecurityRisk   int                `json:"workflow_security_risk"`
	MigrationSafetyRisk    int                `json:"migration_safety_risk"`
	ContainerBuildRisk     int                `json:"container_build_risk"`
	KubernetesSecurityRisk int                `json:"kubernetes_security_risk"`
	TerraformSecurityRisk  int                `json:"terraform_security_risk"`
	OpenAPIContractRisk    int                `json:"openapi_contract_risk"`
	CORSSecurityRisk       int                `json:"cors_security_risk"`
	CookieSecurityRisk     int                `json:"cookie_security_risk"`
	DependencyHealthRisk   int                `json:"dependency_health_risk"`
	CentralityRisk         int                `json:"centrality_risk"`
	CochangeRisk           int                `json:"cochange_risk"`
	OwnershipRisk          int                `json:"ownership_risk"`
	FlakeRisk              int                `json:"flake_risk"`
	OracleRisk             int                `json:"oracle_risk"`
	StaleMarkerRisk        int                `json:"stale_marker_risk"`
	TestGap                bool               `json:"test_gap"`
	PathRisk               int                `json:"path_risk"`
	ContentRisk            int                `json:"content_risk"`
	EvidenceLayers         []string           `json:"evidence_layers,omitempty"`
	Reasons                []string           `json:"reasons"`
	EvidenceLocations      []EvidenceLocation `json:"evidence_locations,omitempty"`
	CullDecision           CullDecision       `json:"cull_decision,omitempty"`
	CullReason             string             `json:"cull_reason,omitempty"`
	Summary                string             `json:"summary"`
	Excerpt                string             `json:"excerpt,omitempty"`
}

// ScoreProvenance records the deterministic baseline and, when accepted, the
// model score that selected the compatibility Score field.
type ScoreProvenance struct {
	Deterministic int    `json:"deterministic"`
	Model         *int   `json:"model,omitempty"`
	SelectedBy    string `json:"selected_by"`
}

func (p ScoreProvenance) IsZero() bool {
	return p.Deterministic == 0 && p.Model == nil && p.SelectedBy == ""
}

// Actionability is the deterministic next-action class for a row. It tells a
// reader whether to treat the row as a likely defect, inspect high-risk evidence
// without assuming a defect, handle dependency policy separately, verify context
// first, or handle it as a hotspot signal.
type Actionability string

const (
	ActionabilityLikelyDefect     Actionability = "likely_defect"
	ActionabilityHighRiskInspect  Actionability = "high_risk_inspect"
	ActionabilityDependencyReview Actionability = "dependency_review"
	ActionabilityVerifyFirst      Actionability = "verify_first"
	ActionabilityInspect          Actionability = "inspect"
	ActionabilityHotspot          Actionability = "hotspot"
)

// CullDecision is the row-level disposition assigned by the cheap-model cull
// classifier. It mirrors the cull ledger bucket names so JSON consumers can
// filter rows without reimplementing Slither's bucket policy.
type CullDecision string

const (
	CullDecisionKeptForPremium CullDecision = "kept_for_premium"
	CullDecisionAlternates     CullDecision = "alternates"
	CullDecisionGenerated      CullDecision = "culled_generated_or_report"
	CullDecisionDocumentation  CullDecision = "culled_documentation"
	CullDecisionTestOnly       CullDecision = "culled_test_only"
	CullDecisionLowSignal      CullDecision = "culled_low_signal"
	CullDecisionDuplicate      CullDecision = "culled_duplicate_surface"
	CullDecisionNeedsEvidence  CullDecision = "needs_more_evidence"
)

// EvidenceLocation points a detector reason at the first concrete source line
// that triggered it. Reasons stay the stable machine key; locations make the
// JSON report faster to validate by a human or follow-up agent.
type EvidenceLocation struct {
	Reason  string `json:"reason"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

type DiscoveryStats struct {
	Source          string `json:"source"`
	GitTracked      int    `json:"git_tracked"`
	GitUntracked    int    `json:"git_untracked"`
	FilesystemFiles int    `json:"filesystem_files"`
	CandidateFiles  int    `json:"candidate_files"`
}

type Report struct {
	SchemaVersion  string
	ReportID       string
	SourceState    SourceState
	Parameters     ReportParameters
	Repo           string
	GeneratedAt    time.Time
	Days           int
	PatternsSource string
	FilesSeen      int
	FilesScored    int
	Discovery      DiscoveryStats
	Model          string
	BaseURL        string
	Build          BuildInfo
	SkippedSignals []string
	Rows           []FileEvidence
	FirstReadQueue []ReviewQueue
	ReviewPlan     []ReviewLane
	Filters        ReportFilters
	WhyTop         []WhyTopEntry
	FreshnessHint  string
	CullLedger     *CullLedger
	CacheStats     *CacheStats
	contextRows    []FileEvidence
	contextEdges   []localImportEdge
}

type SourceState struct {
	Kind       string `json:"kind"`
	Head       string `json:"head"`
	Dirty      bool   `json:"dirty"`
	TreeDigest string `json:"tree_digest"`
}

// ReportParameters contains only normalized inputs that can change evidence or
// scoring behavior. Paths, output mode, credentials, and presentation flags
// deliberately do not participate in report identity.
type ReportParameters struct {
	Days            int      `json:"days"`
	MaxBytes        int64    `json:"max_bytes"`
	Top             int      `json:"top"`
	Focus           string   `json:"focus,omitempty"`
	Include         []string `json:"include,omitempty"`
	Exclude         []string `json:"exclude,omitempty"`
	Inventory       string   `json:"inventory,omitempty"`
	PatternsID      string   `json:"patterns_id"`
	Model           string   `json:"model,omitempty"`
	BaseURL         string   `json:"base_url,omitempty"`
	FallbackModels  []string `json:"fallback_models,omitempty"`
	ModelContractID string   `json:"model_contract_id,omitempty"`
}

type ReportFilters struct {
	Focus     string   `json:"focus,omitempty"`
	Include   []string `json:"include,omitempty"`
	Exclude   []string `json:"exclude,omitempty"`
	Inventory string   `json:"inventory,omitempty"`
}

type WhyTopEntry struct {
	Rank           int                    `json:"rank"`
	Path           string                 `json:"path"`
	Score          int                    `json:"score"`
	Confidence     string                 `json:"confidence,omitempty"`
	Actionability  Actionability          `json:"actionability,omitempty"`
	Evidence       []string               `json:"evidence,omitempty"`
	Reasons        []string               `json:"reasons,omitempty"`
	ScoreBreakdown []WhyTopScoreComponent `json:"score_breakdown,omitempty"`
	VerifyCmd      string                 `json:"verify_cmd,omitempty"`
	Note           string                 `json:"note,omitempty"`
}

type WhyTopScoreComponent struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// CacheStats reports score-cache effectiveness for a run. Nil (omitted) when
// scoring did not run with the cache (no model, or --no-cache).
type CacheStats struct {
	Hits   int `json:"hits"`
	Misses int `json:"misses"`
}

type CullLedger struct {
	RunLabel       string        `json:"run_label"`
	Repo           string        `json:"repo"`
	GeneratedAt    time.Time     `json:"generated_at"`
	RowsConsidered int           `json:"rows_considered"`
	StopReason     string        `json:"stop_reason"`
	SkippedSignals []string      `json:"skipped_signals,omitempty"`
	KeptForPremium CullBucket    `json:"kept_for_premium"`
	Alternates     CullBucket    `json:"alternates"`
	Generated      CullBucket    `json:"culled_generated_or_report"`
	Documentation  CullBucket    `json:"culled_documentation"`
	TestOnly       CullBucket    `json:"culled_test_only"`
	LowSignal      CullBucket    `json:"culled_low_signal"`
	Duplicate      CullBucket    `json:"culled_duplicate_surface"`
	NeedsEvidence  CullBucket    `json:"needs_more_evidence"`
	FirstReadQueue []ReviewQueue `json:"first_read_queue,omitempty"`
	ReviewPlan     []ReviewLane  `json:"review_plan,omitempty"`
}

type CullBucket struct {
	Count    int         `json:"count"`
	Examples []CullEntry `json:"examples"`
}

type CullEntry struct {
	Path                          string        `json:"path"`
	Score                         int           `json:"score"`
	EvidenceClass                 string        `json:"evidence_class,omitempty"`
	Confidence                    string        `json:"confidence,omitempty"`
	Actionability                 Actionability `json:"actionability,omitempty"`
	Caveat                        string        `json:"caveat,omitempty"`
	VerifyCmd                     string        `json:"verify_cmd,omitempty"`
	EvidenceLayers                []string      `json:"evidence_layers,omitempty"`
	StrongestEvidenceIntersection string        `json:"strongest_evidence_intersection,omitempty"`
	Reason                        string        `json:"reason"`
}

type ReviewQueue struct {
	ID            string   `json:"id"`
	Group         string   `json:"group"`
	Lane          string   `json:"lane"`
	EvidenceClass string   `json:"evidence_class,omitempty"`
	Confidence    string   `json:"confidence,omitempty"`
	Reasons       []string `json:"reasons"`
	Caveat        string   `json:"caveat,omitempty"`
	Files         []string `json:"files"`
	OmittedReason string   `json:"omitted_reason,omitempty"`
}

type ReviewLane struct {
	ID            string   `json:"id"`
	Lane          string   `json:"lane"`
	Group         string   `json:"group"`
	EvidenceClass string   `json:"evidence_class,omitempty"`
	Confidence    string   `json:"confidence,omitempty"`
	Files         []string `json:"files"`
	Caveat        string   `json:"caveat,omitempty"`
	Gates         []string `json:"gates"`
	Verify        []string `json:"verify"`
	Why           []string `json:"why"`
	OmittedReason string   `json:"omitted_reason,omitempty"`
}
