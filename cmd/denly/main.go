// Command denly is the Denly Newsletter Telegram to-do bot.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"denly/internal/bot"
	"denly/internal/clock"
	"denly/internal/commands"
	"denly/internal/config"
	"denly/internal/format"
	"denly/internal/parser"
	"denly/internal/scheduler"
	"denly/internal/store"
	"denly/internal/telegram"
)

const usage = `denly: a tiny Telegram to-do bot

usage:
  denly [global flags] <command> [args]

commands:
  check                          validate .env + config.yaml, open the DB, list TODO(me) keys
  serve                          run the Telegram bot + scheduler (the always-on process)
  simulate "<message>"           handle a message as if you'd texted it; prints the reply
  trigger morning|night|recap [--force]
                                 run a scheduled job now (--force ignores "already sent today")

global flags:
`

type globals struct {
	dryRun  bool
	envPath string
	cfgPath string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	var g globals
	fs := flag.NewFlagSet("denly", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&g.dryRun, "dry-run", false, "print messages to the console instead of sending (same as DRY_RUN=true)")
	fs.StringVar(&g.envPath, "env", ".env", "path to the .env file")
	fs.StringVar(&g.cfgPath, "config", "config.yaml", "path to config.yaml")
	fs.Usage = func() {
		fmt.Fprint(out, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return nil
	}

	cmd, rest := fs.Arg(0), fs.Args()[1:]
	switch cmd {
	case "help", "-h", "--help":
		fs.Usage()
		return nil
	case "check", "serve", "simulate", "trigger":
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}

	cfg, err := config.Load(g.envPath, g.cfgPath)
	if err != nil {
		return err
	}
	if g.dryRun {
		cfg.Env.DryRun = true
	}

	switch cmd {
	case "check":
		return cmdCheck(cfg, out)
	case "serve":
		return cmdServe(cfg, out)
	case "simulate":
		if len(rest) == 0 || strings.TrimSpace(strings.Join(rest, " ")) == "" {
			return errors.New(`usage: denly simulate "<message>"`)
		}
		return cmdSimulate(cfg, strings.Join(rest, " "), out)
	case "trigger":
		job, force := parseTrigger(rest)
		switch job {
		case "morning", "night", "recap":
		default:
			return errors.New("usage: denly trigger morning|night|recap [--force]")
		}
		return cmdTrigger(cfg, job, force, out)
	}
	return nil
}

// parseTrigger accepts --force before or after the job name.
func parseTrigger(args []string) (job string, force bool) {
	for _, a := range args {
		switch a {
		case "--force", "-force", "-f":
			force = true
		default:
			if job == "" {
				job = a
			}
		}
	}
	return job, force
}

func cmdCheck(cfg *config.Config, out io.Writer) error {
	s := cfg.Settings
	clk := clock.New(cfg.Loc)

	db, err := store.Open(cfg.Env.DBPath, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	open, err := db.AllOpen()
	if err != nil {
		return err
	}

	name := s.UserName
	if name == "" {
		name = "(none)"
	}
	fmt.Fprintf(out, "config ok ✓\n\n")
	fmt.Fprintf(out, "  bot          %s (for %s)\n", s.BotName, name)
	fmt.Fprintf(out, "  timezone     %s (today is %s, now %s)\n", s.Timezone, clk.Today(), clk.Now().Format("15:04"))
	fmt.Fprintf(out, "  morning      %s  (weekends: %s)\n", s.MorningTime, s.Weekends)
	fmt.Fprintf(out, "  night        %s\n", s.NightTime)
	fmt.Fprintf(out, "  recap        sundays %s\n", s.RecapTime)
	fmt.Fprintf(out, "  carry over   %v\n", cfg.CarryOver())
	fmt.Fprintf(out, "  recurring    %d   quiet dates: %d\n", len(s.Recurring), len(s.QuietDates))
	fmt.Fprintf(out, "  database     %s (%d open tasks)\n", cfg.Env.DBPath, len(open))
	fmt.Fprintf(out, "  dry run      %v\n", cfg.Env.DryRun)

	if missing := cfg.MissingRequired(); len(missing) > 0 {
		fmt.Fprintf(out, "\nstill empty in .env (needed to run for real; fine to skip with --dry-run):\n")
		for _, k := range missing {
			fmt.Fprintf(out, "  - %s\n", k)
		}
	}
	return nil
}

// app holds the wired-up pieces shared by serve / simulate / trigger.
type app struct {
	cfg *config.Config
	clk *clock.Clock
	st  *store.Store
	f   *format.Formatter
	cmd *commands.Handler
	bot *bot.Bot
}

func newApp(cfg *config.Config) (*app, error) {
	clk := clock.New(cfg.Loc)
	st, err := store.Open(cfg.Env.DBPath, clk.Now)
	if err != nil {
		return nil, err
	}
	f := format.New(cfg.Settings)
	cmd := &commands.Handler{St: st, Clk: clk, F: f, CarryOver: cfg.CarryOver()}
	b := &bot.Bot{Cmd: cmd, F: f}
	if p := newParser(cfg, st, clk, cmd, f); p != nil {
		b.Parser = p
	} else {
		slog.Warn("ANTHROPIC_API_KEY / ANTHROPIC_MODEL not set: free-text messages are off, quick commands still work")
	}
	return &app{cfg: cfg, clk: clk, st: st, f: f, cmd: cmd, bot: b}, nil
}

// newParser returns nil when Anthropic isn't configured.
func newParser(cfg *config.Config, st *store.Store, clk *clock.Clock, cmd *commands.Handler, f *format.Formatter) *parser.Parser {
	if cfg.Env.AnthropicAPIKey == "" || cfg.Env.AnthropicModel == "" {
		return nil
	}
	return &parser.Parser{
		St: st, Clk: clk, Cmd: cmd, F: f,
		LLM:         parser.NewClaude(cfg.Env.AnthropicAPIKey, cfg.Env.AnthropicModel),
		Personality: cfg.Settings.Personality,
		Log:         slog.Default(),
	}
}

// cmdSimulate handles one message as if the owner had texted it.
func cmdSimulate(cfg *config.Config, msg string, out io.Writer) error {
	a, err := newApp(cfg)
	if err != nil {
		return err
	}
	defer a.st.Close()
	if err := a.st.TouchInbound(); err != nil {
		return err
	}
	reply, err := a.bot.HandleInbound(msg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "you > %s\n\n%s\n", msg, indent("bot > ", reply))
	return nil
}

func cmdServe(cfg *config.Config, out io.Writer) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if cfg.Env.TelegramToken == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is not set in .env (create a bot with @BotFather)")
	}
	if !cfg.Env.DryRun {
		if missing := cfg.MissingRequired(); len(missing) > 0 {
			log.Warn("settings still empty", "keys", strings.Join(missing, ", "))
		}
	}

	a, err := newApp(cfg)
	if err != nil {
		return err
	}
	defer a.st.Close()

	tg := &telegram.Client{
		Token:  cfg.Env.TelegramToken,
		ChatID: cfg.Env.TelegramChatID,
		Store:  a.st,
		Bot:    a.bot,
		F:      a.f,
		Log:    log,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	username, err := tg.Check(ctx)
	if err != nil {
		return fmt.Errorf("telegram token rejected: %w", err)
	}
	if cfg.Env.TelegramChatID == "" {
		log.Warn("TELEGRAM_CHAT_ID is empty: message the bot once and its chat id will be logged", "bot", "@"+username)
	}
	log.Info("denly listening on telegram", "bot", "@"+username)
	if cfg.Env.TelegramChatID != "" {
		sch := a.scheduler(tg.Send, log)
		go sch.Run(ctx)
	}
	err = tg.Run(ctx)
	log.Info("shutting down")
	return err
}

func indent(prefix, s string) string {
	pad := strings.Repeat(" ", len([]rune(prefix)))
	return prefix + strings.ReplaceAll(s, "\n", "\n"+pad)
}

func (a *app) scheduler(send func(string) error, log *slog.Logger) *scheduler.Scheduler {
	return &scheduler.Scheduler{Cfg: a.cfg, Clk: a.clk, St: a.st, Cmd: a.cmd, F: a.f, Send: send, Log: log}
}

// cmdTrigger runs one scheduled job now. With --dry-run it prints instead of sending.
func cmdTrigger(cfg *config.Config, job string, force bool, out io.Writer) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	a, err := newApp(cfg)
	if err != nil {
		return err
	}
	defer a.st.Close()

	send := consoleSend(out)
	if !cfg.Env.DryRun {
		if cfg.Env.TelegramToken == "" || cfg.Env.TelegramChatID == "" {
			return errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set (or use --dry-run)")
		}
		tg := &telegram.Client{Token: cfg.Env.TelegramToken, ChatID: cfg.Env.TelegramChatID, Log: log}
		send = tg.Send
	}
	return a.scheduler(send, log).Trigger(job, force)
}

// consoleSend prints outgoing messages instead of sending them (dry-run).
func consoleSend(out io.Writer) func(string) error {
	return func(body string) error {
		fmt.Fprintf(out, "\n%s\n", indent("[dry-run] to you > ", body))
		return nil
	}
}
