package slither

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestBuildContextPacketSelectionFocusAndDefault(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "a.go", "package fixture\nfunc A() {}\n")
	writeContextFile(t, repo, "b.go", "package fixture\nfunc B() {}\n")
	report := contextTestReport(repo, "a.go", "b.go")
	report.Rows[0].Score, report.Rows[0].EvidenceLayers, report.Rows[0].ContentRisk = 4, []string{"content-risk", "centrality"}, 1

	defaultPacket, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{BudgetBytes: 4096})
	if err != nil || len(defaultPacket.Targets) != 1 || defaultPacket.Targets[0].ID != report.Rows[0].ID {
		t.Fatalf("default packet = %#v err=%v", defaultPacket, err)
	}
	packet, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{report.Rows[1].ID, report.Rows[0].ID, report.Rows[1].ID}, Focus: "b\\.go", BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{packet.Targets[0].ID, packet.Targets[1].ID}; !reflect.DeepEqual(got, []string{report.Rows[0].ID, report.Rows[1].ID}) {
		t.Fatalf("selection = %#v, want report-row order", got)
	}
	if _, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{"file:a.go"}, BudgetBytes: 4096}); !errors.Is(err, ErrContextTargetNotFound) {
		t.Fatalf("legacy/non-current ID error = %v", err)
	}
}

func TestBuildContextPacketDeclarationLexicalAndComponents(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "go.mod", "module example.test/capsule\n")
	writeContextFile(t, repo, "internal/core/core.go", "package core\n\nfunc Keep() {}\n\nfunc Risky() {\n\tpanic(\"risk\")\n}\n")
	writeContextFile(t, repo, "cmd/app.go", "package cmd\n\nimport \"example.test/capsule/internal/core\"\n\nfunc Run() { core.Risky() }\n")
	writeContextFile(t, repo, "internal/core/core_test.go", "package core\nfunc TestRisky(t testing.T) {}\n")
	report, err := BuildReport(context.Background(), Options{Repo: repo, Top: 20, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	core := findRow(report, "internal/core/core.go")
	if core == nil {
		t.Fatal("missing core row")
	}
	core.EvidenceLocations = []EvidenceLocation{{Reason: "content:panic", Line: 5, Snippet: "panic"}}
	for index := range report.contextRows {
		if report.contextRows[index].Path == core.Path {
			report.contextRows[index].EvidenceLocations = append([]EvidenceLocation(nil), core.EvidenceLocations...)
		}
	}
	packet, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{core.ID}, BudgetBytes: 16 << 10})
	if err != nil {
		t.Fatal(err)
	}
	target := packet.Targets[0]
	if target.Resolution != "go_declaration" || len(target.Components) == 0 || !strings.Contains(target.Components[0].Text, "func Risky") || strings.Contains(target.Components[0].Text, "func Keep") {
		t.Fatalf("Go declaration target = %#v", target)
	}
	kinds := contextComponentKinds(target.Components)
	if !reflect.DeepEqual(kinds, []string{"target", "importer", "test", "support"}) {
		t.Fatalf("component order = %#v", kinds)
	}

	writeContextFile(t, repo, "note.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\n")
	lexical := contextTestReport(repo, "note.txt")
	lexical.Rows[0].EvidenceLocations = []EvidenceLocation{{Reason: "content:note", Line: 10, Snippet: "ten"}}
	lexical.contextRows[0].EvidenceLocations = append([]EvidenceLocation(nil), lexical.Rows[0].EvidenceLocations...)
	packet, err = BuildContextPacket(context.Background(), lexical, ContextPacketRequest{TargetIDs: []string{lexical.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil || packet.Targets[0].Resolution != "lexical" || !strings.Contains(packet.Targets[0].Components[0].Text, "two") || !strings.Contains(packet.Targets[0].Components[0].Text, "eleven") {
		t.Fatalf("lexical target = %#v err=%v", packet.Targets, err)
	}
}

func TestContextImportGraphPreservesCountsAndRanking(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "go.mod", "module example.test/graph\n")
	writeContextFile(t, repo, "core/core.go", "package core\nfunc Value() int { return 1 }\n")
	writeContextFile(t, repo, "cmd/a.go", "package cmd\nimport \"example.test/graph/core\"\nfunc A() { core.Value() }\n")
	writeContextFile(t, repo, "cmd/b.go", "package cmd\nimport \"example.test/graph/core\"\nfunc B() { core.Value() }\n")
	first, err := BuildReport(context.Background(), Options{Repo: repo, Top: 20, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildReport(context.Background(), Options{Repo: repo, Top: 20, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	core := findRow(first, "core/core.go")
	if core == nil || core.IncomingRefs != 2 {
		t.Fatalf("core incoming refs = %#v", core)
	}
	if !reflect.DeepEqual(first.Rows, second.Rows) {
		t.Fatal("retained context graph changed report ranking or rows")
	}
	if len(first.contextEdges) != 2 || first.contextEdges[0].Kind != "go_package" {
		t.Fatalf("retained import graph = %#v", first.contextEdges)
	}
}

func TestBuildContextPacketBudgetAccountingAndOmissions(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "a.go", "package fixture\n"+strings.Repeat("// é\n", 200))
	report := contextTestReport(repo, "a.go")
	request := ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID}, BudgetBytes: 4096}
	packet, err := BuildContextPacket(context.Background(), report, request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(packet)
	if err != nil || packet.UsedBytes != len(encoded) || packet.UsedBytes > request.BudgetBytes {
		t.Fatalf("accounting packet=%#v encoded=%d err=%v", packet, len(encoded), err)
	}
	_, err = BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID}, BudgetBytes: 1})
	var budgetError *ContextBudgetError
	if !errors.As(err, &budgetError) || budgetError.Min <= 1 || !errors.Is(err, ErrContextBudgetTooSmall) {
		t.Fatalf("measured minimum error = %#v err=%v", budgetError, err)
	}
	if _, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID}, BudgetBytes: budgetError.Min}); err != nil {
		t.Fatalf("measured minimum did not fit: %v", err)
	}
	if _, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID}, BudgetBytes: maxContextPacketBytes + 1}); !errors.Is(err, ErrContextBudgetTooLarge) {
		t.Fatalf("large budget error = %v", err)
	}
	if !sort.StringsAreSorted(packet.Omissions) {
		t.Fatalf("omissions not sorted: %#v", packet.Omissions)
	}
	for _, component := range packet.Targets[0].Components {
		if !utf8.ValidString(component.Text) {
			t.Fatalf("component split UTF-8: %#v", component)
		}
	}
}

