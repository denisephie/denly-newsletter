package commands

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"denly/internal/clock"
	"denly/internal/config"
	"denly/internal/format"
	"denly/internal/store"
)

var sg, _ = time.LoadLocation("Asia/Singapore")

// Monday 2026-11-16, 10:00 in Singapore.
var now = time.Date(2026, 11, 16, 10, 0, 0, 0, sg)

func newHandler(t *testing.T) *Handler {
	t.Helper()
	clk := clock.Fixed(sg, now)
	st, err := store.Open(filepath.Join(t.TempDir(), "todo.db"), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Handler{St: st, Clk: clk, F: format.New(config.Defaults()), CarryOver: true}
}

func send(t *testing.T, h *Handler, msg string) string {
	t.Helper()
	reply, handled, err := h.Handle(msg)
	if err != nil {
		t.Fatalf("%q: %v", msg, err)
	}
	if !handled {
		t.Fatalf("%q was not handled as a command", msg)
	}
	return reply
}

func seed(t *testing.T, h *Handler, text, date string) store.Task {
	t.Helper()
	task, err := h.St.AddTask(text, date)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func status(t *testing.T, h *Handler, id int64) string {
	t.Helper()
	task, err := h.St.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	return task.Status
}

// Done-when: "I text `add buy milk` → it appears in `list`."
func TestAddAppearsInList(t *testing.T) {
	h := newHandler(t)
	reply := send(t, h, "add buy milk")
	if !strings.HasPrefix(reply, "added: buy milk ✓") || !strings.Contains(reply, "1. buy milk") {
		t.Fatalf("add reply = %q", reply)
	}
	list := send(t, h, "list")
	if list != "today (mon 16 nov):\n\n1. buy milk" {
		t.Fatalf("list = %q", list)
	}
}

// Done-when: "I text `done 1` → it's marked done and disappears from tomorrow's carry-over."
func TestDoneDisappearsFromTomorrowCarryOver(t *testing.T) {
	h := newHandler(t)
	milk := seed(t, h, "buy milk", "2026-11-16")
	seed(t, h, "call mum", "2026-11-16")

	before := send(t, h, "tomorrow")
	if !strings.Contains(before, "carried over:\n1. buy milk\n2. call mum") {
		t.Fatalf("tomorrow before = %q", before)
	}

	send(t, h, "list")
	reply := send(t, h, "done 1")
	if !strings.HasPrefix(reply, "nice! finished: buy milk ✓") {
		t.Fatalf("done reply = %q", reply)
	}
	if status(t, h, milk.ID) != store.StatusDone {
		t.Fatal("not marked done")
	}
	after := send(t, h, "tomorrow")
	if strings.Contains(after, "buy milk") || !strings.Contains(after, "1. call mum") {
		t.Fatalf("tomorrow after = %q", after)
	}
}

func TestNumbersFollowLastShownList(t *testing.T) {
	h := newHandler(t)
	seed(t, h, "today thing", "2026-11-16")
	gym := seed(t, h, "gym", "2026-11-17")

	send(t, h, "tomorrow") // 1 = gym (tomorrow), 2 = today thing (carried)
	send(t, h, "remove 1")
	if status(t, h, gym.ID) != store.StatusRemoved {
		t.Fatal("remove 1 should refer to tomorrow's list as last shown")
	}
}

func TestCarriedOverNumberingContinues(t *testing.T) {
	h := newHandler(t)
	seed(t, h, "renew passport", "2026-11-12")
	seed(t, h, "finish report", "2026-11-16")
	seed(t, h, "call mum", "2026-11-16")

	list := send(t, h, "list")
	want := "today (mon 16 nov):\n\n1. finish report\n2. call mum\n\ncarried over:\n3. renew passport"
	if list != want {
		t.Fatalf("list =\n%s\nwant\n%s", list, want)
	}
}

func TestMultipleAndAllClear(t *testing.T) {
	h := newHandler(t)
	a := seed(t, h, "a", "2026-11-16")
	b := seed(t, h, "b", "2026-11-16")
	send(t, h, "list")
	reply := send(t, h, "done 1, 2")
	if status(t, h, a.ID) != store.StatusDone || status(t, h, b.ID) != store.StatusDone {
		t.Fatal("both should be done")
	}
	if !strings.Contains(reply, "finished: a, b") || !strings.Contains(reply, "all clear!") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestBadNumberChangesNothing(t *testing.T) {
	h := newHandler(t)
	a := seed(t, h, "a", "2026-11-16")
	send(t, h, "list")
	reply := send(t, h, "done 1 5") // 5 is invalid -> nothing changes, not even 1
	if !strings.Contains(reply, "no task 5") || !strings.Contains(reply, "1. a") {
		t.Fatalf("reply = %q", reply)
	}
	if status(t, h, a.ID) != store.StatusOpen {
		t.Fatal("task 1 changed despite invalid number in same command")
	}
	reply = send(t, h, "remove 0")
	if !strings.Contains(reply, "no task 0") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestNoListShownYet(t *testing.T) {
	h := newHandler(t)
	a := seed(t, h, "a", "2026-11-16")
	reply := send(t, h, "done 1")
	if !strings.Contains(reply, "same list first") || status(t, h, a.ID) != store.StatusOpen {
		t.Fatalf("reply = %q", reply)
	}
	send(t, h, "done 1") // the list was shown by the previous reply
	if status(t, h, a.ID) != store.StatusDone {
		t.Fatal("second try should work")
	}
}

func TestStaleNumberAlreadyDone(t *testing.T) {
	h := newHandler(t)
	a := seed(t, h, "a", "2026-11-16")
	b := seed(t, h, "b", "2026-11-16")
	if err := h.SaveView(View{Kind: format.ViewToday, Date: "2026-11-16", IDs: []int64{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := h.St.SetStatus(a.ID, store.StatusDone); err != nil {
		t.Fatal(err)
	}
	reply := send(t, h, "done 1 2")
	if !strings.Contains(reply, "finished: b") || !strings.Contains(reply, "already sorted: a") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestNotCommandsGoToParser(t *testing.T) {
	h := newHandler(t)
	for _, msg := range []string{
		"tomorrow: gym, call mum, finish report",
		"done gym",
		"remove the report thing",
		"list for friday",
		"i already did the gym today",
		"",
	} {
		if _, handled, err := h.Handle(msg); handled || err != nil {
			t.Errorf("%q: handled=%v err=%v, want parser", msg, handled, err)
		}
	}
}

func TestCaseAndSpacingAndHelp(t *testing.T) {
	h := newHandler(t)
	send(t, h, "  ADD   Buy Milk  ")
	if got := send(t, h, "List"); !strings.Contains(got, "1. Buy Milk") {
		t.Fatalf("list = %q", got)
	}
	send(t, h, "Done #1")
	if got := send(t, h, "help"); !strings.Contains(got, "done <n>") {
		t.Fatalf("help = %q", got)
	}
	if got := send(t, h, "add"); !strings.Contains(got, "add what?") {
		t.Fatalf("empty add = %q", got)
	}
}

func TestCarryOverDisabled(t *testing.T) {
	h := newHandler(t)
	h.CarryOver = false
	seed(t, h, "old", "2026-11-10")
	if got := send(t, h, "list"); strings.Contains(got, "old") {
		t.Fatalf("carry_over=false still shows old task: %q", got)
	}
}

func TestMorningSavesNumbering(t *testing.T) {
	h := newHandler(t)
	seed(t, h, "renew passport", "2026-11-14")
	report := seed(t, h, "finish report", "2026-11-16")
	msg, err := h.Morning()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "1. finish report\n\ncarried over:\n2. renew passport") {
		t.Fatalf("morning = %q", msg)
	}
	send(t, h, "done 1")
	if status(t, h, report.ID) != store.StatusDone {
		t.Fatal("done 1 after the morning message should hit its first task")
	}
}
