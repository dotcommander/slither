package slither

// churnPressureFloor is the post-creation churn level at which a file counts
// as under change pressure for the risk gates and the seed score.
const churnPressureFloor = 120

// Churn pressure semantics.
//
// Raw churn (numstat additions + deletions over the history window) mixes two
// unrelated shapes: files that were born large and barely changed since, and
// files with a problematic spot that keeps changing. Review priority should
// track the second shape, so every pressure gate in the scorer consumes
// post-creation churn (churn excluding the file's creation commit) instead of
// raw churn. The derived churn profile labels the shape for humans and flows
// into row reasons.

const (
	// ChurnProfileStable marks a file touched at most once in the window.
	ChurnProfileStable = "stable"
	// ChurnProfileCreationDominated marks a file whose churn is mostly its
	// creation commit (size, not pressure).
	ChurnProfileCreationDominated = "creation-dominated"
	// ChurnProfileRecurringFixes marks a repeatedly bug-fixed file: the
	// classic problem-spot shape.
	ChurnProfileRecurringFixes = "recurring-fixes"
	// ChurnProfileReworked marks a file rewritten in place after creation by
	// a small number of large visits.
	ChurnProfileReworked = "reworked"
	// ChurnProfileEvolving marks steady post-creation change without the
	// recurring-fix signature.
	ChurnProfileEvolving = "evolving"
)

// classifyChurnProfile labels how a file accumulated its churn. Inputs:
// churn (raw adds+dels in the window), afterCreation (churn excluding the
// creation commit), touches (commits touching the file in the window), and
// fixTouches (commits whose message matched the bug-fix heuristic).
func classifyChurnProfile(churn, afterCreation, touches, fixTouches int) string {
	switch {
	case touches <= 1:
		return ChurnProfileStable
	case churn > 0 && afterCreation*5 <= churn:
		return ChurnProfileCreationDominated
	case fixTouches >= 2 && touches >= 3:
		return ChurnProfileRecurringFixes
	case afterCreation >= churnPressureFloor && touches <= 3:
		return ChurnProfileReworked
	default:
		return ChurnProfileEvolving
	}
}
