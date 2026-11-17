package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsWhenFilesMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "nope.env"), filepath.Join(dir, "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Settings
	if s.Timezone != "Asia/Singapore" || s.MorningTime != "09:00" || s.NightTime != "21:00" {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if s.RecapTime != "20:30" {
		t.Fatalf("recap default = %s, want 20:30", s.RecapTime)
	}
	if !cfg.CarryOver() || s.Weekends != "same" || s.BotName != "Denly Newsletter" {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if cfg.Env.DBPath != "./data/todo.db" {
		t.Fatalf("unexpected env defaults: %+v", cfg.Env)
	}
	if cfg.Loc.String() != "Asia/Singapore" {
		t.Fatalf("loc = %s", cfg.Loc)
	}
}

func TestRepoConfigYAMLIsValid(t *testing.T) {
	cfg, err := Load("", "../../config.yaml")
	if err != nil {
		t.Fatalf("repo config.yaml invalid: %v", err)
	}
	if cfg.Settings.UserName != "Depi" || cfg.Settings.MorningTime != "09:00" {
		t.Fatalf("unexpected: user=%q morning=%q", cfg.Settings.UserName, cfg.Settings.MorningTime)
	}
}

func TestRepoEnvExampleParses(t *testing.T) {
	m, err := readDotEnv("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL"} {
		got, ok := m[k]
		if !ok {
			t.Fatalf("%s missing from .env.example", k)
		}
		if got != "" {
			t.Fatalf("comment-only value for %s should be empty, got %q", k, got)
		}
	}
}

func TestYAMLOverridesAndCarryOverFalse(t *testing.T) {
	dir := t.TempDir()
	y := write(t, dir, "c.yaml", `
timezone: Europe/London
night_time: "22:15"
carry_over: false
weekends: later_morning
nudges: ["go go go"]
recurring:
  - text: gym
    days: [mon, WED]
quiet_dates: ["2026-12-25"]
`)
	cfg, err := Load("", y)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Settings
	if s.Timezone != "Europe/London" || s.NightTime != "22:15" || s.RecapTime != "21:45" {
		t.Fatalf("overrides not applied: %+v", s)
	}
	if cfg.CarryOver() {
		t.Fatal("carry_over: false ignored")
	}
	if s.MorningTime != "09:00" {
		t.Fatal("absent key should keep default")
	}
	if !slices.Equal(s.Nudges, []string{"go go go"}) || len(s.MorningGreetings) == 0 {
		t.Fatalf("lists: %+v", s)
	}
	if !cfg.IsQuietDate("2026-12-25") || cfg.IsQuietDate("2026-12-24") {
		t.Fatal("quiet dates")
	}
}

func TestValidationErrorsNameTheKey(t *testing.T) {
	dir := t.TempDir()
	y := write(t, dir, "c.yaml", `
timezone: Mars/Olympus
morning_time: "9am"
weekends: sometimes
quiet_dates: ["25/12/2026"]
recurring:
  - text: gym
    days: [funday]
`)
	_, err := Load("", y)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"timezone", "morning_time", "weekends", "quiet_dates", "funday"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestDotEnvParsingAndOSOverride(t *testing.T) {
	dir := t.TempDir()
	e := write(t, dir, ".env", `
# comment
TELEGRAM_BOT_TOKEN=tok123   # inline comment
export ANTHROPIC_MODEL="model # with hash"
TELEGRAM_CHAT_ID=XXXX
DRY_RUN=TRUE
DB_PATH=./from-file.db
`)
	t.Setenv("DB_PATH", "./from-os.db")
	cfg, err := Load(e, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Env.TelegramToken != "tok123" {
		t.Fatalf("token = %q", cfg.Env.TelegramToken)
	}
	if cfg.Env.AnthropicModel != "model # with hash" {
		t.Fatalf("quoted value = %q", cfg.Env.AnthropicModel)
	}
	if !cfg.Env.DryRun {
		t.Fatal("DRY_RUN=TRUE not parsed")
	}
	if cfg.Env.DBPath != "./from-os.db" {
		t.Fatalf("OS env should override file, got %q", cfg.Env.DBPath)
	}
	missing := cfg.MissingRequired()
	if !slices.Contains(missing, "TELEGRAM_CHAT_ID") {
		t.Fatalf("placeholder number should count as missing: %v", missing)
	}
	if slices.Contains(missing, "ANTHROPIC_MODEL") {
		t.Fatalf("filled keys reported missing: %v", missing)
	}
}

func TestBadDotEnvLine(t *testing.T) {
	dir := t.TempDir()
	e := write(t, dir, ".env", "JUSTAKEY\n")
	if _, err := Load(e, ""); err == nil || !strings.Contains(err.Error(), ":1:") {
		t.Fatalf("want line-numbered error, got %v", err)
	}
}
