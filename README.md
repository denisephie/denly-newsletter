# Denly Newsletter

A tiny, personal to-do bot that lives in your Telegram.

- **Every morning** it sends you today's to-do list.
- **Every night** it asks what you're doing tomorrow, reads your plain-English reply, and updates the list.
- **Anytime**, text it to add, finish or remove things.
- **Sunday nights** it sends a weekly recap of what you got done.

```
morning! here's your denly newsletter for today:

1. finish report draft
2. call mum
3. gym

carried over:
4. renew passport

reply "done <task number>" when you finish something! have a good day ahead~
```

It's built for **one person**: no accounts, no dashboard, no multi-user anything. It's one Go binary and one SQLite file, and you can tweak everything in a YAML file.

---

---

## How it works

```
 you (Telegram) ──> denly (long polling) ──> SQLite
                        │  ▲
 quick commands (list, add, done…)  │  │ free text
                        ▼  │
             Claude (Anthropic API) → JSON actions
 you (Telegram) <── denly <── scheduler (morning / night / recap)
```

- **Quick commands** are answered instantly with no AI involved.
- **Anything else** ("tomorrow: gym at 6, call mum", "move passport thing to friday") goes to Claude, which returns structured actions. If it isn't sure what you meant, it asks one short question instead of guessing.
- **Unfinished tasks carry over** to the next day automatically (configurable).
- **It only talks to you.** Messages from any chat other than your own are ignored without a reply.
- **No public URL needed.** The bot polls Telegram, so there's no webhook, tunnel or HTTPS setup.

### Commands

Slash forms work too (`/list`, `/done 2`, `/help`).

| Message | What it does |
|---|---|
| `list` | show today's list |
| `tomorrow` | show tomorrow's list |
| `add <text>` | add a task to today |
| `done <n>` | mark task n done |
| `remove <n>` | delete task n |
| `help` | show the commands |
| *anything else* | understood by Claude ("scrap the report, deadline moved") |

Numbers refer to the list you were shown last.

---

## Requirements

