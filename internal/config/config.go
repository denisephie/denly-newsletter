// Package config loads secrets from .env (overridable by real env vars)
// and personal settings from config.yaml, applying defaults and validating.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "time/tzdata" // embed tz database so minimal containers work

	"gopkg.in/yaml.v3"
)

// Env holds secrets and deployment settings from .env / environment.
type Env struct {
	TelegramToken   string
	TelegramChatID  string
	AnthropicAPIKey string
	AnthropicModel  string
	DBPath          string
	DryRun          bool
}

type Emojis struct {
	Morning string `yaml:"morning"`
	Night   string `yaml:"night"`
}

type Recurring struct {
	Text string   `yaml:"text"`
	Days []string `yaml:"days"`
}

// Settings holds personal preferences from config.yaml.
type Settings struct {
	Timezone           string      `yaml:"timezone"`
	MorningTime        string      `yaml:"morning_time"`
	NightTime          string      `yaml:"night_time"`
	RecapTime          string      `yaml:"recap_time"`
	BotName            string      `yaml:"bot_name"`
	UserName           string      `yaml:"user_name"`
	Personality        string      `yaml:"personality"`
	Emojis             Emojis      `yaml:"emojis"`
	MorningGreetings   []string    `yaml:"morning_greetings"`
	NightPrompts       []string    `yaml:"night_prompts"`
	Nudges             []string    `yaml:"nudges"`
	CarryOver          *bool       `yaml:"carry_over"`
	Weekends           string      `yaml:"weekends"`
	WeekendMorningTime string      `yaml:"weekend_morning_time"`
	Recurring          []Recurring `yaml:"recurring"`
	QuietDates         []string    `yaml:"quiet_dates"`
}

type Config struct {
	Env      Env
	Settings Settings
	Loc      *time.Location
}

// CarryOver reports whether unfinished tasks roll over (default true).
func (c *Config) CarryOver() bool {
	return c.Settings.CarryOver == nil || *c.Settings.CarryOver
}

// IsQuietDate reports whether date (YYYY-MM-DD) is in quiet_dates.
func (c *Config) IsQuietDate(date string) bool {
	for _, d := range c.Settings.QuietDates {
		if d == date {
			return true
		}
	}
	return false
}

var Weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// Defaults returns the settings used when config.yaml omits a key.
func Defaults() Settings {
	return Settings{
		Timezone:    "Asia/Singapore",
		MorningTime: "09:00",
		NightTime:   "21:00",
		BotName:     "Denly Newsletter",
		Personality: "warm, lowercase, ascii emojis",
		Emojis:      Emojis{Morning: "☀️", Night: "🌙"},
		MorningGreetings: []string{
			"morning! here's your denly newsletter for today:",
		},
		NightPrompts: []string{
			"it's late~ what's the plan for tomorrow?\njust tell me what's due and i'll sort it out for you!",
		},
		Nudges: []string{
			`reply "done <task number>" when you finish something! have a good day ahead~`,
		},
		Weekends:           "same",
		WeekendMorningTime: "10:00",
	}
}

