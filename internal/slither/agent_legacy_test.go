package slither

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentDoesNotChangeLegacyVersionOrReportPaths(t *testing.T) {
	var version strings.Builder
	if err := RunWithIO(context.Background(), []string{"version"}, strings.NewReader(""), &version, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(version.String(), "slither ") {
		t.Fatalf("version output = %q", version.String())
	}
	repo := agentTestRepo(t)
	config := filepath.Join(t.TempDir(), "config")
	previous := userConfigDir
	userConfigDir = func() (string, error) { return config, nil }
	t.Cleanup(func() { userConfigDir = previous })
	var report strings.Builder
	if err := RunWithIO(context.Background(), []string{"report", repo, "--out", "-"}, strings.NewReader(""), &report, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(report.String(), "# Slither Report\n") {
		t.Fatalf("legacy report output = %q", report.String())
	}
	if _, err := os.Stat(filepath.Join(config, "slither", "config.json")); err != nil {
		t.Fatalf("legacy report config behavior changed: %v", err)
	}
}
