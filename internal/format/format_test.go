package format

import (
	"strings"
	"testing"
	"time"

	"denly/internal/config"
	"denly/internal/store"
)

var sg, _ = time.LoadLocation("Asia/Singapore")

func tasks(texts ...string) []store.Task {
	var out []store.Task
	for i, t := range texts {
		out = append(out, store.Task{ID: int64(100 + i), Text: t})
	}
	return out
}

func TestMorningMatchesSpecExample(t *testing.T) {
	f := New(config.Defaults()) // no user_name
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, sg)
	carried := []store.Task{{ID: 7, Text: "renew passport"}}
	got, ids := f.Morning(now, tasks("finish report draft", "call mum", "gym"), carried)

	want := `☀️ morning! here's your denly newsletter for today:

1. finish report draft
2. call mum
3. gym

carried over:
4. renew passport

reply "done <task number>" when you finish something! have a good day ahead~`
	if got != want {
		t.Fatalf("morning message mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if len(ids) != 4 || ids[0] != 100 || ids[3] != 7 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestMorningFreeDayAndName(t *testing.T) {
	s := config.Defaults()
	s.UserName = "Depi"
	f := New(s)
	got, ids := f.Morning(time.Date(2026, 11, 16, 9, 0, 0, 0, sg), nil, nil)
	if !strings.Contains(got, "free day, nothing planned!") || !strings.Contains(got, "Depi") {
		t.Fatalf("free day = %q", got)
	}
	if ids != nil {
		t.Fatalf("free day should have no ids, got %v", ids)
	}
}

func TestPersonalize(t *testing.T) {
	s := config.Defaults()
	f := New(s)
	if got := f.personalize("morning {name}! {bot} here"); got != "morning! Denly Newsletter here" {
		t.Fatalf("no name: %q", got)
	}
	s.UserName = "Depi"
	f = New(s)
	if got := f.personalize("morning {name}!"); got != "morning Depi!" {
		t.Fatalf("with name: %q", got)
	}
}

func TestPickRotatesByDay(t *testing.T) {
	list := []string{"a", "b", "c"}
	d1 := time.Date(2026, 1, 1, 9, 0, 0, 0, sg) // yearday 1
	d2 := d1.AddDate(0, 0, 1)
	if Pick(list, d1) != "b" || Pick(list, d2) != "c" || Pick(list, d1) != Pick(list, d1) {
		t.Fatal("pick should be deterministic and rotate daily")
	}
	if Pick(nil, d1) != "" {
		t.Fatal("empty list")
	}
}

func TestNightAndList(t *testing.T) {
	f := New(config.Defaults())
	night := f.Night(time.Date(2026, 11, 16, 21, 0, 0, 0, sg))
	if !strings.HasPrefix(night, "🌙 it's late~ what's the plan for tomorrow?\n") {
		t.Fatalf("night = %q", night)
	}

	d := time.Date(2026, 11, 17, 0, 0, 0, 0, sg)
	got, _ := f.List(ViewTomorrow, d, tasks("gym"), nil)
	if got != "tomorrow (tue 17 nov):\n\n1. gym" {
		t.Fatalf("list = %q", got)
	}
	got, _ = f.List(ViewToday, d, nil, tasks("old thing"))
	if got != "today (tue 17 nov):\n\ncarried over:\n1. old thing" {
		t.Fatalf("carried-only list = %q", got)
	}
	empty, ids := f.List(ViewToday, d, nil, nil)
	if !strings.Contains(empty, "nothing on today's list") || ids != nil {
		t.Fatalf("empty = %q", empty)
	}
}
