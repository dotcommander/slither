package slither

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

const reportSchemaVersion = "slither.report/v1"

const (
	modelProjectionContractVersion = "scoring-evidence/v1"
	maxGitMetadataBytes            = 1 << 20
)

var errGitMetadataTooLarge = errors.New("git metadata output exceeds limit")

func sha256Identity(domain string, value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal %s identity: %v", domain, err))
	}
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(payload)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func contentIdentity(inspected []byte, size int64, truncated bool) string {
	return sha256Identity("slither.content/v1", struct {
		Bytes     []byte `json:"bytes"`
		Size      int64  `json:"size"`
		Truncated bool   `json:"truncated"`
	}{inspected, size, truncated})
}

func modelContractIdentity() string {
	return modelContractIdentityFor(batchScoringPromptTemplate, scoreCacheContractVersion, modelProjectionContractVersion)
}

func modelContractIdentityFor(prompt, cacheContract, projectionContract string) string {
	return sha256Identity("slither.model-contract/v1", struct {
		Prompt             string `json:"prompt"`
		CacheContract      string `json:"cache_contract"`
		ProjectionContract string `json:"projection_contract"`
	}{prompt, cacheContract, projectionContract})
}

func normalizedReportParameters(opts Options, patternsID string) ReportParameters {
	p := ReportParameters{
		Days:       opts.Days,
		MaxBytes:   opts.MaxBytes,
		Top:        opts.Top,
		Focus:      opts.Focus,
		Include:    normalizedStrings(opts.Include),
		Exclude:    normalizedStrings(opts.Exclude),
		Inventory:  opts.Inventory,
		PatternsID: patternsID,
	}
	if opts.Model != "" {
		p.Model = opts.Model
		p.BaseURL = scrubOutputSecrets(opts.BaseURL)
		// Provider fallback order and duplicate entries are part of the scoring
		// contract, unlike include/exclude alternatives.
		p.FallbackModels = append([]string(nil), opts.FallbackModels...)
		p.ModelContractID = modelContractIdentity()
	}
	return p
}

func normalizedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	unique := out[:0]
	for _, value := range out {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func deterministicScore(row FileEvidence) int {
	if row.ScoreProvenance.Deterministic != 0 {
		return row.ScoreProvenance.Deterministic
	}
	return row.Score
}

func setDeterministicProvenance(row *FileEvidence) {
	if row == nil {
		return
	}
	deterministic := deterministicScore(*row)
	row.Score = deterministic
	row.ScoreProvenance = ScoreProvenance{Deterministic: deterministic, SelectedBy: "deterministic"}
}

func deterministicReasons(reasons []string) []string {
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if strings.HasPrefix(reason, "model:") || strings.HasPrefix(reason, "model_error:") {
			continue
		}
		out = append(out, reason)
	}
	return out
}

func deterministicLayers(layers []string) []string {
	out := make([]string, 0, len(layers))
	for _, layer := range layers {
		if layer == "model" || layer == "model-error" {
			continue
		}
		out = append(out, layer)
	}
	return out
}

func evidenceIdentity(repo string, row FileEvidence) string {
	// Metadata such as confidence and actionability is score-sensitive. Rebuild
	// it from the deterministic projection so model selection cannot alter
	// evidence identity indirectly.
	deterministic := row
	deterministic.Score = deterministicScore(row)
	deterministic.ScoreProvenance = ScoreProvenance{Deterministic: deterministic.Score, SelectedBy: "deterministic"}
	deterministic.Reasons = deterministicReasons(row.Reasons)
	deterministic.EvidenceLayers = deterministicLayers(row.EvidenceLayers)
	// These fields are finalized after scoring in the production path. Clear
	// every derived field before rebuilding so a model, provider degradation,
	// cull pass, or cache hit cannot leak into the deterministic projection.
	deterministic.ID = ""
	deterministic.EvidenceClass = ""
	deterministic.Confidence = ""
	deterministic.Actionability = ""
	deterministic.Caveat = ""
	deterministic.VerifyCmd = ""
	deterministic.OmittedReason = ""
	finalizeEvidenceMetadata(repo, &deterministic)
	return sha256Identity("slither.evidence/v1", struct {
		ID                     string             `json:"id"`
		Path                   string             `json:"path"`
		ContentID              string             `json:"content_id"`
		EvidenceClass          string             `json:"evidence_class"`
		Confidence             string             `json:"confidence"`
		Actionability          Actionability      `json:"actionability"`
		Caveat                 string             `json:"caveat"`
		VerifyCmd              string             `json:"verify_cmd"`
		OmittedReason          string             `json:"omitted_reason"`
		Bytes                  int64              `json:"bytes"`
		Lines                  int                `json:"lines"`
		SeedScore              float64            `json:"seed_score"`
		DeterministicScore     int                `json:"deterministic_score"`
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
		EvidenceLayers         []string           `json:"evidence_layers"`
		Reasons                []string           `json:"reasons"`
		EvidenceLocations      []EvidenceLocation `json:"evidence_locations"`
	}{
		deterministic.ID, deterministic.Path, deterministic.ContentID, deterministic.EvidenceClass, deterministic.Confidence, deterministic.Actionability, deterministic.Caveat, deterministic.VerifyCmd, deterministic.OmittedReason,
		row.Bytes, row.Lines, row.SeedScore, deterministicScore(row), row.Churn, row.FixTouches, row.Markers, row.Imports, row.IncomingRefs,
		row.SmellRisk, row.HotspotRisk, row.SDKDXRisk, row.UnknownsRisk, row.EnvContractRisk, row.WorkflowSecurityRisk, row.MigrationSafetyRisk,
		row.ContainerBuildRisk, row.KubernetesSecurityRisk, row.TerraformSecurityRisk, row.OpenAPIContractRisk, row.CORSSecurityRisk,
		row.CookieSecurityRisk, row.DependencyHealthRisk, row.CentralityRisk, row.CochangeRisk, row.OwnershipRisk, row.FlakeRisk, row.OracleRisk,
		row.StaleMarkerRisk, row.TestGap, row.PathRisk, row.ContentRisk, deterministicLayers(row.EvidenceLayers), deterministicReasons(row.Reasons), row.EvidenceLocations,
	})
}