func TestBuildContextPacketSafetyCancellationAndUniqueReads(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "a.go", "package fixture\nfunc A() {}\n")
	writeContextFile(t, repo, "b.go", "package fixture\nfunc B() {}\n")
	report := contextTestReport(repo, "a.go", "b.go")
	previous := contextDescriptorReader
	var reads atomic.Int64
	var maxRead atomic.Int64
	contextDescriptorReader = func(reader io.Reader, limit int64) (string, bool, bool, error) {
		reads.Add(1)
		maxRead.Store(limit)
		return previous(reader, limit)
	}
	t.Cleanup(func() { contextDescriptorReader = previous })
	_, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID, report.Rows[1].ID}, BudgetBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 2 || maxRead.Load() != 1024 {
		t.Fatalf("unique/fair source reads = reads %d max %d", reads.Load(), maxRead.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	contextDescriptorReader = func(reader io.Reader, limit int64) (string, bool, bool, error) {
		cancel()
		return previous(reader, limit)
	}
	_, err = BuildContextPacket(ctx, report, ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID}, BudgetBytes: 2048})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "escape.go")); err != nil {
		t.Fatal(err)
	}
	escape := contextTestReport(repo, "escape.go")
	if _, err := BuildContextPacket(context.Background(), escape, ContextPacketRequest{TargetIDs: []string{escape.Rows[0].ID}, BudgetBytes: 2048}); !errors.Is(err, ErrContextUnsafeTarget) {
		t.Fatalf("external symlink error = %v", err)
	}
}

