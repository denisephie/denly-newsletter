# Denly Newsletter

A tiny, personal to-do bot that lives in your WhatsApp.

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

> **Status: work in progress.** Config, the database, the quick commands, the Twilio webhook and the Claude parser work today (in `--dry-run`, replies print to the console). Real sending and the scheduler are being built; see [Roadmap](#roadmap).

---

## How it works

```
 you (WhatsApp) ──> Twilio ──> POST /webhook/whatsapp ──> denly ──> SQLite
                                                          │  ▲
                        quick commands (list, add, done…) │  │ free text
                                                          ▼  │
                                               Claude (Anthropic API) → JSON actions
 you (WhatsApp) <── Twilio <── send_message() <── scheduler (morning / night / recap)
```

- **Quick commands** are answered instantly with no AI involved.
- **Anything else** ("tomorrow: gym at 6, call mum", "move passport thing to friday") goes to Claude, which returns structured actions. If it isn't sure what you meant, it asks one short question instead of guessing.
- **Unfinished tasks carry over** to the next day automatically (configurable).
- **WhatsApp's 24-hour rule is respected.** Free-form messages are only sent within 24h of your last message; otherwise the bot uses a pre-approved template.
- **It only talks to you.** Twilio signatures are verified, and messages from any other number are ignored.

### WhatsApp commands

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
- To actually send and receive WhatsApp messages (not needed for dry-run):
  - a [Twilio](https://www.twilio.com/) account (the WhatsApp Sandbox is free to start)
  - an [Anthropic API](https://console.anthropic.com/) key
  - a public HTTPS URL for the webhook, e.g. [ngrok](https://ngrok.com/) for local use

No C compiler needed; the SQLite driver is pure Go.

---

## Quick start (local, no accounts needed)

```bash
git clone https://github.com/denisephie/denly-newsletter.git
cd denly-newsletter

go build -o denly ./cmd/denly
go test ./...

cp .env.example .env              # fill in later; dry-run works without it
./denly --dry-run check           # shows your settings + which keys are still missing

./denly --dry-run simulate "add buy milk"     # pretend you texted the bot
./denly --dry-run simulate "list"
./denly --dry-run simulate "done 1"
```

`--dry-run` (or `DRY_RUN=true` in `.env`) prints every outgoing message to the console instead of sending it, so you can try everything without Twilio credits.

With `ANTHROPIC_API_KEY` and `ANTHROPIC_MODEL` in `.env`, free text works too (this makes a real, tiny API call):

```bash
./denly --dry-run simulate "tomorrow: gym, call mum, finish report"
./denly --dry-run simulate "move gym to friday"
```

Once the remaining phases land, you'll also be able to do:

```bash
./denly --dry-run trigger morning --force     # send the morning message now
./denly serve                                 # the real thing: webhook + scheduler
```

`./denly --dry-run serve` already runs the webhook today; replies are printed to the console instead of sent.

---

## Configuration

Two files, both in the project root:

- **`.env`**: secrets and deployment settings. Copy from `.env.example`. Real environment variables override it.
- **`config.yaml`**: personal preferences. Every key is optional; delete a line to get the default.

### `.env`

| Key | Required | Notes |
|---|---|---|
| `TWILIO_ACCOUNT_SID` | yes | Twilio console |
| `TWILIO_AUTH_TOKEN` | yes | Twilio console; also used to verify webhook signatures |
| `TWILIO_WHATSAPP_FROM` | yes | defaults to the sandbox number `whatsapp:+14155238886` |
| `MY_WHATSAPP_NUMBER` | yes | your number, e.g. `whatsapp:+6591234567`. The bot only ever talks to this number. |
| `ANTHROPIC_API_KEY` | yes | |
| `ANTHROPIC_MODEL` | yes | any Claude model that supports structured outputs; a small, fast one is plenty (e.g. `claude-haiku-5-5`) |
| `PUBLIC_BASE_URL` | yes | the HTTPS URL Twilio calls, e.g. your ngrok URL (no trailing slash) |
| `TWILIO_NIGHT_TEMPLATE_SID` | later | Content SID of an approved template, used when the 24h window has closed |
| `TWILIO_MORNING_TEMPLATE_SID` | optional | same idea for the morning message |
| `DB_PATH` | | default `./data/todo.db` |
| `PORT` | | default `8000` |
| `DRY_RUN` | | `true` = print instead of send |
| `SKIP_SIGNATURE` | | `true` skips signature checks, **only** honoured together with `DRY_RUN=true` (for local curl tests) |

`./denly check` tells you exactly which required keys are still empty.

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

(The old Twilio/WhatsApp keys and `internal/webhook` are still in the repo but unused.)

---

## Deploying

The scheduler runs inside the server process, so the host must be **always on**. Avoid free tiers that sleep when idle. You also need:

- **HTTPS**, so Twilio can reach `/webhook/whatsapp`
- **persistent storage** for the SQLite file (a mounted volume), or your list vanishes on redeploy

A small VPS, Fly.io (with a volume) or Railway (with a volume) all work. Since it's a single static binary:

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
internal/webhook/   Twilio webhook, signature check, number allowlist
internal/…          messaging, scheduler (in progress)
docs/SPEC.md        the original product spec
```

Run the tests with `go test ./...`.

---

## Roadmap

- [x] Config loading, SQLite store, `denly check`
- [x] Quick commands + message formatting
- [x] Twilio webhook (signature check, number allowlist) + `simulate`
- [x] Claude parser for free-text plans, with clarifying questions
- [ ] `send_message()` with 24h-window / template fallback
- [ ] Scheduler: morning, night, weekly recap, recurring tasks, quiet dates + `trigger`

**Not planned for v1:** multiple users, a web UI, voice notes, images, calendar sync, reminders at arbitrary times.
