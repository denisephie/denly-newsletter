// Package commands implements the instant, no-LLM commands
// (list, tomorrow, add, done, remove, help) and owns the display-number map.
package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"denly/internal/clock"
	"denly/internal/format"
	"denly/internal/store"
)

// MaxTaskLen caps task text so one message can't blow up the list.
const MaxTaskLen = 300

// View is the last numbered list shown to the owner; "done 2" resolves through it.
type View struct {
	Kind string  `json:"view"` // format.ViewToday or format.ViewTomorrow
	Date string  `json:"date"`
	IDs  []int64 `json:"ids"`
}

type Handler struct {
	St        *store.Store
	Clk       *clock.Clock
	F         *format.Formatter
	CarryOver bool
}

// Handle runs msg if it's a quick command. handled=false means it isn't one
// and should go to the LLM parser (e.g. "done gym", "tomorrow: gym, call mum").
func (h *Handler) Handle(msg string) (reply string, handled bool, err error) {
	msg = strings.TrimSpace(msg)
	word, arg, _ := strings.Cut(msg, " ")
	arg = strings.TrimSpace(arg)

	switch strings.ToLower(word) {
	case "list", "today":
		if arg == "" {
			reply, err = h.Show(format.ViewToday)
			return reply, true, err
		}
	case "tomorrow":
		if arg == "" {
			reply, err = h.Show(format.ViewTomorrow)
			return reply, true, err
		}
	case "help", "commands", "?":
		if arg == "" {
			return h.F.Help(), true, nil
		}
	case "add":
		reply, err = h.add(arg)
		return reply, true, err
	case "done", "finished":
		if nums, ok := parseNums(arg); ok {
			reply, err = h.mutate(nums, store.StatusDone)
			return reply, true, err
		}
	case "remove", "delete":
		if nums, ok := parseNums(arg); ok {
			reply, err = h.mutate(nums, store.StatusRemoved)
			return reply, true, err
		}
	}
	return "", false, nil
}

// Show renders today's or tomorrow's list and remembers its numbering.
func (h *Handler) Show(kind string) (string, error) {
	date := h.Clk.Today()
	if kind == format.ViewTomorrow {
		date = h.Clk.Tomorrow()
	}
	tasks, carried, err := h.listFor(date)
	if err != nil {
		return "", err
	}
	d, _ := h.Clk.ParseDate(date)
	text, ids := h.F.List(kind, d, tasks, carried)
	return text, h.SaveView(View{Kind: kind, Date: date, IDs: ids})
}

// Morning builds today's morning message and remembers its numbering.
func (h *Handler) Morning() (string, error) {
	date := h.Clk.Today()
	tasks, carried, err := h.listFor(date)
	if err != nil {
		return "", err
	}
	text, ids := h.F.Morning(h.Clk.Now(), tasks, carried)
	return text, h.SaveView(View{Kind: format.ViewToday, Date: date, IDs: ids})
}

// listFor returns open tasks on date plus (if enabled) open tasks from earlier days.
func (h *Handler) listFor(date string) (tasks, carried []store.Task, err error) {
	if tasks, err = h.St.OpenOn(date); err != nil {
		return nil, nil, err
	}
	if h.CarryOver {
		if carried, err = h.St.OpenBefore(date); err != nil {
			return nil, nil, err
		}
	}
	return tasks, carried, nil
}

func (h *Handler) add(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return h.F.AddWhat(), nil
	}
	if r := []rune(text); len(r) > MaxTaskLen {
		text = string(r[:MaxTaskLen])
	}
	if _, err := h.St.AddTask(text, h.Clk.Today()); err != nil {
		return "", err
	}
	list, err := h.Show(format.ViewToday)
	return h.F.Added(text) + "\n\n" + list, err
}

// mutate marks the tasks behind display numbers done/removed, all or nothing.
func (h *Handler) mutate(nums []int, status string) (string, error) {
	view, ok, err := h.LoadView()
	if err != nil {
		return "", err
	}
	if !ok {
		list, err := h.Show(format.ViewToday)
		return h.F.NoListYet() + "\n\n" + list, err
	}
	ids := make([]int64, 0, len(nums))
	for _, n := range nums {
		if n < 1 || n > len(view.IDs) {
			list, err := h.Show(view.Kind)
			return h.F.BadNumber(n) + "\n\n" + list, err
		}
		ids = append(ids, view.IDs[n-1])
	}

	var changed, skipped []string
	err = h.St.WithTx(func(tx *store.Store) error {
		for _, id := range ids {
			t, err := tx.GetTask(id)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if t.Status != store.StatusOpen {
				skipped = append(skipped, t.Text)
				continue
			}
			if err := tx.SetStatus(id, status); err != nil {
				return err
			}
			changed = append(changed, t.Text)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("update tasks: %w", err)
	}

	var parts []string
	if len(changed) > 0 {
		if status == store.StatusDone {
			parts = append(parts, h.F.Done(changed))
		} else {
			parts = append(parts, h.F.Removed(changed))
		}
	}
	if len(skipped) > 0 {
		parts = append(parts, h.F.NotOpen(skipped))
	}

	list, err := h.Show(view.Kind)
	if err != nil {
		return "", err
	}
	if v, _, _ := h.LoadView(); len(v.IDs) == 0 && view.Kind == format.ViewToday && status == store.StatusDone {
		list = h.F.AllClear()
	}
	return strings.TrimSpace(strings.Join(parts, " ") + "\n\n" + list), nil
}

// parseNums accepts "2", "1 3", "1, 3", "1,3". Any non-number means "not a
// quick command" (so "done gym" goes to the parser). Duplicates are dropped.
func parseNums(arg string) ([]int, bool) {
	fields := strings.FieldsFunc(arg, func(r rune) bool { return r == ',' || r == ' ' })
	if len(fields) == 0 {
		return nil, false
	}
	var nums []int
	for _, f := range fields {
		n, err := strconv.Atoi(strings.TrimPrefix(f, "#"))
		if err != nil {
			return nil, false
		}
		if !slices.Contains(nums, n) {
			nums = append(nums, n)
		}
	}
	return nums, true
}

func (h *Handler) SaveView(v View) error {
	if v.IDs == nil {
		v.IDs = []int64{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return h.St.SetState(store.KeyDisplayMap, string(b))
}

// LoadView returns the last shown list; ok=false if nothing was shown yet.
func (h *Handler) LoadView() (View, bool, error) {
	raw, err := h.St.GetState(store.KeyDisplayMap)
	if err != nil || raw == "" {
		return View{}, false, err
	}
	var v View
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return View{}, false, nil // corrupt map: behave as if none was shown
	}
	return v, true, nil
}
