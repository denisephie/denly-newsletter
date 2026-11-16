package bot

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"denly/internal/clock"
	"denly/internal/commands"
	"denly/internal/config"
	"denly/internal/format"
	"denly/internal/store"
)

type fakeParser struct {
	got   []string
	reply string
	err   error
}

func (p *fakeParser) Handle(text string) (string, error) {
	p.got = append(p.got, text)
	return p.reply, p.err
}

func newBot(t *testing.T) *Bot {
	t.Helper()
	sg, _ := time.LoadLocation("Asia/Singapore")
	clk := clock.Fixed(sg, time.Date(2026, 11, 16, 10, 0, 0, 0, sg))
	st, err := store.Open(filepath.Join(t.TempDir(), "todo.db"), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := format.New(config.Defaults())
	return &Bot{Cmd: &commands.Handler{St: st, Clk: clk, F: f, CarryOver: true}, F: f}
}

func TestRouting(t *testing.T) {
	b := newBot(t)
	p := &fakeParser{reply: "parsed!"}
	b.Parser = p

	if r, _ := b.HandleInbound("add milk"); !strings.Contains(r, "added: milk") {
		t.Fatalf("command reply = %q", r)
	}
	if len(p.got) != 0 {
		t.Fatal("quick command reached the parser")
	}
	if r, _ := b.HandleInbound("  scrap the report  "); r != "parsed!" {
		t.Fatalf("free text reply = %q", r)
	}
	if len(p.got) != 1 || p.got[0] != "scrap the report" {
		t.Fatalf("parser got %q", p.got)
	}
	if r, _ := b.HandleInbound("   "); !strings.Contains(r, "here's what i understand") {
		t.Fatalf("empty message reply = %q", r)
	}
}

func TestParserErrorPropagates(t *testing.T) {
	b := newBot(t)
	b.Parser = &fakeParser{err: errors.New("api down")}
	if _, err := b.HandleInbound("something vague"); err == nil {
		t.Fatal("want error")
	}
}