func sourceLimitations(signals []string) []string {
	var out []string
	for _, signal := range signals {
		if strings.HasPrefix(signal, "git_ls_files:") || strings.HasPrefix(signal, "filesystem_walk:") || strings.HasPrefix(signal, "scan:unreadable_") || strings.HasPrefix(signal, "scan:content_truncated") || strings.HasPrefix(signal, "git_metadata:") {
			out = append(out, signal)
		}
	}
	return normalizedStrings(out)
}

func sourceStateForReport(ctx context.Context, repo string, discovery DiscoveryStats, rows []FileEvidence, signals []string) (SourceState, []string, error) {
	if err := ctx.Err(); err != nil {
		return SourceState{}, nil, err
	}
	state := SourceState{Kind: discovery.Source}
	if state.Kind == "" {
		state.Kind = "filesystem"
	}
	if state.Kind == "git" {
		head, err := gitMetadataRunner(ctx, repo, "rev-parse", "--verify", "--quiet", "HEAD")
		if err != nil {
			if ctx.Err() != nil {
				return SourceState{}, nil, ctx.Err()
			}
			if !isUnbornGitRepository(ctx, repo) {
				signals = append(signals, "git_metadata:head_unavailable")
			}
			// An unborn repository has no HEAD but is otherwise healthy; all
			// other failures retain an empty, conservative head value.
			state.Head = ""
		} else {
			state.Head = strings.TrimSpace(head)
		}
		status, err := gitMetadataRunner(ctx, repo, "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			if ctx.Err() != nil {
				return SourceState{}, nil, ctx.Err()
			}
			state.Dirty = true
			signals = append(signals, "git_metadata:dirty_unavailable")
		} else {
			state.Dirty = strings.TrimSpace(status) != ""
		}
	} else {
		state.Kind = "filesystem"
	}
	type treeRow struct {
		Path      string `json:"path"`
		ContentID string `json:"content_id"`
	}
	treeRows := make([]treeRow, 0, len(rows))
	for _, row := range rows {
		treeRows = append(treeRows, treeRow{row.Path, row.ContentID})
	}
	sort.Slice(treeRows, func(i, j int) bool { return treeRows[i].Path < treeRows[j].Path })
	state.TreeDigest = sha256Identity("slither.tree/v1", struct {
		Rows        []treeRow      `json:"rows"`
		Discovery   DiscoveryStats `json:"discovery"`
		Limitations []string       `json:"limitations"`
	}{treeRows, discovery, sourceLimitations(signals)})
	return state, signals, nil
}

func isUnbornGitRepository(ctx context.Context, repo string) bool {
	ref, err := gitMetadataRunner(ctx, repo, "symbolic-ref", "-q", "HEAD")
	return err == nil && strings.TrimSpace(ref) != ""
}

var gitMetadataRunner = gitMetadata

func gitMetadata(ctx context.Context, repo string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	out, readErr := readBoundedGitMetadata(stdout)
	if errors.Is(readErr, errGitMetadataTooLarge) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", readErr
	}
	if readErr != nil {
		_ = cmd.Wait()
		return "", readErr
	}
	if err := cmd.Wait(); err != nil {
		return "", err
	}
	return string(out), nil
}

func readBoundedGitMetadata(reader io.Reader) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(reader, maxGitMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxGitMetadataBytes {
		return nil, errGitMetadataTooLarge
	}
	return bytes.Clone(out), nil
}

func reportIdentity(report Report) string {
	type reportRow struct {
		EvidenceID string `json:"evidence_id"`
		Score      int    `json:"score"`
	}
	rows := make([]reportRow, len(report.Rows))
	for i, row := range report.Rows {
		rows[i] = reportRow{row.EvidenceID, row.Score}
	}
	return sha256Identity("slither.report/v1", struct {
		SchemaVersion string           `json:"schema_version"`
		SourceState   SourceState      `json:"source_state"`
		Parameters    ReportParameters `json:"parameters"`
		Rows          []reportRow      `json:"rows"`
	}{report.SchemaVersion, report.SourceState, report.Parameters, rows})
}
