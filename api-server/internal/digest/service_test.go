package digest

import (
	"strings"
	"testing"
	"time"
)

func snap() *Snapshot {
	return &Snapshot{
		Day:             time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
		RidesCompleted:  12,
		RidesCancelled:  3,
		RidesRequested:  20,
		FareRWF:         48000,
		FarePrevRWF:     40000,
		PaymentsByState: map[string]int{},
		StorageOK:       true,
		DBSize:          "27 MB",
	}
}

func TestFormatCleanDaySaysSo(t *testing.T) {
	out := Format(snap(), "prod")
	if !strings.Contains(out, "✅ Nothing needs attention") {
		t.Fatalf("a clean day must say so explicitly:\n%s", out)
	}
	if strings.Contains(out, "⚠️") {
		t.Fatalf("clean day should carry no warnings:\n%s", out)
	}
}

// The whole point of the digest is that actionable items are impossible to
// miss — these are the lines a human is supposed to act on.
func TestFormatSurfacesEveryActionableItem(t *testing.T) {
	s := snap()
	s.PendingApplications = 4
	s.ExpiringDocuments = 2
	s.OpenTickets = 7
	s.OpenIncidents = 1

	out := Format(s, "prod")
	for _, want := range []string{
		"4 driver application(s) awaiting review",
		"2 driver(s) with documents expiring",
		"7 open support ticket(s)",
		"1 open safety incident(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing actionable line %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Nothing needs attention") {
		t.Error("must not claim all-clear while items are pending")
	}
}

// Broken storage is the failure this whole feature exists to catch: it must
// never be reported as a quiet zero.
func TestFormatFlagsUnreachableStorage(t *testing.T) {
	s := snap()
	s.StorageOK = false
	s.StorageErr = "bucket \"rides-docs\" unreachable: 401 Unauthorized"

	out := Format(s, "staging")
	if !strings.Contains(out, "Object storage unreachable") {
		t.Fatalf("storage outage must appear under Needs attention:\n%s", out)
	}
	if !strings.Contains(out, "UNREACHABLE") {
		t.Fatalf("platform section must not report storage as ok:\n%s", out)
	}
}

func TestDelta(t *testing.T) {
	cases := []struct {
		cur, prev int
		want      string
	}{
		{12, 10, "(▲2, +20%)"},
		{8, 10, "(▼2, -20%)"},
		{10, 10, "(=)"},
		{5, 0, "(new)"},
		{0, 0, ""}, // no data either day — say nothing rather than "+100%"
	}
	for _, c := range cases {
		if got := delta(c.cur, c.prev); got != c.want {
			t.Errorf("delta(%d,%d) = %q, want %q", c.cur, c.prev, got, c.want)
		}
	}
}

func TestMoneyGroupsThousands(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1,000", 48000: "48,000", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := money(in); got != want {
			t.Errorf("money(%d) = %q, want %q", in, got, want)
		}
	}
}

// Map iteration order is randomised; the same day must render identically.
func TestStatusesIsDeterministic(t *testing.T) {
	m := map[string]int{"SUCCESS": 3, "FAILED": 1, "PENDING": 2}
	first := statuses(m)
	for i := 0; i < 20; i++ {
		if got := statuses(m); got != first {
			t.Fatalf("statuses() not deterministic: %q vs %q", got, first)
		}
	}
	if first != "failed 1, pending 2, success 3" {
		t.Fatalf("unexpected rendering: %q", first)
	}
}

func TestUntilNextRunPicksTomorrowOncePastTheHour(t *testing.T) {
	loc := time.UTC
	s := &Service{loc: loc, hour: 7}

	s.now = func() time.Time { return time.Date(2026, 7, 26, 6, 30, 0, 0, loc) }
	if got := s.untilNextRun(); got != 30*time.Minute {
		t.Errorf("before the hour: got %v, want 30m", got)
	}

	// Already past 07:00 → must schedule tomorrow, not fire immediately in a loop.
	s.now = func() time.Time { return time.Date(2026, 7, 26, 7, 30, 0, 0, loc) }
	if got := s.untilNextRun(); got != 23*time.Hour+30*time.Minute {
		t.Errorf("after the hour: got %v, want 23h30m", got)
	}
}
