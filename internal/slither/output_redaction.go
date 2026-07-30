package slither

import "regexp"

var outputSecretPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		pattern:     regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/?#@\s]+@`),
		replacement: `${1}`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\bBearer[ \t]+)[A-Za-z0-9._~+/=-]{12,}(["',;)\]}]|$)`),
		replacement: `${1}[redacted]${2}`,
	},
	{
		pattern:     regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{4,}\b`),
		replacement: `[redacted]`,
	},
	{
		pattern:     regexp.MustCompile(`\bwhsig_[A-Za-z0-9._~+/=-]{12,}`),
		replacement: `[redacted]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization)\b["']?[ \t]*(?::|=)[ \t]*["']?(?:[A-Za-z][A-Za-z0-9_-]*[ \t]+)?)([A-Za-z0-9][A-Za-z0-9._~+/=-]{7,})`),
		replacement: `${1}[redacted]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|password|passwd|credential)\b["']?[ \t]*(?::|=)[ \t]*["']?)([A-Za-z0-9][A-Za-z0-9._~+/=-]{7,})`),
		replacement: `${1}[redacted]`,
	},
}

func scrubJSONRows(rows []FileEvidence) []FileEvidence {
	out := make([]FileEvidence, len(rows))
	for i, row := range rows {
		out[i] = row
		out[i].Summary = scrubOutputSecrets(row.Summary)
		out[i].Excerpt = scrubOutputSecrets(row.Excerpt)
		if len(row.EvidenceLocations) == 0 {
			continue
		}
		out[i].EvidenceLocations = append([]EvidenceLocation(nil), row.EvidenceLocations...)
		for j := range out[i].EvidenceLocations {
			out[i].EvidenceLocations[j].Snippet = scrubOutputSecrets(out[i].EvidenceLocations[j].Snippet)
		}
	}
	return out
}

func scrubOutputSecrets(text string) string {
	for _, secretPattern := range outputSecretPatterns {
		text = secretPattern.pattern.ReplaceAllString(text, secretPattern.replacement)
	}
	return text
}
