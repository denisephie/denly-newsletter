// Command denly is the Denly Newsletter WhatsApp to-do bot.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"denly/internal/bot"
	"denly/internal/clock"
	"denly/internal/commands"
	"denly/internal/config"
	"denly/internal/format"
	"denly/internal/store"
	"denly/internal/webhook"
)

const usage = `denly: a tiny WhatsApp to-do bot

usage:
  denly [global flags] <command> [args]

commands:
  check                          validate .env + config.yaml, open the DB, list TODO(me) keys
  serve                          run the webhook + scheduler (the always-on process)
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
		_ = force
		return notYet("trigger "+job, "phase 6")
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

func notYet(what, phase string) error {
	return fmt.Errorf("%s isn't built yet (coming in %s)", what, phase)
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
		fmt.Fprintf(out, "\nTODO(me), required to send/receive for real (fine to skip in --dry-run):\n")
		for _, k := range missing {
			fmt.Fprintf(out, "  - %s\n", k)
		}
	}
	if missing := cfg.MissingOptional(); len(missing) > 0 {
		fmt.Fprintf(out, "\nTODO(me), optional (needed once the 24h window closes / real sender):\n")
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
	// Parser (phase 4) plugs in as bot.Parser.
	return &app{cfg: cfg, clk: clk, st: st, f: f, cmd: cmd, bot: &bot.Bot{Cmd: cmd, F: f}}, nil
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
	if !cfg.Env.DryRun {
		if missing := cfg.MissingRequired(); len(missing) > 0 {
			return fmt.Errorf("missing required settings in .env: %s (or run with --dry-run)", strings.Join(missing, ", "))
		}
		return notYet("sending real WhatsApp messages", "phase 5; use --dry-run for now")
	}
	if cfg.Env.MyNumber == "" || strings.Contains(cfg.Env.MyNumber, "XXXX") {
		log.Warn("MY_WHATSAPP_NUMBER is not set: every message will be ignored")
	}
	if cfg.Env.SkipSignature {
		log.Warn("SKIP_SIGNATURE=true: webhook signatures are NOT checked (dry-run only)")
	}

	a, err := newApp(cfg)
	if err != nil {
		return err
	}
	defer a.st.Close()

	wh := &webhook.Handler{
		AuthToken:     cfg.Env.TwilioAuthToken,
		PublicBaseURL: cfg.Env.PublicBaseURL,
		MyNumber:      cfg.Env.MyNumber,
		SkipSignature: cfg.Env.DryRun && cfg.Env.SkipSignature,
		Store:         a.st,
		Bot:           a.bot,
		F:             a.f,
		Send:          consoleSend(out),
		Log:           log,
	}
	srv := &http.Server{
		Addr:              ":" + cfg.Env.Port,
		Handler:           wh.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("denly listening", "addr", srv.Addr, "webhook", webhook.Path, "dry_run", cfg.Env.DryRun)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = srv.Shutdown(shutCtx)
	wh.Wait() // let in-flight replies finish
	return err
}

// consoleSend prints outgoing messages instead of sending them (dry-run).
// Phase 5 replaces this with messaging.SendMessage.
func consoleSend(out io.Writer) func(string) error {
	return func(body string) error {
		fmt.Fprintf(out, "\n%s\n", indent("[dry-run] to you > ", body))
		return nil
	}
}

func indent(prefix, s string) string {
	pad := strings.Repeat(" ", len([]rune(prefix)))
	return prefix + strings.ReplaceAll(s, "\n", "\n"+pad)
}
