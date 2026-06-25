package slither

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type modelScore struct {
	Score   int      `json:"score"`
	Summary string   `json:"summary"`
	Reasons []string `json:"reasons"`
}

func scoringPrompt(evidence FileEvidence) string {
	payload, _ := json.MarshalIndent(evidence, "", "  ")
	return fmt.Sprintf(`You are Slither, a cheap-model scout. Score this file as a premium-model target.

Return only compact JSON with this shape:
{"score":1-5,"summary":"one sentence","reasons":["short evidence reason"]}

Scoring:
1 low signal; 2 minor; 3 plausible; 4 strong; 5 urgent/high leverage.
Prefer files with impact times opportunity: central code, security/config/persistence boundaries, churn-like clues, complexity, TODO/FIXME, risky APIs, weak tests, or architectural leverage.
Do not invent evidence outside this payload.

File evidence:
%s`, string(payload))
}

func parseModelScore(raw string) (modelScore, error) {
	trimmed := strings.TrimSpace(raw)
	var score modelScore
	if err := json.Unmarshal([]byte(trimmed), &score); err == nil {
		return score, nil
	}
	match := regexp.MustCompile(`(?s)\{.*\}`).FindString(trimmed)
	if match == "" {
		return modelScore{}, fmt.Errorf("no JSON object in response")
	}
	if err := json.Unmarshal([]byte(match), &score); err != nil {
		return modelScore{}, err
	}
	return score, nil
}
