package slither

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorPreflightSafetyAndWarnings(t *testing.T) {
	if got, _, err := doctorModelsPreflight(context.Background(), "http://token@example.test/v1"); err != nil || got.Status != "refused_unsafe_endpoint" {
		t.Fatalf("userinfo preflight = %#v, %v", got, err)
	}
	previous := doctorLookupIPAddr
	doctorLookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.5")}}, nil
	}
	t.Cleanup(func() { doctorLookupIPAddr = previous })
	got, warning, err := doctorModelsPreflight(context.Background(), "http://model.test/v1")
	if err != nil || got.Status != "refused_non_loopback" || !strings.Contains(warning, "loopback") {
		t.Fatalf("non-loopback preflight = %#v, %q, %v", got, warning, err)
	}
}

func TestDoctorLoopbackPreflightLimitsAndRedaction(t *testing.T) {
	for _, test := range []struct {
		name string
		body []byte
		wait bool
		want string
	}{
		{name: "reachable", body: []byte(`{"data":[]}`), want: "reachable"},
		{name: "invalid JSON", body: []byte(`provider says secret`), want: "invalid_response"},
		{name: "oversized", body: bytes.Repeat([]byte("x"), doctorPreflightCap+1), want: "response_too_large"},
		{name: "timeout", wait: true, want: "unreachable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("doctor sent Authorization")
				}
				if test.wait {
					<-r.Context().Done()
					return
				}
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			got, warning, err := doctorModelsPreflight(context.Background(), server.URL+"/v1")
			if err != nil || got.Status != test.want {
				t.Fatalf("preflight = %#v, %q, %v", got, warning, err)
			}
			if strings.Contains(warning, "secret") || strings.Contains(warning, server.URL) {
				t.Fatalf("warning leaked raw server data: %q", warning)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := doctorModelsPreflight(canceled, "http://127.0.0.1:1/v1"); err == nil {
		t.Fatal("canceled doctor preflight succeeded")
	}
}

func TestReportDefaultsAndUsageErrorsAreTyped(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--summary"}, "slither-summary.md"},
		{[]string{"--summary", "--json"}, "slither-summary.json"},
		{[]string{"--summary", "--out", defaultOut}, defaultOut},
		{[]string{"--json", "--out", defaultOut}, defaultOut},
	} {
		opts, err := resolveReportOptions(defaultConfig(), test.args)
		if err != nil || opts.Out != test.want {
			t.Fatalf("%v => %q, %v; want %q", test.args, opts.Out, err, test.want)
		}
	}
	setTempConfigDir(t)
	for _, test := range [][]string{{"outputs", "missing"}, {"report", "--focus", "["}, {"report", "--top", "0"}, {"eval", "--json"}, {"completion", "fish"}} {
		if err := Run(context.Background(), test, &strings.Builder{}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "run slither "+test[0]+" --help") {
			t.Fatalf("usage %v = %v", test, err)
		}
	}
	if err := Run(context.Background(), []string{"report", filepath.Join(t.TempDir(), "missing")}, &strings.Builder{}, &strings.Builder{}); err == nil || strings.Contains(err.Error(), "--help") {
		t.Fatalf("runtime report error = %v", err)
	}
}

func TestEvalMarkdownSlotsFixtureParityAndPermissions(t *testing.T) {
	dir := t.TempDir()
	report := outcomeTestReport("a", "b")
	for index := 1; index < 20; index++ {
		report.Rows = append(report.Rows, FileEvidence{Path: fmt.Sprintf("row-%d.go", index), EvidenceID: "sha256:" + fmt.Sprintf("%064x", index), Score: 4, ScoreProvenance: ScoreProvenance{Deterministic: 4, SelectedBy: "deterministic"}})
	}
	refreshOutcomeTestReport(&report)
	reportPath, outcomes := filepath.Join(dir, "report.json"), filepath.Join(dir, "outcomes.jsonl")
	writeEvalReport(t, reportPath, report)
	if err := os.WriteFile(outcomes, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := evaluateOutcomeFiles(context.Background(), outcomes, []string{reportPath})
	if err != nil || result.Calibration.topKSlots != 15 {
		t.Fatalf("evaluation slots = %#v, %v", result.Calibration, err)
	}
	markdown := renderEvalMarkdown(result)
	if !strings.Contains(markdown, "slots `15`") || strings.Contains(markdown, reportPath) || strings.Contains(markdown, "sha256:") {
		t.Fatalf("markdown = %s", markdown)
	}
	output := filepath.Join(dir, "eval.md")
	var stdout bytes.Buffer
	args := []string{"--outcomes", outcomes, "--report", reportPath, "--markdown"}
	if err := runEval(context.Background(), args, &stdout); err != nil {
		t.Fatal(err)
	}
	if err := runEval(context.Background(), append(args, "--out", output), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, output); string(got) != stdout.String() {
		t.Fatal("markdown file/stdout mismatch")
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("markdown permissions = %v, %v", info, err)
	}
	if err := runEval(context.Background(), append(args, "--out", outcomes), &bytes.Buffer{}); err == nil {
		t.Fatal("markdown output alias accepted")
	}
	zero := renderEvalMarkdown(evalResult{Calibration: evalCalibration{TopK: evalTopK, topKSlots: 0, TopKScoreSaturation: 1}})
	if !strings.Contains(zero, "slots `0`") {
		t.Fatalf("zero markdown = %s", zero)
	}
}

func TestRunnableEvalExpectedJSONAndCatalogDetails(t *testing.T) {
	base := filepath.Join("testdata", "runnable_eval")
	var output bytes.Buffer
	if err := runEval(context.Background(), []string{"--outcomes", filepath.Join(base, "outcomes.jsonl"), "--report", filepath.Join(base, "report.json"), "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), string(readTestFile(t, filepath.Join(base, "expected.json"))); got != want {
		t.Fatalf("runnable eval JSON mismatch\nwant=%s\ngot=%s", want, got)
	}
	var human strings.Builder
	if err := runOutputs([]string{"agent"}, &human); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"operation query:", "required: limit", "stable errors:", "limits:", "compatibility:"} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("catalog human missing %q", want)
		}
	}
	catalog := buildOutputCatalog()
	if len(catalog.Surfaces[4].OperationDetail) != 5 || len(catalog.Surfaces[4].Errors) != len(agentProtocolErrors) {
		t.Fatalf("agent catalog = %#v", catalog.Surfaces[4])
	}
}

