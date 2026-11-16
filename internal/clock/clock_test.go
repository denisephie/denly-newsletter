package clock

import (
	"testing"
	"time"
)

func TestTodayUsesOwnerTimezone(t *testing.T) {
	sg, _ := time.LoadLocation("Asia/Singapore")
	// 2026-11-16 17:30 UTC == 2026-11-17 01:30 in Singapore.
	c := Fixed(sg, time.Date(2026, 11, 16, 17, 30, 0, 0, time.UTC))
	if got := c.Today(); got != "2026-11-17" {
		t.Fatalf("Today = %s, want 2026-11-17", got)
	}
	if got := c.Tomorrow(); got != "2026-11-18" {
		t.Fatalf("Tomorrow = %s, want 2026-11-18", got)
	}
	if got := c.AddDays(-17); got != "2026-10-31" {
		t.Fatalf("AddDays(-17) = %s, want 2026-10-31", got)
	}
}

func TestAddDaysAcrossDST(t *testing.T) {
	ldn, _ := time.LoadLocation("Europe/London")
	// Clocks go back on 2026-10-25 in London.
	c := Fixed(ldn, time.Date(2026, 10, 24, 23, 30, 0, 0, ldn))
	if got := c.Tomorrow(); got != "2026-10-25" {
		t.Fatalf("Tomorrow = %s, want 2026-10-25", got)
	}
	if got := c.AddDays(2); got != "2026-10-26" {
		t.Fatalf("AddDays(2) = %s, want 2026-10-26", got)
	}
}
