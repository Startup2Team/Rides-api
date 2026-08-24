package waitlist

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

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

// smtpDialTimeout bounds how long Send will wait to connect+negotiate with
// the SMTP server. net/smtp has no native timeout support (a plain
// smtp.SendMail can hang on the OS TCP timeout, which is minutes on some
// networks) — this keeps a wedged Gmail/Workspace endpoint from blocking the
// (already-detached, timeout-bounded) notify goroutine indefinitely.
const smtpDialTimeout = 10 * time.Second

// SMTPMailer is a minimal net/smtp mailer for Google Workspace/Gmail SMTP
// (STARTTLS on port 587). Deliberately generic and dependency-free — this
// sends a one-line confirmation, not a transactional-email platform.
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

// Send delivers a single HTML email over STARTTLS, bounded by smtpDialTimeout
// (or the ctx deadline, whichever is tighter — ctx is expected to be the
// detached, timeout-bounded context notify() runs under, not a request
// context). Callers treat email delivery as a best-effort side effect and
// log-and-continue on error rather than failing the request.
func (m *SMTPMailer) Send(ctx context.Context, to, subject, htmlBody string) error {
	if !m.Configured() {
		return fmt.Errorf("smtp: not configured")
	}
	// Defense in depth: validate:"email" on the request DTO already rejects
	// newlines, but a recipient with an embedded CR/LF could otherwise smuggle
	// extra SMTP headers/commands into a hand-built message.
	if strings.ContainsAny(to, "\r\n") {
		return fmt.Errorf("smtp: recipient contains invalid characters")
	}

	timeout := smtpDialTimeout
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}

	conn, err := net.DialTimeout("tcp", m.addr, timeout)
	if err != nil {
		return fmt.Errorf("smtp: dial %s: %w", m.addr, err)
	}
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: new client: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}

	if ok, _ := client.Extension("AUTH"); ok {
		auth := smtp.PlainAuth("", m.username, m.password, m.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}

	// SMTP_FROM may be a display-name form ("Rides Rw <noreply@…>"); the
	// envelope MAIL FROM must be the bare address, while the From: header
	// below keeps the full display-name form. Fall back to the raw value if
	// it isn't parseable as an address.
	envelopeFrom := m.from
	if parsed, perr := mail.ParseAddress(m.from); perr == nil {
		envelopeFrom = parsed.Address
	}
	if err := client.Mail(envelopeFrom); err != nil {
		return fmt.Errorf("smtp: mail from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp: rcpt to: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: data: %w", err)
	}
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=\"UTF-8\"\r\n\r\n%s",
		m.from, to, subject, htmlBody,
	)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: close data: %w", err)
	}

	return client.Quit()
}
