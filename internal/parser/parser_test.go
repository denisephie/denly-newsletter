package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"denly/internal/clock"
	"denly/internal/commands"
	"denly/internal/config"
	"denly/internal/format"
	"denly/internal/store"
)

var sg, _ = time.LoadLocation("Asia/Singapore")

// Monday 2026-11-16 21:05 SG: the owner is answering the night prompt.
var now = time.Date(2026, 11, 16, 21, 5, 0, 0, sg)

// fakeLLM returns canned replies in order and records what it was sent.
type fakeLLM struct {
	replies []string
	err     error
	users   []string
	systems []string
}

func (f *fakeLLM) Complete(_ context.Context, system, user string) (string, error) {
	f.systems = append(f.systems, system)
	f.users = append(f.users, user)
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return "", errors.New("fakeLLM: no reply queued")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r, nil
}

type env struct {
	p   *Parser
	st  *store.Store
	llm *fakeLLM
}

func setup(t *testing.T, replies ...string) *env {
	t.Helper()
	clk := clock.Fixed(sg, now)
	st, err := store.Open(filepath.Join(t.TempDir(), "todo.db"), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := format.New(config.Defaults())
	llm := &fakeLLM{replies: replies}
	return &env{
		p: &Parser{
			St: st, Clk: clk, F: f, LLM: llm,
			Cmd: &commands.Handler{St: st, Clk: clk, F: f, CarryOver: true},
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		st: st, llm: llm,
	}
}

func (e *env) seed(t *testing.T, text, date string) store.Task {
	t.Helper()
	task, err := e.st.AddTask(text, date)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (e *env) handle(t *testing.T, msg string) string {
	t.Helper()
	reply, err := e.p.Handle(msg)
	if err != nil {
		t.Fatalf("%q: %v", msg, err)
	}
	return reply
}

func (e *env) task(t *testing.T, id int64) store.Task {
	t.Helper()
	task, err := e.st.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (e *env) openCount(t *testing.T) int {
	t.Helper()
	open, err := e.st.AllOpen()
	if err != nil {
		t.Fatal(err)
	}
	return len(open)
}

// Done-when: "tomorrow: gym, call mum, finish report" → those 3 tasks tomorrow, and the bot shows them.
func TestTomorrowListOfThree(t *testing.T) {
	e := setup(t, `{"actions":[
		{"type":"add","text":"gym","date":"2026-11-17"},
		{"type":"add","text":"call mum","date":"2026-11-17"},
		{"type":"add","text":"finish report","date":"2026-11-17"}],"clarification":null}`)
	reply := e.handle(t, "tomorrow: gym, call mum, finish report")

	got, err := e.st.OpenOn("2026-11-17")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Text != "gym" || got[1].Text != "call mum" || got[2].Text != "finish report" {
		t.Fatalf("tomorrow's tasks = %+v", got)
	}
	if !strings.Contains(reply, "tomorrow (tue 17 nov):\n\n1. gym\n2. call mum\n3. finish report") {
		t.Fatalf("reply should show tomorrow's list:\n%s", reply)
	}
}

func TestSpecExamples(t *testing.T) {
	e := setup(t)
	passport := e.seed(t, "renew passport", "2026-11-14")
	gym := e.seed(t, "gym", "2026-11-16")
	report := e.seed(t, "finish report", "2026-11-17")

	e.llm.replies = []string{
		fmt.Sprintf(`{"actions":[{"type":"move","task_id":%d,"date":"2026-11-20"}],"clarification":null}`, passport.ID),
		fmt.Sprintf(`{"actions":[{"type":"done","task_id":%d}],"clarification":null}`, gym.ID),
		fmt.Sprintf(`{"actions":[{"type":"remove","task_id":%d}],"clarification":null}`, report.ID),
	}

	reply := e.handle(t, "move passport thing to friday")
	if e.task(t, passport.ID).Date != "2026-11-20" || !strings.Contains(reply, "renew passport moved to fri 20 nov") {
		t.Fatalf("move: %q", reply)
	}
	reply = e.handle(t, "i already did the gym today")
	if e.task(t, gym.ID).Status != store.StatusDone || !strings.Contains(reply, "✓ gym") {
		t.Fatalf("done: %q", reply)
	}
	reply = e.handle(t, "scrap the report, boss pushed the deadline")
	if e.task(t, report.ID).Status != store.StatusRemoved || !strings.Contains(reply, "✗ finish report") {
		t.Fatalf("remove: %q", reply)
	}
}

func TestPromptCarriesDateAndTasks(t *testing.T) {
	e := setup(t, `{"actions":[],"clarification":"which one?"}`)
	task := e.seed(t, "renew passport", "2026-11-14")
	e.handle(t, "move it")

	user := e.llm.users[0]
	for _, want := range []string{
		"now: monday 2026-11-16 21:05",
		"tomorrow=2026-11-17",
		"fri=2026-11-20",
		fmt.Sprintf("%d | 2026-11-14 | renew passport", task.ID),
		"message:\nmove it",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q:\n%s", want, user)
		}
	}
	if strings.Contains(e.llm.systems[0], "2026") {
		t.Error("system prompt should be static (no dates) so it caches")
	}
}

func TestInvalidOutputChangesNothing(t *testing.T) {
	cases := map[string]string{
		"not json":         `sure! here you go`,
		"truncated":        `{"actions":[{"type":"add"`,
		"unknown field":    `{"actions":[],"clarification":null,"extra":1}`,
		"missing actions":  `{"clarification":null}`,
		"unknown type":     `{"actions":[{"type":"snooze","task_id":1}],"clarification":null}`,
		"unknown task id":  `{"actions":[{"type":"done","task_id":999}],"clarification":null}`,
		"past date":        `{"actions":[{"type":"add","text":"x","date":"2026-11-15"}],"clarification":null}`,
		"bad date":         `{"actions":[{"type":"add","text":"x","date":"friday"}],"clarification":null}`,
		"empty text":       `{"actions":[{"type":"add","text":"  ","date":"2026-11-17"}],"clarification":null}`,
		"no actions":       `{"actions":[],"clarification":null}`,
		"one bad in batch": `{"actions":[{"type":"add","text":"ok","date":"2026-11-17"},{"type":"done","task_id":999}],"clarification":null}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t, raw)
			keep := e.seed(t, "existing", "2026-11-16")
			reply := e.handle(t, "something")
			if reply != "didn't quite get that, can you rephrase?" {
				t.Fatalf("reply = %q", reply)
			}
			if e.openCount(t) != 1 || e.task(t, keep.ID).Status != store.StatusOpen {
				t.Fatal("invalid output changed the list")
			}
		})
	}
}

func TestClarificationRoundTrip(t *testing.T) {
	e := setup(t,
		`{"actions":[],"clarification":"which report: the draft or the slides?"}`,
		`{"actions":[{"type":"add","text":"finish report slides","date":"2026-11-17"}],"clarification":null}`,
	)
	reply := e.handle(t, "add the report thing")
	if reply != "which report: the draft or the slides?" || e.openCount(t) != 0 {
		t.Fatalf("first reply = %q", reply)
	}
	if v, _ := e.st.GetState(store.KeyAwaitingClarification); v == "" {
		t.Fatal("pending clarification not stored")
	}

	reply = e.handle(t, "the slides")
	second := e.llm.users[1]
	for _, want := range []string{"earlier message: add the report thing", "you asked: which report", "my answer: the slides"} {
		if !strings.Contains(second, want) {
			t.Errorf("follow-up prompt missing %q:\n%s", want, second)
		}
	}
	if !strings.Contains(reply, "+ finish report slides (tue 17 nov)") {
		t.Fatalf("second reply = %q", reply)
	}
	if v, _ := e.st.GetState(store.KeyAwaitingClarification); v != "" {
		t.Fatal("pending clarification not cleared")
	}
}

func TestStaleClarificationIgnored(t *testing.T) {
	e := setup(t, `{"actions":[],"clarification":"hm?"}`)
	old, _ := json.Marshal(pending{Original: "old", Question: "q?", AskedAt: now.Add(-13 * time.Hour).UTC().Format(time.RFC3339)})
	if err := e.st.SetState(store.KeyAwaitingClarification, string(old)); err != nil {
		t.Fatal(err)
	}
	e.handle(t, "new message")
	if strings.Contains(e.llm.users[0], "earlier message") {
		t.Fatal("stale clarification should not be folded in")
	}
}

func TestLLMFailureKeepsStateAndSaysSo(t *testing.T) {
	e := setup(t)
	e.llm.err = errors.New("connection refused")
	pd, _ := json.Marshal(pending{Original: "o", Question: "q?", AskedAt: now.UTC().Format(time.RFC3339)})
	e.st.SetState(store.KeyAwaitingClarification, string(pd))

	reply := e.handle(t, "tomorrow: gym")
	if !strings.Contains(reply, "brain's offline") || e.openCount(t) != 0 {
		t.Fatalf("reply = %q", reply)
	}
	if v, _ := e.st.GetState(store.KeyAwaitingClarification); v == "" {
		t.Fatal("pending clarification lost on a failed call")
	}
}

// ---- Claude client against a fake Messages API ----

func fakeAPI(t *testing.T, respond func(w http.ResponseWriter, body map[string]any)) (*httptest.Server, *http.Request) {
	t.Helper()
	var got http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		respond(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func message(stop, text string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": "test-model",
		"content":     []any{map[string]any{"type": "text", "text": text}},
		"stop_reason": stop, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	return string(b)
}

func TestClaudeRequestShape(t *testing.T) {
	var body map[string]any
	srv, req := fakeAPI(t, func(w http.ResponseWriter, b map[string]any) {
		body = b
		io.WriteString(w, message("end_turn", `{"actions":[],"clarification":null}`))
	})
	c := NewClaude("sk-test", "my-configured-model", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	out, err := c.Complete(context.Background(), "sys", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"actions":[],"clarification":null}` {
		t.Fatalf("out = %q", out)
	}
	if req.URL.Path != "/v1/messages" || req.Header.Get("Authorization") != "Bearer sk-test" || req.Header.Get("Anthropic-Version") == "" {
		t.Fatalf("path %s, headers %v", req.URL.Path, req.Header)
	}
	if body["model"] != "my-configured-model" {
		t.Fatalf("model = %v (must come from config)", body["model"])
	}
	oc, _ := body["output_config"].(map[string]any)
	fmtCfg, _ := oc["format"].(map[string]any)
	if oc["effort"] != "low" || fmtCfg["type"] != "json_schema" || fmtCfg["schema"] == nil {
		t.Fatalf("output_config = %v", oc)
	}
	if _, ok := body["thinking"]; ok {
		t.Fatal("thinking should be left to the model default")
	}
}

func TestClaudeRefusalAndCutoff(t *testing.T) {
	for stop, want := range map[string]string{"refusal": "refused", "max_tokens": "max_tokens"} {
		srv, _ := fakeAPI(t, func(w http.ResponseWriter, _ map[string]any) {
			io.WriteString(w, message(stop, `{"actions":[`))
		})
		c := NewClaude("sk-test", "m", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
		if _, err := c.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", stop, err)
		}
	}
}

func TestClaudeHTTPError(t *testing.T) {
	srv, _ := fakeAPI(t, func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	})
	c := NewClaude("bad", "m", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestSchemaIsStrict(t *testing.T) {
	// Structured outputs require additionalProperties:false and full "required" on every object.
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch m := v.(type) {
		case map[string]any:
			if m["type"] == "object" {
				if m["additionalProperties"] != false {
					t.Errorf("%s: additionalProperties must be false", path)
				}
				props, _ := m["properties"].(map[string]any)
				req, _ := m["required"].([]string)
				if len(req) != len(props) {
					t.Errorf("%s: required %v doesn't cover all properties", path, req)
				}
			}
			for k, c := range m {
				walk(path+"."+k, c)
			}
		case []any:
			for i, c := range m {
				walk(fmt.Sprintf("%s[%d]", path, i), c)
			}
		}
	}
	walk("schema", Schema)
}
