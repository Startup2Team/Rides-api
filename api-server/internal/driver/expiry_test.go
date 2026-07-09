package driver

import (
	"strings"
	"testing"
	"time"
)

// alertsFromDates decides which documents show up as expiring/expired — it
// feeds BOTH the session banner and the daily push job, so an off-by-one here
// either spams drivers or lets a license lapse silently. We pin the window
// boundary (30 days), the EXPIRED/EXPIRING_SOON flip at day 0, and nil-date
// handling.
func date(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestAlertsFromDates(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 30, 0, 0, time.UTC) // mid-day: date math must ignore time-of-day

	tests := []struct {
		name       string
		license    *time.Time
		wantCount  int
		wantStatus string
		wantDays   int
	}{
		{"no dates set → no alerts", nil, 0, "", 0},
		{"far future (60d) → outside window", date(2026, 9, 7), 0, "", 0},
		{"day 31 → just outside window", date(2026, 8, 9), 0, "", 0},
		{"day 30 → window edge, alerts", date(2026, 8, 8), 1, "EXPIRING_SOON", 30},
		{"day 7", date(2026, 7, 16), 1, "EXPIRING_SOON", 7},
		{"today → still EXPIRING_SOON, 0 days", date(2026, 7, 9), 1, "EXPIRING_SOON", 0},
		{"yesterday → EXPIRED, -1", date(2026, 7, 8), 1, "EXPIRED", -1},
		{"long expired", date(2026, 6, 9), 1, "EXPIRED", -30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			alerts := alertsFromDates(tc.license, nil, nil, now)
			if len(alerts) != tc.wantCount {
				t.Fatalf("got %d alerts, want %d (%+v)", len(alerts), tc.wantCount, alerts)
			}
			if tc.wantCount == 1 {
				a := alerts[0]
				if a.Status != tc.wantStatus || a.DaysLeft != tc.wantDays || a.Document != "license" {
					t.Errorf("alert = %+v, want status=%s days=%d doc=license", a, tc.wantStatus, tc.wantDays)
				}
			}
		})
	}
}

func TestAlertsFromDates_MultipleDocuments(t *testing.T) {
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	// license expired, insurance in-window, authorization far future → 2 alerts.
	alerts := alertsFromDates(date(2026, 7, 1), date(2026, 7, 20), date(2027, 1, 1), now)
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2: %+v", len(alerts), alerts)
	}
	if alerts[0].Document != "license" || alerts[0].Status != "EXPIRED" {
		t.Errorf("first alert = %+v, want expired license", alerts[0])
	}
	if alerts[1].Document != "insurance" || alerts[1].Status != "EXPIRING_SOON" {
		t.Errorf("second alert = %+v, want expiring insurance", alerts[1])
	}
}

// The day-mark policy is what stops a 30-day window sending 30 pushes. A
// regression that returns true for every day would spam every driver daily.
func TestShouldNotifyExpiry(t *testing.T) {
	notify := []int{30, 14, 7, 3, 1, 0, -1}
	for _, d := range notify {
		if !shouldNotifyExpiry(d) {
			t.Errorf("daysLeft=%d: want notify", d)
		}
	}
	quiet := []int{29, 15, 8, 6, 4, 2, -2, -30, 31}
	for _, d := range quiet {
		if shouldNotifyExpiry(d) {
			t.Errorf("daysLeft=%d: must NOT notify (day-mark policy)", d)
		}
	}
}

// Message copy must match the alert's urgency — an "expires in 0 days" or a
// "expires in -3 days" reading would look broken to drivers.
func TestExpiryMessage(t *testing.T) {
	tests := []struct {
		alert     DocumentAlert
		wantTitle string
		wantIn    string
	}{
		{DocumentAlert{Document: "license", DaysLeft: -1, ExpiresOn: "2026-07-08", Status: "EXPIRED"}, "Document expired", "expired on 2026-07-08"},
		{DocumentAlert{Document: "insurance", DaysLeft: 0, ExpiresOn: "2026-07-09"}, "Document expires today", "expires today"},
		{DocumentAlert{Document: "authorization", DaysLeft: 1, ExpiresOn: "2026-07-10"}, "Document expires tomorrow", "expires tomorrow"},
		{DocumentAlert{Document: "license", DaysLeft: 7, ExpiresOn: "2026-07-16"}, "Document expiring soon", "expires in 7 days"},
	}
	for _, tc := range tests {
		title, body := expiryMessage(tc.alert)
		if title != tc.wantTitle {
			t.Errorf("title = %q, want %q", title, tc.wantTitle)
		}
		if !strings.Contains(body, tc.wantIn) {
			t.Errorf("body = %q, want it to contain %q", body, tc.wantIn)
		}
	}
}
