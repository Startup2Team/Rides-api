// Package timeutil provides small time helpers shared across packages.
//
// Every "daily" figure and every daily Redis counter MUST go through this
// package. Rwanda is UTC+2 with no DST, so a UTC calendar day rolls over at
// 02:00 local — a driver's counters reset mid-shift and their earnings get
// attributed to the wrong day. There is no such thing as a correct "today"
// that does not name a timezone.
package timeutil

import "time"

// FallbackTimezone is used when no IANA name is configured. Rwanda is UTC+2
// year-round (no DST).
const FallbackTimezone = "Africa/Kigali"

// Location resolves an IANA timezone name, falling back to UTC when the name is
// unknown (e.g. a container image shipped without tzdata). Never returns nil,
// so callers can use the result unconditionally.
func Location(name string) *time.Location {
	if name == "" {
		name = FallbackTimezone
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

// StartOfLocalDay returns midnight at the start of the local calendar day
// containing `at`.
func StartOfLocalDay(at time.Time, loc *time.Location) time.Time {
	local := at.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

// DayWindow returns the half-open range [start, end) covering the local
// calendar day containing `at`. Half-open is deliberate: an inclusive end
// boundary either double-counts or drops rows landing exactly on midnight.
func DayWindow(at time.Time, loc *time.Location) (time.Time, time.Time) {
	start := StartOfLocalDay(at, loc)
	return start, start.AddDate(0, 0, 1)
}

// DaysWindow returns [start, end) covering the `days` local calendar days
// ending with the day containing `at` — so DaysWindow(now, loc, 7) starts at
// midnight six days ago and ends at tomorrow's midnight.
func DaysWindow(at time.Time, loc *time.Location, days int) (time.Time, time.Time) {
	if days < 1 {
		days = 1
	}
	start, end := DayWindow(at, loc)
	return start.AddDate(0, 0, -(days - 1)), end
}

// EndOfLocalDay returns the instant the local day containing `at` ends — i.e.
// midnight starting the NEXT local day. Used for Redis TTLs on daily counters.
//
// It returns the start of the next day rather than 23:59:59 of this one for two
// reasons: the boundary lines up exactly with DayWindow's half-open end, and the
// result is always strictly in the future. The old 23:59:59 form meant any
// increment landing in that final second produced an ExpireAt already in the
// past, which Redis honours by deleting the key immediately — silently wiping
// the whole day's count.
func EndOfLocalDay(at time.Time, loc *time.Location) time.Time {
	_, end := DayWindow(at, loc)
	return end
}
