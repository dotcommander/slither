package slither

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStableFileIDPreservesDistinctPathIdentity(t *testing.T) {
	t.Parallel()
	if stableFileID("a-b.go") == stableFileID("a_b.go") {
		t.Fatal("distinct paths produced the same evidence ID")
	}
	if got, want := stableFileID("internal/a.go"), stableFileID("internal/a.go"); got != want {
		t.Fatalf("stableFileID is not deterministic: %q != %q", got, want)
	}
}

func TestVerifyCommandsQuoteRepositoryControlledPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"php", verifyCmdForPath("check;echo INJECTED.php"), "php -l 'check;echo INJECTED.php'"},
		{"shell", verifyCmdForPath("scripts/check;echo INJECTED.sh"), "bash -n 'scripts/check;echo INJECTED.sh'"},
		{"sql", verifyCmdForPath("db/a file.sql"), "psql \"$TEST_DATABASE_URL\" -v ON_ERROR_STOP=1 -f 'db/a file.sql'"},
		{"js", verifyCmdForPath("-danger.js"), "bun build --no-bundle --outfile /tmp/slither-bun-check.js ./-danger.js"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Fatalf("verify command = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestResolveReportOptionsPreservesEndOfOptions(t *testing.T) {
	t.Parallel()
	opts, err := resolveReportOptions(defaultConfig(), []string{"--", "--repo-starts-with-dash"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Repo != "--repo-starts-with-dash" {
		t.Fatalf("repo = %q, want dash-prefixed positional", opts.Repo)
	}
}

func TestRenderMarkdownEscapesPathAndCommandTableSyntax(t *testing.T) {
	t.Parallel()
	row := FileEvidence{
		Path:           "bad|`name.go",
		Score:          4,
		SeedScore:      4,
		ContentRisk:    4,
		EvidenceLayers: []string{"content-risk", "cochange"},
		Reasons:        []string{"content:stateful_store:1"},
		VerifyCmd:      "go test './bad|name/...'",
	}
	md := RenderMarkdown(Report{Rows: []FileEvidence{row}})
	if strings.Contains(md, "`bad|`name.go`") {
		t.Fatalf("raw path broke Markdown table:\n%s", md)
	}
	if !strings.Contains(md, "<code>bad&#124;`name.go</code>") {
		t.Fatalf("escaped path missing from Markdown:\n%s", md)
	}
	if strings.Contains(md, "go test './bad|name/...' |") {
		t.Fatalf("raw command pipe broke Markdown table:\n%s", md)
	}
}

func TestRenderMarkdownEscapesHeaderCodeValues(t *testing.T) {
	t.Parallel()
	report := Report{
		Repo:           "repo`name\nnext",
		PatternsSource: "patterns`custom\r.json",
	}
	md := RenderMarkdown(report)

	wantRepo := "> Slither creeps like a snake through " + markdownCodeCell(report.Repo) + ","
	if !strings.Contains(md, wantRepo) {
		t.Fatalf("escaped repository header missing: want %q in:\n%s", wantRepo, md)
	}
	wantPatterns := "- Patterns source: " + markdownCodeCell(report.PatternsSource) + "\n"
	if !strings.Contains(md, wantPatterns) {
		t.Fatalf("escaped patterns header missing: want %q in:\n%s", wantPatterns, md)
	}
	if strings.Contains(md, "`"+markdownCodeCell(report.Repo)+"`") || strings.Contains(md, "`"+markdownCodeCell(report.PatternsSource)+"`") {
		t.Fatalf("header code values received extra backticks:\n%s", md)
	}
}

func TestRenderJSONScrubsSecretsWithoutMutatingReport(t *testing.T) {
	t.Parallel()
	report := Report{
		Repo:       "/repo",
		CullLedger: &CullLedger{},
		Rows: []FileEvidence{{
			Path:    "config.go",
			Summary: "Authorization: Custom opaqueTokenValue123 while prose remains",
			Excerpt: `Bearer bearerTokenValue123" and Bearer abcdefghijklmnop" and eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.signaturePart`,
			EvidenceLocations: []EvidenceLocation{
				{Reason: "content:config", Line: 1, Snippet: `api_key="customCredential987654" remains structured`},
				{Reason: "content:webhook", Line: 2, Snippet: "signature whsig_privateValue123"},
				{Reason: "content:authorization", Line: 3, Snippet: "Authorization: Basic dXNlcjpwYXNz"},
			},
		}},
	}

	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload reportEnvelope
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(payload.Rows))
	}
	got := payload.Rows[0]
	for _, secret := range []string{"opaqueTokenValue123", "bearerTokenValue123", "abcdefghijklmnop", "eyJhbGciOiJIUzI1NiJ9", "customCredential987654", "whsig_privateValue123", "dXNlcjpwYXNz"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("secret %q survived JSON scrubbing: %s", secret, data)
		}
	}
	if !strings.Contains(got.Summary, "while prose remains") || !strings.Contains(got.EvidenceLocations[0].Snippet, "remains structured") {
		t.Fatalf("non-secret prose was not preserved: %#v", got)
	}
	if got.CullDecision == "" {
		t.Fatalf("cull disposition missing after redaction: %#v", got)
	}
	if report.Rows[0].Summary != "Authorization: Custom opaqueTokenValue123 while prose remains" ||
		report.Rows[0].Excerpt != `Bearer bearerTokenValue123" and Bearer abcdefghijklmnop" and eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.signaturePart` ||
		report.Rows[0].EvidenceLocations[0].Snippet != `api_key="customCredential987654" remains structured` ||
		report.Rows[0].CullDecision != "" {
		t.Fatalf("RenderJSON mutated its input report: %#v", report.Rows[0])
	}
}

