package alerting

import (
	"testing"
	"time"
)

// newTestNotifier builds a notifier with a controllable clock and no worker
// (we only exercise the shouldSend policy, never the network).
func newTestNotifier(start time.Time) (*Notifier, *time.Time) {
	clock := start
	n := &Notifier{
		lastKey: map[string]time.Time{},
		now:     func() time.Time { return clock },
	}
	return n, &clock
}

// The dedupe window is what turns a crash-loop (hundreds of identical errors)
// into ONE alert per 10 minutes. If this regresses to always-true the team
// group gets flooded and mutes the bot — which is worse than no alerting.
func TestShouldSend_PerKeyCooldown(t *testing.T) {
	n, clock := newTestNotifier(time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	if !n.shouldSend("db down") {
		t.Fatal("first occurrence must send")
	}
	if n.shouldSend("db down") {
		t.Fatal("immediate repeat must be suppressed")
	}
	*clock = clock.Add(9 * time.Minute)
	if n.shouldSend("db down") {
		t.Fatal("repeat inside the 10-min window must be suppressed")
	}
	*clock = clock.Add(2 * time.Minute) // now 11 min after first
	if !n.shouldSend("db down") {
		t.Fatal("repeat after the window must send again")
	}
}

// Distinct errors are independent — one noisy error must not silence others.
func TestShouldSend_DistinctKeysIndependent(t *testing.T) {
	n, _ := newTestNotifier(time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	if !n.shouldSend("db down") || !n.shouldSend("redis down") {
		t.Fatal("distinct keys must both send")
	}
}

// The global cap bounds total volume even when every message is unique
// (e.g. errors that embed IDs) — the sliding hour must also RELEASE capacity.
func TestShouldSend_GlobalHourlyCap(t *testing.T) {
	n, clock := newTestNotifier(time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	for i := 0; i < globalHourlyCap; i++ {
		if !n.shouldSend(time.Now().String() + string(rune('a'+i))) {
			t.Fatalf("send %d under the cap must pass", i)
		}
		*clock = clock.Add(time.Second)
	}
	if n.shouldSend("one more") {
		t.Fatal("cap reached — must suppress")
	}
	*clock = clock.Add(61 * time.Minute) // slide the window past all sends
	if !n.shouldSend("after window") {
		t.Fatal("capacity must free up after the hour slides")
	}
}

// nil notifier (env unset) must be safe to call — dev machines have no token.
func TestNilNotifierIsSafe(t *testing.T) {
	var n *Notifier
	n.Notify("should not panic")
	if NewTelegram("", "", "development") != nil {
		t.Fatal("empty credentials must produce a disabled (nil) notifier")
	}
}
