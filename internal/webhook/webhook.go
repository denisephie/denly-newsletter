// Package webhook receives WhatsApp messages from Twilio.
//
// Security rules (see docs/SPEC.md): every request must carry a valid
// X-Twilio-Signature, and messages from anyone but the owner are ignored
// without a reply.
package webhook

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"denly/internal/format"
	"denly/internal/store"
)

const (
	Path       = "/webhook/whatsapp"
	emptyTwiML = `<?xml version="1.0" encoding="UTF-8"?><Response></Response>`
	maxBody    = 64 << 10
)

// Inbound turns a message into a reply (implemented by *bot.Bot).
type Inbound interface {
	HandleInbound(text string) (string, error)
}

type Handler struct {
	AuthToken     string
	PublicBaseURL string // e.g. https://abc.ngrok.app (no trailing slash)
	MyNumber      string // e.g. whatsapp:+6591234567
	SkipSignature bool   // only ever true in dry-run local testing

	Store *store.Store
	Bot   Inbound
	F     *format.Formatter
	Send  func(body string) error // replies go out through messaging.SendMessage
	Log   *slog.Logger

	wg sync.WaitGroup
}

// Routes returns the HTTP handler: POST /webhook/whatsapp and GET /healthz.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path, h.whatsapp)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	return mux
}

// Wait blocks until all in-flight message handling has finished.
func (h *Handler) Wait() { h.wg.Wait() }

func (h *Handler) whatsapp(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := r.PostForm

	if !h.SkipSignature {
		want := Signature(h.AuthToken, h.PublicBaseURL+r.URL.RequestURI(), form)
		got := r.Header.Get("X-Twilio-Signature")
		if got == "" || !hmac.Equal([]byte(got), []byte(want)) {
			h.Log.Warn("rejected webhook: bad signature", "remote", r.RemoteAddr)
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}

	from := form.Get("From")
	if NormalizeNumber(from) != NormalizeNumber(h.MyNumber) || h.MyNumber == "" {
		h.Log.Warn("ignored message from unknown number", "from", Mask(from))
		writeTwiML(w)
		return
	}

	if err := h.Store.TouchInbound(); err != nil {
		h.Log.Error("record inbound", "err", err)
	}
	writeTwiML(w) // answer Twilio right away; reply asynchronously

	body := form.Get("Body")
	media, _ := strconv.Atoi(form.Get("NumMedia"))
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.handle(body, media)
	}()
}

func (h *Handler) handle(body string, media int) {
	var reply string
	if strings.TrimSpace(body) == "" && media > 0 {
		reply = h.F.TextOnly()
	} else {
		var err error
		reply, err = h.Bot.HandleInbound(body)
		if err != nil {
			h.Log.Error("handle message", "err", err)
			reply = h.F.Oops()
		}
	}
	if reply == "" {
		return
	}
	if err := h.Send(reply); err != nil {
		h.Log.Error("send reply", "err", err)
	}
}

func writeTwiML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Write([]byte(emptyTwiML))
}

// Signature computes Twilio's X-Twilio-Signature: base64(HMAC-SHA1(token,
// fullURL + each POST param sorted by name, as name+value)).
func Signature(authToken, fullURL string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(fullURL)
	for _, k := range keys {
		vals := append([]string(nil), form[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			b.WriteString(k)
			b.WriteString(v)
		}
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// NormalizeNumber makes "+65 9123 4567" and "whatsapp:+6591234567" compare equal.
func NormalizeNumber(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "whatsapp:") {
		s = "whatsapp:" + s
	}
	return s
}

// Mask hides all but the last 4 digits of a number for logs.
func Mask(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}
