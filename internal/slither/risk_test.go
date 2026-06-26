package slither

import "testing"

func TestHotspotRiskStaticHighComplexity(t *testing.T) {
	t.Parallel()

	score, _ := hotspotRisk(60, 0, 0, 0)
	if score <= 0 {
		t.Fatalf("expected complexity-only hotspot signal, got score %d", score)
	}
}
