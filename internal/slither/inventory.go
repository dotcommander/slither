package slither

// This file owns the CLI-facing inventory routing. It deliberately delegates
// all classification and ordering to reviewLaneRules/BuildReviewPlanForRepo so
// --inventory cannot drift from the canonical review-plan policy.

func validReviewLane(lane string) bool {
	for _, rule := range reviewLaneRules {
		if rule.lane == lane {
			return true
		}
	}
	return false
}

func rowMatchesInventory(row FileEvidence, lane string) bool {
	if lane == "" {
		return true
	}
	for _, rule := range reviewLaneRules {
		if rule.lane == lane {
			return rule.match(row)
		}
	}
	return false
}

// BuildInventoryForRepo selects one canonical review lane. data-integrity
// keeps its prior specialized queue builder so its existing result remains
// compatible; every other lane is filtered from the canonical full plan.
func BuildInventoryForRepo(repo string, rows []FileEvidence, lane string) ([]ReviewQueue, []ReviewLane) {
	if lane == "data-integrity" {
		return BuildDataIntegrityInventoryForRepo(repo, rows)
	}
	queues, plan := BuildReviewPlanForRepo(repo, rows)
	return filterReviewQueuesByLane(queues, lane), filterReviewLanesByLane(plan, lane)
}

func filterReviewQueuesByLane(in []ReviewQueue, lane string) []ReviewQueue {
	out := make([]ReviewQueue, 0, len(in))
	for _, item := range in {
		if item.Lane == lane {
			out = append(out, item)
		}
	}
	return out
}

func filterReviewLanesByLane(in []ReviewLane, lane string) []ReviewLane {
	out := make([]ReviewLane, 0, len(in))
	for _, item := range in {
		if item.Lane == lane {
			out = append(out, item)
		}
	}
	return out
}
