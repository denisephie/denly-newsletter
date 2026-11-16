package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "todo.db") // sub/ checks dir creation
	s, err := Open(path, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// must unwraps (value, error); a panic fails the test with the error.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestTaskLifecycle(t *testing.T) {
	s, _ := openTemp(t)
	a := must(s.AddTask("finish report", "2026-11-16"))
	b := must(s.AddTask("call mum", "2026-11-16"))
	old := must(s.AddTask("renew passport", "2026-11-14"))
	must(s.AddTask("gym", "2026-11-17"))

	today := must(s.OpenOn("2026-11-16"))
	if len(today) != 2 || today[0].ID != a.ID || today[1].ID != b.ID {
		t.Fatalf("OpenOn = %+v", today)
	}
	carried := must(s.OpenBefore("2026-11-16"))
	if len(carried) != 1 || carried[0].ID != old.ID {
		t.Fatalf("OpenBefore = %+v", carried)
	}

	if err := s.SetStatus(a.ID, StatusDone); err != nil {
		t.Fatal(err)
	}
	got := must(s.GetTask(a.ID))
	if got.Status != StatusDone || got.CompletedAt != "2026-11-16T01:00:00Z" {
		t.Fatalf("done task = %+v", got)
	}
	if err := s.SetStatus(b.ID, StatusRemoved); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveTask(old.ID, "2026-11-20"); err != nil {
		t.Fatal(err)
	}
	if n := len(must(s.OpenOn("2026-11-16"))); n != 0 {
		t.Fatalf("expected empty today, got %d", n)
	}
	if n := len(must(s.OpenBefore("2026-11-16"))); n != 0 {
		t.Fatalf("moved task still carried over")
	}
	if n := len(must(s.AllOpen())); n != 2 {
		t.Fatalf("AllOpen = %d, want 2", n)
	}

	done := must(s.DoneBetween(t0.Add(-time.Hour), t0.Add(time.Hour)))
	if len(done) != 1 || done[0].ID != a.ID {
		t.Fatalf("DoneBetween = %+v", done)
	}

	if ok := must(s.ExistsOn("gym", "2026-11-17")); !ok {
		t.Fatal("ExistsOn gym")
	}
	if ok := must(s.ExistsOn("call mum", "2026-11-16")); ok {
		t.Fatal("removed task should not count for ExistsOn")
	}
}

func TestNotFound(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.GetTask(99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask: %v", err)
	}
	if err := s.SetStatus(99, StatusDone); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := s.MoveTask(99, "2026-11-17"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MoveTask: %v", err)
	}
}

func TestStateAndInbound(t *testing.T) {
	s, _ := openTemp(t)
	if v := must(s.GetState("x")); v != "" {
		t.Fatalf("unset state = %q", v)
	}
	if err := s.SetState("x", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetState("x", "2"); err != nil {
		t.Fatal(err)
	}
	if v := must(s.GetState("x")); v != "2" {
		t.Fatalf("state = %q", v)
	}
	if err := s.DeleteState("x"); err != nil {
		t.Fatal(err)
	}
	if v := must(s.GetState("x")); v != "" {
		t.Fatalf("deleted state = %q", v)
	}

	if at := must(s.LastInboundAt()); !at.IsZero() {
		t.Fatalf("never-messaged should be zero, got %v", at)
	}
	if err := s.TouchInbound(); err != nil {
		t.Fatal(err)
	}
	if at := must(s.LastInboundAt()); !at.Equal(t0) {
		t.Fatalf("LastInboundAt = %v", at)
	}
}

func TestTxRollback(t *testing.T) {
	s, _ := openTemp(t)
	boom := errors.New("boom")
	err := s.WithTx(func(tx *Store) error {
		if _, err := tx.AddTask("a", "2026-11-16"); err != nil {
			return err
		}
		if err := tx.SetState("k", "v"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := len(must(s.AllOpen())); n != 0 {
		t.Fatal("task survived rollback")
	}
	if v := must(s.GetState("k")); v != "" {
		t.Fatal("state survived rollback")
	}

	if err := s.WithTx(func(tx *Store) error {
		_, err := tx.AddTask("b", "2026-11-16")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(must(s.AllOpen())); n != 1 {
		t.Fatal("commit lost")
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	s, path := openTemp(t)
	must(s.AddTask("survive restart", "2026-11-16"))
	if err := s.SetState(KeyLastMorningSent, "2026-11-16"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path, nil) // migrations must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if n := len(must(s2.OpenOn("2026-11-16"))); n != 1 {
		t.Fatal("task lost on reopen")
	}
	if v := must(s2.GetState(KeyLastMorningSent)); v != "2026-11-16" {
		t.Fatal("state lost on reopen")
	}
}
