package slither

import (
	"context"
	"strings"
	"testing"
)

func TestSecretEvidenceRedactedInReport(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	fixture := `package main

const token = "YOUR_API_KEY"

func config() {
	password := "mySuperSecret123!"
	_ = password
}
`
	writeFile(t, tmp, "secrets.go", fixture)

	report, err := BuildReport(context.Background(), Options{Repo: tmp, Top: 10, MaxBytes: 500_000, Days: 90})
	if err != nil {
		t.Fatal(err)
	}

	row := findRow(report, "secrets.go")
	if row == nil {
		t.Fatal("missing secrets.go row")
	}

	for _, loc := range row.EvidenceLocations {
		if strings.HasPrefix(loc.Reason, "content:provider_token_literal") || strings.HasPrefix(loc.Reason, "content:credential_assignment_literal") {
			if loc.Snippet != "[redacted]" {
				t.Fatalf("secret snippet not redacted: reason=%s snippet=%q", loc.Reason, loc.Snippet)
			}
			if loc.Line <= 0 {
				t.Fatalf("secret line should be > 0: reason=%s line=%d", loc.Reason, loc.Line)
			}
		}
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
	if strings.Contains(raw, "YOUR_API_KEY") {
		t.Fatal("provider token literal leaked into rendered JSON")
	}
	if strings.Contains(raw, "mySuperSecret123!") {
		t.Fatal("credential literal leaked into rendered JSON")
	}
}

func TestConfigFormatSecretsAreDetectedAndRedacted(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	writeFile(t, tmp, "settings.yaml", "api_key: YOUR_API_KEY"sk-aaaaaaaaaaaaaaaaaaaaaaaa\"\n")

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