func TestBuildContextPacketSecretRiskDoesNotReadSource(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "secret.go", "package fixture\nconst token = \"sk-should-not-read\"\n")
	report := contextTestReport(repo, "secret.go")
	report.Rows[0].EvidenceLayers = []string{"secret-risk"}
	report.contextRows[0].EvidenceLayers = []string{"secret-risk"}
	previous := contextDescriptorReader
	contextDescriptorReader = func(io.Reader, int64) (string, bool, bool, error) {
		t.Fatal("secret row was read")
		return "", false, false, nil
	}
	t.Cleanup(func() { contextDescriptorReader = previous })
	packet, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID}, BudgetBytes: 2048})
	if err != nil || packet.Targets[0].Resolution != "redacted" || packet.Targets[0].Components[0].Text != "[redacted: secret-risk evidence]" {
		t.Fatalf("secret packet = %#v err=%v", packet, err)
	}
}

func TestContextPacketScrubsEvidenceAndSecretNeighbors(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "a.go", "package fixture\nfunc A() {}\n")
	writeContextFile(t, repo, "secret.go", "package fixture\nconst token = \"sk-should-not-read\"\n")
	report := contextTestReport(repo, "a.go", "secret.go")
	report.Rows[0].EvidenceLocations = []EvidenceLocation{{Reason: "content:token", Line: 2, Snippet: "api_key=abcdefghijklmnopqrstuvwx"}}
	report.Rows[1].EvidenceLayers = []string{"secret-risk"}
	report.contextRows = append([]FileEvidence(nil), report.Rows...)
	report.contextEdges = []localImportEdge{{Importer: "a.go", Imported: "secret.go", Kind: "local_module"}}
	previous := contextDescriptorReader
	var reads atomic.Int64
	contextDescriptorReader = func(reader io.Reader, limit int64) (string, bool, bool, error) {
		reads.Add(1)
		return previous(reader, limit)
	}
	t.Cleanup(func() { contextDescriptorReader = previous })
	packet, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 {
		t.Fatalf("secret neighbor source reads = %d, want target only", reads.Load())
	}
	if got := packet.Targets[0].EvidenceLocations[0].Snippet; strings.Contains(got, "abcdefghijkl") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("evidence snippet was not scrubbed: %q", got)
	}
	components := packet.Targets[0].Components
	if len(components) != 2 || components[1].Text != "[redacted: secret-risk evidence]" {
		t.Fatalf("secret neighbor component = %#v", components)
	}
}

func TestContextPacketSecretMarkerIsAtomicUnderBudget(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "a.go", "package fixture\nfunc A() {}\n")
	writeContextFile(t, repo, "secret.go", "package fixture\nconst token = \"sk-should-not-read\"\n")
	report := contextTestReport(repo, "a.go", "secret.go")
	report.Rows[1].EvidenceLayers = []string{"secret-risk"}
	report.contextRows = append([]FileEvidence(nil), report.Rows...)
	report.contextEdges = []localImportEdge{{Importer: "a.go", Imported: "secret.go", Kind: "local_module"}}
	request := ContextPacketRequest{TargetIDs: []string{report.Rows[0].ID}, BudgetBytes: 4096}
	full, err := BuildContextPacket(context.Background(), report, request)
	if err != nil {
		t.Fatal(err)
	}
	if !containsContextMarker(full.Targets[0].Components) {
		t.Fatalf("full packet omitted redacted marker: %#v", full)
	}
	_, err = BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: request.TargetIDs, BudgetBytes: 1})
	var budgetError *ContextBudgetError
	if !errors.As(err, &budgetError) {
		t.Fatalf("minimum budget error = %v", err)
	}
	tight, err := BuildContextPacket(context.Background(), report, ContextPacketRequest{TargetIDs: request.TargetIDs, BudgetBytes: budgetError.Min})
	if err != nil {
		t.Fatal(err)
	}
	if containsContextMarker(tight.Targets[0].Components) {
		for _, component := range tight.Targets[0].Components {
			if strings.Contains(component.Text, "[redacted:") && component.Text != "[redacted: secret-risk evidence]" {
				t.Fatalf("redacted marker was trimmed: %#v", component)
			}
		}
		return
	}
	want := "target:" + report.Rows[0].ID + ":component:imported:" + report.Rows[1].ID + ":budget"
	count := 0
	for _, omission := range tight.Omissions {
		if strings.Contains(omission, ":component:imported:"+report.Rows[1].ID+":") {
			count++
			if omission != want {
				t.Fatalf("secret omission = %q, want only budget omission %q", omission, want)
			}
		}
	}
	if count != 1 {
		t.Fatalf("secret marker omissions = %#v, want exactly one", tight.Omissions)
	}
}

