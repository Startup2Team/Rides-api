package timeutil

import (
	"testing"
	"time"
)

func kigali(t *testing.T) *time.Location {
	t.Helper()
	loc := Location(FallbackTimezone)
	if loc == time.UTC {
		t.Skip("tzdata unavailable in this environment")
	}
	return loc
}

func TestLocationFallsBackToUTCForUnknownZone(t *testing.T) {
	if got := Location("Not/AZone"); got != time.UTC {
		t.Fatalf("unknown zone: want UTC, got %v", got)
	}
}

func TestLocationEmptyNameUsesPlatformDefault(t *testing.T) {
	if got := Location(""); got.String() != FallbackTimezone && got != time.UTC {
		t.Fatalf("empty name: want %s (or UTC without tzdata), got %v", FallbackTimezone, got)
	}
}

// The bug this package exists to prevent: a driver working at 00:30 Kigali time
// is on a NEW local day, while UTC still calls it yesterday. Their earnings must
// land in the new day and their penalty counters must already have reset.
func TestDayWindowUsesLocalMidnightNotUTC(t *testing.T) {
	loc := kigali(t)
	// 00:30 on 9 Aug Kigali == 22:30 on 8 Aug UTC.
	at := time.Date(2026, 8, 9, 0, 30, 0, 0, loc)

	start, end := DayWindow(at, loc)

	if got := start.In(loc).Format("2006-01-02 15:04"); got != "2026-08-09 00:00" {
		t.Errorf("start: want 2026-08-09 00:00 local, got %s", got)
	}
	if got := end.In(loc).Format("2006-01-02 15:04"); got != "2026-08-10 00:00" {
		t.Errorf("end: want 2026-08-10 00:00 local, got %s", got)
	}
	if !at.After(start) || !at.Before(end) {
		t.Errorf("%v should fall inside [%v, %v)", at, start, end)
	}
	// Under the old UTC-day logic this instant belonged to 8 August.
	if utcDay := at.UTC().Format("2006-01-02"); utcDay != "2026-08-08" {
		t.Fatalf("test premise broken: expected the UTC day to still be 2026-08-08, got %s", utcDay)
	}
}

// A ride completed at 23:59 local belongs to today; one at 00:00 belongs to
// tomorrow. Half-open bounds are what make that exact, with no ride counted
// twice and none dropped.
func TestDayWindowBoundariesAreHalfOpen(t *testing.T) {
	loc := kigali(t)
	start, end := DayWindow(time.Date(2026, 8, 9, 12, 0, 0, 0, loc), loc)

	lastMoment := time.Date(2026, 8, 9, 23, 59, 59, 0, loc)
	if !lastMoment.Before(end) {
		t.Error("23:59:59 local should fall inside today's window")
	}
	if !end.Equal(time.Date(2026, 8, 10, 0, 0, 0, 0, loc)) {
		t.Error("end should be exactly the next local midnight")
	}
	// Tomorrow's window must begin exactly where today's ends — no gap, no overlap.
	nextStart, _ := DayWindow(end, loc)
	if !nextStart.Equal(end) {
		t.Errorf("adjacent windows must abut: today ends %v, tomorrow starts %v", end, nextStart)
	}
	if !start.Before(end) {
		t.Error("start must precede end")
	}
}

func TestDaysWindowSpansWholeLocalCalendarDays(t *testing.T) {
	loc := kigali(t)
	start, end := DaysWindow(time.Date(2026, 8, 9, 15, 0, 0, 0, loc), loc, 7)

	if got := start.In(loc).Format("2006-01-02 15:04"); got != "2026-08-03 00:00" {
		t.Errorf("start: want 2026-08-03 00:00 local, got %s", got)
	}
	if got := end.In(loc).Format("2006-01-02 15:04"); got != "2026-08-10 00:00" {
		t.Errorf("end: want 2026-08-10 00:00 local, got %s", got)
	}
	if got := end.Sub(start).Hours(); got != 7*24 {
		t.Errorf("want exactly 7 days, got %v hours", got)
	}
}

func TestDaysWindowClampsNonPositiveDaysToOne(t *testing.T) {
	loc := kigali(t)
	at := time.Date(2026, 8, 9, 15, 0, 0, 0, loc)

	for _, days := range []int{0, -3} {
		start, end := DaysWindow(at, loc, days)
		dayStart, dayEnd := DayWindow(at, loc)
		if !start.Equal(dayStart) || !end.Equal(dayEnd) {
			t.Errorf("days=%d: want the single-day window, got [%v, %v)", days, start, end)
		}
	}
}

// The old EndOfDay returned 23:59:59, so an increment landing in that final
// second set a Redis TTL already in the past and Redis dropped the key —
// silently wiping the day's count. The boundary must always be in the future.
func TestEndOfLocalDayIsAlwaysInTheFuture(t *testing.T) {
	loc := kigali(t)
	for _, at := range []time.Time{
		time.Date(2026, 8, 9, 23, 59, 59, 999_999_999, loc),
		time.Date(2026, 8, 9, 0, 0, 0, 0, loc),
		time.Date(2026, 8, 9, 1, 59, 59, 0, loc), // just before the old UTC rollover
	} {
		if end := EndOfLocalDay(at, loc); !end.After(at) {
			t.Errorf("EndOfLocalDay(%v) = %v, which is not after the input", at, end)
		}
	}
}

func TestEndOfLocalDayMatchesDayWindowEnd(t *testing.T) {
	loc := kigali(t)
	at := time.Date(2026, 8, 9, 9, 15, 0, 0, loc)
	_, end := DayWindow(at, loc)
	if got := EndOfLocalDay(at, loc); !got.Equal(end) {
		t.Errorf("counter expiry %v must line up with the query window end %v", got, end)
	}
}

func TestStartOfLocalDayNormalisesAcrossZones(t *testing.T) {
	loc := kigali(t)
	// The same instant expressed in UTC must resolve to the same local day start.
	at := time.Date(2026, 8, 9, 0, 30, 0, 0, loc)
	if got, want := StartOfLocalDay(at.UTC(), loc), StartOfLocalDay(at, loc); !got.Equal(want) {
		t.Errorf("zone of the input must not matter: got %v, want %v", got, want)
	}
}
