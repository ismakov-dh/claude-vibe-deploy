# Security Rules for Vibecoders

If you build and deploy apps on this platform, **read this once and follow it every time.** These rules exist because real mistakes have leaked secrets to the public internet and put production data at risk. The platform enforces some of them automatically; the rest are on you.

---

## 1. Never put secrets in your code

A "secret" is anything that grants access: API keys, passwords, tokens, database URLs with passwords, private keys.

**Do this:**
- Put every secret in a `.env` file.
- Read it in code via the environment (`process.env.X`, `os.environ["X"]`, `os.Getenv("X")`).
- Pass it at deploy time: `vd deploy ... --env-file .env`.

**Never do this:**
```js
const apiKey = "sk-proj-abc123...";          // ❌ hardcoded — deploy will be BLOCKED
const db = "postgresql://u:realpass@host/db"; // ❌ hardcoded credentials
```

> `vd deploy` scans your source and **refuses to deploy** if it finds hardcoded secrets (`POLICY_VIOLATION`). `.env` files are never scanned — that's where secrets belong.

## 2. Never push secrets to GitHub

Most leaks happen when a `.env` file gets committed to a **public** repo. Once pushed, assume the secret is compromised forever — bots scrape GitHub within seconds.

**Before your first commit, create a `.gitignore`:**
```gitignore
.env
.env.*
*.pem
*.key
*.crt
secrets/
node_modules/
__pycache__/
.venv/
venv/
```

**Extra protection (strongly recommended):**
- Install [gitleaks](https://github.com/gitleaks/gitleaks) as a pre-commit hook so a commit with a secret is blocked locally:
  ```bash
  # .git/hooks/pre-commit
  #!/bin/sh
  gitleaks protect --staged --no-banner || {
    echo "Secret detected in staged changes — commit blocked."; exit 1;
  }
  ```
- Prefer **private** repos for anything with config.
- If you leaked a secret: **rotate it immediately** (revoke the old key, issue a new one). Deleting the commit is not enough — it's already scraped.

## 3. Never touch the production database directly

You may be tempted to point a Postgres MCP, a SQL client, or a connection string at the prod database while building. **Don't.**

The supported — and only — way to read production data:

1. Build a dashboard app that reads from `DATABASE_URL`.
2. Deploy it with read-only prod access:
   ```bash
   vd deploy ./my-dashboard --name my-dashboard --db prod-ro --db-name <database>
   ```
3. vd injects a **read-only** `DATABASE_URL` scoped to that database.

This gives you SELECT-only access through a controlled, auditable path. Connecting directly bypasses those controls and risks the production system.

## 4. Use the database the platform gives you

If your app needs its own database, use the built-in PostgreSQL:
```bash
vd deploy ./my-app --name my-app --db postgres
```
`DATABASE_URL` is injected automatically.

**Do not** add Supabase, Firebase, MongoDB Atlas, PlanetScale, Upstash, or any external managed service. They aren't needed, they put your data outside the platform's backups and access controls, and `vd deploy` will warn you (`--allow-external` exists only for rare, deliberate exceptions).

## 5. Quick checklist before every deploy

- [ ] `.gitignore` exists and lists `.env`
- [ ] No secrets hardcoded in source — all in `.env`
- [ ] `.env` is **not** committed to git
- [ ] Database is `--db postgres` (own) or `--db prod-ro` (read prod) — not an external service
- [ ] Not connecting to prod from your laptop
- [ ] If you ever leaked a key: rotated, not just deleted

---

*Questions or an exception you think is justified? Ask the platform team before working around any of these rules.*
