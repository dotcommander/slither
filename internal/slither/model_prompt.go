package slither

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// scoringEvidence is a compact projection of FileEvidence for the scoring
// prompt. It carries only fields that inform scoring and uses omitempty so the
// many zero-valued risk signals and empty fields vanish from the payload. The
// Excerpt, Summary, and report-only fields are deliberately omitted.
type scoringEvidence struct {
	Index                  int      `json:"index"`
	Path                   string   `json:"path"`
	Lines                  int      `json:"lines,omitempty"`
	DeterministicScore     int      `json:"deterministic_score,omitempty"`
	Markers                int      `json:"markers,omitempty"`
	Churn                  int      `json:"churn,omitempty"`
	FixTouches             int      `json:"fix_touches,omitempty"`
	IncomingRefs           int      `json:"incoming_refs,omitempty"`
	PathRisk               int      `json:"path_risk,omitempty"`
	ContentRisk            int      `json:"content_risk,omitempty"`
	SmellRisk              int      `json:"smell_risk,omitempty"`
	HotspotRisk            int      `json:"hotspot_risk,omitempty"`
	SDKDXRisk              int      `json:"sdk_dx_risk,omitempty"`
	UnknownsRisk           int      `json:"unknowns_risk,omitempty"`
	EnvContractRisk        int      `json:"env_contract_risk,omitempty"`
	WorkflowSecurityRisk   int      `json:"workflow_security_risk,omitempty"`
	MigrationSafetyRisk    int      `json:"migration_safety_risk,omitempty"`
	ContainerBuildRisk     int      `json:"container_build_risk,omitempty"`
	KubernetesSecurityRisk int      `json:"kubernetes_security_risk,omitempty"`
	TerraformSecurityRisk  int      `json:"terraform_security_risk,omitempty"`
	OpenAPIContractRisk    int      `json:"openapi_contract_risk,omitempty"`
	CORSSecurityRisk       int      `json:"cors_security_risk,omitempty"`
	CookieSecurityRisk     int      `json:"cookie_security_risk,omitempty"`
	DependencyHealthRisk   int      `json:"dependency_health_risk,omitempty"`
	CentralityRisk         int      `json:"centrality_risk,omitempty"`
	CochangeRisk           int      `json:"cochange_risk,omitempty"`
	OwnershipRisk          int      `json:"ownership_risk,omitempty"`
	FlakeRisk              int      `json:"flake_risk,omitempty"`
	OracleRisk             int      `json:"oracle_risk,omitempty"`
	StaleMarkerRisk        int      `json:"stale_marker_risk,omitempty"`
	TestGap                bool     `json:"test_gap,omitempty"`
	EvidenceLayers         []string `json:"evidence_layers,omitempty"`
	Reasons                []string `json:"reasons,omitempty"`
}

func projectEvidence(index int, e FileEvidence) scoringEvidence {
	return scoringEvidence{
		Index:                  index,
		Path:                   e.Path,
		Lines:                  e.Lines,
		DeterministicScore:     e.Score,
		Markers:                e.Markers,
		Churn:                  e.Churn,
		FixTouches:             e.FixTouches,
		IncomingRefs:           e.IncomingRefs,
		PathRisk:               e.PathRisk,
		ContentRisk:            e.ContentRisk,
		SmellRisk:              e.SmellRisk,
		HotspotRisk:            e.HotspotRisk,
		SDKDXRisk:              e.SDKDXRisk,
		UnknownsRisk:           e.UnknownsRisk,
		EnvContractRisk:        e.EnvContractRisk,
		WorkflowSecurityRisk:   e.WorkflowSecurityRisk,
		MigrationSafetyRisk:    e.MigrationSafetyRisk,
		ContainerBuildRisk:     e.ContainerBuildRisk,
		KubernetesSecurityRisk: e.KubernetesSecurityRisk,
		TerraformSecurityRisk:  e.TerraformSecurityRisk,
		OpenAPIContractRisk:    e.OpenAPIContractRisk,
		CORSSecurityRisk:       e.CORSSecurityRisk,
		CookieSecurityRisk:     e.CookieSecurityRisk,
		DependencyHealthRisk:   e.DependencyHealthRisk,
		CentralityRisk:         e.CentralityRisk,
		CochangeRisk:           e.CochangeRisk,
		OwnershipRisk:          e.OwnershipRisk,
		FlakeRisk:              e.FlakeRisk,
		OracleRisk:             e.OracleRisk,
		StaleMarkerRisk:        e.StaleMarkerRisk,
		TestGap:                e.TestGap,
		EvidenceLayers:         e.EvidenceLayers,
		Reasons:                e.Reasons,
	}
}

type batchModelScore struct {
	Index   int      `json:"index"`
	Score   int      `json:"score"`
	Summary string   `json:"summary"`
	Reasons []string `json:"reasons"`
}

func batchScoringPrompt(batch []FileEvidence) string {
	projections := make([]scoringEvidence, len(batch))
	for i, e := range batch {
		projections[i] = projectEvidence(i, e)
	}
	payload, _ := json.MarshalIndent(projections, "", "  ")
	return fmt.Sprintf(`You are Slither, a cheap-model scout. Score each file as a premium-model target.

Return only a compact JSON array, one object per file, keyed by the file's index:
[{"index":0,"score":3,"summary":"one sentence","reasons":["short evidence reason"]}]

Scoring:
1 low signal; 2 minor; 3 plausible; 4 strong; 5 urgent/high leverage.
Prefer files with impact times opportunity: central code, security/config/persistence boundaries, churn-like clues, complexity, TODO/FIXME, risky APIs, weak tests, or architectural leverage.
Score every index exactly once. Do not invent evidence outside this payload.

Files:
%s`, string(payload))
}

func parseModelScores(raw string) ([]batchModelScore, error) {
	trimmed := strings.TrimSpace(raw)
	var scores []batchModelScore
	if err := json.Unmarshal([]byte(trimmed), &scores); err == nil {
		return scores, nil
	}
	match := regexp.MustCompile(`(?s)\[.*\]`).FindString(trimmed)
	if match == "" {
		return nil, fmt.Errorf("no JSON array in response")
	}
	if err := json.Unmarshal([]byte(match), &scores); err != nil {
		return nil, err
	}
	return scores, nil
}
