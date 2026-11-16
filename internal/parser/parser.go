// Package parser turns free-text messages ("tomorrow: gym, call mum",
// "scrap the report") into validated task actions using Claude.
package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"denly/internal/clock"
	"denly/internal/commands"
	"denly/internal/format"
	"denly/internal/store"
)

const (
	maxActions       = 30
	clarificationTTL = 12 * time.Hour
)

type Action struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Date   string `json:"date,omitempty"`
	TaskID int64  `json:"task_id,omitempty"`
}

type Result struct {
	Actions       []Action `json:"actions"`
	Clarification *string  `json:"clarification"`
}

// pending is stored in state while the bot waits for an answer to its question.
type pending struct {
	Original string `json:"original"`
	Question string `json:"question"`
	AskedAt  string `json:"asked_at"`
}

type Parser struct {
	St          *store.Store
	Clk         *clock.Clock
	Cmd         *commands.Handler
	F           *format.Formatter
	LLM         LLM
	Personality string
	Log         *slog.Logger
}

// Handle implements bot.FreeText.
func (p *Parser) Handle(text string) (string, error) {
	message, hadPending, err := p.withPendingContext(text)
	if err != nil {
		return "", err
	}
	open, err := p.St.AllOpen()
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	raw, err := p.LLM.Complete(ctx, p.systemPrompt(), p.userPrompt(message, open))
	if err != nil {
		p.Log.Error("parser: llm call failed", "err", err)
		return p.F.ThinkingFailed(), nil // pending question (if any) is kept for the retry
	}
	if hadPending {
		if err := p.St.DeleteState(store.KeyAwaitingClarification); err != nil {
			return "", err
		}
	}

	res, err := decode(raw)
	if err != nil {
		p.Log.Warn("parser: invalid json from model", "err", err, "raw", truncate(raw, 300))
		return p.F.Rephrase(), nil
	}

	if q := clarification(res); q != "" {
		if err := p.savePending(pending{Original: message, Question: q, AskedAt: p.Clk.Now().UTC().Format(time.RFC3339)}); err != nil {
			return "", err
		}
		return q, nil
	}
	if len(res.Actions) == 0 {
		return p.F.Rephrase(), nil
	}
	if err := p.validate(res.Actions, open); err != nil {
		p.Log.Warn("parser: rejected actions", "err", err, "raw", truncate(raw, 300))
		return p.F.Rephrase(), nil
	}
	return p.apply(res.Actions)
}

// withPendingContext folds an outstanding clarification into the message.
// hadPending tells the caller to clear it once the model has answered.
func (p *Parser) withPendingContext(text string) (msg string, hadPending bool, err error) {
	raw, err := p.St.GetState(store.KeyAwaitingClarification)
	if err != nil || raw == "" {
		return text, false, err
	}
	var pd pending
	if json.Unmarshal([]byte(raw), &pd) != nil {
		return text, true, nil
	}
	if at, err := time.Parse(time.RFC3339, pd.AskedAt); err != nil || p.Clk.Now().Sub(at) > clarificationTTL {
		return text, true, nil
	}
	return fmt.Sprintf("earlier message: %s\nyou asked: %s\nmy answer: %s", pd.Original, pd.Question, text), true, nil
}

func (p *Parser) savePending(pd pending) error {
	b, err := json.Marshal(pd)
	if err != nil {
		return err
	}
	return p.St.SetState(store.KeyAwaitingClarification, string(b))
}

