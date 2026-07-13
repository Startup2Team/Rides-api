package customer

// Customer loyalty (gamification) levels, derived from lifetime COMPLETED rides.
// Fares are paid off-app, so completed-ride count is the reliable on-platform
// signal for progression; total spend (sum of agreed fares) is surfaced for
// display only. Perks are descriptive for the mobile UI — engine-level
// behaviour (e.g. priority matching) is not wired to these yet.

type levelTier struct {
	name     string
	index    int
	minRides int
	perks    []string
}

// levelTiers is ordered lowest → highest. A customer sits in the highest tier
// whose minRides they meet or exceed.
var levelTiers = []levelTier{
	{name: "BRONZE", index: 0, minRides: 0, perks: []string{"Standard booking", "In-app support"}},
	{name: "SILVER", index: 1, minRides: 10, perks: []string{"Faster support responses"}},
	{name: "GOLD", index: 2, minRides: 50, perks: []string{"Faster support responses", "Early access to new features"}},
	{name: "PREMIUM", index: 3, minRides: 150, perks: []string{"Dedicated support", "Early access to new features"}},
}

// CustomerLevel is the gamification snapshot returned to the mobile app.
type CustomerLevel struct {
	Level            string   `json:"level"`
	LevelIndex       int      `json:"level_index"`
	CompletedRides   int      `json:"completed_rides"`
	TotalSpend       float64  `json:"total_spend"`
	CurrentThreshold int      `json:"current_threshold"`
	NextLevel        *string  `json:"next_level"`     // null at the top tier
	NextThreshold    *int     `json:"next_threshold"` // null at the top tier
	RidesToNextLevel int      `json:"rides_to_next_level"`
	ProgressToNext   float64  `json:"progress_to_next"` // 0..1 toward the next tier (1.0 at the top)
	Perks            []string `json:"perks"`
}

// computeLevel derives the loyalty snapshot from lifetime completed rides.
// Pure + deterministic so the thresholds can be unit-tested at their edges.
func computeLevel(completedRides int, totalSpend float64) CustomerLevel {
	if completedRides < 0 {
		completedRides = 0
	}
	if totalSpend < 0 {
		totalSpend = 0
	}

	cur := levelTiers[0]
	for _, t := range levelTiers {
		if completedRides >= t.minRides {
			cur = t
		}
	}

	lvl := CustomerLevel{
		Level:            cur.name,
		LevelIndex:       cur.index,
		CompletedRides:   completedRides,
		TotalSpend:       totalSpend,
		CurrentThreshold: cur.minRides,
		Perks:            cur.perks,
	}

	if cur.index+1 < len(levelTiers) {
		next := levelTiers[cur.index+1]
		name, thresh := next.name, next.minRides
		lvl.NextLevel = &name
		lvl.NextThreshold = &thresh

		if remaining := next.minRides - completedRides; remaining > 0 {
			lvl.RidesToNextLevel = remaining
		}
		if span := next.minRides - cur.minRides; span > 0 {
			p := float64(completedRides-cur.minRides) / float64(span)
			if p < 0 {
				p = 0
			} else if p > 1 {
				p = 1
			}
			lvl.ProgressToNext = p
		}
	} else {
		// Top tier — no next level to progress toward.
		lvl.ProgressToNext = 1
	}

	return lvl
}
