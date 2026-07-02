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

const token = "sk-aaaaaaaaaaaaaaaaaaaaaaaa"

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
	if strings.Contains(raw, "sk-aaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatal("provider token literal leaked into rendered JSON")
	}
	if strings.Contains(raw, "mySuperSecret123!") {
		t.Fatal("credential literal leaked into rendered JSON")
	}
}
