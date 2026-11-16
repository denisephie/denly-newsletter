package webhook

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"denly/internal/bot"
	"denly/internal/clock"
	"denly/internal/commands"
	"denly/internal/config"
	"denly/internal/format"
	"denly/internal/store"
)

const (
	token   = "test-auth-token"
	baseURL = "https://denly.example.com"
	me      = "whatsapp:+6591234567"
)

type fixture struct {
	h    *Handler
	st   *store.Store
	srv  *httptest.Server
	mu   sync.Mutex
	sent []string
}

func (f *fixture) Sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func setup(t *testing.T) *fixture {
	t.Helper()
	sg, _ := time.LoadLocation("Asia/Singapore")
	clk := clock.Fixed(sg, time.Date(2026, 11, 16, 10, 0, 0, 0, sg))
	st, err := store.Open(filepath.Join(t.TempDir(), "todo.db"), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := format.New(config.Defaults())
	b := &bot.Bot{Cmd: &commands.Handler{St: st, Clk: clk, F: f, CarryOver: true}, F: f}

	fx := &fixture{st: st}
	fx.h = &Handler{
		AuthToken: token, PublicBaseURL: baseURL, MyNumber: me,
		Store: st, Bot: b, F: f,
		Send: func(body string) error {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.sent = append(fx.sent, body)
			return nil
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	fx.srv = httptest.NewServer(fx.h.Routes())
	t.Cleanup(fx.srv.Close)
	return fx
}

// post sends a form to the webhook, signed with sigToken ("" = no signature header).
func (fx *fixture) post(t *testing.T, form url.Values, sigToken string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", fx.srv.URL+Path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if sigToken != "" {
		req.Header.Set("X-Twilio-Signature", Signature(sigToken, baseURL+Path, form))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	fx.h.Wait()
	return resp
}

func msg(from, body string) url.Values {
	return url.Values{
		"From": {from}, "To": {"whatsapp:+14155238886"}, "Body": {body},
		"MessageSid": {"SM123"}, "NumMedia": {"0"},
	}
}

func TestSignatureMatchesTwilioDocsExample(t *testing.T) {
	// Example from Twilio's "Webhooks security" docs.
	form := url.Values{
		"CallSid": {"CA1234567890ABCDE"}, "Caller": {"+12349013030"}, "Digits": {"1234"},
		"From": {"+12349013030"}, "To": {"+18005551212"},
	}
	got := Signature("12345", "https://mycompany.com/myapp.php?foo=1&bar=2", form)
	if got != "0/KCTR6DLpKmkAf8muzZqo1nDgQ=" {
		t.Fatalf("signature = %s", got)
	}
}

func TestValidMessageFromMeGetsReply(t *testing.T) {
	fx := setup(t)
	resp := fx.post(t, msg(me, "add buy milk"), token)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "xml") {
		t.Fatalf("status %d, type %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Response></Response>") {
		t.Fatalf("body = %s", body)
	}
	sent := fx.Sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "1. buy milk") {
		t.Fatalf("sent = %q", sent)
	}
	if at, _ := fx.st.LastInboundAt(); at.IsZero() {
		t.Fatal("last_inbound_at not recorded")
	}
}

// Done-when: "A request with a bad Twilio signature is rejected."
func TestBadSignatureRejected(t *testing.T) {
	fx := setup(t)
	for name, sigToken := range map[string]string{"wrong token": "not-the-token", "missing": ""} {
		resp := fx.post(t, msg(me, "add buy milk"), sigToken)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, resp.StatusCode)
		}
	}
	if len(fx.Sent()) != 0 {
		t.Fatal("replied to a forged request")
	}
	if open, _ := fx.st.AllOpen(); len(open) != 0 {
		t.Fatal("forged request changed the list")
	}
	if at, _ := fx.st.LastInboundAt(); !at.IsZero() {
		t.Fatal("forged request opened the 24h window")
	}
}

func TestTamperedBodyRejected(t *testing.T) {
	fx := setup(t)
	form := msg(me, "list")
	sig := Signature(token, baseURL+Path, form)
	form.Set("Body", "remove 1")
	req, _ := http.NewRequest("POST", fx.srv.URL+Path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

// Done-when: "A message from any other number is ignored."
func TestOtherNumberIgnored(t *testing.T) {
	fx := setup(t)
	resp := fx.post(t, msg("whatsapp:+6599999999", "add hacked"), token)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(fx.Sent()) != 0 {
		t.Fatal("replied to a stranger")
	}
	if open, _ := fx.st.AllOpen(); len(open) != 0 {
		t.Fatal("stranger changed the list")
	}
	if at, _ := fx.st.LastInboundAt(); !at.IsZero() {
		t.Fatal("stranger opened the 24h window")
	}
}

func TestNoNumberConfiguredIgnoresEveryone(t *testing.T) {
	fx := setup(t)
	fx.h.MyNumber = ""
	fx.post(t, msg("", "list"), token)
	if len(fx.Sent()) != 0 {
		t.Fatal("empty MY_WHATSAPP_NUMBER must not match an empty From")
	}
}

func TestMediaOnly(t *testing.T) {
	fx := setup(t)
	form := msg(me, "")
	form.Set("NumMedia", "1")
	fx.post(t, form, token)
	if sent := fx.Sent(); len(sent) != 1 || !strings.Contains(sent[0], "text only") {
		t.Fatalf("sent = %q", sent)
	}
}

func TestFreeTextWithoutParser(t *testing.T) {
	fx := setup(t)
	fx.post(t, msg(me, "tomorrow: gym, call mum"), token)
	if sent := fx.Sent(); len(sent) != 1 || !strings.Contains(sent[0], "can't understand free text") {
		t.Fatalf("sent = %q", sent)
	}
}

func TestSkipSignatureAndRoutes(t *testing.T) {
	fx := setup(t)
	fx.h.SkipSignature = true
	if resp := fx.post(t, msg(me, "list"), ""); resp.StatusCode != 200 {
		t.Fatalf("skip-signature status %d", resp.StatusCode)
	}

	resp, err := http.Get(fx.srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz %d", resp.StatusCode)
	}
	resp, err = http.Get(fx.srv.URL + Path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET webhook %d, want 405", resp.StatusCode)
	}
}

func TestNormalizeAndMask(t *testing.T) {
	if NormalizeNumber("+65 9123 4567") != NormalizeNumber("whatsapp:+6591234567") {
		t.Fatal("normalize")
	}
	if Mask("whatsapp:+6591234567") != "****4567" {
		t.Fatal("mask")
	}
}
