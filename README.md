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

> **Status: work in progress.** Config loading, the database and `denly check` work today. The commands, webhook, LLM parser, messaging and scheduler are being built; see [Roadmap](#roadmap).

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
git clone <this-repo-url> denly-newsletter
cd denly-newsletter

go build -o denly ./cmd/denly
go test ./...

cp .env.example .env              # fill in later; dry-run works without it
./denly --dry-run check           # shows your settings + which keys are still missing
```

`--dry-run` (or `DRY_RUN=true` in `.env`) prints every outgoing message to the console instead of sending it, so you can try everything without Twilio credits.

Once the remaining phases land, you'll also be able to do:

```bash
./denly --dry-run simulate "add buy milk"     # pretend you texted the bot
./denly --dry-run simulate "list"
./denly --dry-run trigger morning --force     # send the morning message now
./denly serve                                 # the real thing: webhook + scheduler
```

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
| `ANTHROPIC_MODEL` | yes | any Claude model; a small, fast one is plenty |
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
| `carry_over` | `true` | roll unfinished tasks to the next day |
| `weekends` | `same` | `same`, `skip_morning` or `later_morning` |
| `weekend_morning_time` | `10:00` | used with `later_morning` |
| `recurring` | `[]` | e.g. `- {text: gym, days: [mon, wed, fri]}` |
| `quiet_dates` | `[]` | `YYYY-MM-DD` dates when the bot stays silent |

---

## Connecting WhatsApp (Twilio)

1. **Join the sandbox.** In the Twilio console go to *Messaging → Try it out → Send a WhatsApp message* and text `join <your-sandbox-code>` to the sandbox number from your phone.
   Sandbox sessions **expire after 3 days**, so you'll need to re-send the join message. For permanent daily use, register your own WhatsApp sender.
2. **Expose the server.** Run `./denly serve`, then in another terminal `ngrok http 8000`. Put the `https://…` URL in `.env` as `PUBLIC_BASE_URL` and restart.
3. **Set the webhook.** In the sandbox settings, set *"When a message comes in"* to `PUBLIC_BASE_URL/webhook/whatsapp`, method **POST**.
4. **Text the bot** `help`.

**About the 24-hour window:** WhatsApp only lets businesses send free-form messages within 24h of your last message. If you haven't texted the bot in ~23h, it sends the night prompt (and optionally the morning message) via an approved **utility template** instead. Create one in Twilio's *Content Template Builder*, get it approved, and put its Content SID in `TWILIO_NIGHT_TEMPLATE_SID`. If no template is set and the window is closed, the bot skips that message and logs a warning.

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
internal/…          commands, format, parser, messaging, webhook, scheduler (in progress)
docs/SPEC.md        the original product spec
```

Run the tests with `go test ./...`.

---

## Roadmap

- [x] Config loading, SQLite store, `denly check`
- [ ] Quick commands + message formatting
- [ ] Twilio webhook (signature check, number allowlist) + `simulate`
- [ ] Claude parser for free-text plans, with clarifying questions
- [ ] `send_message()` with 24h-window / template fallback
- [ ] Scheduler: morning, night, weekly recap, recurring tasks, quiet dates + `trigger`

**Not planned for v1:** multiple users, a web UI, voice notes, images, calendar sync, reminders at arbitrary times.
