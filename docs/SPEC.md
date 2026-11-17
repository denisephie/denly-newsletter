# Daily To-Do WhatsApp Bot

> **Note:** this is the original spec. The bot now runs on **Telegram** (long polling) instead of WhatsApp/Twilio, so the WhatsApp rules, Twilio setup, templates and 24-hour window below no longer apply. See the README for how it works today.

A tiny personal bot that lives in my WhatsApp:

- **Every morning** it sends me today's to-do list.
- **Every night** it asks what I'm doing tomorrow, reads my reply, and updates the list.
- I can also text it anytime to add / finish / remove things.

It's for **one user (me)**. Keep it simple, cute, and easy to tweak. No auth system, no dashboards, no multi-tenant anything.

> **Note for the builder (Claude):** read this whole file first, then build the project in the order under [Build Plan](#build-plan). Anything marked **`TODO(me)`** is a personal detail I'll fill in myself. Don't invent values for those; put clearly named placeholders in `config.yaml` / `.env.example` and list them at the end of your work.

---

## How it behaves

### Morning message (default 09:00)
Sent automatically. Contains:
1. A greeting in my chosen tone (see [Personality](#personality)).
2. Today's tasks, numbered. Carried-over unfinished tasks from earlier days are shown in their own short section.
3. A one-line nudge at the end.

Example:

```
morning! here's your denly newsletter for today:

1. finish report draft
2. call mum
3. gym 

carried over:
4. renew passport

reply "done <task number>" when you finish something! have a good day ahead~
```

If there is nothing on the list, send a short "free day, nothing planned!" message instead.

### Night message (default 21:00)
Asks what's happening tomorrow. Short and friendly, with a hint that I can reply in plain English:

```
it's late~ what's the plan for tomorrow?
just tell me what's due and i'll sort it out for you!
```

### My reply → list update
Any free-text message that isn't an obvious command goes to Claude (Anthropic API), which turns it into structured actions. Examples of what I might say:

- "tomorrow: finish the report, call mum, gym at 6"
- "move passport thing to friday"
- "i already did the gym today"
- "scrap the report, boss pushed the deadline"

The bot then replies with the updated list for tomorrow so I can see it was understood. If Claude isn't confident what I meant, the bot **asks one short clarifying question** instead of guessing.

### Quick commands (no LLM needed, instant)
| Message | What it does |
|---|---|
| `list` | show today's list |
| `tomorrow` | show tomorrow's list |
| `add <text>` | add a task to today |
| `done <n>` | mark task n done |
| `remove <n>` | delete task n |
| `help` | show this table (short version) |

Everything else → the LLM parser.

### End of day
Unfinished tasks automatically **carry over** to the next day (configurable; see `config.yaml`).

---

## Tech stack

- **Python 3.11+**
- **FastAPI**: the webhook that receives my WhatsApp messages
- **APScheduler**: the morning and night jobs (runs in the same process)
- **SQLite**: all data lives in one file, no setup
- **Twilio WhatsApp API**: sending and receiving messages
- **Anthropic API**: understanding my free-text plans
- Config via `.env` (secrets) and `config.yaml` (personal settings)

Keep dependencies minimal. No ORM, plain `sqlite3` is fine.

---

## WhatsApp rules the code MUST respect

These are real platform rules and they shape the design:

1. **24-hour window.** The bot can only send free-form messages within 24 hours of my last message to it. Outside that window, it can only send a pre-approved **message template**.
2. **The night prompt needs a template.** Because my last reply may be ~24h old by the next evening, send the night prompt using an approved *utility template* when the window is closed.
3. **Build one helper, `send_message()`,** that checks `last_inbound_at` (stored in the DB). If I messaged within the last ~23 hours, send free-form text. Otherwise send the template (by Content SID from `.env`). All outbound messages go through this helper.
4. **Twilio Sandbox is for prototyping only.** I must send `join <sandbox-code>` to the sandbox number, and the session **expires after 3 days**, so I have to rejoin. For permanent daily use I'll register a real WhatsApp sender (`TODO(me)`).
5. **Validate Twilio's request signature** on the webhook (`X-Twilio-Signature`) and **ignore any message not from my number.** The bot must never respond to or leak data to anyone else.

---

## Project structure

```
todo-bot/
├── README.md
├── .env.example
├── config.yaml              # personal settings (times, tone, name...)
├── requirements.txt
├── app/
│   ├── main.py              # FastAPI app + webhook + scheduler startup
│   ├── scheduler.py         # morning / night jobs
│   ├── messaging.py         # send_message() with 24h-window logic
│   ├── commands.py          # list / add / done / remove / help
│   ├── parser.py            # Claude call: free text -> structured actions
│   ├── db.py                # SQLite helpers + schema
│   ├── formatting.py        # builds the pretty messages
│   └── config.py            # loads config.yaml + .env
└── tests/
    └── test_parser_and_commands.py
```

### Data model (SQLite)

`tasks`
| column | type | notes |
|---|---|---|
| id | INTEGER PK | |
| text | TEXT | |
| date | TEXT (YYYY-MM-DD) | the day it's planned for, in my timezone |
| status | TEXT | `open` / `done` / `removed` |
| created_at | TEXT | |
| completed_at | TEXT NULL | |

`state` (key/value): `last_inbound_at`, `last_morning_sent`, `last_night_sent`, `awaiting_clarification` (JSON, nullable).

Task numbers shown to me (1, 2, 3...) are **per-message display numbers** mapped to task ids for the *current list*, so `done 2` refers to what I last saw. Store that mapping in `state`.

### The LLM parser contract

`parser.py` sends my message + the current open tasks to Claude and requires **JSON only** back:

```json
{
  "actions": [
    {"type": "add",    "text": "finish report", "date": "2026-11-17"},
    {"type": "done",   "task_id": 12},
    {"type": "remove", "task_id": 7},
    {"type": "move",   "task_id": 9, "date": "2026-11-20"}
  ],
  "clarification": null
}
```

- If unclear, return `"actions": []` and a short `"clarification"` question.
- Resolve relative dates ("tomorrow", "friday") using today's date **in my timezone**, which is passed in the prompt.
- Validate the JSON with a schema before applying; on invalid output, tell me "didn't quite get that, can you rephrase?" and change nothing.
- Model name comes from `.env` (`ANTHROPIC_MODEL`), not hardcoded.

---

## Build Plan

Build in this order and make each step work before the next:

1. **Skeleton + config loading.** `config.py`, `.env.example`, `config.yaml`, DB schema.
2. **Commands + formatting.** `list`, `add`, `done`, `remove`, `help` with pretty output. Add tests.
3. **Webhook.** FastAPI endpoint for Twilio, signature validation, allowlist for my number, update `last_inbound_at`, route to commands or parser.
4. **Parser.** Claude call with strict JSON output, validation, clarification flow. Add tests with mocked API responses.
5. **Messaging helper.** `send_message()` with the 24h-window / template fallback.
6. **Scheduler.** Morning and night jobs in my timezone, carry-over logic, don't double-send if the server restarts (check `last_*_sent`).
7. **Docs.** Fill in a short "Run it" section at the bottom of this README with the exact commands that work.

Include a `--dry-run` flag / env var that prints messages to the console instead of sending, so I can test without using Twilio credits.

---

## `.env.example`

```
# Twilio
TWILIO_ACCOUNT_SID=
TWILIO_AUTH_TOKEN=
TWILIO_WHATSAPP_FROM=whatsapp:+14155238886   # sandbox number by default
TWILIO_NIGHT_TEMPLATE_SID=                   # Content SID, needed when 24h window is closed
TWILIO_MORNING_TEMPLATE_SID=                 # optional, same idea

# Me
MY_WHATSAPP_NUMBER=whatsapp:+65XXXXXXXX      # TODO(me): E.164 format

# Anthropic
ANTHROPIC_API_KEY=
ANTHROPIC_MODEL=                             # TODO(me): pick a model (a small/fast one is plenty)

# App
PUBLIC_BASE_URL=                             # the https URL Twilio calls (ngrok or deployed)
DB_PATH=./data/todo.db
DRY_RUN=false
```

---

## Personal custom things I need to fill in

Everything below is personal. The builder should expose each one in `config.yaml` or `.env` with a sensible placeholder.

### Required (the bot won't work without these)
| # | What | Where | Notes |
|---|---|---|---|
| 1 | My WhatsApp number | `.env` → `MY_WHATSAPP_NUMBER` | E.164 format, e.g. `whatsapp:+65...` |
| 2 | Twilio Account SID + Auth Token | `.env` | from the Twilio console |
| 3 | Twilio WhatsApp sender | `.env` → `TWILIO_WHATSAPP_FROM` | sandbox number to start, own sender later |
| 4 | Anthropic API key + model | `.env` | |
| 5 | Public URL for the webhook | `.env` → `PUBLIC_BASE_URL` | ngrok while testing, hosting URL later |
| 6 | Night-prompt template SID | `.env` | needed once the sandbox is replaced with a real sender; template text submitted to WhatsApp for approval |

### Personalization
| # | What | Where | Default if I skip it |
|---|---|---|---|
| 7 | **Timezone** | `config.yaml` → `timezone` | `Asia/Singapore` |
| 8 | **Morning time** | `config.yaml` → `morning_time` | `08:00` |
| 9 | **Night time** | `config.yaml` → `night_time` | `21:00` |
| 10 | **Bot's name** | `config.yaml` → `bot_name` | `Denly Newsletter` |
| 11 | **My name / nickname** | `config.yaml` → `Depi` | none (no name used) |
| 12 | **Personality / tone** | `config.yaml` → `personality` | `warm, lowercase, ascii emojis` |
| 13 | **Emoji set** | `config.yaml` → `emojis` | ☀️ 🌙 |
| 14 | **Greetings** | `config.yaml` → `morning_greetings`, `night_prompts` | a few rotating defaults |
| 15 | **Carry over unfinished tasks?** | `config.yaml` → `carry_over` | `true` |
| 16 | **Weekend behaviour** | `config.yaml` → `weekends` | `same` (options: `same`, `skip_morning`, `later_morning`) |
| 18 | **Recurring tasks** (optional) | `config.yaml` → `recurring` | none. e.g. "gym every mon/wed/fri", "water plants sunday" |
| 19 | **Quiet days** (optional) | `config.yaml` → `quiet_dates` | none. dates when the bot stays silent (holidays, trips) |
| 20 | **Closing nudge line** | `config.yaml` → `nudges` | a few rotating defaults |

### Questions I should decide before / while building
- Should the morning message also pull in my **calendar** (Google Calendar)? *(Not in v1. Mark as a future idea.)*
- Do I want a **weekly recap** on Sunday night ("you finished 14 things this week")? *(Sure!)*
- Where should it live long-term: my laptop, a small VPS, Railway, or Fly? *(Needs an always-on process, since the scheduler is in-process. Don't pick a host that sleeps when idle.)*

---

## 🚀 Deploying

- The server must be **always on** (scheduler runs inside it) and reachable over **HTTPS** so Twilio can call the webhook.
- SQLite file needs **persistent storage** (a mounted volume), or the list vanishes on every redeploy.
- In the Twilio console, set the WhatsApp **"When a message comes in"** webhook to `PUBLIC_BASE_URL/webhook/whatsapp` (HTTP POST).
- Local testing: run the server, start `ngrok http 8000`, paste the ngrok URL into Twilio.

---

## Done when:

- [ ] I text `add buy milk` → it appears in `list`.
- [ ] I text `done 1` → it's marked done and disappears from tomorrow's carry-over.
- [ ] At the configured morning time I get the list (verified with `DRY_RUN` and a manual trigger).
- [ ] At the configured night time I get the planning prompt.
- [ ] I reply "tomorrow: gym, call mum, finish report" → tomorrow's list contains those 3 tasks and the bot shows me the result.
- [ ] A message from any **other** number is ignored.
- [ ] A request with a bad Twilio signature is rejected.
- [ ] Restarting the server doesn't re-send today's messages or lose my tasks.
- [ ] `pytest` passes.

## Out of scope (v1):

Multiple users, a web UI, voice notes, images, calendar sync, reminders at arbitrary times, analytics. Possibly later. Not now. 🌱

---

## Run it

*(Builder: fill this section in with the exact, tested commands: install, set up `.env`, run locally, run tests, trigger morning/night jobs manually for testing.)*
