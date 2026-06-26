package slither

import "time"

type Options struct {
	Repo      string
	Out       string
	Top       int
	MaxBytes  int64
	Days      int
	Patterns  string
	Model     string
	BaseURL   string
	APIKeyEnv string
	Local     bool
	JSON           bool
	Cull           bool
	FallbackModels []string
	NoCache        bool
}

type FileEvidence struct {
	ID                     string   `json:"id,omitempty"`
	Path                   string   `json:"path"`
	EvidenceClass          string   `json:"evidence_class,omitempty"`
	Confidence             string   `json:"confidence,omitempty"`
	Caveat                 string   `json:"caveat,omitempty"`
	VerifyCmd              string   `json:"verify_cmd,omitempty"`
	OmittedReason          string   `json:"omitted_reason,omitempty"`
	Bytes                  int64    `json:"bytes"`
	Lines                  int      `json:"lines"`
	Score                  int      `json:"score"`
	SeedScore              float64  `json:"seed_score"`
	Churn                  int      `json:"churn"`
	FixTouches             int      `json:"fix_touches"`
	Markers                int      `json:"markers"`
	Imports                int      `json:"imports"`
	IncomingRefs           int      `json:"incoming_refs"`
	SmellRisk              int      `json:"smell_risk"`
	HotspotRisk            int      `json:"hotspot_risk"`
	SDKDXRisk              int      `json:"sdk_dx_risk"`
	UnknownsRisk           int      `json:"unknowns_risk"`
	EnvContractRisk        int      `json:"env_contract_risk"`
	WorkflowSecurityRisk   int      `json:"workflow_security_risk"`
	MigrationSafetyRisk    int      `json:"migration_safety_risk"`
	ContainerBuildRisk     int      `json:"container_build_risk"`
	KubernetesSecurityRisk int      `json:"kubernetes_security_risk"`
	TerraformSecurityRisk  int      `json:"terraform_security_risk"`
	OpenAPIContractRisk    int      `json:"openapi_contract_risk"`
	CORSSecurityRisk       int      `json:"cors_security_risk"`
	CookieSecurityRisk     int      `json:"cookie_security_risk"`
	DependencyHealthRisk   int      `json:"dependency_health_risk"`
	CentralityRisk         int      `json:"centrality_risk"`
	CochangeRisk           int      `json:"cochange_risk"`
	OwnershipRisk          int      `json:"ownership_risk"`
	FlakeRisk              int      `json:"flake_risk"`
	OracleRisk             int      `json:"oracle_risk"`
	StaleMarkerRisk        int      `json:"stale_marker_risk"`
	TestGap                bool     `json:"test_gap"`
	PathRisk               int      `json:"path_risk"`
	ContentRisk            int      `json:"content_risk"`
	EvidenceLayers         []string `json:"evidence_layers,omitempty"`
	Reasons                []string `json:"reasons"`
	Summary                string   `json:"summary"`
	Excerpt                string   `json:"excerpt,omitempty"`
}

type DiscoveryStats struct {
	Source          string `json:"source"`
	GitTracked      int    `json:"git_tracked"`
	GitUntracked    int    `json:"git_untracked"`
	FilesystemFiles int    `json:"filesystem_files"`
	CandidateFiles  int    `json:"candidate_files"`
}

type Report struct {
	Repo           string
	GeneratedAt    time.Time
	Days           int
	PatternsSource string
	FilesSeen      int
	FilesScored    int
	Discovery      DiscoveryStats
	Model          string
	BaseURL        string
	SkippedSignals []string
	Rows           []FileEvidence
	FirstReadQueue []ReviewQueue
	ReviewPlan     []ReviewLane
	CullLedger     *CullLedger
	CacheStats     *CacheStats
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
	Path                          string   `json:"path"`
	Score                         int      `json:"score"`
	EvidenceClass                 string   `json:"evidence_class,omitempty"`
	Confidence                    string   `json:"confidence,omitempty"`
	Caveat                        string   `json:"caveat,omitempty"`
	VerifyCmd                     string   `json:"verify_cmd,omitempty"`
	EvidenceLayers                []string `json:"evidence_layers,omitempty"`
	StrongestEvidenceIntersection string   `json:"strongest_evidence_intersection,omitempty"`
	Reason                        string   `json:"reason"`
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
