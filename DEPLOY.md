# Deploying to Railway

The agent ships as a Docker image. Railway builds the `Dockerfile`, runs
migrations on startup, and runs the agent as a daemon (first cycle immediately,
then every `agent.interval`). Secrets come from Railway **Variables**; nothing
secret is committed.

> Why Railway and not your laptop: the DXC corporate network blocks the RapidAPI
> gateway (`*.p.rapidapi.com`). Railway runs on its own network, so JSearch and
> everything else work there.

## 0. Prerequisites
- A GitHub account and a [Railway](https://railway.app) account (free trial).
- Your keys: `GROQ_API_KEY`, `RAPIDAPI_KEY` (Telegram optional).

## 1. Edit the committed deploy config
Open [`config.railway.yaml`](config.railway.yaml) and set your real, **non-secret**
profile (name, email, phone, roles, skills, summary). Leave `dry_run: true` for
the first deploy. All secrets stay as `${VAR}` placeholders.

## 2. Push the repo to GitHub
```bash
git remote add origin https://github.com/<you>/job-agent.git
git push -u origin main
```

## 3. Create the Railway project
1. Railway dashboard → **New Project** → **Deploy from GitHub repo** → pick this repo.
   Railway detects the `Dockerfile` and `railway.toml` automatically.
2. **Add a database**: in the project, **New** → **Database** → **PostgreSQL**.

## 4. Set environment variables
On the **agent service** → **Variables**, add:

| Variable | Value |
|----------|-------|
| `DATABASE_URL` | `${{Postgres.DATABASE_URL}}` (reference the Postgres service) |
| `GROQ_API_KEY` | your Groq key |
| `RAPIDAPI_KEY` | your JSearch key |
| `TELEGRAM_BOT_TOKEN` | *(optional)* |
| `TELEGRAM_CHAT_ID` | *(optional)* |

> If you don't set the Telegram vars, notifications are simply skipped — the
> agent logs the cycle report instead.

## 5. Deploy & watch
Railway builds and starts the service. Open **Deployments → Logs**. You should see:
```
running migrations
scrapers ready count=1
cycle start
query complete ... jobs=N        <- real JSearch jobs
matched ... score=0.xx
dry run: would apply ...          <- because dry_run=true
cycle complete scraped=N matched=M applied=0
```

## 6. Going live
Once the dry-run logs look right:
1. Add your CV so the applier can upload it — set `cv_path` and provide the file
   via a Railway **Volume** mounted at `/app/assets`, or bake it into the image.
2. Set `dry_run: false` in `config.railway.yaml`, commit, push. Railway redeploys.
3. Validate the seeded Greenhouse/Lever selectors against a live form first (the
   `selector_health` table flags broken ones). Start with `auto_apply_above`
   high (e.g. 0.9) to apply only to very strong matches.

## Cron instead of a daemon (optional)
To run on a schedule instead of continuously, in **Settings → Deploy** set a
**Cron Schedule** (e.g. `0 */4 * * *`) and change the start command to use
`-once`:
```
/app/agent -config /app/config.railway.yaml -migrations /app/migrations -once
```

## Alternative: Railway CLI
```bash
npm i -g @railway/cli
railway login
railway init
railway add --database postgres
railway variables set GROQ_API_KEY=... RAPIDAPI_KEY=...
railway up
```