func TestScrubOutputSecretsPreservesNonSecretProse(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"Bearer authentication uses a token supplied by the caller.",
		"Load example.test.localhost before authentication.",
		"The whsig_header field names the webhook signature header.",
	} {
		if got := scrubOutputSecrets(want); got != want {
			t.Fatalf("non-secret prose = %q, want %q", got, want)
		}
	}
}

func TestRenderMarkdownRemovesCommandLineBreaks(t *testing.T) {
	t.Parallel()
	row := FileEvidence{
		Path:      "bad\rname.go",
		Score:     4,
		SeedScore: 4,
		Reasons:   []string{"content:stateful_store:1"},
		VerifyCmd: "go test './bad\rname/...'",
	}
	md := RenderMarkdown(Report{Rows: []FileEvidence{row}})
	if strings.ContainsAny(md, "\r") {
		t.Fatalf("carriage return survived Markdown rendering: %q", md)
	}
	if !strings.Contains(md, `bad\rname.go`) {
		t.Fatalf("control characters were not rendered unambiguously: %q", md)
	}
	if got := escapeCell(row.VerifyCmd); got != `go test './bad\rname/...'` {
		t.Fatalf("command control character rendering = %q", got)
	}
}

func TestShellQuoteRoundTripsMetacharacters(t *testing.T) {
	t.Parallel()
	values := []string{
		"a b.go",
		"a'b.go",
		"a$b.go",
		"a;b.go",
	}
	for _, value := range values {
		value := value
		t.Run(strings.ReplaceAll(value, "\n", `\n`), func(t *testing.T) {
			t.Parallel()
			quoted := shellQuote(value)
			if strings.ContainsAny(quoted, "\r\n") {
				t.Fatalf("quoted argument contains a literal line break: %q", quoted)
			}
			out, err := exec.Command("sh", "-c", "printf '%s' "+quoted).Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := string(out); got != value {
				t.Fatalf("round trip = %q, want %q (quoted %q)", got, value, quoted)
			}
		})
	}
	if got := shellPathArg("-danger.js"); got != "./-danger.js" {
		t.Fatalf("leading-dash path = %q", got)
	}
}

func TestVerifyCommandOmittedForControlBearingPath(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"line\nbreak.php", "carriage\rreturn.sh", "trailing.sql\n"} {
		if got := verifyCmdForPath(path); got != "" {
			t.Fatalf("verify command for %q = %q, want omitted", path, got)
		}
	}
}

func TestMarkdownCodeCellDistinguishesControlFromLiteralEscape(t *testing.T) {
	t.Parallel()
	control := markdownCodeCell("line\nbreak.go")
	literal := markdownCodeCell(`line\nbreak.go`)
	if control == literal {
		t.Fatalf("control and literal escape rendered identically: %q", control)
	}
	if !strings.Contains(control, `line\nbreak.go`) || !strings.Contains(literal, `line\\nbreak.go`) {
		t.Fatalf("unexpected control rendering: control=%q literal=%q", control, literal)
	}
}

func TestAtomicWriteFileDoesNotBroadenPermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := atomicWriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertMode := func(want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode = %04o, want %04o", got, want)
		}
	}
	assertMode(0o600)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertMode(0o400)
}
