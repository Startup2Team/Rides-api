// Package timeutil provides small time helpers shared across packages.
package timeutil

import "time"

// EndOfDay returns the last second of the current UTC day (23:59:59).
// Used to set Redis key TTLs that should expire at midnight.
func EndOfDay() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
}
