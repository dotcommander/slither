package slither

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
)

func TestSecretEvidenceRedactedInReport(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	providerToken := "sk-" + strings.Repeat("a", 24)
	password := "prod-" + strings.Repeat("xY7", 6)
	fixture := "package main\n\nconst token = \"" + providerToken + "\"\n\n" +
		"func config() {\n\tpassword := \"" + password + "\"\n" +
		"\texec.Command(\"sh\", \"-c\", password)\n\t_ = password\n}\n"
	writeFile(t, tmp, "secrets.go", fixture)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 500_000, Days: 90})
	if err != nil {
		t.Fatal(err)
	}

	row := findRow(report, "secrets.go")
	if row == nil {
		t.Fatal("missing secrets.go row")
	}

	foundSecondaryEvidence := false
	for _, loc := range row.EvidenceLocations {
		if loc.Snippet != "[redacted]" {
			t.Fatalf("snippet on secret-risk row not redacted: reason=%s snippet=%q", loc.Reason, loc.Snippet)
		}
		if strings.HasPrefix(loc.Reason, "content:shell_boundary:") {
			foundSecondaryEvidence = true
		}
		if (strings.HasPrefix(loc.Reason, "content:provider_token_literal") || strings.HasPrefix(loc.Reason, "content:credential_assignment_literal")) && loc.Line <= 0 {
			t.Fatalf("secret line should be > 0: reason=%s line=%d", loc.Reason, loc.Line)
		}
	}
	if !foundSecondaryEvidence {
		t.Fatalf("missing secondary shell evidence: locations=%#v", row.EvidenceLocations)
	}

	if row.Excerpt != "[redacted: secret-risk evidence]" {
		t.Fatalf("Excerpt = %q, want '[redacted: secret-risk evidence]'", row.Excerpt)
	}
	if row.Summary != "[redacted: secret-risk evidence]" {
		t.Fatalf("Summary = %q, want '[redacted: secret-risk evidence]'", row.Summary)
	}

	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	if strings.Contains(raw, providerToken) {
		t.Fatal("provider token literal leaked into rendered JSON")
	}
	if strings.Contains(raw, password) {
		t.Fatal("credential literal leaked into rendered JSON")
	}
}

func TestContentRiskLocationsConvertRegexp2RuneOffsets(t *testing.T) {
	t.Parallel()

	pattern := regexp2.MustCompile(`yaml\.load`, regexp2.None)
	patterns := scoringPatterns{ContentPatterns: []contentPattern{{
		ID:         "unsafe_yaml_load",
		Pattern:    pattern,
		Weight:     1,
		MaxMatches: 1,
	}}}
	text := "// café 🐍\n" +
		"yaml.load(stream)\n"

	_, _, locations := contentRiskWithLocations(patterns, "loader.go", text)
	if len(locations) != 1 {
		t.Fatalf("locations = %#v, want one", locations)
	}
	if locations[0].Line != 2 || locations[0].Snippet != "yaml.load(stream)" {
		t.Fatalf("location = %#v, want line 2 yaml.load snippet", locations[0])
	}
}

func TestRedactSecretRiskEvidenceLocationsCoversEveryEvidenceSource(t *testing.T) {
	t.Parallel()

	evidence := FileEvidence{
		Reasons: []string{"content:provider_token_literal:1", "unknowns:env_assumptions:1"},
		EvidenceLocations: []EvidenceLocation{
			{Reason: "content:provider_token_literal:1", Line: 2, Snippet: `token = "sk-secret"`},
			{Reason: "unknowns:env_assumptions:1", Line: 4, Snippet: `os.Getenv("TOKEN")`},
		},
	}

	redactSecretRiskEvidenceLocations(&evidence)
	for _, location := range evidence.EvidenceLocations {
		if location.Snippet != "[redacted]" {
			t.Fatalf("location not redacted: %#v", location)
		}
	}
}

func TestContentRiskSurfacesRegexp2Timeout(t *testing.T) {
	t.Parallel()

	pattern := regexp2.MustCompile(`^(a+)+$`, regexp2.None)
	pattern.MatchTimeout = time.Millisecond
	patterns := scoringPatterns{ContentPatterns: []contentPattern{{
		ID:         "pathological",
		Pattern:    pattern,
		Weight:     1,
		MaxMatches: 1,
	}}}

	started := time.Now()
	score, reasons, locations := contentRiskWithLocations(patterns, "fixture.txt", strings.Repeat("a", 20_000)+"!")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("detector timeout was not bounded: %s", elapsed)
	}
	if score != 0 {
		t.Fatalf("score = %d, want detector errors not to inflate risk", score)
	}
	if len(reasons) != 1 || reasons[0] != "detector_error:pathological" {
		t.Fatalf("reasons = %#v, want explicit detector error", reasons)
	}
	if len(locations) != 1 || locations[0].Reason != reasons[0] || locations[0].Snippet != "[detector error]" {
		t.Fatalf("locations = %#v, want explicit detector error evidence", locations)
	}
}

func TestConfigFormatSecretsAreDetectedAndRedacted(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	secret := "sk-" + strings.Repeat("a", 24)
	writeFile(t, tmp, "settings.yaml", "api_key: \""+secret+"\"\n")

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 500_000, Days: 90})
	if err != nil {
		t.Fatal(err)
	}

	row := findRow(report, "settings.yaml")
	if row == nil {
		t.Fatal("missing settings.yaml row")
	}
	if !contains(row.EvidenceLayers, "secret-risk") || !containsReasonPrefix(row.Reasons, "content:provider_token_literal:") {
		t.Fatalf("config secret not detected: layers=%#v reasons=%#v", row.EvidenceLayers, row.Reasons)
	}
	if row.Summary != "[redacted: secret-risk evidence]" || row.Excerpt != "[redacted: secret-risk evidence]" {
		t.Fatalf("secret config summary/excerpt not redacted: summary=%q excerpt=%q", row.Summary, row.Excerpt)
	}

	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "YOUR_API_KEY") {
		t.Fatal("config secret literal leaked into rendered JSON")
	}
}
