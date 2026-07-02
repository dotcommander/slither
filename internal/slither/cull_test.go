package slither

import "testing"

func TestGeneratedPathClassifiesSlitherCullOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{"slither-cull.json", true},
		{"out/slither-cull-report.md", true},
		{"slither-report.md", true},
		{"src/main.go", false},
		{"docs/readme.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			got := isGeneratedOrReportPath(tt.path)
			if got != tt.want {
				t.Errorf("isGeneratedOrReportPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
