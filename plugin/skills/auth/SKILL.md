---
name: auth
description: Add "sign in with the platform account" to a vibe-deploy app. Use when the user wants login / accounts / per-user data in their vibecoded app.
---

# Add platform login to a vibe-deploy app

**IMPORTANT: Always communicate with the user in their language. Detect the language they use and respond in the same language throughout the session.**

The platform's identity provider is **Authentik**. Your app signs people in with the
**OpenID Connect authorization code flow, run entirely on the server side** — the app is a
*confidential client*. It exchanges the code for tokens inside the container, reads the
identity out of the ID token, throws the tokens away, and hands the browser **its own signed
session cookie**. No OAuth token ever reaches the browser. This is the BFF pattern and it is
what the platform owner requires.

You write this once, from the examples below, and it is ~60 lines. The person you are working
with cannot debug auth — follow the file exactly rather than improvising a variant.

Load `/vibe` for platform constraints and `/deploy` for the deploy step. Auth is an **external
integration** with Authentik, not a vd capability.

---

## 0. Hard rules — do not negotiate

1. **No signup screen.** Accounts are created centrally in Authentik. Public signup is
   disabled. Your app has no registration form, no password form, no password reset — the
   person is sent to Authentik and comes back signed in. If the user asks for "register",
   explain that the platform admin adds people (§1).
2. **Subdomain routing only.** Use the `vd deploy` default. **Never** `--routing path`: the
   redirect URI is registered as an exact string, and a path-routed app shares its cookie
   origin with every other path-routed app on the same host — their session cookies would
   collide.
3. **One container, two jobs.** UI and API in the **same** app (Express + static files,
   FastAPI + SPA, Next.js). The login round trip and the API must share an origin.
4. **No refresh tokens, no token storage, no `offline_access`.** After the code exchange the
   app keeps nothing but its own cookie. When the cookie expires, the app bounces the browser
   through Authentik again — with a live SSO session (30 days) that returns without a password
   and without a visible interruption.
5. **Secrets in `.env` only.** `vd deploy` blocks a hardcoded client secret with
   `POLICY_VIOLATION`.

---

## 1. Who does what

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
>    the access grant; the app checks nothing else.
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

## 2. `.env` — six variables, nothing in source

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

## 3. The session model

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

## 4. Python — FastAPI + Authlib

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
    """Dependency for every protected route. Returns 401 JSON — never a redirect (§6)."""
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
it is the health check (§8).

---

## 5. Node — Express + openid-client

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

// Guard every protected route. 401 JSON — never a redirect (§6).
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

`GET /` must stay public — it is the health check (§8).

(Go: `github.com/coreos/go-oidc/v3/oidc` + `golang.org/x/oauth2`. Same shape — discovery by
issuer, PKCE, verify the ID token, check `groups`, set your own signed cookie, keep no tokens.)

---

## 6. The browser side — two rules

**1. Your API answers `401` JSON. It never redirects an XHR.** A redirect to Authentik is
cross-origin; `fetch` cannot follow it usefully and the failure surfaces as an unnamed network
error. The guard in §4/§5 already does the right thing.

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

## 7. Per-user data — keyed by `sub`

Store per-user rows under the ID-token `sub`. **Do not copy `email`/`name` into your tables** —
they change in Authentik and your copy goes stale; read them from the session (§4/§5), which is
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

## 8. Deploy

Build inside the normal `/vibe` constraints, deploy with `/deploy`. Auth-specific rules:

- **`--name` MUST equal the name from §1** — the URL, the redirect URI and `vibe-<name>` are
  registered against it.
- **Subdomain routing** — never `--routing path` (§0).
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

## 9. Pre-flight checklist

- [ ] App **name** chosen; used for `--name`, the redirect URI and `vibe-<name>`.
- [ ] Admin checklist (§1) sent to the user; `client_id` / `client_secret` / issuer received.
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

## 10. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Authentik: `Invalid redirect URI` | `APP_BASE_URL` does not match the registration exactly — scheme, host, trailing slash, `/auth/callback`. Do not derive it from the request; behind the proxy the app sees `http`. |
| Loop: login → app → login | The session cookie is not coming back. `Secure` requires HTTPS (fine on the platform), `SameSite=Lax` is required — `Strict` drops the cookie on the return from Authentik. Check `SESSION_SECRET` is set and stable. |
| `403 no_access` for someone who should have access | Not a member of `vibe-<name>`, or the provider is missing the `profile` scope mapping, so no `groups` claim arrives at all. Ask the admin to check both. |
| `KeyError: 'userinfo'` (Python) | The provider returned no ID token — the `openid` scope is missing from `client_kwargs`. |
| Unnamed network error in the SPA, no status | You called `/auth/login` with `fetch`. It must be a top-level navigation (§6). |
| Signed out every hour while actively working | The cookie is not being re-signed. Python: the session must stay non-empty (do not `clear()` it in a guard). Node: `requireUser` must call `put(...)` on every request. |
| Logout returns to the app still signed in | You only cleared the cookie. Hit the Authentik end-session endpoint too — otherwise the SSO session immediately signs the person back in. |
| Logout hits the end-session endpoint and the person is *still* signed in | The provider has no invalidation flow set. Ask the admin for `default-provider-invalidation-flow` (§1) — the SSO session is ended by that flow's stage, not by the redirect itself, so without it the endpoint returns and the session lives on. |
| `POLICY_VIOLATION` on deploy | `OIDC_CLIENT_SECRET` or `SESSION_SECRET` is in source. Move to `.env`, deploy with `--env-file`. |

---

## 11. Planned: forward auth from the platform

The platform's target is to put an Authentik proxy provider and Traefik `forwardAuth` in front
of the app (`vd deploy --auth`), after which the app reads identity from `X-authentik-*`
headers behind a trusted ingress and **carries no auth code at all**. That needs a vd release
(Traefik file provider, an outpost route per app host, an ingress secret) and does not exist
yet — everything in this skill is what works today. When it lands, this skill is replaced and
apps drop §4/§5 entirely.

Background and the session/revocation contract this skill follows:
auth-service repo, `docs/adr/ADR-005-browser-session-renewal.md` and
`docs/adr/ADR-004-authentik-replaces-auth-service.md`.
