package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StatusFunc returns a short human-readable status for /status replies.
// Callers typically hit the local /health endpoint and format env + result.
type StatusFunc func(ctx context.Context) string

// StartCommands long-polls Telegram getUpdates and answers /status and /help
// in the configured team chat (and DMs to the bot). Safe no-op on a nil
// notifier. Never panics; network errors back off and retry.
func (n *Notifier) StartCommands(ctx context.Context, status StatusFunc) {
	n.StartCommandsWith(ctx, map[string]StatusFunc{"status": status})
}

// StartCommandsWith is StartCommands with an arbitrary command set, so callers
// can expose more than health — /stats, /pending and anything added later.
// Keys are bare command names without the leading slash. A nil notifier, an
// empty set, or an unanswerable command are all safe no-ops.
func (n *Notifier) StartCommandsWith(ctx context.Context, handlers map[string]StatusFunc) {
	if n == nil || len(handlers) == 0 {
		return
	}
	live := make(map[string]StatusFunc, len(handlers))
	for name, fn := range handlers {
		if fn != nil {
			live[strings.ToLower(name)] = fn
		}
	}
	if len(live) == 0 {
		return
	}
	go n.commandLoop(ctx, live)
}

func (n *Notifier) commandLoop(ctx context.Context, handlers map[string]StatusFunc) {
	// Long-poll client: Telegram holds the request up to ~25s, so the HTTP
	// timeout must be longer than the send client's 10s.
	longClient := &http.Client{Timeout: 35 * time.Second}
	offset := 0
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, next, err := n.getUpdates(ctx, longClient, offset)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		offset = next

		for _, u := range updates {
			n.handleUpdate(ctx, u, handlers)
		}
	}
}

type tgUpdate struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int `json:"message_id"`
		Text      string
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func (n *Notifier) getUpdates(ctx context.Context, client *http.Client, offset int) ([]tgUpdate, int, error) {
	q := url.Values{
		"timeout": {"25"},
		"offset":  {strconv.Itoa(offset)},
		// Only messages — we don't need chat_member noise.
		"allowed_updates": {`["message"]`},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.telegram.org/bot"+n.token+"/getUpdates?"+q.Encode(), nil)
	if err != nil {
		return nil, offset, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, offset, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, offset, err
	}
	var parsed struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, offset, err
	}
	if !parsed.OK {
		return nil, offset, fmt.Errorf("telegram getUpdates not ok")
	}
	next := offset
	for _, u := range parsed.Result {
		if u.UpdateID+1 > next {
			next = u.UpdateID + 1
		}
	}
	return parsed.Result, next, nil
}

// commandAliases maps what people actually type onto a registered handler.
var commandAliases = map[string]string{
	"ping": "status", "health": "status",
	"digest": "stats", "summary": "stats", "today": "stats",
	"review": "pending", "queue": "pending",
}

func (n *Notifier) handleUpdate(ctx context.Context, u tgUpdate, handlers map[string]StatusFunc) {
	if u.Message == nil {
		return
	}
	cmd, _ := parseCommand(u.Message.Text)
	if cmd == "" {
		return
	}
	chatID := strconv.FormatInt(u.Message.Chat.ID, 10)

	if alias, ok := commandAliases[cmd]; ok {
		cmd = alias
	}

	if cmd == "help" || cmd == "start" {
		n.reply(chatID, helpText(handlers))
		return
	}
	if fn, ok := handlers[cmd]; ok {
		// Answers can be slow (the digest runs a handful of queries), so bound
		// them — a hung database must not wedge the whole command loop.
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		n.reply(chatID, fn(cctx))
		return
	}
	// Unknown slash-command — point at /help, don't spam on random text.
	if strings.HasPrefix(strings.TrimSpace(u.Message.Text), "/") {
		n.reply(chatID, "Unknown command. Try /help")
	}
}

// parseCommand extracts the command name from "/status", "/status@bot", etc.
// Returns ("", "") when the text is not a bot command.
func parseCommand(text string) (cmd, bot string) {
	text = strings.TrimSpace(text)
	if text == "" || text[0] != '/' {
		return "", ""
	}
	// First token only.
	field := strings.Fields(text)[0]
	field = strings.TrimPrefix(field, "/")
	if i := strings.IndexByte(field, '@'); i >= 0 {
		return strings.ToLower(field[:i]), strings.ToLower(field[i+1:])
	}
	return strings.ToLower(field), ""
}

// commandHelp describes each registered command. Only the ones actually wired
// up are listed, so /help never advertises something that will answer
// "Unknown command".
var commandHelp = map[string]string{
	"status":  "/status — API health + env (also /ping /health)",
	"stats":   "/stats — yesterday's rides, revenue, signups and anything needing attention (also /digest /summary)",
	"pending": "/pending — driver applications and documents waiting on a human (also /review /queue)",
}

func helpText(handlers map[string]StatusFunc) string {
	// Fixed order for the documented commands so /help reads the same every
	// time; anything registered but undocumented is appended alphabetically.
	lines := make([]string, 0, len(handlers)+1)
	for _, name := range []string{"status", "stats", "pending"} {
		if _, ok := handlers[name]; ok {
			lines = append(lines, commandHelp[name])
		}
	}
	extra := make([]string, 0)
	for name := range handlers {
		if _, documented := commandHelp[name]; !documented {
			extra = append(extra, "/"+name)
		}
	}
	sort.Strings(extra)
	lines = append(lines, extra...)
	lines = append(lines, "/help — this message")

	return strings.TrimSpace("Rides alerts bot — commands:\n" + strings.Join(lines, "\n") + `

You also get automatically:
• 📊 a daily summary each morning
• 🚀 on every API boot/deploy
• 🔴 on Error-level API logs (rate-limited)
• 🔴 when object storage goes unreachable
• 🚨 from GitHub if /health is down`)
}

// reply sends immediately (no dedupe/cap) — command answers must always land.
func (n *Notifier) reply(chatID, text string) {
	if text == "" {
		return
	}
	form := url.Values{
		"chat_id": {chatID},
		"text":    {text},
	}
	resp, err := n.client.PostForm("https://api.telegram.org/bot"+n.token+"/sendMessage", form)
	if err != nil {
		return
	}
	resp.Body.Close()
}
