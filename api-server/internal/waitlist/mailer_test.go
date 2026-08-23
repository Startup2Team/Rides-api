package waitlist_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/waitlist"
)

func TestSMTPMailer_NotConfigured_ReturnsErrorWithoutDialing(t *testing.T) {
	m := waitlist.NewSMTPMailer(config.SMTPConfig{})
	assert.False(t, m.Configured())

	err := m.Send(context.Background(), "someone@example.com", "subject", "<p>hi</p>")
	require.Error(t, err)
}

func TestSMTPMailer_RecipientWithCRLF_Rejected(t *testing.T) {
	m := waitlist.NewSMTPMailer(config.SMTPConfig{Host: "smtp.example.com", Username: "u", Password: "p"})
	require.True(t, m.Configured())

	err := m.Send(context.Background(), "victim@example.com\r\nBcc: attacker@evil.com", "subject", "<p>hi</p>")
	require.Error(t, err, "a recipient containing CRLF must be rejected before any SMTP command is sent")
}

// TestSMTPMailer_DialTimeout_BoundsBlockingTime proves Send doesn't hang past
// its bound when the SMTP server is unreachable — the historical bug being
// fixed here is net/smtp.SendMail having no timeout at all (relying on the OS
// TCP timeout, which can be minutes). 192.0.2.1 is a TEST-NET-1 address
// (RFC 5737) reserved for documentation and never routable, so a connection
// to it reliably has to wait out our own deadline rather than a real network
// round trip.
func TestSMTPMailer_DialTimeout_BoundsBlockingTime(t *testing.T) {
	m := waitlist.NewSMTPMailer(config.SMTPConfig{Host: "192.0.2.1", Port: 25, Username: "u", Password: "p"})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := m.Send(ctx, "someone@example.com", "subject", "<p>hi</p>")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 5*time.Second, "Send must honor the context deadline, not block on the OS TCP timeout")
}
