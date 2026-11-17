package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBot struct {
	mu   sync.Mutex
	seen []string
}

func (f *fakeBot) HandleInbound(t string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, t)
	return "reply:" + t, nil
}

func TestNormalizeCommand(t *testing.T) {
	for in, want := range map[string]string{
		"/help":             "help",
		"/start":            "help",
		"/done@denly_bot 2": "done 2",
		"/add buy milk":     "add buy milk",
		"add /x":            "add /x",
		"  hi  ":            "hi",
	} {
		if got := normalizeCommand(in); got != want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplit(t *testing.T) {
	long := strings.Repeat("line\n", 2000) // 10000 runes
	parts := split(long, 4096)
	if len(parts) < 3 {
		t.Fatalf("parts = %d", len(parts))
	}
	if strings.Join(parts, "") != long {
		t.Fatal("split lost text")
	}
	for _, p := range parts {
		if len([]rune(p)) > 4096 {
			t.Fatal("part too long")
		}
	}
}

// Only the owner's chat gets answered; strangers are ignored silently.
func TestOwnerOnly(t *testing.T) {
	var mu sync.Mutex
	var sent []url.Values
	served := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			mu.Lock()
			first := !served
			served = true
			mu.Unlock()
			if !first {
				time.Sleep(50 * time.Millisecond)
				w.Write([]byte(`{"ok":true,"result":[]}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
				map[string]any{"update_id": 1, "message": map[string]any{"text": "/help", "chat": map[string]any{"id": 111}}},
				map[string]any{"update_id": 2, "message": map[string]any{"text": "hi", "chat": map[string]any{"id": 999}}},
			}})
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			mu.Lock()
			sent = append(sent, form)
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	defer srv.Close()

	fb := &fakeBot{}
	c := &Client{Token: "T", ChatID: "111", API: srv.URL, Bot: fb,
		Touch: func() error { return nil }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0].Get("chat_id") != "111" || sent[0].Get("text") != "reply:help" {
		t.Fatalf("sent = %v", sent)
	}
	if len(fb.seen) != 1 || fb.seen[0] != "help" {
		t.Fatalf("bot saw %v", fb.seen)
	}
}