func TestInventoryRoutingAllLanesAndCanonicalOrder(t *testing.T) {
	row := FileEvidence{Path: "cmd/api/database_cli.go", Score: 4, PathRisk: 1, ContentRisk: 1, SDKDXRisk: 1, MigrationSafetyRisk: 1, OpenAPIContractRisk: 1, DependencyHealthRisk: 1, WorkflowSecurityRisk: 1, FlakeRisk: 1, HotspotRisk: 1, TestGap: true, CentralityRisk: 1, EvidenceLayers: []string{"content-risk", "secret-risk"}, Reasons: []string{"shell_boundary", "background_context"}}
	for _, lane := range reviewLaneNamesForMetadata() {
		if !validReviewLane(lane) || !rowMatchesInventory(row, lane) {
			t.Fatalf("lane %q did not match", lane)
		}
	}
	_, plan := BuildReviewPlan([]FileEvidence{row})
	if len(plan) != len(reviewLaneNamesForMetadata()) {
		t.Fatalf("multi-lane plan = %#v", plan)
	}
	for index, lane := range reviewLaneNamesForMetadata() {
		if plan[index].Lane != lane {
			t.Fatalf("plan[%d] = %s, want %s", index, plan[index].Lane, lane)
		}
	}
	if validReviewLane("missing") || rowMatchesInventory(row, "missing") {
		t.Fatal("invalid lane accepted")
	}
}
