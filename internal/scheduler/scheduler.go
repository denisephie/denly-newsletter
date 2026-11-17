// Package scheduler sends the morning list, the night prompt and the Sunday
// recap at the owner's local times. It is restart-safe: each job records the
// date it last sent in the store, so a restart never double-sends.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"denly/internal/clock"
	"denly/internal/commands"
	"denly/internal/config"
	"denly/internal/format"
	"denly/internal/store"
)

const (
	Morning = "morning"
	Night   = "night"
	Recap   = "recap"

	// A job still runs if the process was down at its time, but only this long
	// afterwards, so a restart at 3am doesn't send "what's the plan for tomorrow?".
	catchUp  = 2 * time.Hour
	interval = 30 * time.Second
)

type Scheduler struct {
	Cfg  *config.Config
	Clk  *clock.Clock
	St   *store.Store
	Cmd  *commands.Handler
	F    *format.Formatter
	Send func(body string) error
	Log  *slog.Logger
}

// Run checks for due jobs every 30 seconds until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.Tick()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs every job that is due right now.
func (s *Scheduler) Tick() {
	for _, job := range []string{Morning, Night, Recap} {
		due, err := s.due(job)
		if err != nil {
			s.Log.Error("scheduler: check job", "job", job, "err", err)
			continue
		}
		if !due {
			continue
		}
		if err := s.run(job); err != nil {
			s.Log.Error("scheduler: job failed, will retry", "job", job, "err", err)
		}
	}
}

// Trigger runs a job immediately. Without force it refuses to repeat a job
// that already went out today.
func (s *Scheduler) Trigger(job string, force bool) error {
	if !force {
		last, err := s.St.GetState(lastKey(job))
		if err != nil {
			return err
		}
		if last == s.Clk.Today() {
			return fmt.Errorf("%s already sent today (use --force to send again)", job)
		}
	}
	return s.run(job)
}

func lastKey(job string) string {
	switch job {
	case Morning:
		return store.KeyLastMorningSent
	case Night:
		return store.KeyLastNightSent
	default:
		return store.KeyLastRecapSent
	}
}

// at returns the job's scheduled minute-of-day, and whether it runs today at all.
func (s *Scheduler) at(job string, now time.Time) (int, bool) {
	st := s.Cfg.Settings
	weekend := now.Weekday() == time.Saturday || now.Weekday() == time.Sunday
	var hhmm string
	switch job {
	case Morning:
		hhmm = st.MorningTime
		if weekend {
			switch st.Weekends {
			case "skip_morning":
				return 0, false
			case "later_morning":
				hhmm = st.WeekendMorningTime
			}
		}
	case Night:
		hhmm = st.NightTime
	case Recap:
		if now.Weekday() != time.Sunday {
			return 0, false
		}
		hhmm = st.RecapTime
	default:
		return 0, false
	}
	m, err := config.ParseHHMM(hhmm)
	return m, err == nil
}

func (s *Scheduler) due(job string) (bool, error) {
	now := s.Clk.Now()
	today := s.Clk.Today()
	if s.Cfg.IsQuietDate(today) {
		return false, nil
	}
	sched, ok := s.at(job, now)
	if !ok {
		return false, nil
	}
	since := time.Duration(now.Hour()*60+now.Minute()-sched) * time.Minute
	if since < 0 || since > catchUp {
		return false, nil
	}
	last, err := s.St.GetState(lastKey(job))
	if err != nil {
		return false, err
	}
	return last != today, nil
}

func (s *Scheduler) run(job string) error {
	var body string
	var err error
	switch job {
	case Morning:
		if err = s.ensureRecurring(s.Clk.Today()); err == nil {
			body, err = s.Cmd.Morning()
		}
	case Night:
		if err = s.ensureRecurring(s.Clk.Tomorrow()); err == nil {
			body = s.F.Night(s.Clk.Now())
		}
	case Recap:
		body, err = s.recap()
	default:
		return fmt.Errorf("unknown job %q", job)
	}
	if err != nil {
		return err
	}
	if err := s.Send(body); err != nil {
		return err
	}
	s.Log.Info("scheduler: sent", "job", job)
	return s.St.SetState(lastKey(job), s.Clk.Today())
}

// ensureRecurring adds the configured recurring tasks that fall on date,
// skipping any that already exist there (so it's safe to call repeatedly).
func (s *Scheduler) ensureRecurring(date string) error {
	d, err := s.Clk.ParseDate(date)
	if err != nil {
		return err
	}
	for _, r := range s.Cfg.Settings.Recurring {
		if !onDay(r.Days, d.Weekday()) {
			continue
		}
		text := strings.TrimSpace(r.Text)
		exists, err := s.St.ExistsOn(text, date)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := s.St.AddTask(text, date); err != nil {
				return err
			}
		}
	}
	return nil
}

func onDay(days []string, wd time.Weekday) bool {
	for _, d := range days {
		if w, ok := config.Weekdays[strings.ToLower(d)]; ok && w == wd {
			return true
		}
	}
	return false
}

func (s *Scheduler) recap() (string, error) {
	now := s.Clk.Now()
	done, err := s.St.DoneBetween(now.AddDate(0, 0, -7), now.Add(time.Minute))
	if err != nil {
		return "", err
	}
	open, err := s.St.AllOpen()
	if err != nil {
		return "", err
	}
	return s.F.Recap(done, len(open)), nil
}
