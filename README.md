# Job Search & Auto-Apply Agent

A single-binary Go service that autonomously **discovers** job listings from
multiple sources, **scores** each against your profile with Claude, and
**auto-applies** through a headless browser using data-driven form filling
backed by PostgreSQL — then **notifies** you on Telegram.

It runs as a one-shot job (cron) or a continuous daemon, on Railway/Docker or
locally.

---

## Architecture

```
                 ┌─────────────────────────── agent (orchestrator) ──────────────────────────┐
                 │  scrape ─► dedup ─► match ─► [review] ─► apply ─► notify                    │
                 └────┬───────────┬──────────────┬──────────────┬───────────────┬─────────────┘
                      │           │              │              │               │
                ┌─────▼────┐ ┌────▼─────┐  ┌─────▼─────┐  ┌─────▼──────┐  ┌─────▼──────┐
                │ scraper  │ │  store   │  │  matcher  │  │  applier   │  │  notifier  │
                │ JSearch  │ │ pgx/v5   │  │  Claude   │  │ go-rod +   │  │  Telegram  │
                │ career   │ │ Postgres │  │  Messages │  │ field_maps │  │            │
                └──────────┘ └──────────┘  └───────────┘  └────────────┘  └────────────┘
                      ▲           ▲                              ▲
              ratelimit + retry   │                    platform / field_mapping /
              (token bucket,      │                    common_question rows drive
              backoff+jitter)     └── migrations + seed data   the generic form filler
```

Every component sits behind an interface (`Scraper`, `Matcher`, `Store`,
`Applier`, `Notifier`) so they can be swapped or faked in tests.

### The data-driven applier

