// Package format builds every user-facing message. All wording lives here
// and reads from config (tone, emojis, greetings, nudges, names).
package format

import (
	"fmt"
	"strings"
	"time"

	"denly/internal/config"
	"denly/internal/store"
)

// View kinds: which list a set of display numbers belongs to.
const (
	ViewToday    = "today"
	ViewTomorrow = "tomorrow"
)

type Formatter struct {
	s config.Settings
}

func New(s config.Settings) *Formatter { return &Formatter{s: s} }

// Pick chooses one line from list, rotating by day of year so the same day
// always gets the same line (stable for tests, varied day to day).
func Pick(list []string, now time.Time) string {
	if len(list) == 0 {
		return ""
	}
	return list[now.YearDay()%len(list)]
}

// personalize fills {name} and {bot}. With no user_name, "{name}" vanishes
// along with the space before it ("morning {name}!" -> "morning!").
func (f *Formatter) personalize(s string) string {
	if f.s.UserName == "" {
		s = strings.ReplaceAll(s, " {name}", "")
		s = strings.ReplaceAll(s, "{name}", "")
	} else {
		s = strings.ReplaceAll(s, "{name}", f.s.UserName)
	}
	return strings.ReplaceAll(s, "{bot}", f.s.BotName)
}

func withEmoji(emoji, s string) string {
	if emoji == "" {
		return s
	}
	return emoji + " " + s
}

// DayLabel renders a date like "mon 16 nov".
func DayLabel(t time.Time) string {
	return strings.ToLower(t.Format("Mon 2 Jan"))
}

// TaskLines numbers tasks then carried tasks (numbering continues), and
// returns the task ids in display order (index 0 = number 1).
func TaskLines(tasks, carried []store.Task) (string, []int64) {
	var b strings.Builder
	ids := make([]int64, 0, len(tasks)+len(carried))
	n := 0
	line := func(t store.Task) {
		n++
		ids = append(ids, t.ID)
		fmt.Fprintf(&b, "%d. %s\n", n, t.Text)
	}
	for _, t := range tasks {
		line(t)
	}
	if len(carried) > 0 {
		if len(tasks) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("carried over:\n")
		for _, t := range carried {
			line(t)
		}
	}
	return strings.TrimRight(b.String(), "\n"), ids
}

// Morning is the daily list message. Returns ids for the display map
// (empty when it's a free day).
func (f *Formatter) Morning(now time.Time, tasks, carried []store.Task) (string, []int64) {
	if len(tasks)+len(carried) == 0 {
		return withEmoji(f.s.Emojis.Morning, f.personalize("morning {name}! free day, nothing planned! enjoy~")), nil
	}
	lines, ids := TaskLines(tasks, carried)
	greeting := withEmoji(f.s.Emojis.Morning, f.personalize(Pick(f.s.MorningGreetings, now)))
	msg := greeting + "\n\n" + lines
	if nudge := f.personalize(Pick(f.s.Nudges, now)); nudge != "" {
		msg += "\n\n" + nudge
	}
	return msg, ids
}

// Night is the evening planning prompt.
func (f *Formatter) Night(now time.Time) string {
	return withEmoji(f.s.Emojis.Night, f.personalize(Pick(f.s.NightPrompts, now)))
}

// List renders the `list` / `tomorrow` view for date.
func (f *Formatter) List(view string, date time.Time, tasks, carried []store.Task) (string, []int64) {
	if len(tasks)+len(carried) == 0 {
		if view == ViewTomorrow {
			return fmt.Sprintf("nothing planned for tomorrow (%s) yet~ tell me what's coming up!", DayLabel(date)), nil
		}
		return fmt.Sprintf("nothing on today's list (%s)! add something with \"add <task>\"", DayLabel(date)), nil
	}
	title := "today"
	if view == ViewTomorrow {
		title = "tomorrow"
	}
	lines, ids := TaskLines(tasks, carried)
	return fmt.Sprintf("%s (%s):\n\n%s", title, DayLabel(date), lines), ids
}

// Help is the short command reference.
func (f *Formatter) Help() string {
	return `here's what i understand~

list: today's list
tomorrow: tomorrow's list
add <task>: add to today
done <n>: mark task n done
remove <n>: delete task n

or just tell me in plain words, like
"tomorrow: gym, call mum, finish report"`
}

// ---- command replies ----

func (f *Formatter) Added(text string) string { return "added: " + text + " ✓" }

func (f *Formatter) AddWhat() string { return `add what? try "add buy milk"` }

func (f *Formatter) Done(texts []string) string {
	return "nice! finished: " + strings.Join(texts, ", ") + " ✓"
}

func (f *Formatter) Removed(texts []string) string {
	return "removed: " + strings.Join(texts, ", ")
}

// NotOpen is appended when a number points at a task that's already done/removed.
func (f *Formatter) NotOpen(texts []string) string {
	return "(already sorted: " + strings.Join(texts, ", ") + ")"
}

func (f *Formatter) BadNumber(n int) string {
	return fmt.Sprintf("hmm, there's no task %d on the list you last saw. here it is again:", n)
}

func (f *Formatter) NoListYet() string {
	return "let's look at the same list first, then try again with the number~"
}

func (f *Formatter) AllClear() string { return "all clear! (ﾉ◕ヮ◕)ﾉ*:・ﾟ✧" }

// ---- bot / webhook replies ----

func (f *Formatter) TextOnly() string { return "text only for now~ (no voice notes or pictures yet)" }

func (f *Formatter) Oops() string {
	return "oops, something went wrong on my side (╥﹏╥) try again in a bit?"
}

// NoParser is used when free text arrives but no Claude parser is configured.
func (f *Formatter) NoParser() string {
	return `i can't understand free text right now, but these work: list, tomorrow, add <task>, done <n>, remove <n>, help`
}

// ---- parser replies ----

func (f *Formatter) Rephrase() string { return "didn't quite get that, can you rephrase?" }

// ThinkingFailed is used when Claude can't be reached.
func (f *Formatter) ThinkingFailed() string {
	return "my brain's offline for a sec (╥﹏╥) quick commands still work: list, add <task>, done <n>"
}

func (f *Formatter) ActionAdded(text string, date time.Time) string {
	return fmt.Sprintf("+ %s (%s)", text, DayLabel(date))
}
func (f *Formatter) ActionDone(text string) string    { return "✓ " + text }
func (f *Formatter) ActionRemoved(text string) string { return "✗ " + text }
func (f *Formatter) ActionMoved(text string, date time.Time) string {
	return fmt.Sprintf("→ %s moved to %s", text, DayLabel(date))
}
