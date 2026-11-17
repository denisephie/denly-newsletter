// Package telegram talks to the Telegram Bot API by long polling, so no public
// URL or tunnel is needed. Only the owner's chat is answered; every other chat
// is ignored without a reply.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"denly/internal/format"
	"denly/internal/store"
)

const (
	defaultAPI  = "https://api.telegram.org"
	pollSeconds = 30
	maxMessage  = 4096 // Telegram's per-message limit
)

// Inbound turns a message into a reply (implemented by *bot.Bot).
type Inbound interface {
	HandleInbound(text string) (string, error)
}

type Client struct {
	Token  string
	ChatID string // owner's chat id; empty = learn mode (log the id, answer nobody)
	API    string // override in tests; default https://api.telegram.org

	Store *store.Store
	Touch func() error // records inbound time; defaults to Store.TouchInbound
	Bot   Inbound
	F     *format.Formatter
	Log   *slog.Logger
	HTTP  *http.Client
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text    string `json:"text"`
		Caption string `json:"caption"`
		Chat    struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From struct {
			Username string `json:"username"`
		} `json:"from"`
	} `json:"message"`
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (c *Client) base() string {
	api := c.API
	if api == "" {
		api = defaultAPI
	}
	return api + "/bot" + c.Token
}

func (c *Client) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: (pollSeconds + 15) * time.Second}
}

func (c *Client) call(ctx context.Context, method string, form url.Values) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpc().Do(req)
	if err != nil {
		return nil, redact(err, c.Token)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, redact(err, c.Token)
	}
	var r apiResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("telegram %s: HTTP %d, unreadable response", method, resp.StatusCode)
	}
	if !r.OK {
		return nil, fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	return r.Result, nil
}

// redact keeps the bot token (part of every URL) out of logs.
func redact(err error, token string) error {
	if token == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "<token>"))
}

// Check verifies the token and returns the bot's @username.
func (c *Client) Check(ctx context.Context) (string, error) {
	res, err := c.call(ctx, "getMe", nil)
	if err != nil {
		return "", err
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(res, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// Send delivers text to the owner, split to Telegram's size limit.
func (c *Client) Send(body string) error {
	if c.ChatID == "" {
		return fmt.Errorf("TELEGRAM_CHAT_ID is not set")
	}
	return c.sendTo(context.Background(), c.ChatID, body)
}

func (c *Client) sendTo(ctx context.Context, chatID, body string) error {
	for _, part := range split(body, maxMessage) {
		_, err := c.call(ctx, "sendMessage", url.Values{"chat_id": {chatID}, "text": {part}})
		if err != nil {
			return err
		}
	}
	return nil
}

// split breaks s into chunks of at most n runes, preferring line breaks.
func split(s string, n int) []string {
	if utf8.RuneCountInString(s) <= n {
		return []string{s}
	}
	var out []string
	r := []rune(s)
	for len(r) > n {
		cut := n
		for i := n; i > n/2; i-- {
			if r[i-1] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	return append(out, string(r))
}

// Run polls until ctx is cancelled. Messages are handled one at a time.
func (c *Client) Run(ctx context.Context) error {
	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		res, err := c.call(ctx, "getUpdates", url.Values{
			"offset":          {strconv.FormatInt(offset, 10)},
			"timeout":         {strconv.Itoa(pollSeconds)},
			"allowed_updates": {`["message"]`},
		})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			c.Log.Warn("telegram poll failed", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		var ups []update
		if err := json.Unmarshal(res, &ups); err != nil {
			c.Log.Warn("telegram: bad updates payload", "err", err)
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			c.handle(ctx, u)
		}
	}
	return nil
}

func (c *Client) handle(ctx context.Context, u update) {
	m := u.Message
	if m == nil {
		return
	}
	chat := strconv.FormatInt(m.Chat.ID, 10)
	if c.ChatID == "" {
		c.Log.Warn("TELEGRAM_CHAT_ID not set; message ignored. Put this in .env to claim the bot", "chat_id", chat)
		return
	}
	if chat != c.ChatID {
		c.Log.Warn("ignored message from unknown chat", "chat", mask(chat))
		return
	}
	touch := c.Touch
	if touch == nil {
		touch = c.Store.TouchInbound
	}
	if err := touch(); err != nil {
		c.Log.Error("record inbound", "err", err)
	}

	text := m.Text
	var reply string
	if strings.TrimSpace(text) == "" {
		reply = c.F.TextOnly()
	} else {
		text = normalizeCommand(text)
		var err error
		reply, err = c.Bot.HandleInbound(text)
		if err != nil {
			c.Log.Error("handle message", "err", err)
			reply = c.F.Oops()
		}
	}
	if reply == "" {
		return
	}
	if err := c.sendTo(ctx, chat, reply); err != nil {
		c.Log.Error("send reply", "err", err)
	}
}

// normalizeCommand maps Telegram's "/help" or "/help@botname" to plain "help".
func normalizeCommand(t string) string {
	t = strings.TrimSpace(t)
	if !strings.HasPrefix(t, "/") {
		return t
	}
	first, rest, _ := strings.Cut(t[1:], " ")
	first, _, _ = strings.Cut(first, "@")
	if first == "start" {
		first = "help"
	}
	return strings.TrimSpace(first + " " + rest)
}

func mask(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}
