package slither

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRepoManifestAvailability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		write   bool
		wantOK  bool
	}{
		{name: "valid", content: `{"scripts":{"test":"go test ./..."}}`, write: true, wantOK: true},
		{name: "missing", wantOK: false},
		{name: "oversize", content: strings.Repeat("x", int(maxRepoManifestBytes)+1), write: true, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			if tt.write {
				if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			data, ok := readRepoManifest(repo, "package.json")
			if ok != tt.wantOK {
				t.Fatalf("available = %t, want %t (bytes returned %d)", ok, tt.wantOK, len(data))
			}
			if !ok && data != nil {
				t.Fatalf("unavailable manifest returned %d partial bytes", len(data))
			}
		})
	}
}

func TestManifestConsumersRejectMalformedAndOversizedFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		file    string
		content string
		check   func(string) bool
	}{
		{name: "malformed package", file: "package.json", content: `{`, check: func(repo string) bool { return packageHasScript(repo, "test") }},
		{name: "malformed composer", file: "composer.json", content: `{`, check: func(repo string) bool { return composerHasScript(repo, "test") }},
		{name: "malformed go module", file: "go.mod", content: "not a module declaration\n", check: func(repo string) bool { return goModulePath(repo) != "" }},
		{name: "oversized package", file: "package.json", content: `{"scripts":{"test":"go test"}}` + strings.Repeat(" ", int(maxRepoManifestBytes)), check: func(repo string) bool { return packageHasScript(repo, "test") }},
		{name: "oversized composer", file: "composer.json", content: `{"scripts":{"test":"phpunit"}}` + strings.Repeat(" ", int(maxRepoManifestBytes)), check: func(repo string) bool { return composerHasScript(repo, "test") }},
		{name: "oversized go module", file: "go.mod", content: "module example.test/partial\n" + strings.Repeat(" ", int(maxRepoManifestBytes)), check: func(repo string) bool { return goModulePath(repo) != "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, tt.file), []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.check(repo) {
				t.Fatalf("%s was parsed as available", tt.file)
			}
		})
	}
}

func TestManifestConsumersReadValidFiles(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	files := map[string]string{
		"go.mod":        "module example.test/app\n",
		"package.json":  `{"scripts":{"test":"go test ./..."}}`,
		"composer.json": `{"scripts":{"test":"phpunit"}}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := goModulePath(repo); got != "example.test/app" {
		t.Fatalf("module path = %q", got)
	}
	if !packageHasScript(repo, "test") {
		t.Fatal("valid package script unavailable")
	}
	if !composerHasScript(repo, "test") {
		t.Fatal("valid composer script unavailable")
	}
}
