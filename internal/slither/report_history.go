package slither

import (
	"context"
	"strings"
)

type reportHistory struct {
	fixTouches    map[string]int
	cochange      map[string]cochangeInfo
	ownership     map[string]ownershipInfo
	fixSkip       string
	cochangeSkip  string
	ownershipSkip string
}

// combinedHistory replaces three overlapping name-only history traversals.
// If the combined stream exceeds the existing output limit, retain each
// signal's original independent limit and skip/failure behavior.
func combinedHistory(ctx context.Context, repo string, bounds gitHistoryBounds) reportHistory {
	args := append(bounds.args(), "--format=format:__SLITHER_META__%ae%x00%s", "--name-only")
	out, err := runGitOutput(ctx, repo, append([]string{"log"}, args...)...)
	if err != nil {
		fix, fixSkip := bugfixTouchesByFile(ctx, repo, bounds)
		co, coSkip := cochangeByFile(ctx, repo, bounds)
		owners, ownerSkip := ownershipByFile(ctx, repo, bounds)
		return reportHistory{fixTouches: fix, cochange: co, ownership: owners, fixSkip: fixSkip, cochangeSkip: coSkip, ownershipSkip: ownerSkip}
	}
	if out == "" {
		return reportHistory{fixTouches: map[string]int{}, cochange: map[string]cochangeInfo{}, ownership: map[string]ownershipInfo{}, fixSkip: "no recent git history", cochangeSkip: "no recent git history", ownershipSkip: "no recent git history"}
	}
	result := reportHistory{fixTouches: map[string]int{}, cochange: map[string]cochangeInfo{}, ownership: map[string]ownershipInfo{}}
	authorsByFile := map[string]map[string]int{}
	fileCommits := map[string]int{}
	pairCounts := map[[2]string]int{}
	var currentFiles []string
	currentAuthor := ""
	isFix := false
	commits := 0
	commitsUsed := 0
	flush := func() {
		unique := uniqueIncludedFiles(currentFiles)
		if len(unique) < 2 || len(unique) > 15 {
			return
		}
		commitsUsed++
		for _, rel := range unique {
			fileCommits[rel]++
		}
		for i, rel := range unique {
			for _, other := range unique[i+1:] {
				pairCounts[[2]string{rel, other}]++
			}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if meta, found := strings.CutPrefix(line, "__SLITHER_META__"); found {
			flush()
			currentFiles = nil
			commits++
			currentAuthor, meta, _ = strings.Cut(meta, "\x00")
			currentAuthor = strings.TrimSpace(currentAuthor)
			if currentAuthor == "" {
				currentAuthor = "unknown"
			}
			isFix = bugfixSubjectPattern.MatchString(meta)
			continue
		}
		rel := strings.TrimSpace(line)
		if rel == "" {
			continue
		}
		currentFiles = append(currentFiles, rel)
		if isFix {
			result.fixTouches[rel]++
		}
		if currentAuthor != "" {
			if authorsByFile[rel] == nil {
				authorsByFile[rel] = map[string]int{}
			}
			authorsByFile[rel][currentAuthor]++
		}
	}
	flush()
	if commits < 30 {
		result.fixTouches = map[string]int{}
		result.fixSkip = "insufficient commit count:" + itoa(commits)
	}
	if len(authorsByFile) == 0 {
		result.ownershipSkip = "no file author history"
	}
	for rel, authors := range authorsByFile {
		touches, top := 0, 0
		for _, count := range authors {
			touches += count
			if count > top {
				top = count
			}
		}
		if touches > 0 {
			result.ownership[rel] = ownershipInfo{AuthorCount: len(authors), Touches: touches, TopShare: float64(top) / float64(touches)}
		}
	}
	if commitsUsed == 0 {
		result.cochangeSkip = "no bounded multi-file commits"
		return result
	}
	edges := map[string][]float64{}
	for pair, count := range pairCounts {
		if count < 2 {
			continue
		}
		a, b := pair[0], pair[1]
		denominator := fileCommits[a] + fileCommits[b] - count
		if denominator <= 0 {
			continue
		}
		weight := float64(count) / float64(denominator)
		if weight < 0.2 {
			continue
		}
		edges[a] = append(edges[a], weight)
		edges[b] = append(edges[b], weight)
	}
	for rel, weights := range edges {
		maximum := 0.0
		for _, weight := range weights {
			maximum = max(maximum, weight)
		}
		result.cochange[rel] = cochangeInfo{PartnerCount: len(weights), MaxJaccard: maximum}
	}
	return result
}