// Load reads envPath (optional) and yamlPath (optional) and validates the result.
// Missing *secrets* are not an error here; see MissingRequired.
func Load(envPath, yamlPath string) (*Config, error) {
	fileEnv, err := readDotEnv(envPath)
	if err != nil {
		return nil, err
	}
	get := func(k string) string {
		if v, ok := os.LookupEnv(k); ok {
			return strings.TrimSpace(v)
		}
		return fileEnv[k]
	}

	cfg := &Config{Env: Env{
		TelegramToken:   get("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:  get("TELEGRAM_CHAT_ID"),
		AnthropicAPIKey: get("ANTHROPIC_API_KEY"),
		AnthropicModel:  get("ANTHROPIC_MODEL"),
		DBPath:          get("DB_PATH"),
		DryRun:          isTrue(get("DRY_RUN")),
	}}
	if cfg.Env.DBPath == "" {
		cfg.Env.DBPath = "./data/todo.db"
	}

	cfg.Settings, err = readSettings(yamlPath)
	if err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// MissingRequired lists required .env keys that are empty or still placeholders.
// The bot can't talk to Telegram/Anthropic until these are filled.
func (c *Config) MissingRequired() []string {
	var missing []string
	check := func(key, val string) {
		if val == "" || strings.Contains(val, "XXXX") {
			missing = append(missing, key)
		}
	}
	check("TELEGRAM_BOT_TOKEN", c.Env.TelegramToken)
	check("TELEGRAM_CHAT_ID", c.Env.TelegramChatID)
	check("ANTHROPIC_API_KEY", c.Env.AnthropicAPIKey)
	check("ANTHROPIC_MODEL", c.Env.AnthropicModel)
	return missing
}

func readSettings(path string) (Settings, error) {
	s := Defaults()
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read %s: %w", path, err)
	}
	// Unmarshal over defaults: keys present in the file win, absent keys keep defaults.
	var fromFile Settings
	if err := yaml.Unmarshal(data, &fromFile); err != nil {
		return s, fmt.Errorf("parse %s: %w", path, err)
	}
	mergeSettings(&s, fromFile)
	return s, nil
}

func mergeSettings(dst *Settings, src Settings) {
	str := func(d *string, v string) {
		if v != "" {
			*d = v
		}
	}
	list := func(d *[]string, v []string) {
		if len(v) > 0 {
			*d = v
		}
	}
	str(&dst.Timezone, src.Timezone)
	str(&dst.MorningTime, src.MorningTime)
	str(&dst.NightTime, src.NightTime)
	str(&dst.RecapTime, src.RecapTime)
	str(&dst.BotName, src.BotName)
	dst.UserName = src.UserName // empty means "no name", which is a valid choice
	str(&dst.Personality, src.Personality)
	str(&dst.Emojis.Morning, src.Emojis.Morning)
	str(&dst.Emojis.Night, src.Emojis.Night)
	list(&dst.MorningGreetings, src.MorningGreetings)
	list(&dst.NightPrompts, src.NightPrompts)
	list(&dst.Nudges, src.Nudges)
	if src.CarryOver != nil {
		dst.CarryOver = src.CarryOver
	}
	str(&dst.Weekends, src.Weekends)
	str(&dst.WeekendMorningTime, src.WeekendMorningTime)
	dst.Recurring = src.Recurring
	dst.QuietDates = src.QuietDates
}

func (c *Config) validate() error {
	s := &c.Settings
	var errs []error

	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		errs = append(errs, fmt.Errorf("config.yaml timezone %q: %w", s.Timezone, err))
	}
	c.Loc = loc

	for key, val := range map[string]string{
		"morning_time": s.MorningTime, "night_time": s.NightTime, "weekend_morning_time": s.WeekendMorningTime,
	} {
		if _, err := ParseHHMM(val); err != nil {
			errs = append(errs, fmt.Errorf("config.yaml %s: %w", key, err))
		}
	}
	if s.RecapTime == "" {
		if m, err := ParseHHMM(s.NightTime); err == nil {
			m = ((m-30)%(24*60) + 24*60) % (24 * 60)
			s.RecapTime = fmt.Sprintf("%02d:%02d", m/60, m%60)
		}
	} else if _, err := ParseHHMM(s.RecapTime); err != nil {
		errs = append(errs, fmt.Errorf("config.yaml recap_time: %w", err))
	}

	switch s.Weekends {
	case "same", "skip_morning", "later_morning":
	default:
		errs = append(errs, fmt.Errorf("config.yaml weekends %q: want same, skip_morning or later_morning", s.Weekends))
	}
	for _, d := range s.QuietDates {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			errs = append(errs, fmt.Errorf("config.yaml quiet_dates %q: want YYYY-MM-DD", d))
		}
	}
	for i, r := range s.Recurring {
		if strings.TrimSpace(r.Text) == "" {
			errs = append(errs, fmt.Errorf("config.yaml recurring[%d]: text is empty", i))
		}
		if len(r.Days) == 0 {
			errs = append(errs, fmt.Errorf("config.yaml recurring[%d] (%s): days is empty", i, r.Text))
		}
		for _, d := range r.Days {
			if _, ok := Weekdays[strings.ToLower(d)]; !ok {
				errs = append(errs, fmt.Errorf("config.yaml recurring[%d] (%s): unknown day %q (use mon..sun)", i, r.Text, d))
			}
		}
	}
	return errors.Join(errs...)
}

// ParseHHMM parses "HH:MM" into minutes after midnight.
func ParseHHMM(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM (24h)", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// readDotEnv parses KEY=VALUE lines. Supports comments, inline " # comments",
// optional "export " prefix and quoted values. A missing file is not an error.
func readDotEnv(path string) (map[string]string, error) {
	out := map[string]string{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
			if end := strings.IndexByte(val[1:], val[0]); end >= 0 {
				val = val[1 : end+1]
			}
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		} else if strings.HasPrefix(val, "#") {
			val = ""
		}
		out[key] = val
	}
	return out, sc.Err()
}

func isTrue(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
