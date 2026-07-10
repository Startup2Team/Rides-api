// Package alerting pushes operational alerts to Telegram — the "something is
// breaking RIGHT NOW" channel that on-box logs can't provide. It is wired as a
// zerolog hook in main, so every Error-level log line becomes a (deduped,
// rate-limited) Telegram message to the team group. The Telegram Bot API is
// free: create a bot with @BotFather, put the token + chat id in the env.
//
//	TELEGRAM_BOT_TOKEN  — from @BotFather
//	TELEGRAM_CHAT_ID    — the team group's chat id (add the bot to the group)
//
// Unset env → a disabled notifier that does nothing (dev default).
package alerting

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	// perKeyCooldown suppresses repeats of the SAME error message — a crash
	// loop produces one alert per window, not hundreds.
	perKeyCooldown = 10 * time.Minute
	// globalHourlyCap is the ceiling across ALL alerts, so even many distinct
	// errors can't flood the group (and Telegram's own limits stay far away).
	globalHourlyCap = 20
	// queueSize bounds the async send queue; when full, alerts are dropped
	// rather than ever blocking a request path.
	queueSize = 64
)

// Notifier sends messages to a Telegram chat, asynchronously.
type Notifier struct {
	token   string
	chatID  string
	env     string
	client  *http.Client
	queue   chan string
	mu      sync.Mutex
	lastKey map[string]time.Time
	sentLog []time.Time // timestamps of sends within the sliding hour
	now     func() time.Time
}

// NewTelegram builds a notifier. Empty token/chatID → returns nil (callers and
// the zerolog hook treat a nil notifier as disabled).
func NewTelegram(token, chatID, env string) *Notifier {
	if token == "" || chatID == "" {
		return nil
	}
	n := &Notifier{
		token:   token,
		chatID:  chatID,
		env:     env,
		client:  &http.Client{Timeout: 10 * time.Second},
		queue:   make(chan string, queueSize),
		lastKey: map[string]time.Time{},
		now:     time.Now,
	}
	go n.worker()
	return n
}

// worker drains the queue in the background so callers never block on Telegram.
func (n *Notifier) worker() {
	for text := range n.queue {
		n.post(text)
	}
}

func (n *Notifier) post(text string) {
	form := url.Values{
		"chat_id": {n.chatID},
		"text":    {text},
	}
	resp, err := n.client.PostForm("https://api.telegram.org/bot"+n.token+"/sendMessage", form)
	if err != nil {
		return // alerting must never take the app down; drop silently
	}
	resp.Body.Close()
}

// shouldSend applies the dedupe + rate-limit policy. Pure state transition on
// the notifier's clock, so it is unit-testable with a fake now().
func (n *Notifier) shouldSend(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()

	// Per-key cooldown: same message repeating → one alert per window.
	if last, ok := n.lastKey[key]; ok && now.Sub(last) < perKeyCooldown {
		return false
	}

	// Global sliding-hour cap.
	cutoff := now.Add(-time.Hour)
	kept := n.sentLog[:0]
	for _, t := range n.sentLog {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	n.sentLog = kept
	if len(n.sentLog) >= globalHourlyCap {
		return false
	}

	n.lastKey[key] = now
	n.sentLog = append(n.sentLog, now)
	return true
}

// Notify sends a free-form message (subject to the global cap, no dedupe key).
// Used for startup/deploy notices.
func (n *Notifier) Notify(text string) {
	if n == nil {
		return
	}
	if !n.shouldSend("notify:" + text) {
		return
	}
	select {
	case n.queue <- text:
	default: // queue full — drop, never block
	}
}

// ── zerolog hook ──────────────────────────────────────────────────────────────

// Hook returns a zerolog hook that forwards Error-and-worse log events to
// Telegram. Attach with: log = log.Hook(notifier.Hook()).
func (n *Notifier) Hook() zerolog.Hook {
	return errorHook{n: n}
}

type errorHook struct{ n *Notifier }

func (h errorHook) Run(e *zerolog.Event, level zerolog.Level, message string) {
	if h.n == nil || level < zerolog.ErrorLevel || message == "" {
		return
	}
	if !h.n.shouldSend(message) {
		return
	}
	text := fmt.Sprintf("🔴 [%s] %s: %s", strings.ToUpper(h.n.env), strings.ToUpper(level.String()), message)
	select {
	case h.n.queue <- text:
	default:
	}
}
