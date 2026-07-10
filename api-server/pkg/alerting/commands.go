package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	if n == nil || status == nil {
		return
	}
	go n.commandLoop(ctx, status)
}

func (n *Notifier) commandLoop(ctx context.Context, status StatusFunc) {
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
			n.handleUpdate(ctx, u, status)
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

func (n *Notifier) handleUpdate(ctx context.Context, u tgUpdate, status StatusFunc) {
	if u.Message == nil {
		return
	}
	cmd, _ := parseCommand(u.Message.Text)
	if cmd == "" {
		return
	}
	chatID := strconv.FormatInt(u.Message.Chat.ID, 10)

	switch cmd {
	case "status", "ping", "health":
		n.reply(chatID, status(ctx))
	case "help", "start":
		n.reply(chatID, helpText())
	default:
		// Unknown slash-command — point at /help, don't spam on random text.
		if strings.HasPrefix(strings.TrimSpace(u.Message.Text), "/") {
			n.reply(chatID, "Unknown command. Try /help")
		}
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

func helpText() string {
	return strings.TrimSpace(`
Rides alerts bot — commands:
/status — API health + env (also /ping /health)
/help — this message

You also get automatic alerts:
• 🚀 on every API boot/deploy
• 🔴 on Error-level API logs (rate-limited)
• 🚨 from GitHub if /health is down
• ✅ daily OK ping when the site is up
`)
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