func TestContextPacketRetainsSourceAndBudgetOmissions(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "truncated.go", "package fixture\n"+strings.Repeat("// source\n", 64))
	truncated := contextTestReport(repo, "truncated.go")
	truncated.Parameters.MaxBytes = 16
	packet, err := BuildContextPacket(context.Background(), truncated, ContextPacketRequest{TargetIDs: []string{truncated.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if !containsContextOmission(packet.Omissions, "target:"+truncated.Rows[0].ID+":component:target:"+truncated.Rows[0].ID+":source_truncated") {
		t.Fatalf("missing source truncation omission: %#v", packet.Omissions)
	}

	writeContextFile(t, repo, "budget.go", "package fixture\n\nfunc Risky() {\n"+strings.Repeat("\tprintln(\"ordinary\")\n", 30)+"\tpanic(\"risk\")\n}\n")
	budgeted := contextTestReport(repo, "budget.go")
	budgeted.Rows[0].EvidenceLocations = []EvidenceLocation{{Reason: "content:panic", Line: 34, Snippet: "panic"}}
	budgeted.contextRows[0].EvidenceLocations = append([]EvidenceLocation(nil), budgeted.Rows[0].EvidenceLocations...)
	packet, err = BuildContextPacket(context.Background(), budgeted, ContextPacketRequest{TargetIDs: []string{budgeted.Rows[0].ID}, BudgetBytes: 1140})
	if err != nil {
		t.Fatal(err)
	}
	if !containsContextOmission(packet.Omissions, "target:"+budgeted.Rows[0].ID+":component:target:"+budgeted.Rows[0].ID+":budget") {
		t.Fatalf("missing ordinary budget omission: %#v", packet.Omissions)
	}
}

func TestContextPacketFinalizesEmptySecretAndLexicalFallback(t *testing.T) {
	repo := t.TempDir()
	empty := contextTestReport(repo, "missing.go")
	packet, err := BuildContextPacket(context.Background(), empty, ContextPacketRequest{BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	assertContextPacketBytes(t, packet)

	writeContextFile(t, repo, "secret.go", "package fixture\nconst token = \"sk-should-not-read\"\n")
	secret := contextTestReport(repo, "secret.go")
	secret.Rows[0].EvidenceLayers = []string{"secret-risk"}
	secret.contextRows[0].EvidenceLayers = []string{"secret-risk"}
	packet, err = BuildContextPacket(context.Background(), secret, ContextPacketRequest{TargetIDs: []string{secret.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	assertContextPacketBytes(t, packet)

	writeContextFile(t, repo, "comment.go", "// evidence outside declarations\n\nfunc A() {}\n")
	lexical := contextTestReport(repo, "comment.go")
	lexical.Rows[0].EvidenceLocations = []EvidenceLocation{{Reason: "content:comment", Line: 1, Snippet: "evidence"}}
	lexical.contextRows[0].EvidenceLocations = append([]EvidenceLocation(nil), lexical.Rows[0].EvidenceLocations...)
	packet, err = BuildContextPacket(context.Background(), lexical, ContextPacketRequest{TargetIDs: []string{lexical.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil || packet.Targets[0].Resolution != "lexical" {
		t.Fatalf("no-enclosing-declaration fallback = %#v err=%v", packet.Targets, err)
	}
}

func TestContextPacketOmissionUniquenessAndIntermediateSymlink(t *testing.T) {
	repo := t.TempDir()
	writeContextFile(t, repo, "a.go", "package fixture\nfunc A() {}\n")
	writeContextFile(t, repo, "b.go", "package fixture\nfunc B() {}\n")
	writeContextFile(t, repo, "go.mod", "module example.test/context\n")
	report := contextTestReport(repo, "a.go", "b.go", "go.mod")
	plans := planContextComponents(report.Rows[:2], report.contextRows, nil)
	omissions := contextPlanOmissions(plans)
	if len(omissions) != len(uniqueStrings(omissions)) {
		t.Fatalf("omissions collide across targets: %#v", omissions)
	}

	outside := t.TempDir()
	writeContextFile(t, outside, "nested/escape.go", "package outside\n")
	if err := os.Symlink(filepath.Join(outside, "nested"), filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}
	escape := contextTestReport(repo, "linked/escape.go")
	if _, err := BuildContextPacket(context.Background(), escape, ContextPacketRequest{TargetIDs: []string{escape.Rows[0].ID}, BudgetBytes: 4096}); !errors.Is(err, ErrContextUnsafeTarget) {
		t.Fatalf("intermediate symlink error = %v", err)
	}
}

func assertProofObligationContract(t *testing.T) {
	t.Helper()
	rows := []FileEvidence{
		{ID: "slither:file:YXV0aC5nbw", EvidenceID: "sha256:" + strings.Repeat("a", 64), Path: "internal/auth.go", Actionability: ActionabilityLikelyDefect, Reasons: []string{"content:ssrf_url_param:1", "model:review auth", "content:ssrf_url_param:1", "workflow_security:unpinned_actions:1"}, VerifyCmd: "go test ./internal/..."},
		{ID: "slither:file:aW52YXJpYW50Lmdv", EvidenceID: "sha256:" + strings.Repeat("b", 64), Path: "internal/invariant.go", Actionability: ActionabilityHighRiskInspect, Reasons: []string{"workflow_security:unpinned_actions:1"}, VerifyCmd: "go test ./internal/..."},
		{ID: "slither:file:Z28ubW9k", EvidenceID: "sha256:" + strings.Repeat("c", 64), Path: "go.mod", Actionability: ActionabilityDependencyReview, Reasons: []string{"dependency_health:go_module_replace"}, VerifyCmd: "go test ./..."},
		{ID: "slither:file:aG90Lmdv", EvidenceID: "sha256:" + strings.Repeat("d", 64), Path: "internal/hot.go", Actionability: ActionabilityHotspot, Reasons: []string{"git:hotspot"}},
		{ID: "slither:file:d2Vhay5nbw", EvidenceID: "sha256:" + strings.Repeat("e", 64), Path: "internal/weak.go", Actionability: ActionabilityVerifyFirst, Reasons: []string{"low-signal"}, VerifyCmd: "go test ./internal/..."},
		{ID: "slither:file:c2VydmljZS5nbw", EvidenceID: "sha256:" + strings.Repeat("f", 64), Path: "internal/service.go", Actionability: ActionabilityInspect, Reasons: []string{"content:stateful_store:1"}, VerifyCmd: "go test ./internal/..."},
	}
	want := []struct {
		Actionability Actionability   `json:"actionability"`
		Proof         ProofObligation `json:"proof"`
	}{}
	data, err := os.ReadFile(filepath.Join(llmAgentFixtureDir, "proof-obligations-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(rows) {
		t.Fatalf("proof golden count = %d, want %d", len(want), len(rows))
	}
	for index, row := range rows {
		got, err := proofObligationForRow(row)
		if err != nil {
			t.Fatal(err)
		}
		if want[index].Actionability != row.Actionability || !reflect.DeepEqual(got, want[index].Proof) {
			t.Fatalf("proof golden %s = %#v, want %#v", row.Actionability, got, want[index].Proof)
		}
		if got.ID != proofObligationIdentity(row.EvidenceID, got) {
			t.Fatalf("proof ID = %q, want canonical identity", got.ID)
		}
	}
}

func assertPromotionGateContract(t *testing.T) {
	t.Helper()
	for _, actionability := range []Actionability{
		ActionabilityLikelyDefect,
		ActionabilityHighRiskInspect,
		ActionabilityDependencyReview,
		ActionabilityHotspot,
		ActionabilityVerifyFirst,
		ActionabilityInspect,
	} {
		for _, path := range []string{"testdata/auth.fixture", "internal/auth_test.go", "api/generated.pb.go", "docs/auth.md"} {
			row := FileEvidence{ID: stableFileID(path), EvidenceID: "sha256:" + strings.Repeat("a", 64), Path: path, Actionability: actionability, VerifyCmd: "go test ./..."}
			got, err := proofObligationForRow(row)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(got.PromotionGate, "; Production source or runtime witness required before promotion") {
				t.Fatalf("%s/%s promotion gate = %q", actionability, path, got.PromotionGate)
			}
			productionWitnesses := 0
			for _, falsifier := range got.Falsifiers {
				if falsifier == (Falsifier{Operation: FalsifierOperationInspect, Subject: "production_witness"}) {
					productionWitnesses++
				}
			}
			if productionWitnesses != 1 || got.Falsifiers[len(got.Falsifiers)-1] != (Falsifier{Operation: FalsifierOperationInspect, Subject: "production_witness"}) {
				t.Fatalf("%s/%s falsifiers = %#v", actionability, path, got.Falsifiers)
			}
		}
	}

	base := FileEvidence{ID: "slither:file:YQ", EvidenceID: "sha256:" + strings.Repeat("b", 64), Path: "internal/a.go", Actionability: ActionabilityInspect, Reasons: []string{"content:stateful_store:1"}, VerifyCmd: "go test ./internal/..."}
	first, err := proofObligationForRow(base)
	if err != nil {
		t.Fatal(err)
	}
	modelChanged := base
	modelChanged.Score = 99
	modelScore := 3
	modelChanged.ScoreProvenance = ScoreProvenance{Deterministic: 2, Model: &modelScore, SelectedBy: "model"}
	modelChanged.Summary = "model prose"
	modelChanged.Reasons = append(modelChanged.Reasons, "model:prose", "model_error:provider")
	modelChanged.EvidenceLayers = []string{"content-risk", "model", "model-error"}
	second, err := proofObligationForRow(modelChanged)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !reflect.DeepEqual(first.Support, []string{"content:stateful_store:1"}) {
		t.Fatalf("model-only proof drift = first %#v second %#v", first, second)
	}
	for _, changed := range []FileEvidence{
		func() FileEvidence { row := base; row.EvidenceID = "sha256:" + strings.Repeat("c", 64); return row }(),
		func() FileEvidence { row := base; row.Actionability = ActionabilityVerifyFirst; return row }(),
		func() FileEvidence { row := base; row.Path = "docs/a.md"; return row }(),
		func() FileEvidence { row := base; row.VerifyCmd = "go test ./..."; return row }(),
	} {
		got, err := proofObligationForRow(changed)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID == first.ID {
			t.Fatalf("proof identity did not change for %#v", changed)
		}
	}

	for _, actionability := range []Actionability{"", "unknown"} {
		if _, err := proofObligationForRow(FileEvidence{ID: base.ID, EvidenceID: base.EvidenceID, Path: base.Path, Actionability: actionability}); !errors.Is(err, ErrUnsupportedActionability) {
			t.Fatalf("actionability %q error = %v", actionability, err)
		}
	}
	proof, err := proofObligationForRow(FileEvidence{ID: base.ID, EvidenceID: base.EvidenceID, Path: base.Path, Actionability: ActionabilityInspect})
	if err != nil || proof.Support == nil || proof.Verify == nil || len(proof.Support) != 0 || len(proof.Verify) != 0 {
		t.Fatalf("empty deterministic evidence proof = %#v err=%v", proof, err)
	}
	for _, falsifier := range []Falsifier{
		{Operation: FalsifierOperationRead, Path: "a.go"},
		{Operation: FalsifierOperationSearch, Query: "a", Path: "internal"},
		{Operation: FalsifierOperationInspect, Subject: "callers"},
		{Operation: FalsifierOperationRunVerify, Command: "go test ./..."},
	} {
		if err := falsifier.Validate(); err != nil {
			t.Fatalf("valid falsifier %#v: %v", falsifier, err)
		}
	}
	if err := (Falsifier{Operation: FalsifierOperationRead, Path: "a.go", Query: "no"}).Validate(); !errors.Is(err, ErrInvalidFalsifier) {
		t.Fatalf("invalid falsifier error = %v", err)
	}
	for _, path := range []string{"/outside", "../outside.go"} {
		if err := (Falsifier{Operation: FalsifierOperationRead, Path: path}).Validate(); !errors.Is(err, ErrInvalidFalsifier) {
			t.Fatalf("invalid read path %q error = %v", path, err)
		}
	}
	if err := (Falsifier{Operation: FalsifierOperationSearch, Path: "/outside", Query: "auth"}).Validate(); !errors.Is(err, ErrInvalidFalsifier) {
		t.Fatalf("invalid search scope error = %v", err)
	}

	repo := t.TempDir()
	writeContextFile(t, repo, "internal/a.go", "package internal\nfunc A() {}\n")
	baseReport := contextTestReport(repo, "internal/a.go")
	baseReport.Rows[0].Score = 2
	baseReport.Rows[0].ScoreProvenance = ScoreProvenance{Deterministic: 2, SelectedBy: "deterministic"}
	baseReport.Rows[0].Reasons = []string{"content:stateful_store:1"}
	baseReport.Rows[0].EvidenceLayers = []string{"content-risk"}
	baseReport.Rows[0].VerifyCmd = "go test ./internal/..."
	baseReport.contextRows = append([]FileEvidence(nil), baseReport.Rows...)
	stable, err := BuildContextPacket(context.Background(), baseReport, ContextPacketRequest{TargetIDs: []string{baseReport.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	modelProjection := baseReport
	modelProjection.Rows = append([]FileEvidence(nil), baseReport.Rows...)
	modelProjection.contextRows = append([]FileEvidence(nil), baseReport.contextRows...)
	modelScore = 5
	modelProjection.Rows[0].Score = 5
	modelProjection.Rows[0].ScoreProvenance = ScoreProvenance{Deterministic: 2, Model: &modelScore, SelectedBy: "model"}
	modelProjection.Rows[0].Actionability = ActionabilityInspect
	modelProjection.Rows[0].Reasons = append(modelProjection.Rows[0].Reasons, "model:raise priority")
	modelProjection.Rows[0].EvidenceLayers = append(modelProjection.Rows[0].EvidenceLayers, "model")
	modelProjection.contextRows[0] = modelProjection.Rows[0]
	modelPacket, err := BuildContextPacket(context.Background(), modelProjection, ContextPacketRequest{TargetIDs: []string{modelProjection.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if stable.Targets[0].Actionability != ActionabilityVerifyFirst || stable.Targets[0].ProofObligation.ID != modelPacket.Targets[0].ProofObligation.ID || modelPacket.Targets[0].Actionability != stable.Targets[0].Actionability {
		t.Fatalf("model projection packet drift = stable %#v model %#v", stable.Targets[0], modelPacket.Targets[0])
	}
	deterministicChange := modelProjection
	deterministicChange.Rows = append([]FileEvidence(nil), modelProjection.Rows...)
	deterministicChange.Rows[0].ScoreProvenance = ScoreProvenance{Deterministic: 5, Model: &modelScore, SelectedBy: "model"}
	deterministicChange.Rows[0].CochangeRisk = 1
	deterministicChange.Rows[0].Reasons = append(deterministicChange.Rows[0].Reasons, "cochange:partners:1")
	deterministicChange.Rows[0].EvidenceLayers = append(deterministicChange.Rows[0].EvidenceLayers, "cochange")
	deterministicChange.contextRows = append([]FileEvidence(nil), deterministicChange.Rows...)
	changedPacket, err := BuildContextPacket(context.Background(), deterministicChange, ContextPacketRequest{TargetIDs: []string{deterministicChange.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if changedPacket.Targets[0].ProofObligation.ID == stable.Targets[0].ProofObligation.ID || changedPacket.Targets[0].Actionability != ActionabilityInspect {
		t.Fatalf("deterministic classification did not affect packet = %#v", changedPacket.Targets[0])
	}

	zeroDeterministic := baseReport
	zeroDeterministic.Rows = append([]FileEvidence(nil), baseReport.Rows...)
	zeroDeterministic.Rows[0].Score = 0
	zeroDeterministic.Rows[0].ScoreProvenance = ScoreProvenance{Deterministic: 0, SelectedBy: "deterministic"}
	zeroDeterministic.contextRows = append([]FileEvidence(nil), zeroDeterministic.Rows...)
	zeroPacket, err := BuildContextPacket(context.Background(), zeroDeterministic, ContextPacketRequest{TargetIDs: []string{zeroDeterministic.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	zeroModel := zeroDeterministic
	zeroModel.Rows = append([]FileEvidence(nil), zeroDeterministic.Rows...)
	zeroModel.contextRows = append([]FileEvidence(nil), zeroDeterministic.contextRows...)
	zeroModel.Rows[0].Score = 5
	zeroModel.Rows[0].ScoreProvenance = ScoreProvenance{Deterministic: 0, Model: &modelScore, SelectedBy: "model"}
	zeroModel.Rows[0].Actionability = ActionabilityInspect
	zeroModel.Rows[0].Reasons = append(zeroModel.Rows[0].Reasons, "model:raise priority")
	zeroModel.Rows[0].EvidenceLayers = append(zeroModel.Rows[0].EvidenceLayers, "model")
	zeroModel.contextRows[0] = zeroModel.Rows[0]
	zeroModelPacket, err := BuildContextPacket(context.Background(), zeroModel, ContextPacketRequest{TargetIDs: []string{zeroModel.Rows[0].ID}, BudgetBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if zeroPacket.Targets[0].Actionability != zeroModelPacket.Targets[0].Actionability || zeroPacket.Targets[0].ProofObligation.ID != zeroModelPacket.Targets[0].ProofObligation.ID {
		t.Fatalf("zero deterministic score packet drift = zero %#v model %#v", zeroPacket.Targets[0], zeroModelPacket.Targets[0])
	}
}

func assertContextPacketBytes(t *testing.T, packet ContextPacket) {
	t.Helper()
	encoded, err := json.Marshal(packet)
	if err != nil || packet.UsedBytes != len(encoded) || packet.UsedBytes > packet.BudgetBytes {
		t.Fatalf("packet bytes = used %d encoded %d budget %d err=%v", packet.UsedBytes, len(encoded), packet.BudgetBytes, err)
	}
}

func contextTestReport(repo string, paths ...string) Report {
	rows := make([]FileEvidence, len(paths))
	for index, path := range paths {
		rows[index] = FileEvidence{ID: stableFileID(path), EvidenceID: "sha256:" + strings.Repeat(string(rune('a'+index)), 64), Path: path, Actionability: ActionabilityInspect, Caveat: "inspect source"}
	}
	return Report{Repo: repo, ReportID: exampleReportID, Parameters: ReportParameters{MaxBytes: 4096}, Rows: rows, contextRows: append([]FileEvidence(nil), rows...)}
}

func writeContextFile(t *testing.T, repo, rel, text string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contextComponentKinds(components []ContextComponent) []string {
	out := make([]string, len(components))
	for index, component := range components {
		out[index] = component.Kind
	}
	return out
}

func containsContextMarker(components []ContextComponent) bool {
	for _, component := range components {
		if component.Text == "[redacted: secret-risk evidence]" {
			return true
		}
	}
	return false
}

func containsContextOmission(omissions []string, wanted string) bool {
	return sort.SearchStrings(omissions, wanted) < len(omissions) && omissions[sort.SearchStrings(omissions, wanted)] == wanted
}
