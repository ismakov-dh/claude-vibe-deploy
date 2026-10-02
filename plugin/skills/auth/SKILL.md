---
name: auth
description: Add "sign in with the platform account" to a vibe-deploy app. Use when the user wants login / accounts / per-user data in their vibecoded app.
---

# Add platform login to a vibe-deploy app

**IMPORTANT: Always communicate with the user in their language. Detect the language they use and respond in the same language throughout the session.**

The platform signs people in for you. Deploy with **`vd deploy --auth`** and the platform puts
the app behind its identity provider (Authentik): anyone who is not signed in is sent to the
platform login page before a single request reaches your code. Your app writes **no login
code** — it reads who the person is from request headers.

> **`--auth` needs vd with forward-auth support.** If `vd deploy --auth` answers
> `unknown flag: --auth` or `AUTH_NOT_CONFIGURED`, the server is not set up for it yet: use the
> [fallback](#fallback-server-side-oidc--only-when---auth-is-unavailable) at the end of this
> file, and tell the user a platform admin can enable `--auth` later.

Load `/vibe` for platform constraints and `/deploy` for the deploy step.

A complete, running example — guard, `api()` helper, sign-out — is
[`examples/auth-demo`](https://github.com/ismakov-dh/claude-vibe-deploy/tree/main/examples/auth-demo),
live at `https://auth-demo.apps.platform.acuradai.com`. Copy from it when in doubt.

---

## 0. Hard rules — do not negotiate

1. **No login screen, no signup screen, no password form, no password reset.** The platform
   handles all of it. If the user asks for "register", explain that people are added by the
   platform team (§1).
2. **Subdomain routing only.** `vd deploy --auth` refuses `--routing path`.
3. **Trust the identity headers only together with the ingress secret** (§3). Every app on the
   platform shares one network and can reach yours directly, skipping the login — the secret is
   what proves a request came through it.
4. **Key your data on `X-authentik-uid`**, never on email. Do not store email or name.
5. **One container** serves UI and API, as for every vibe-deploy app.

---

## 1. Who does what

| Step | Who |
|---|---|
| Everything: code, `vd deploy --auth`, verification | **You, the agent** |
| Decide who may use the app; grant and revoke with `vd access` | **The app's owner** (you run it on their word) |

`vd deploy --auth` creates that group, the Authentik application and everything else itself,
and prints the group name in its output (`auth.group`). Tell the user, in their language:

> The app is behind platform login. Nobody can open it until you give them access — tell me
> who, by their platform email, and I run `vd access <name> add <email>`. Removing someone
> (`vd access <name> remove <email>`) ends their access when their sign-in expires, within
> `auth.ttl` (an hour by default); to lock someone out immediately, a platform admin
> deactivates their account.

Access is granted and revoked by the app's owner with `vd access` — no platform admin needed:

```bash
ssh vd-server "vd access <name> list --json"
ssh vd-server "vd access <name> add person@example.com --json"
ssh vd-server "vd access <name> remove person@example.com --json"
# the app's database MCP (after vd mcp-oauth / --mcp-oauth): add --mcp; removal bites within 5 minutes
ssh vd-server "vd access <name> add person@example.com --mcp --json"
```

Only ever for an address the user gave you. `NO_ACCOUNT` means the person has no platform
account yet — a platform admin invites them first. `ACCESS_FORBIDDEN` means vd has no rights on
that group (older apps) — a platform admin grants them.

Until someone is in the group, the app shows "access denied" to everyone — that is correct.

---

## 2. What happens on a request

```
browser → <name>.apps… → platform login? no session → Authentik login page → back, signed in
                       → member of vibe-<name>? no → Authentik "access denied" (your app never sees it)
                       → yes → your app receives the request with identity headers attached
```

The sign-in lasts `--auth-ttl` (default `hours=1`). After that the platform checks again with
Authentik — silently, without a password, as long as the person's platform session (30 days)
is alive: a page load just goes round and comes back, and an SPA's API calls renew through the
`api()` helper in §6. No token ever reaches the browser, and your app stores none.

---

## 3. What your app receives

| Header | Meaning | Use |
|---|---|---|
| `X-Vibe-Ingress` | secret set by the platform on every request that passed login | **must equal `VIBE_INGRESS_SECRET`**, else answer `401` |
| `X-authentik-uid` | stable user id (64 hex chars) | your tables' user key |
| `X-authentik-email` | email | display, and `APP_ADMINS` (§7b) — never a key |
| `X-authentik-name` | full name | display only — never a key |
| `X-authentik-username` | username | display only |
| `X-authentik-groups` | `\|`-separated group names | **do not use** — not your app's roles (§7b) |

Read them through the `header()` helper in §4/§5, never raw: the values are UTF-8 but arrive
decoded as latin-1, so any non-ASCII name — Cyrillic included — turns into mojibake otherwise.

`VIBE_INGRESS_SECRET` is injected into the container by `vd deploy --auth`. Do not put it in
`.env` yourself and do not log it.

`X-authentik-uid` differs between the test and production platforms (separate installations)
and is otherwise permanent — it survives redeploys.

---

## 4. Python — FastAPI

```python
# auth.py — the whole of it.
import hmac
import os
from fastapi import HTTPException, Request

INGRESS_SECRET = os.environ["VIBE_INGRESS_SECRET"]


def header(request: Request, name: str) -> str:
    """Identity headers arrive as UTF-8 bytes, but HTTP headers are read as
    latin-1 — so "Дамир" would come out as "Ð\x94Ð°Ð¼Ð¸Ñ\x80". Undo that; keep the
    raw value if it was not UTF-8 after all."""
    raw = request.headers.get(name, "")
    try:
        return raw.encode("latin-1").decode("utf-8")
    except (UnicodeEncodeError, UnicodeDecodeError):
        return raw


def current_user(request: Request) -> dict:
    """Dependency for every route. Rejects anything that did not come through
    platform login — including a neighbouring container calling us directly."""
    # Bytes, not str: compare_digest raises TypeError on a non-ASCII str, which a
    # client can send and which would surface as a 500 instead of a 401.
    got = request.headers.get("x-vibe-ingress", "").encode("latin-1")
    if not hmac.compare_digest(got, INGRESS_SECRET.encode()):
        raise HTTPException(401, "not signed in")
    uid = header(request, "x-authentik-uid")
    if not uid:
        raise HTTPException(401, "not signed in")
    return {
        "uid": uid,
        "email": header(request, "x-authentik-email"),
        "name": header(request, "x-authentik-name"),
    }
```

```python
# main.py
from fastapi import Depends, FastAPI
from auth import current_user

app = FastAPI()


@app.get("/api/me")
def me(user: dict = Depends(current_user)):
    return user
```

`GET /` may stay without the dependency: the platform health check calls it from inside the
container, and browsers can only reach it through login anyway. Declare it for **`HEAD` as
well** — the health check is `wget --spider`, which sends `HEAD`, and a plain `@app.get("/")`
answers it `405`, so the deploy fails `UNHEALTHY`:

```python
@app.api_route("/", methods=["GET", "HEAD"])
def index():
    return "ok"
```

---

## 5. Node — Express

```js
// auth.js — the whole of it.
import { timingSafeEqual } from 'node:crypto'

const SECRET = Buffer.from(process.env.VIBE_INGRESS_SECRET)

// Identity headers arrive as UTF-8 bytes, but Node reads header values as
// latin-1 — so "Дамир" would come out as mojibake. Undo that; keep the raw value
// if it was not UTF-8 after all.
function header(req, name) {
  const raw = req.get(name) || ''
  const text = Buffer.from(raw, 'latin1').toString('utf8')
  return text.includes('\uFFFD') && !raw.includes('\uFFFD') ? raw : text
}

// Guard every route. Rejects anything that did not come through platform
// login — including a neighbouring container calling us directly.
export function requireUser(req, res, next) {
  const got = Buffer.from(req.get('x-vibe-ingress') || '')
  const uid = header(req, 'x-authentik-uid')
  if (got.length !== SECRET.length || !timingSafeEqual(got, SECRET) || !uid) {
    return res.status(401).json({ error: 'not_signed_in' })
  }
  req.user = {
    uid,
    email: header(req, 'x-authentik-email'),
    name: header(req, 'x-authentik-name'),
  }
  next()
}
```

```js
// server.js
import express from 'express'
import { requireUser } from './auth.js'

const app = express()
app.get('/', (req, res) => res.send('ok'))   // health check; Express answers HEAD for GET routes
app.get('/api/me', requireUser, (req, res) => res.json(req.user))
app.listen(3000, '0.0.0.0')
```

(`"type": "module"` in `package.json`. No dependencies beyond `express`.)

---

## 6. The browser side

The sign-in lasts an hour. When it lapses, the platform answers your SPA's `fetch` with a
redirect towards Authentik, which `fetch` cannot follow across origins. Route **every** API call
through this helper; it renews silently and the person never notices:

```js
// api.js — the only frontend auth code the app needs.
const RETRYABLE = new Set(['GET', 'HEAD'])

export async function api(path, init = {}) {
  const call = () => fetch(path, { ...init, credentials: 'same-origin', redirect: 'manual' })
  let r = await call()
  if (r.type !== 'opaqueredirect') return check(r)

  // The sign-in lapsed. One silent round through the platform session sets a
  // fresh one: no password, no navigation, the page and its unsaved state stay.
  await fetch('/outpost.goauthentik.io/start?rd=' + encodeURIComponent(location.pathname),
              { mode: 'no-cors', credentials: 'include' })
  if (!RETRYABLE.has((init.method || 'GET').toUpperCase())) {
    // Never replay a write on your own: it may or may not have happened.
    throw new Error('Your session was renewed — please repeat the action.')
  }
  r = await call()
  if (r.type === 'opaqueredirect') {   // the platform session itself is gone
    location.reload()                  // full navigation → the sign-in page
    return new Promise(() => {})
  }
  return check(r)
}

function check(r) {
  // A 401 comes from your own guard, not the platform: the request never went
  // through login at all. Navigating would loop; this is a misconfiguration.
  if (r.status === 401) throw new Error('not signed in — the app is misconfigured, contact the admin')
  return r
}
```

- `redirect: 'manual'` is what makes the bounce visible (`opaqueredirect`) instead of an
  anonymous network error. Your own API should not redirect, so nothing legitimate is lost.
- The renewal is one `no-cors` round trip to the platform's `start` endpoint: with a live
  platform session it sets a fresh sign-in cookie in about a second. It is tried **once**; if the
  retry is bounced again, the platform session is over and a reload shows the sign-in page.
- Writes (`POST`, `PUT`, `PATCH`, `DELETE`) are **not** repeated after a renewal — show the
  error and let the person press the button again. A retried write could run twice.
- Only the bounce means "sign in again". A `401` is your own guard, and navigating on it loops.
- `rd` is a path on this app's host (a full URL on the same host works too); the platform
  refuses anything pointing elsewhere.
- **Log out** is a navigation too: `location.assign('/outpost.goauthentik.io/sign_out')`. It
  ends the platform session for **every** platform app; other apps the person has open keep
  working until their own sign-in lapses, then ask for the password.
- Warn before navigating away from unsaved input: the reload above only happens when the whole
  platform session has ended (30 days, or someone signed out), but then it does happen.

---

## 7. Per-user data

Key rows on `X-authentik-uid` with a reversible migration (Prisma / Alembic / Django), created
lazily after the guard passes:

```python
await db.execute(
    "INSERT INTO app_users (uid) VALUES (%s) ON CONFLICT (uid) DO NOTHING", (user["uid"],)
)
```

Anything finer than "may use the app" — ownership, teams, per-record rights, roles — you model
yourself on that key. Authentik only decides who gets in. Roles: §7b.

---

## 7b. Roles inside the app

"Only `tasks-admin` may open `/admin`" is **your app's** job. The platform has no roles for your
app and will not get any: Authentik holds exactly **one** group per app, `vibe-<name>`, which
decides who gets in at all, and `vd access` manages only that group. Do not ask the user or a
platform admin for extra groups or roles, and do not read `X-authentik-groups` for roles — it
says nothing about your app. If an example or prompt implies platform roles, build them here.

The pattern: a table of roles keyed on `uid`, the first admins from an env var, a guard, and an
`/admin` page where admins grant roles to people who have opened the app at least once.

**Migration** (reversible; Alembic `upgrade`/`downgrade`, a Prisma migration, or Django — same SQL):

```sql
-- up
ALTER TABLE app_users
  ADD COLUMN last_email text,          -- display cache, refreshed on every request
  ADD COLUMN last_name  text,          -- never a key, never used to authorize
  ADD COLUMN last_seen  timestamptz;
CREATE TABLE app_roles (
  uid        text NOT NULL REFERENCES app_users(uid) ON DELETE CASCADE,
  role       text NOT NULL,
  granted_by text NOT NULL,            -- uid of the admin
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (uid, role)
);
-- down
DROP TABLE app_roles;
ALTER TABLE app_users DROP COLUMN last_email, DROP COLUMN last_name, DROP COLUMN last_seen;
```

**The first admins** come from `APP_ADMINS` in the app's `.env` — comma-separated platform
emails, e.g. `APP_ADMINS=anna@example.com,boris@example.com`, passed with `--env-file`. They are
compared case-insensitively against `X-authentik-email` on each request; nothing about them is
stored. That is safe as config because people cannot change their own email on this platform.
`APP_ADMINS` are always admins, so the app cannot lock itself out. An admin passes every role
check. Keep the list of roles in code; reject any other name.

**Python (FastAPI, psycopg):**

```python
# roles.py — on top of current_user from auth.py
import os
import psycopg
from fastapi import Depends, HTTPException, Request
from auth import current_user

ROLES = {"admin", "tasks-admin"}          # every role the app knows
ADMINS = {e.strip().lower() for e in os.environ.get("APP_ADMINS", "").split(",") if e.strip()}
DB = os.environ["DATABASE_URL"]


def q(sql: str, args: tuple = ()) -> list:
    # ponytail: a connection per call; use a pool once traffic matters.
    with psycopg.connect(DB) as c:
        cur = c.execute(sql, args)
        return cur.fetchall() if cur.description else []


def user(u: dict = Depends(current_user)) -> dict:
    """current_user plus a row in app_users, so admins can find the person."""
    q("""INSERT INTO app_users (uid, last_email, last_name, last_seen) VALUES (%s, %s, %s, now())
         ON CONFLICT (uid) DO UPDATE SET last_email = EXCLUDED.last_email,
           last_name = EXCLUDED.last_name, last_seen = now()""",
      (u["uid"], u["email"], u["name"]))
    return u


def is_admin(u: dict) -> bool:
    return u["email"].lower() in ADMINS or bool(
        q("SELECT 1 FROM app_roles WHERE uid = %s AND role = 'admin'", (u["uid"],)))


def require_role(role: str):
    assert role in ROLES, role
    def guard(u: dict = Depends(user)) -> dict:
        if is_admin(u) or q("SELECT 1 FROM app_roles WHERE uid = %s AND role = %s", (u["uid"], role)):
            return u
        raise HTTPException(403, "forbidden")
    return guard
```

```python
# main.py — the admin API; the /admin page calls it through the api() helper (§6)
from fastapi import Depends, HTTPException, Request
from roles import ROLES, q, require_role, user


@app.get("/api/admin/users")
def admin_users(admin: dict = Depends(require_role("admin"))):
    rows = q("""SELECT u.uid, u.last_email, u.last_name, u.last_seen,
                       coalesce(array_agg(r.role) FILTER (WHERE r.role IS NOT NULL), '{}')
                FROM app_users u LEFT JOIN app_roles r USING (uid)
                GROUP BY u.uid ORDER BY u.last_seen DESC NULLS LAST""")
    return [{"uid": r[0], "email": r[1], "name": r[2], "last_seen": r[3], "roles": r[4]} for r in rows]


@app.post("/api/admin/roles")
async def admin_roles(request: Request, admin: dict = Depends(require_role("admin"))):
    # JSON only: a cross-site form cannot send it without a preflight, so no CSRF token needed.
    if request.headers.get("content-type", "").split(";")[0] != "application/json":
        raise HTTPException(415, "json only")
    body = await request.json()
    uid, role, grant = body.get("uid"), body.get("role"), body.get("grant")
    if role not in ROLES or not isinstance(grant, bool) or not q("SELECT 1 FROM app_users WHERE uid = %s", (uid,)):
        raise HTTPException(400, "unknown user or role")
    if grant:
        q("INSERT INTO app_roles (uid, role, granted_by) VALUES (%s, %s, %s) ON CONFLICT DO NOTHING",
          (uid, role, admin["uid"]))
    else:
        q("DELETE FROM app_roles WHERE uid = %s AND role = %s", (uid, role))
    return {"ok": True}


@app.get("/api/tasks/admin")
def tasks_admin(u: dict = Depends(require_role("tasks-admin"))):
    ...
```

Use `Depends(user)` (not `current_user`) on the app's ordinary routes too, so everyone who opens
the app appears in the admin list.

**Node (Express, pg):**

```js
// roles.js — on top of requireUser from auth.js
import pg from 'pg'
import { requireUser } from './auth.js'

export const ROLES = new Set(['admin', 'tasks-admin'])   // every role the app knows
const ADMINS = new Set((process.env.APP_ADMINS || '').split(',').map(s => s.trim().toLowerCase()).filter(Boolean))
export const pool = new pg.Pool({ connectionString: process.env.DATABASE_URL })

// Errors reach Express's error handler instead of hanging the request.
const wrap = fn => (req, res, next) => Promise.resolve(fn(req, res, next)).catch(next)

// requireUser plus a row in app_users, so admins can find the person.
export const user = [requireUser, wrap(async (req, res, next) => {
  await pool.query(
    `INSERT INTO app_users (uid, last_email, last_name, last_seen) VALUES ($1, $2, $3, now())
     ON CONFLICT (uid) DO UPDATE SET last_email = $2, last_name = $3, last_seen = now()`,
    [req.user.uid, req.user.email, req.user.name])
  next()
})]

export async function isAdmin(u) {
  if (ADMINS.has(u.email.toLowerCase())) return true
  const r = await pool.query(`SELECT 1 FROM app_roles WHERE uid = $1 AND role = 'admin'`, [u.uid])
  return r.rowCount > 0
}

export function requireRole(role) {
  if (!ROLES.has(role)) throw new Error('unknown role ' + role)
  return [...user, wrap(async (req, res, next) => {
    if (await isAdmin(req.user)) return next()
    const r = await pool.query('SELECT 1 FROM app_roles WHERE uid = $1 AND role = $2', [req.user.uid, role])
    if (r.rowCount === 0) return res.status(403).json({ error: 'forbidden' })
    next()
  })]
}
```

```js
// server.js — the admin API; the /admin page calls it through the api() helper (§6)
import { ROLES, pool, requireRole, user } from './roles.js'

app.use(express.json())   // only parses application/json — a cross-site form cannot send it

app.get('/api/admin/users', requireRole('admin'), async (req, res) => {
  const r = await pool.query(
    `SELECT u.uid, u.last_email AS email, u.last_name AS name, u.last_seen,
            coalesce(array_agg(r.role) FILTER (WHERE r.role IS NOT NULL), '{}') AS roles
     FROM app_users u LEFT JOIN app_roles r USING (uid)
     GROUP BY u.uid ORDER BY u.last_seen DESC NULLS LAST`)
  res.json(r.rows)
})

app.post('/api/admin/roles', requireRole('admin'), async (req, res) => {
  const { uid, role, grant } = req.body || {}
  if (!req.is('application/json') || !ROLES.has(role) || typeof grant !== 'boolean') {
    return res.status(400).json({ error: 'bad_request' })
  }
  const known = await pool.query('SELECT 1 FROM app_users WHERE uid = $1', [uid])
  if (known.rowCount === 0) return res.status(400).json({ error: 'unknown_user' })
  if (grant) {
    await pool.query('INSERT INTO app_roles (uid, role, granted_by) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING',
      [uid, role, req.user.uid])
  } else {
    await pool.query('DELETE FROM app_roles WHERE uid = $1 AND role = $2', [uid, role])
  }
  res.json({ ok: true })
})

app.get('/api/tasks/admin', requireRole('tasks-admin'), (req, res) => { /* … */ })
```

Use `...user` on the app's ordinary routes too, so everyone who opens the app appears in the
admin list. (Express 4: these routes are async — keep the `wrap()`; Express 5 handles it itself.)

The `/admin` page itself: a table from `GET /api/admin/users`, a checkbox per role, each change a
`POST /api/admin/roles` with `{uid, role, grant}` via `api()`. Someone who has never opened the
app is not in the list — ask them to open it once, or add their email to `APP_ADMINS` if they
are to be an admin. Revoking a role takes effect on their next request. Taking someone out of the
app altogether is `vd access <name> remove <email>`, not a role.

---

## 8. Deploy

```bash
tar cf - --exclude='node_modules' --exclude='.git' --exclude='__pycache__' --exclude='.venv' --exclude='venv' --exclude='.next' ./<name> \
  | ssh vd-server "vd push <name> --json"

ssh vd-server "vd deploy /opt/vibe-deploy/push/<name> --name <name> --auth --db postgres --json"
```

- The JSON carries an `auth` block: `group`, `ttl`, and the sentence to relay to the user (§1).
- `--auth` is **sticky**: later deploys keep it without the flag. The only way to make the app
  public again is `vd destroy`, then deploy without `--auth`.
- `--auth-ttl hours=1` (or any `days=/hours=/minutes=`) shortens the sign-in. Use a short one
  for apps deployed with `--db prod-ro` — they show patient data, and vd refuses more than `hours=1`.
- `vd status <name> --json` reports `auth.state`: `ok`, `broken` (redeploy fixes it),
  `drift` (header auth in Authentik differs from what vd deployed, see below) or `unknown`
  (Authentik unreachable from the server).

### Service accounts: `--auth-bearer` (off by default)

Only when a script, not a person, must call the app (an export job, a cron elsewhere). With
`--auth-bearer` the outpost also accepts `Authorization: Bearer <token>` — a client_credentials
token issued by **this app's** provider — and `Authorization: Basic`, from accounts in the app's
group. A platform admin creates the service account and puts it in `vibe-<name>`.

- The outpost handles both headers. Your app writes no token code and sees the service account
  in the same `X-authentik-uid/-email/-name` headers as a person, with the same `X-Vibe-Ingress`.
- **Limiting it is your app's job.** The group admits the account to every route. Recognise it
  by `X-authentik-uid` (or email) and allow only what it needs, e.g. `GET` on the export
  endpoints; answer 403 to everything else.
- **It also opens Basic to people — that is how a person scripts the app.** The outpost accepts
  an Authentik *app password* as `Authorization: Basic <username>:<app password>`. Any member of
  the group can create one for themselves in their Authentik settings (self-service, revocable
  there), and their own scripts then reach the app as them, skipping the login page (and MFA,
  once there is MFA). Treat every member as able to script the app. Turn the flag on only for
  apps where that is acceptable, and tell the user. App passwords work **only** on this header
  path: since 2026-10-01 the platform's login page no longer accepts them as a password.
- **Revoking an app password** (or removing the person from the group) bites within about a
  minute: the outpost caches a successful Basic check for 60 s.
- **A failed header check is a `302` to the login page, not a `401`.** Scripts must treat any
  redirect to `auth.<platform>` as "credentials rejected" — `curl -f` alone will not notice;
  check the status code (or use `--max-redirs 0`) and fail on `3xx`.
- Sticky: redeploys keep it; `--auth-bearer=false` turns it off. Needs `--auth`
  (`AUTH_BEARER_REQUIRES_AUTH` otherwise). The deploy JSON's `auth.bearer` and `vd status`
  report it.

Verify in a browser: open the app → platform login → back in the app; `/api/me` shows the
right person; a second browser profile that is not in the group gets "access denied".

---

## 9. Checklist

- [ ] No login, signup or password code anywhere in the app.
- [ ] Every route that serves data uses the guard; the guard checks **`X-Vibe-Ingress`** first.
- [ ] Constant-time comparison on **bytes** (`hmac.compare_digest(a.encode(...), b.encode())` / `timingSafeEqual`).
- [ ] User rows keyed on `X-authentik-uid`; email/name at most as a refreshed display cache, never a key or a permission.
- [ ] Roles, if any, live in the app's own `app_roles` (§7b) — none requested from the platform, none read from `X-authentik-groups`.
- [ ] SPA uses `redirect: 'manual'`; **only** `opaqueredirect` navigates to `/outpost.goauthentik.io/start?rd=<full URL>`, a `401` is an error.
- [ ] Logout navigates to `/outpost.goauthentik.io/sign_out`.
- [ ] Deployed with `--auth`, subdomain routing; group name relayed to the user.

---

## 10. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `unknown flag: --auth` | The server's vd predates forward auth. Use the [fallback](#fallback-server-side-oidc--only-when---auth-is-unavailable). |
| `AUTH_NOT_CONFIGURED` | The server was never set up for Authentik. A platform admin runs `vd init --authentik-url … --authentik-internal …`. Until then, use the fallback. |
| `AUTH_FAILED` | Authentik rejected the setup. Nothing was deployed on the server. If the group binding could not be made, vd also takes the app off the platform login service, so it answers `404` instead of being open to everyone — `details` says what it undid. Retry once; if it repeats, give the `details` to the platform admin. |
| `AUTH_REQUIRES_SUBDOMAIN` | Drop `--routing path`. |
| Every request to your API is `401` | The guard's secret check fails — you compared against a hardcoded value or a stale `.env`. Read `VIBE_INGRESS_SECRET` from the environment. |
| Everyone gets Authentik's "access denied" | Nobody is in `vibe-<name>` yet: `vd access <name> add <email>` on the user's word (§1). |
| SPA shows network errors after a while | Missing `redirect: 'manual'`, so the login bounce looks like an outage (§6). |
| SPA calls fail about an hour after the page was opened, a reload fixes it | API calls do not go through the `api()` helper, so the hourly renewal never happens (§6). |
| "Your session was renewed — please repeat the action" | Expected, rare: a write hit the hourly renewal and was deliberately not repeated. The person presses the button again. |
| `ROLLBACK_WOULD_UNPROTECT` | The previous version was public; rolling back would publish it. Fix forward and redeploy with `--auth`. |
| `404` from the app for a few minutes right after the **first** `--auth` deploy | The platform's login service picks up new apps on a 5-minute refresh. `vd status` shows `auth.state: ok` already; wait five minutes and retry before debugging anything. |
| Names show as `Ð Ð°Ð¼Ð¸Ñ…` | Headers read raw. Use the `header()` helper (§4/§5): UTF-8 bytes decoded as latin-1. |
| After typing the password the person lands in Authentik's own screens, not the app | They are not in `vibe-<name>` yet. Authentik answers with its "access denied" page and its links lead into Authentik. `vd access <name> add <email>`, then have them open the app's address again. |
| A user asks for a role "in the platform" (`tasks-admin`, `editor`, …) | There are none. Build it in the app (§7b); never ask an admin for extra groups. |
| "Log out" ends on the Authentik login page instead of back in the app | The server has no `vibe-provider-invalidation-flow` yet (`vd deploy` warns about it). Logout still works; only the landing page differs. Once a platform admin creates the flow, the next deploy picks it up. |
| `vd status` says `auth.state: broken` | Something was removed or changed in Authentik by hand — look at `auth.authentik`. `binding: false` is the serious one: without an enabled binding to `vibe-<name>` the app is open to **every** signed-in account. Redeploy — vd recreates or repairs it. |

---

# Fallback: server-side OIDC — only when `--auth` is unavailable

Use this **only** if the server cannot do `--auth` (see the note at the top). Here the app does
the login itself: it is a confidential OIDC client that runs the authorization code flow on the
server, keeps no tokens, and gives the browser its own signed session cookie. It costs ~60 lines
and one extra human step — the platform admin registers the app in Authentik (§F1).

Same hard rules as above for signup, subdomain routing and one container. Additionally: **no
refresh tokens, no token storage, no `offline_access`**, and **secrets in `.env` only**.

## F1. Who does what

| Step | Who |
|---|---|
| Create the OIDC provider, application and access group in Authentik; hand over `client_id` + `client_secret` | **The platform admin — the only human step** |
| Everything else: app name, `.env`, login/callback/logout routes, group check, session cookie, `sub`-keyed data, deploy, verification | **You, the agent** |

Pick the app name **first** (lowercase, starts with a letter, 2–63 chars, `a-z 0-9 -`). It fixes
the app URL, the redirect URI and the access group name, and all three are registered by the
admin. Changing the name later breaks login.

### The admin's checklist — send this to the user, in their language

> **Create in Authentik** (test: `https://auth.test.platform.acuradai.com`,
> prod: `https://auth.platform.acuradai.com`):
>
> 1. **Group** `vibe-<name>` — everyone who may use the app becomes a member. Membership is
>    the access grant; the app checks nothing else. (An admin creates this group by hand, so
>    `vd access` cannot manage it: in the fallback, the admin adds and removes people.)
> 2. **Provider**, type *OAuth2/OpenID*:
>    - Client type: **Confidential**
>    - Redirect URIs (both, exact, strict match):
>      `https://<name>.apps.platform.acuradai.com/auth/callback`
>      `https://<name>.apps.platform.acuradai.com/`
>      (the second one is the post-logout return — Authentik validates it against this same list)
>    - Signing key: any **RS256** certificate
>    - Subject mode: **`user_uuid`** (stable identifier; the app stores data under it)
>    - Scopes: the default `openid`, `profile`, `email` mappings — `profile` is what carries
>      the `groups` claim the app gates on
>    - Invalidation flow: **`default-provider-invalidation-flow`** — this is what actually ends
>      the SSO session on logout. Without it "log out" only closes the app's own session and
>      the next visit signs the person straight back in.
> 3. **Application**, slug **`<name>`**, bound to that provider, with a policy binding to the
>    group `vibe-<name>` so that only its members can authorize.
> 4. Send back: **`client_id`**, **`client_secret`**, and the **issuer URL**
>    `https://<authentik-host>/application/o/<name>/`.

Until this exists you can still build and deploy the app — nobody can sign in yet, that is all.

---

## F2. `.env` — six variables, nothing in source

```bash
# .env  — pushed with the app, injected via `vd deploy --env-file`. NEVER commit.
OIDC_ISSUER=https://auth.test.platform.acuradai.com/application/o/<name>/
OIDC_CLIENT_ID=<from the admin>
OIDC_CLIENT_SECRET=<from the admin>
SESSION_SECRET=<generate: openssl rand -hex 32>
APP_GROUP=vibe-<name>
APP_BASE_URL=https://<name>.apps.platform.acuradai.com
```

- `OIDC_ISSUER` is the **per-application** issuer, with the slug in it. Discovery lives at
  `${OIDC_ISSUER}/.well-known/openid-configuration` — the libraries below fetch it themselves,
  so no other Authentik URL is ever hardcoded.
- `APP_BASE_URL` exists so the redirect URI is a **constant** that matches the admin's
  registration character for character. Do not build it from the incoming request: behind
  nginx + Traefik the app sees `http`, and the resulting `http://…/auth/callback` is rejected
  by Authentik with `redirect_uri` mismatch.
- `SESSION_SECRET` signs your own cookie. Rotating it logs everyone out — harmless.
- Also write `.env.example` (committed, placeholders only) and create `.gitignore` **first**,
  listing at least `.env`, `.env.*`, `*.pem`, `*.key`, `node_modules/`, `__pycache__/`, `.venv/`.

---

## F3. The session model

```
browser ──GET /private──▶ app: no cookie → 302 /auth/login
        ──/auth/login──▶ app → 302 Authentik authorize (PKCE + state + nonce)
                               Authentik: live SSO session? → straight back, no password
        ──/auth/callback─▶ app: exchange code (server-to-server), read ID token claims,
                                check APP_GROUP ∈ groups, drop the tokens,
                                set its own cookie → 302 back to /private
```

The cookie is **HttpOnly, Secure, SameSite=Lax**, holds `sub`, `email`, `name`, is signed with
`SESSION_SECRET`, and expires after **1 hour, sliding** — every authenticated request re-signs
it, so an active person is never interrupted, and an idle one silently re-enters through the
SSO session. Access removal therefore takes effect within an hour, the same bound reporting
lives with: remove someone from `vibe-<name>` and their next round trip through Authentik is
denied.

Do not extend the TTL beyond an hour, and do not make the cookie permanent — the hour *is* the
revocation contract.

That cookie is the **only** session state anywhere: no server-side store, nothing in the
database, nothing on disk. A redeploy therefore costs nothing to protect — at worst everyone
signs in again, silently, through the SSO session. Same for rotating `SESSION_SECRET`.

---

## F4. Python — FastAPI + Authlib

```
# requirements.txt
authlib
itsdangerous
httpx
```

```python
# auth.py — all authentication lives in this file.
import os
from fastapi import APIRouter, Request, HTTPException
from fastapi.responses import HTMLResponse, RedirectResponse, JSONResponse
from authlib.integrations.starlette_client import OAuth, OAuthError

ISSUER       = os.environ["OIDC_ISSUER"].rstrip("/")
APP_GROUP    = os.environ["APP_GROUP"]
APP_BASE_URL = os.environ["APP_BASE_URL"].rstrip("/")
REDIRECT_URI = f"{APP_BASE_URL}/auth/callback"     # must match the admin's registration exactly

oauth = OAuth()
oauth.register(
    name="authentik",
    server_metadata_url=f"{ISSUER}/.well-known/openid-configuration",
    client_id=os.environ["OIDC_CLIENT_ID"],
    client_secret=os.environ["OIDC_CLIENT_SECRET"],
    client_kwargs={"scope": "openid profile email", "code_challenge_method": "S256"},
)

router = APIRouter()

# Never redirect back to /auth/login from the callback: if authorization keeps
# failing (access_denied, a provider misconfiguration, cookies blocked) that is an
# infinite bounce with no way out for a person who cannot read a URL bar.
FAILED = """<!doctype html><meta charset=utf-8><title>Sign-in failed</title>
<p>Sign-in did not complete. <a href="/auth/login">Try again</a>.</p>"""


def safe_rd(rd: str) -> str:
    """Same-site returns only. `//evil.com` and `/\\evil.com` are protocol-relative
    URLs — they start with `/` and still leave the site."""
    return rd if rd.startswith("/") and rd[1:2] not in ("/", "\\") else "/"


@router.get("/auth/login")
async def login(request: Request, rd: str = "/"):
    request.session["rd"] = safe_rd(rd)
    return await oauth.authentik.authorize_redirect(request, REDIRECT_URI)


@router.get("/auth/callback")
async def callback(request: Request):
    try:
        token = await oauth.authentik.authorize_access_token(request)   # server-to-server
    except OAuthError:
        request.session.clear()
        return HTMLResponse(FAILED, status_code=400)

    claims = token["userinfo"]                       # verified ID-token claims
    if APP_GROUP not in (claims.get("groups") or []):
        request.session.clear()
        return JSONResponse(
            {"error": "no_access", "group": APP_GROUP}, status_code=403
        )

    rd = request.session.get("rd", "/")
    request.session.clear()                          # drops state/nonce; tokens are never stored
    request.session.update({
        "sub": claims["sub"],                        # stable id — your link key
        "email": claims.get("email"),
        "name": claims.get("name"),
    })
    return RedirectResponse(rd)


@router.get("/auth/logout")
async def logout(request: Request):
    request.session.clear()
    meta = await oauth.authentik.load_server_metadata()
    return RedirectResponse(
        f"{meta['end_session_endpoint']}?post_logout_redirect_uri={APP_BASE_URL}/"
    )


def current_user(request: Request) -> dict:
    """Dependency for every protected route. Returns 401 JSON — never a redirect (§F6)."""
    sub = request.session.get("sub")
    if not sub:
        raise HTTPException(401, "not signed in")
    return {"sub": sub,
            "email": request.session.get("email"),
            "name": request.session.get("name")}
```

```python
# main.py
import os
from fastapi import Depends, FastAPI
from starlette.middleware.sessions import SessionMiddleware
from auth import router as auth_router, current_user

app = FastAPI()

# The signed cookie IS the session: no server-side store, nothing to lose on redeploy.
# max_age enforces the 1-hour expiry on the server side when the cookie is read, and the
# middleware re-sends the cookie on every response while the session is non-empty — that is
# what makes it sliding. httponly is always on; https_only adds Secure.
app.add_middleware(
    SessionMiddleware,
    secret_key=os.environ["SESSION_SECRET"],
    max_age=3600,
    same_site="lax",
    https_only=True,
)
app.include_router(auth_router)


@app.get("/api/me")
async def me(user: dict = Depends(current_user)):
    return user
```

Every protected route takes `user: dict = Depends(current_user)`. `GET /` must stay public —
it is the health check (§F8).

---

## F5. Node — Express + openid-client

```jsonc
// package.json — "type": "module" is required (top-level await below)
{ "type": "module",
  "dependencies": { "express": "^4", "openid-client": "^6", "jose": "^5", "cookie-parser": "^1" } }
```

```js
// auth.js — all authentication lives in this file.
import * as client from 'openid-client'
import * as jose from 'jose'
import cookieParser from 'cookie-parser'

const { OIDC_ISSUER, OIDC_CLIENT_ID, OIDC_CLIENT_SECRET,
        SESSION_SECRET, APP_GROUP, APP_BASE_URL } = process.env

const BASE = APP_BASE_URL.replace(/\/$/, '')
const REDIRECT_URI = `${BASE}/auth/callback`   // must match the admin's registration exactly
const TTL = 3600                               // 1 hour, sliding
const KEY = new TextEncoder().encode(SESSION_SECRET)
const COOKIE = { httpOnly: true, secure: true, sameSite: 'lax', path: '/' }

// Never redirect back to /auth/login from the callback: if authorization keeps
// failing (access_denied, a provider misconfiguration, cookies blocked) that is an
// infinite bounce with no way out for a person who cannot read a URL bar.
const FAILED = `<!doctype html><meta charset=utf-8><title>Sign-in failed</title>
<p>Sign-in did not complete. <a href="/auth/login">Try again</a>.</p>`

// Same-site returns only: `//evil.com` and `/\evil.com` are protocol-relative URLs —
// they start with `/` and still leave the site.
const safeRd = (rd) => (/^\/(?![/\\])/.test(rd) ? rd : '/')

const config = await client.discovery(
  new URL(OIDC_ISSUER), OIDC_CLIENT_ID, OIDC_CLIENT_SECRET)

// The signed JWT cookie IS the session — no server-side store.
async function put(res, name, payload, ttl) {
  const jwt = await new jose.SignJWT(payload)
    .setProtectedHeader({ alg: 'HS256' })
    .setExpirationTime(`${ttl}s`)
    .sign(KEY)
  res.cookie(name, jwt, { ...COOKIE, maxAge: ttl * 1000 })
}
async function read(req, name) {
  try { return (await jose.jwtVerify(req.cookies[name] || '', KEY)).payload }
  catch { return null }
}

export function mountAuth(app) {
  app.use(cookieParser())

  app.get('/auth/login', async (req, res) => {
    const verifier  = client.randomPKCECodeVerifier()
    const challenge = await client.calculatePKCECodeChallenge(verifier)
    const nonce     = client.randomNonce()
    const state     = client.randomState()
    // 10 minutes is one round trip; this cookie carries no identity.
    await put(res, 'vd_oidc',
      { verifier, nonce, state, rd: safeRd(String(req.query.rd || '/')) }, 600)
    res.redirect(client.buildAuthorizationUrl(config, {
      redirect_uri: REDIRECT_URI,
      scope: 'openid profile email',
      code_challenge: challenge, code_challenge_method: 'S256',
      state, nonce,
    }).href)
  })

  app.get('/auth/callback', async (req, res) => {
    const tmp = await read(req, 'vd_oidc')
    if (!tmp) return res.status(400).send(FAILED)   // cookie blocked or stale callback
    res.clearCookie('vd_oidc', COOKIE)

    let claims
    try {
      const tokens = await client.authorizationCodeGrant(   // server-to-server
        config, new URL(req.originalUrl, BASE),
        { pkceCodeVerifier: tmp.verifier, expectedNonce: tmp.nonce, expectedState: tmp.state })
      claims = tokens.claims()                              // tokens are dropped right here
    } catch { return res.status(400).send(FAILED) }

    if (!(claims.groups || []).includes(APP_GROUP)) {
      return res.status(403).json({ error: 'no_access', group: APP_GROUP })
    }
    await put(res, 'vd_session',
      { sub: claims.sub, email: claims.email, name: claims.name }, TTL)
    res.redirect(tmp.rd)
  })

  app.get('/auth/logout', (req, res) => {
    res.clearCookie('vd_session', COOKIE)
    res.redirect(client.buildEndSessionUrl(config,
      { post_logout_redirect_uri: `${BASE}/` }).href)
  })
}

// Guard every protected route. 401 JSON — never a redirect (§F6).
export async function requireUser(req, res, next) {
  const s = await read(req, 'vd_session')
  if (!s) return res.status(401).json({ error: 'not_signed_in' })
  req.user = { sub: s.sub, email: s.email, name: s.name }
  await put(res, 'vd_session', req.user, TTL)     // sliding: re-signed on activity
  next()
}
```

```js
// server.js
import express from 'express'
import { mountAuth, requireUser } from './auth.js'

const app = express()
mountAuth(app)

app.get('/api/me', requireUser, (req, res) => res.json(req.user))

app.listen(3000, '0.0.0.0')   // 0.0.0.0, always — see /vibe
```

`GET /` must stay public — it is the health check (§F8).

(Go: `github.com/coreos/go-oidc/v3/oidc` + `golang.org/x/oauth2`. Same shape — discovery by
issuer, PKCE, verify the ID token, check `groups`, set your own signed cookie, keep no tokens.)

---

## F6. The browser side — two rules

**1. Your API answers `401` JSON. It never redirects an XHR.** A redirect to Authentik is
cross-origin; `fetch` cannot follow it usefully and the failure surfaces as an unnamed network
error. The guard in §F4/§F5 already does the right thing.

**2. The frontend turns a `401` into a full-page navigation:**

```js
async function api(path, init) {
  const r = await fetch(path, { ...init, credentials: 'same-origin' })
  if (r.status === 401) {
    window.location.assign(
      '/auth/login?rd=' + encodeURIComponent(location.pathname + location.search))
    return new Promise(() => {})          // navigation in flight; never resolve
  }
  if (r.status === 403) throw new Error('no access to this app')   // do NOT bounce to login
  return r
}
```

- `403` means *signed in, not in the group* — show "no access", never a login loop.
- Logout is also a navigation: `window.location.assign('/auth/logout')`.
- Plain HTML page routes (not XHR) may simply `302` to `/auth/login` server-side.
- Warn the user before navigating away from unsaved input.

---

## F7. Per-user data — keyed by `sub`

Store per-user rows under the ID-token `sub`. **Do not copy `email`/`name` into your tables** —
they change in Authentik and your copy goes stale; read them from the session (§F4/§F5), which is
refreshed at every login.

vibe-deploy requires a **migration tool** (Prisma for Node, Alembic for Python, Django's
built-in), never raw `CREATE TABLE`, and the migration must be reversible:

```prisma
// Prisma — prisma/schema.prisma  (run `npx prisma migrate deploy` on container start)
model AppUser {
  sub         String   @id          // the ID token's `sub`
  preferences Json     @default("{}")
  createdAt   DateTime @default(now())
}
```

```python
# Alembic — upgrade():   (run `alembic upgrade head` on container start)
op.create_table("app_users",
    sa.Column("sub", sa.Text, primary_key=True),          # the ID token's `sub`
    sa.Column("preferences", postgresql.JSONB, server_default="{}", nullable=False),
    sa.Column("created_at", sa.TIMESTAMP(timezone=True), server_default=sa.text("now()")),
)
# downgrade(): op.drop_table("app_users")
```

Create the row lazily in the handler, after the guard passes, using the auto-injected
`DATABASE_URL` (`--db postgres`):

```python
await db.execute(
    "INSERT INTO app_users (sub) VALUES (%s) ON CONFLICT (sub) DO NOTHING", (user["sub"],)
)
```

Group membership is the access gate. Anything finer — document ownership, team scoping,
per-record permissions — you model yourself, keyed by `sub`. Authentik does not manage it.

---

## F8. Deploy

Build inside the normal `/vibe` constraints, deploy with `/deploy`. Auth-specific rules:

- **`--name` MUST equal the name from §F1** — the URL, the redirect URI and `vibe-<name>` are
  registered against it.
- **Subdomain routing** — never `--routing path` (hard rule 2 at the top).
- **`GET /` stays public.** Traefik health-checks it every 30s with no cookie; if it redirects
  or 401s, that is fine (any HTTP response passes), but do not make it slow or DB-dependent.
- `--db postgres` for the `sub`-keyed table, `--env-file` for `.env`.
- `POLICY_VIOLATION` on deploy means a secret is in source — move it to `.env`.
  `authlib` / `openid-client` / `jose` are not flagged; you do not need `--allow-external`.

```bash
tar cf - --exclude='node_modules' --exclude='.git' --exclude='__pycache__' --exclude='.venv' --exclude='venv' --exclude='.next' ./<name> \
  | ssh vd-server "vd push <name> --json"

ssh vd-server "vd deploy /opt/vibe-deploy/push/<name> --name <name> --db postgres \
  --env-file /opt/vibe-deploy/push/<name>/.env --json"

ssh vd-server "vd status <name> --json"    # the `url` field is the live origin
```

Then verify by hand, in a browser: open the app → Authentik login → back in the app; open it
again in a new tab → in without a password; `/auth/logout` → back at the Authentik login.

---

## F9. Pre-flight checklist

- [ ] App **name** chosen; used for `--name`, the redirect URI and `vibe-<name>`.
- [ ] Admin checklist (§F1) sent to the user; `client_id` / `client_secret` / issuer received.
- [ ] `.gitignore` written **first**; `.env` not committed; `.env.example` committed.
- [ ] All six variables in `.env`; `SESSION_SECRET` freshly generated.
- [ ] Code exchange is server-side; **no token reaches the browser**; nothing stored.
- [ ] `APP_GROUP ∈ claims.groups` checked at callback → `403` when missing.
- [ ] Session cookie: HttpOnly, Secure, SameSite=Lax, **1 h, sliding**.
- [ ] API returns `401` JSON; the frontend does a **top-level navigation** to `/auth/login`.
- [ ] `403` shows "no access" and does **not** bounce to login.
- [ ] `rd` accepted only when it starts with `/` **and not** `//` or `/\` — otherwise it is an
      open redirect off the site.
- [ ] A failed callback answers **400 with a "try again" link**, never a redirect to
      `/auth/login` — that is an infinite bounce.
- [ ] Logout clears the cookie **and** hits the Authentik end-session endpoint.
- [ ] `sub`-keyed table via a reversible migration, run on container start.
- [ ] No `email`/`name` copied into the database.
- [ ] `GET /` public; deployed with **subdomain** routing.

---

## F10. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Authentik: `Invalid redirect URI` | `APP_BASE_URL` does not match the registration exactly — scheme, host, trailing slash, `/auth/callback`. Do not derive it from the request; behind the proxy the app sees `http`. |
| Loop: login → app → login | The session cookie is not coming back. `Secure` requires HTTPS (fine on the platform), `SameSite=Lax` is required — `Strict` drops the cookie on the return from Authentik. Check `SESSION_SECRET` is set and stable. |
| `403 no_access` for someone who should have access | Not a member of `vibe-<name>`, or the provider is missing the `profile` scope mapping, so no `groups` claim arrives at all. Ask the admin to check both. |
| `KeyError: 'userinfo'` (Python) | The provider returned no ID token — the `openid` scope is missing from `client_kwargs`. |
| Unnamed network error in the SPA, no status | You called `/auth/login` with `fetch`. It must be a top-level navigation (§F6). |
| Signed out every hour while actively working | The cookie is not being re-signed. Python: the session must stay non-empty (do not `clear()` it in a guard). Node: `requireUser` must call `put(...)` on every request. |
| Logout returns to the app still signed in | You only cleared the cookie. Hit the Authentik end-session endpoint too — otherwise the SSO session immediately signs the person back in. |
| Logout hits the end-session endpoint and the person is *still* signed in | The provider has no invalidation flow set. Ask the admin for `default-provider-invalidation-flow` (§F1) — the SSO session is ended by that flow's stage, not by the redirect itself, so without it the endpoint returns and the session lives on. |
| `POLICY_VIOLATION` on deploy | `OIDC_CLIENT_SECRET` or `SESSION_SECRET` is in source. Move to `.env`, deploy with `--env-file`. |

---
