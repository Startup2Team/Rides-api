package waitlist

import (
	"context"
	"fmt"
	"net/smtp"

	"github.com/workspace/ride-platform/config"
)

// Mailer sends the optional waitlist confirmation email. Nil-safe: Service
// checks Configured() before calling Send, so an unset SMTP config never
// blocks or fails a signup — SMS is the primary confirmation channel, email
// lights up whenever Pacifique provides Google Workspace credentials.
type Mailer interface {
	Configured() bool
	Send(ctx context.Context, to, subject, htmlBody string) error
}

// SMTPMailer is a minimal net/smtp mailer for Google Workspace/Gmail SMTP
// (STARTTLS on port 587, which net/smtp.SendMail negotiates automatically
// when the server advertises it). Deliberately generic and dependency-free —
// this sends a one-line confirmation, not a transactional-email platform.
type SMTPMailer struct {
	host     string
	addr     string // host:port
	username string
	password string
	from     string
}

func NewSMTPMailer(cfg config.SMTPConfig) *SMTPMailer {
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	from := cfg.From
	if from == "" {
		from = cfg.Username
	}
	return &SMTPMailer{
		host:     cfg.Host,
		addr:     fmt.Sprintf("%s:%d", cfg.Host, port),
		username: cfg.Username,
		password: cfg.Password,
		from:     from,
	}
}

// Configured reports whether enough SMTP env vars are set to attempt a send.
// SMTP_HOST/USERNAME/PASSWORD are the minimum; SMTP_FROM falls back to the
// username above.
func (m *SMTPMailer) Configured() bool {
	return m != nil && m.host != "" && m.username != "" && m.password != ""
}

// Send delivers a single HTML email. net/smtp has no context support —
// SendMail is one blocking dial+send bounded only by the OS TCP timeout —
// which is acceptable because callers treat email delivery as a best-effort
// side effect and log-and-continue on error rather than failing the request.
func (m *SMTPMailer) Send(_ context.Context, to, subject, htmlBody string) error {
	if !m.Configured() {
		return fmt.Errorf("smtp: not configured")
	}

	auth := smtp.PlainAuth("", m.username, m.password, m.host)

	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=\"UTF-8\"\r\n\r\n%s",
		m.from, to, subject, htmlBody,
	)

	return smtp.SendMail(m.addr, auth, m.from, []string{to}, []byte(msg))
}
