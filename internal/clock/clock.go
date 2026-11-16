// Package clock gives every package one notion of "now" and "today",
// always in the owner's timezone. Tests use Fixed to freeze time.
package clock

import "time"

// DateLayout is the on-disk format for task dates.
const DateLayout = "2006-01-02"

type Clock struct {
	Loc   *time.Location
	nowFn func() time.Time
}

// New returns a real clock in loc.
func New(loc *time.Location) *Clock {
	return &Clock{Loc: loc, nowFn: time.Now}
}

// Fixed returns a clock frozen at t (useful in tests). Use Set to move it.
func Fixed(loc *time.Location, t time.Time) *Clock {
	c := &Clock{Loc: loc}
	c.Set(t)
	return c
}

// Set moves a clock to t (it stays frozen there).
func (c *Clock) Set(t time.Time) { c.nowFn = func() time.Time { return t } }

// Now is the current time in the owner's timezone.
func (c *Clock) Now() time.Time { return c.nowFn().In(c.Loc) }

// Today is today's date (YYYY-MM-DD) in the owner's timezone.
func (c *Clock) Today() string { return c.AddDays(0) }

// Tomorrow is tomorrow's date (YYYY-MM-DD) in the owner's timezone.
func (c *Clock) Tomorrow() string { return c.AddDays(1) }

// AddDays returns today's date shifted by n days (calendar days, DST-safe).
func (c *Clock) AddDays(n int) string {
	now := c.Now()
	return time.Date(now.Year(), now.Month(), now.Day()+n, 12, 0, 0, 0, c.Loc).Format(DateLayout)
}

// ParseDate parses a YYYY-MM-DD date as midnight in the owner's timezone.
func (c *Clock) ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation(DateLayout, s, c.Loc)
}