The applier has **no per-ATS code**. It detects the platform from
`platforms.url_pattern` (regex), then reads `field_mappings`, `platform_steps`
and `common_questions` from the database to fill and submit the form. Supporting
a new ATS is an `INSERT`, not a code change (see
[Adding a new platform](#adding-a-new-platform)).

LinkedIn is seeded as **notify-only** (`enabled=false`): the agent sends you the
link instead of automating it. Unknown ATSes are also routed to a notification.

---

## Project layout

```
cmd/agent          Entry point: wiring, migrations, graceful shutdown
cmd/test-scraper   CLI to verify a JSearch API key
internal/config    YAML config + env expansion + validation
internal/models    Domain types
internal/scraper   Scraper interface, registry, JSearch + career-page scrapers
internal/matcher   Matcher interface + Claude implementation
internal/applier   Applier interface, ATS detector, rod-based engine
internal/store     Store interface + pgx/v5 implementation + migrate runner
internal/notifier  Notifier interface + Telegram
internal/ratelimit Per-source token buckets + monthly budget
internal/retry     Exponential backoff with jitter
internal/agent     Orchestrator
migrations         SQL schema (000001) + seed data (000002)
```

---

## Setup

### Prerequisites
- Go 1.22+
- PostgreSQL 16 (or use `docker compose`)
- Chromium (bundled in the Docker image; locally, go-rod will use an installed
  Chrome/Chromium or download one)
- API keys: RapidAPI (JSearch), Anthropic, Telegram bot

### Local (with Docker Postgres)

```bash
cp config.example.yaml config.yaml      # then edit it
export DATABASE_URL="postgres://jobagent:jobagent@localhost:5432/jobagent?sslmode=disable"
export RAPIDAPI_KEY=...  ANTHROPIC_API_KEY=...  TELEGRAM_BOT_TOKEN=...  TELEGRAM_CHAT_ID=...

docker compose up -d postgres            # start Postgres
make migrate-up                          # apply schema + seed
make run-once                            # run a single cycle (dry-run by default)
```

Put your CV at the path referenced by `profile.cv_path` (default `./assets/cv.pdf`).

### Verify your JSearch key

```bash
go run ./cmd/test-scraper -key "$RAPIDAPI_KEY" -query "Full Stack Developer in Israel"
```

### Full Docker stack

```bash
make docker-run        # builds image, starts postgres + agent
```

### Going live

The agent starts in **dry-run** mode (`agent.dry_run: true`) — it scores and
reports but never submits. When you trust the matches and your `field_mappings`,
set `dry_run: false` (or omit `-dry-run`) and re-run.

---

## Configuration reference

| Key | Meaning |
|-----|---------|
| `agent.interval` | Cycle interval in daemon mode (e.g. `4h`) |
| `agent.max_concurrent` | Bounded parallelism for scrapers |
| `agent.dry_run` | If true, never submit applications |
| `agent.log_level` | `debug` / `info` / `warn` / `error` |
| `profile.*` | Your identity, target roles, skills, CV path, summary |
| `sources[]` | Scraper sources; `type` is `jsearch`, `career_page`, or `apify` |
| `matching.provider` | Matcher backend: `groq` (free, default in config), `anthropic`, or `openai_compatible` |
| `matching.base_url` | OpenAI-compatible endpoint (e.g. `https://api.groq.com/openai/v1`); auto-set for `groq` |
| `matching.model` | Scoring model (Groq: `llama-3.3-70b-versatile`; Claude: `claude-haiku-4-5-20251001`) |
| `matching.cover_letter_model` | Cover-letter model (defaults to the scoring model for Groq) |
| `matching.min_score` | At/above this → `matched`, below → `rejected` |
| `matching.auto_apply_above` | At/above this a matched job is auto-applied |
| `notifier.type` | `telegram` (anything else disables notifications) |
| `database.dsn` | Postgres DSN (`postgres://…?sslmode=disable`) |

`${VAR}` placeholders anywhere in the YAML are expanded from the environment.

### CLI flags (`cmd/agent`)

```
-config        path to config YAML (default config.yaml)
-migrations    migrations directory (default ./migrations)
-once          run a single cycle and exit
-dry-run       force dry-run (overrides config)
-skip-migrate  don't run migrations on startup
-migrate-only  apply migrations and exit
-migrate-down  roll back migrations and exit
```

---

## Discovery sources

### JSearch (`type: jsearch`)
Aggregates LinkedIn/Indeed/Glassdoor/ZipRecruiter via the RapidAPI JSearch
endpoint. Good general coverage, but historically thin for Israel-specific
postings.

### Career page (`type: career_page`)
Generic CSS-selector scraper for a single company's own careers page. No API
key, no quota.

### Apify (`type: apify`)
Runs any [Apify Actor](https://apify.com/store) — e.g. a LinkedIn or Indeed
jobs scraper — and maps its output into jobs. Useful for markets JSearch covers
poorly. Because different actors return different JSON shapes, field names are
resolved from a small candidate list per field, with optional overrides:

```yaml
sources:
  - name: apify_linkedin
    type: apify
    enabled: true
    extra:
      api_token: "${APIFY_TOKEN}"
      actor_id: "bebity/linkedin-jobs-scraper"   # "user/actor" or "user~actor"
      input_json: |
        {"title": "Junior Software Engineer", "location": "Israel", "rows": 50}
      timeout_secs: "180"
      # Only needed if the actor uses different JSON keys than the defaults
      # (title, companyName, location, applyUrl/link, descriptionText, id):
      # field_title: "jobTitle"
      # field_apply_url: "applyUrl"
```

Verify an actor before wiring it in:
```bash
go run ./cmd/test-apify -token "$APIFY_TOKEN" -actor bebity/linkedin-jobs-scraper \
  -input '{"title":"Junior Software Engineer","location":"Israel","rows":25}'
```

**Auto-apply vs. notify-only is automatic and needs no extra config.** The
scraper's job is only to produce a `Job{ApplyURL: ...}`; the existing platform
detector (`internal/applier/detector.go`) decides what happens next purely from
that URL:
- If the real application link resolves to a supported ATS (Greenhouse, Lever,
  Ashby, Comeet, SmartRecruiters) — the job is **auto-applied**, exactly like a
  JSearch-discovered job.
- If it stays on LinkedIn/Indeed's own apply flow (LinkedIn "Easy Apply",
  "Indeed Apply") — the job is **notify-only**: you get a Telegram link and
  apply yourself. This is deliberate — driving a bot through your own logged-in
  LinkedIn/Indeed session risks your personal account being banned and breaches
  their Terms of Service, so the applier will never attempt it.

---

## Adding a new platform

No code required. Insert a platform row plus its field mappings:

```sql
INSERT INTO platforms (name, display_name, url_pattern, flow_type, success_indicator)
VALUES ('breezy', 'Breezy HR', 'app\.breezy\.hr', 'single_page', '.application-confirmation');

WITH p AS (SELECT id FROM platforms WHERE name = 'breezy')
INSERT INTO field_mappings
  (platform_id, profile_field, css_selector, input_method, is_required, field_order, fallback_selectors)
VALUES
  ((SELECT id FROM p), 'full_name', 'input[name="name"]',  'type',   true, 1, ARRAY['#name']),
  ((SELECT id FROM p), 'email',     'input[name="email"]', 'type',   true, 2, ARRAY['input[type=email]']),
  ((SELECT id FROM p), 'cv_path',   'input[type="file"]',  'upload', true, 3, '{}');
```

- `input_method`: `type`, `textarea`, `select`, `upload`, `click`, `custom`
  (`custom` runs `custom_script` via the page's JS engine).
- `fallback_selectors` and `label_text` are tried in order when the primary
  `css_selector` misses; `selector_health` tracks what breaks.
- For multi-step (Workday-style) forms set `flow_type='multi_step'` and add
  `platform_steps` rows (`next_selector`, `arrival_indicator`).
- `common_questions` (regex → answer) cover questions like "authorized to work?"
  globally or per-platform.

---

## Behaviour & reliability

- **Errors**: a failing scraper is logged and reported but doesn't stop the
  others; a failing match leaves the job `new` to retry next cycle; a failing
  application sets the job `failed` with a reason and an error screenshot.
- **Rate limiting**: JSearch is capped at a monthly budget (200 free-tier);
  Claude at 50 req/min — token buckets per source.
- **Retries**: network errors and HTTP 429/5xx retry with exponential backoff +
  jitter, honouring `Retry-After`; 4xx (except 429) do not retry.
- **Graceful shutdown**: SIGINT/SIGTERM cancels the context; in-progress work
  unwinds, the browser and DB pool close cleanly.

---

## Testing

```bash
make test               # unit tests (no external services)
make test-integration   # spins up Postgres via testcontainers-go (needs Docker)
```

Unit tests cover config validation, model conversions, JSON/HTML parsing, URL
platform detection, retry logic, rate limiting and the orchestration loop (with
in-memory fakes). The integration test applies the real migrations against a
throwaway Postgres container and exercises the full `Store`.
