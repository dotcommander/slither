package slither

type topKScoreHealth struct {
	Slots          int
	DistinctScores int
	Saturation     float64
}

// scoreHealthForTopK is the single saturation definition used by report
// summaries and eval. Zero slots intentionally have saturation 1: no ranking
// diversity was observed, matching the existing eval/v1 contract.
func scoreHealthForTopK(scores []int, topK int) topKScoreHealth {
	health := topKScoreHealth{}
	if topK > len(scores) {
		topK = len(scores)
	}
	seen := make(map[int]struct{}, topK)
	for _, score := range scores[:topK] {
		health.Slots++
		seen[score] = struct{}{}
	}
	health.DistinctScores = len(seen)
	health.Saturation = scoreSaturation(health.DistinctScores, health.Slots)
	return health
}

func scoreSaturation(distinctScores, slots int) float64 {
	if slots == 0 {
		return 1
	}
	return 1 - float64(distinctScores)/float64(slots)
}