func decode(raw string) (Result, error) {
	var res Result
	dec := json.NewDecoder(bytes.NewReader([]byte(strings.TrimSpace(raw))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		return Result{}, err
	}
	if dec.More() {
		return Result{}, fmt.Errorf("trailing data after json")
	}
	if res.Actions == nil {
		return Result{}, fmt.Errorf("missing actions")
	}
	return res, nil
}

func clarification(res Result) string {
	if res.Clarification == nil {
		return ""
	}
	return strings.TrimSpace(*res.Clarification)
}

// validate checks every action; any problem rejects the whole batch.
func (p *Parser) validate(actions []Action, open []store.Task) error {
	if len(actions) > maxActions {
		return fmt.Errorf("%d actions (max %d)", len(actions), maxActions)
	}
	isOpen := make(map[int64]bool, len(open))
	for _, t := range open {
		isOpen[t.ID] = true
	}
	today := p.Clk.Today()
	checkDate := func(i int, d string) error {
		if _, err := p.Clk.ParseDate(d); err != nil {
			return fmt.Errorf("action %d: bad date %q", i, d)
		}
		if d < today { // YYYY-MM-DD compares correctly as strings
			return fmt.Errorf("action %d: date %s is in the past", i, d)
		}
		return nil
	}
	checkID := func(i int, id int64) error {
		if !isOpen[id] {
			return fmt.Errorf("action %d: task_id %d is not an open task", i, id)
		}
		return nil
	}
	for i, a := range actions {
		switch a.Type {
		case "add":
			if strings.TrimSpace(a.Text) == "" {
				return fmt.Errorf("action %d: add with empty text", i)
			}
			if err := checkDate(i, a.Date); err != nil {
				return err
			}
		case "done", "remove":
			if err := checkID(i, a.TaskID); err != nil {
				return err
			}
		case "move":
			if err := checkID(i, a.TaskID); err != nil {
				return err
			}
			if err := checkDate(i, a.Date); err != nil {
				return err
			}
		default:
			return fmt.Errorf("action %d: unknown type %q", i, a.Type)
		}
	}
	return nil
}

// apply runs all actions in one transaction, then shows the most relevant list.
func (p *Parser) apply(actions []Action) (string, error) {
	tomorrow := p.Clk.Tomorrow()
	view := format.ViewToday
	var lines []string

	err := p.St.WithTx(func(tx *store.Store) error {
		for _, a := range actions {
			switch a.Type {
			case "add":
				text := strings.TrimSpace(a.Text)
				if r := []rune(text); len(r) > commands.MaxTaskLen {
					text = string(r[:commands.MaxTaskLen])
				}
				if _, err := tx.AddTask(text, a.Date); err != nil {
					return err
				}
				d, _ := p.Clk.ParseDate(a.Date)
				lines = append(lines, p.F.ActionAdded(text, d))
			case "done", "remove":
				t, err := tx.GetTask(a.TaskID)
				if err != nil {
					return err
				}
				if t.Status != store.StatusOpen {
					continue // e.g. the same task listed twice
				}
				status, line := store.StatusDone, p.F.ActionDone(t.Text)
				if a.Type == "remove" {
					status, line = store.StatusRemoved, p.F.ActionRemoved(t.Text)
				}
				if err := tx.SetStatus(a.TaskID, status); err != nil {
					return err
				}
				lines = append(lines, line)
			case "move":
				t, err := tx.GetTask(a.TaskID)
				if err != nil {
					return err
				}
				if err := tx.MoveTask(a.TaskID, a.Date); err != nil {
					return err
				}
				d, _ := p.Clk.ParseDate(a.Date)
				lines = append(lines, p.F.ActionMoved(t.Text, d))
			}
			if (a.Type == "add" || a.Type == "move") && a.Date == tomorrow {
				view = format.ViewTomorrow
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("apply actions: %w", err)
	}

	list, err := p.Cmd.Show(view)
	if err != nil {
		return "", err
	}
	return "got it!\n" + strings.Join(lines, "\n") + "\n\n" + list, nil
}

// systemPrompt is static (no dates or tasks), so it stays cache-friendly.
func (p *Parser) systemPrompt() string {
	tone := p.Personality
	if tone == "" {
		tone = "warm, lowercase"
	}
	return `You manage one person's personal to-do list. They text you on WhatsApp; you turn each message into actions on the list.

Reply with JSON only, matching the schema:
- {"type":"add","text":...,"date":"YYYY-MM-DD"}: a new task. Keep the text short, in their words, without the date words ("finish the report", not "tomorrow finish the report"). A list like "tomorrow: gym, call mum, finish report" is one add per item.
- {"type":"done","task_id":N}: they finished an open task ("i already did the gym").
- {"type":"remove","task_id":N}: they dropped a task ("scrap the report", "cancel gym").
- {"type":"move","task_id":N,"date":"YYYY-MM-DD"}: re-plan a task ("move the passport thing to friday").

Rules:
- task_id must be an id from the open task list you're given. Match loosely by meaning ("passport thing" = "renew passport"). Never invent ids.
- Resolve relative dates with the calendar you're given. A weekday name means its next occurrence that isn't in the past. Never use a date before today.
- If an add has no day at all: before 18:00 use today; from 18:00 on use tomorrow (they're usually answering the evening "what's the plan for tomorrow?" prompt).
- If you're not confident what they mean (which task, which day, or whether it's a to-do at all), return "actions": [] and one short clarifying question in "clarification". Otherwise "clarification" is null.
- If the message isn't about their to-dos at all (e.g. "thanks!"), return "actions": [] and a short friendly reply in "clarification".
- When the message includes "earlier message / you asked / my answer", use all three together; if the answer doesn't relate to the question, treat it as a new message.
- Write any clarification in this tone: ` + tone + `.`
}

func (p *Parser) userPrompt(message string, open []store.Task) string {
	now := p.Clk.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "now: %s %s %s\n", strings.ToLower(now.Format("Monday")), now.Format(clock.DateLayout), now.Format("15:04"))
	b.WriteString("calendar:")
	for i := 0; i <= 7; i++ {
		d, _ := p.Clk.ParseDate(p.Clk.AddDays(i))
		label := strings.ToLower(d.Format("Mon"))
		switch i {
		case 0:
			label = "today"
		case 1:
			label = "tomorrow"
		}
		fmt.Fprintf(&b, " %s=%s", label, d.Format(clock.DateLayout))
	}
	b.WriteString("\n\nopen tasks (id | date | text):\n")
	if len(open) == 0 {
		b.WriteString("(none)\n")
	}
	for _, t := range open {
		fmt.Fprintf(&b, "%d | %s | %s\n", t.ID, t.Date, t.Text)
	}
	b.WriteString("\nmessage:\n")
	b.WriteString(message)
	return b.String()
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
