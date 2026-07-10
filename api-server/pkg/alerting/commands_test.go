package alerting

import (
	"context"
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in, cmd, bot string
	}{
		{"/status", "status", ""},
		{"/status@rides_rw_alerts_bot", "status", "rides_rw_alerts_bot"},
		{"/STATUS please", "status", ""},
		{"/help", "help", ""},
		{"/ping", "ping", ""},
		{"hello", "", ""},
		{"", "", ""},
		{"status", "", ""},
	}
	for _, c := range cases {
		cmd, bot := parseCommand(c.in)
		if cmd != c.cmd || bot != c.bot {
			t.Fatalf("parseCommand(%q) = (%q,%q), want (%q,%q)", c.in, cmd, bot, c.cmd, c.bot)
		}
	}
}

func TestNilStartCommandsSafe(t *testing.T) {
	var n *Notifier
	n.StartCommands(context.Background(), func(context.Context) string { return "x" })
}