- **Go 1.23+** ([go.dev/dl](https://go.dev/dl/))
- A Telegram bot token from `@BotFather` (free)
- An [Anthropic API](https://console.anthropic.com/) key, for free-text messages (quick commands and scheduled messages don't need it)

No C compiler needed; the SQLite driver is pure Go.

---

## Quick start (local, no accounts needed)

```bash
git clone https://github.com/denisephie/denly-newsletter.git
cd denly-newsletter

go build -o denly ./cmd/denly
go test ./...

cp .env.example .env              # fill in later; dry-run works without it
./denly --dry-run check           # shows your settings + which keys are still empty

./denly --dry-run simulate "add buy milk"     # pretend you messaged the bot
./denly --dry-run simulate "list"
./denly --dry-run simulate "done 1"
```

`--dry-run` (or `DRY_RUN=true` in `.env`) prints outgoing messages to the console instead of sending them.

With `ANTHROPIC_API_KEY` and `ANTHROPIC_MODEL` in `.env`, free text works too (this makes a real, tiny API call):

```bash
./denly --dry-run simulate "tomorrow: gym, call mum, finish report"
./denly --dry-run simulate "move gym to friday"
```

Scheduled jobs can be fired by hand:

```bash
./denly --dry-run trigger morning --force     # print the morning message now
./denly trigger night --force                 # actually send the night prompt
./denly serve                                 # the real thing: Telegram bot + scheduler
```

---

## Configuration

Two files, both in the project root:

- **`.env`**: secrets and deployment settings. Copy from `.env.example`. Real environment variables override it.
- **`config.yaml`**: personal preferences. Every key is optional; delete a line to get the default.

### `.env`

| Key | Required | Notes |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | yes | from `@BotFather` |
| `TELEGRAM_CHAT_ID` | yes | your chat id; leave empty on first run and the bot logs it when you message it |
| `ANTHROPIC_API_KEY` | for free text | |
| `ANTHROPIC_MODEL` | for free text | any Claude model that supports structured outputs; a small, fast one is plenty (e.g. `claude-haiku-5-5`) |
| `DB_PATH` | | default `./data/todo.db` |
| `DRY_RUN` | | `true` = print instead of send (`trigger`) |

`./denly check` tells you which of these are still empty.

### `config.yaml`

| Key | Default | Notes |
|---|---|---|
| `timezone` | `Asia/Singapore` | any IANA name; all "today"/"tomorrow" logic uses it |
| `morning_time` | `09:00` | 24h `HH:MM` |
| `night_time` | `21:00` | |
| `recap_time` | night time − 30 min | weekly recap, Sundays |
| `bot_name` | `Denly Newsletter` | |
| `user_name` | *(none)* | your name or nickname; empty = no name used |
| `personality` | `warm, lowercase, ascii emojis` | tone hint for the AI replies |
| `emojis.morning` / `emojis.night` | sun / moon emoji | |
| `morning_greetings` | 1 default | list; rotates daily |
| `night_prompts` | 1 default | list; rotates daily |
| `nudges` | 1 default | closing line of the morning message; rotates daily |

Greetings, prompts and nudges can include `{name}` (your `user_name`) and `{bot}` (`bot_name`).
| `carry_over` | `true` | roll unfinished tasks to the next day |
| `weekends` | `same` | `same`, `skip_morning` or `later_morning` |
| `weekend_morning_time` | `10:00` | used with `later_morning` |
| `recurring` | `[]` | e.g. `- {text: gym, days: [mon, wed, fri]}` |
| `quiet_dates` | `[]` | `YYYY-MM-DD` dates when the bot stays silent |

---

## Connecting Telegram

The bot talks to Telegram by long polling, so you don't need ngrok or a public URL.

1. **Create the bot.** In Telegram, open `@BotFather`, send `/newbot`, follow the prompts, and copy the token into `.env` as `TELEGRAM_BOT_TOKEN`.
2. **Run it.** `./denly serve`, then send your new bot any message.
3. **Claim it.** With `TELEGRAM_CHAT_ID` empty, the bot answers nobody and logs your chat id. Put it in `.env` as `TELEGRAM_CHAT_ID` and restart. From then on only that chat gets replies.
4. **Text the bot** `help`.

---

## Deploying

The scheduler runs inside the server process, so the host must be **always on**. Avoid free tiers that sleep when idle. It also needs **persistent storage** for the SQLite file (a mounted volume), or your list vanishes on redeploy. No public URL or HTTPS is required.

Any always-on machine works (a spare computer, a Raspberry Pi, or a VPS). Since it's a single static binary:

```bash
GOOS=linux GOARCH=amd64 go build -o denly ./cmd/denly   # cross-compile from anywhere
```

Restarting is safe: tasks live in SQLite, and the bot records what it already sent today, so it won't double-send.

---

## Project layout

```
cmd/denly/          CLI entrypoint (check, serve, simulate, trigger)
internal/config/    .env + config.yaml loading, defaults, validation
internal/store/     SQLite schema, tasks, key/value state
internal/clock/     "today" in your timezone (frozen in tests)
internal/commands/  list, tomorrow, add, done, remove, help + display numbers
internal/format/    every message the bot sends
internal/bot/       routes a message: quick command -> parser -> reply
internal/parser/    Claude call (structured outputs), validation, clarifying questions
internal/telegram/  Telegram long-polling client, owner-only
internal/scheduler/ morning / night / Sunday recap jobs, restart-safe
docs/SPEC.md        the original product spec
```

Run the tests with `go test ./...`.

---

## Roadmap

- [x] Config loading, SQLite store, `denly check`
- [x] Quick commands + message formatting
- [x] `simulate` for local testing
- [x] Claude parser for free-text plans, with clarifying questions
- [x] Telegram client (long polling, owner-only)
- [x] Scheduler: morning, night, weekly recap, recurring tasks, quiet dates + `trigger`

**Not planned for v1:** multiple users, a web UI, voice notes, images, calendar sync, reminders at arbitrary times.
