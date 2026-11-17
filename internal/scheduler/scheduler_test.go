package scheduler

import (
	"errors"
	"io"
	"log/slog"
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

var sgt = time.FixedZone("SGT", 8*3600)

type env struct {
	s    *Scheduler
	clk  *clock.Clock
	sent []string
	fail error
}

// newEnv starts at Mon 2026-11-16 08:00 SGT with default settings.
func newEnv(t *testing.T, mut func(*config.Settings)) *env {
	t.Helper()
	set := config.Defaults()
	set.RecapTime = "20:30"
	if mut != nil {
		mut(&set)
	}
	clk := clock.Fixed(sgt, time.Date(2026, 11, 16, 8, 0, 0, 0, sgt))
	st, err := store.Open(filepath.Join(t.TempDir(), "todo.db"), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := format.New(set)
	e := &env{clk: clk}
	e.s = &Scheduler{
		Cfg: &config.Config{Settings: set, Loc: sgt},
		Clk: clk, St: st, F: f,
		Cmd: &commands.Handler{St: st, Clk: clk, F: f, CarryOver: true},
		Send: func(b string) error {
			if e.fail != nil {
				return e.fail
			}
			e.sent = append(e.sent, b)
			return nil
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return e
}

func (e *env) at(day, hhmm string) {
	t, _ := time.ParseInLocation("2006-01-02 15:04", day+" "+hhmm, sgt)
	e.clk.Set(t)
}

func TestMorningSendsOnceAtTime(t *testing.T) {
	e := newEnv(t, nil)
	e.s.St.AddTask("finish report", "2026-11-16")

	e.s.Tick() // 08:00, too early
	if len(e.sent) != 0 {
		t.Fatalf("sent early: %v", e.sent)
	}
	e.at("2026-11-16", "09:00")
	e.s.Tick()
	e.s.Tick() // second tick must not repeat
	if len(e.sent) != 1 || !strings.Contains(e.sent[0], "1. finish report") {
		t.Fatalf("sent = %q", e.sent)
	}
}

func TestRestartDoesNotDoubleSend(t *testing.T) {
	e := newEnv(t, nil)
	e.at("2026-11-16", "09:05")
	e.s.Tick()
	// "restart": a fresh Scheduler over the same store
	s2 := *e.s
	s2.Tick()
	if len(e.sent) != 1 {
		t.Fatalf("sent = %d", len(e.sent))
	}
}

func TestCatchUpWindow(t *testing.T) {
	e := newEnv(t, nil)
	e.at("2026-11-16", "10:30") // 1.5h late: still sends
	e.s.Tick()
	if len(e.sent) != 1 {
		t.Fatalf("late morning not sent: %v", e.sent)
	}
	e2 := newEnv(t, nil)
	e2.at("2026-11-16", "15:00") // 6h late: skipped
	e2.s.Tick()
	if len(e2.sent) != 0 {
		t.Fatalf("stale morning sent: %v", e2.sent)
	}
}

func TestSendFailureRetries(t *testing.T) {
	e := newEnv(t, nil)
	e.at("2026-11-16", "09:00")
	e.fail = errors.New("network down")
	e.s.Tick()
	if len(e.sent) != 0 {
		t.Fatal("should not have sent")
	}
	e.fail = nil
	e.s.Tick()
	if len(e.sent) != 1 {
		t.Fatalf("retry did not send: %v", e.sent)
	}
}

func TestNightPrompt(t *testing.T) {
	e := newEnv(t, nil)
	e.at("2026-11-16", "21:00")
	e.s.Tick()
	if len(e.sent) != 1 || !strings.Contains(e.sent[0], "plan for tomorrow") {
		t.Fatalf("sent = %q", e.sent)
	}
}

func TestQuietDate(t *testing.T) {
	e := newEnv(t, func(s *config.Settings) { s.QuietDates = []string{"2026-11-16"} })
	e.at("2026-11-16", "09:00")
	e.s.Tick()
	e.at("2026-11-16", "21:00")
	e.s.Tick()
	if len(e.sent) != 0 {
		t.Fatalf("quiet date sent: %v", e.sent)
	}
}

func TestWeekendRules(t *testing.T) {
	// 2026-11-21 is a Saturday.
	skip := newEnv(t, func(s *config.Settings) { s.Weekends = "skip_morning" })
	skip.at("2026-11-21", "09:00")
	skip.s.Tick()
	if len(skip.sent) != 0 {
		t.Fatal("skip_morning still sent")
	}

	later := newEnv(t, func(s *config.Settings) { s.Weekends = "later_morning" })
	later.at("2026-11-21", "09:30")
	later.s.Tick()
	if len(later.sent) != 0 {
		t.Fatal("later_morning sent before 10:00")
	}
	later.at("2026-11-21", "10:00")
	later.s.Tick()
	if len(later.sent) != 1 {
		t.Fatal("later_morning did not send at 10:00")
	}
}

func TestRecapOnlySundays(t *testing.T) {
	e := newEnv(t, nil)
	e.at("2026-11-16", "20:30") // Monday
	e.s.Tick()
	if len(e.sent) != 0 {
		t.Fatalf("recap on a Monday: %v", e.sent)
	}

	e.at("2026-11-15", "20:30") // Sunday
	tk, _ := e.s.St.AddTask("gym", "2026-11-15")
	e.s.St.SetStatus(tk.ID, store.StatusDone)
	e.s.Tick()
	if len(e.sent) != 1 || !strings.Contains(e.sent[0], "gym") {
		t.Fatalf("sent = %q", e.sent)
	}
}

func TestRecurringAddedOnceAndOnlyOnMatchingDays(t *testing.T) {
	e := newEnv(t, func(s *config.Settings) {
		s.Recurring = []config.Recurring{
			{Text: "gym", Days: []string{"mon", "wed"}},
			{Text: "laundry", Days: []string{"sat"}},
		}
	})
	e.at("2026-11-16", "09:00") // Monday
	e.s.Tick()
	if err := e.s.Trigger(Morning, true); err != nil { // forced second run
		t.Fatal(err)
	}
	open, _ := e.s.St.OpenOn("2026-11-16")
	if len(open) != 1 || open[0].Text != "gym" {
		t.Fatalf("open = %+v", open)
	}
}

func TestNightAddsTomorrowsRecurring(t *testing.T) {
	e := newEnv(t, func(s *config.Settings) {
		s.Recurring = []config.Recurring{{Text: "gym", Days: []string{"tue"}}}
	})
	e.at("2026-11-16", "21:00") // Monday night
	e.s.Tick()
	open, _ := e.s.St.OpenOn("2026-11-17")
	if len(open) != 1 || open[0].Text != "gym" {
		t.Fatalf("tomorrow = %+v", open)
	}
}

func TestTriggerRefusesRepeatWithoutForce(t *testing.T) {
	e := newEnv(t, nil)
	e.at("2026-11-16", "09:00")
	if err := e.s.Trigger(Morning, false); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Trigger(Morning, false); err == nil {
		t.Fatal("repeat without --force should fail")
	}
	if err := e.s.Trigger(Morning, true); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 2 {
		t.Fatalf("sent = %d", len(e.sent))
	}
}
