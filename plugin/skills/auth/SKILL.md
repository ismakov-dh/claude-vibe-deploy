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
| Add the people who may use the app to the group **`vibe-<name>`** in Authentik | **The human — the only step** |

`vd deploy --auth` creates that group, the Authentik application and everything else itself,
and prints the group name in its output (`auth.group`). Tell the user, in their language:

> The app is behind platform login. To let someone in, add them to the group
> **`vibe-<name>`** in Authentik (`https://auth.platform.acuradai.com`). Nobody else can
> open it. Removing someone from the group revokes access within `auth.ttl` (a week by
> default); to lock someone out immediately, deactivate their account.

Until someone is in the group, the app shows "access denied" to everyone — that is correct.

---

## 2. What happens on a request

```
browser → <name>.apps… → platform login? no session → Authentik login page → back, signed in
                       → member of vibe-<name>? no → Authentik "access denied" (your app never sees it)
                       → yes → your app receives the request with identity headers attached
```

The sign-in lasts `--auth-ttl` (default `days=7`). After that the platform checks again with
Authentik — silently, without a password, as long as the person's platform session (30 days)
is alive. No token ever reaches the browser, and your app stores none.

---

## 3. What your app receives

| Header | Meaning | Use |
|---|---|---|
| `X-Vibe-Ingress` | secret set by the platform on every request that passed login | **must equal `VIBE_INGRESS_SECRET`**, else answer `401` |
| `X-authentik-uid` | stable user id (64 hex chars) | your tables' user key |
| `X-authentik-email` | email | display only — do not store |
| `X-authentik-name` | full name | display only — do not store |
| `X-authentik-username` | username | display only |
| `X-authentik-groups` | `\|`-separated group names | not needed — access was already decided |

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


def current_user(request: Request) -> dict:
    """Dependency for every route. Rejects anything that did not come through
    platform login — including a neighbouring container calling us directly."""
    got = request.headers.get("x-vibe-ingress", "")
    if not hmac.compare_digest(got, INGRESS_SECRET):
        raise HTTPException(401, "not signed in")
    uid = request.headers.get("x-authentik-uid", "")
    if not uid:
        raise HTTPException(401, "not signed in")
    return {
        "uid": uid,
        "email": request.headers.get("x-authentik-email", ""),
        "name": request.headers.get("x-authentik-name", ""),
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
container, and browsers can only reach it through login anyway.

---

## 5. Node — Express

```js
// auth.js — the whole of it.
import { timingSafeEqual } from 'node:crypto'

const SECRET = Buffer.from(process.env.VIBE_INGRESS_SECRET)

// Guard every route. Rejects anything that did not come through platform
// login — including a neighbouring container calling us directly.
export function requireUser(req, res, next) {
  const got = Buffer.from(req.get('x-vibe-ingress') || '')
  const uid = req.get('x-authentik-uid')
  if (got.length !== SECRET.length || !timingSafeEqual(got, SECRET) || !uid) {
    return res.status(401).json({ error: 'not_signed_in' })
  }
  req.user = {
    uid,
    email: req.get('x-authentik-email') || '',
    name: req.get('x-authentik-name') || '',
  }
  next()
}
```

```js
// server.js
import express from 'express'
import { requireUser } from './auth.js'

const app = express()
app.get('/api/me', requireUser, (req, res) => res.json(req.user))
app.listen(3000, '0.0.0.0')
```

(`"type": "module"` in `package.json`. No dependencies beyond `express`.)

---

## 6. The browser side

When the sign-in lapses, the platform answers your SPA's `fetch` with a redirect to the login
page — which `fetch` cannot follow across origins. Detect it and do a **full-page navigation**:

```js
async function api(path, init) {
  const r = await fetch(path, { ...init, credentials: 'same-origin', redirect: 'manual' })
  if (r.type === 'opaqueredirect' || r.status === 401) {
    // rd must be a full URL on this app's host; the outpost rejects anything else
    location.assign('/outpost.goauthentik.io/start?rd=' + encodeURIComponent(location.href))
    return new Promise(() => {})          // navigating away; never resolve
  }
  return r
}
```

- `redirect: 'manual'` is what makes the bounce visible (`opaqueredirect`) instead of an
  anonymous network error. Your own API should not redirect, so nothing legitimate is lost.
- **Log out** is a navigation too: `location.assign('/outpost.goauthentik.io/sign_out')`. It
  ends the platform session for **every** platform app; other apps the person has open keep
  working until their own sign-in lapses, then ask for the password.
- Warn before navigating away from unsaved input. With the default week-long sign-in this is
  rare, but it happens.

---

## 7. Per-user data

Key rows on `X-authentik-uid` with a reversible migration (Prisma / Alembic / Django), created
lazily after the guard passes:

```python
await db.execute(
    "INSERT INTO app_users (uid) VALUES (%s) ON CONFLICT (uid) DO NOTHING", (user["uid"],)
)
```

Anything finer than "may use the app" — ownership, teams, per-record rights — you model
yourself on that key. Authentik only decides who gets in.

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
  for apps deployed with `--db prod-ro` — they show production data.
- `vd status <name> --json` reports `auth.state`: `ok`, `broken` (redeploy fixes it) or
  `unknown` (Authentik unreachable from the server).

Verify in a browser: open the app → platform login → back in the app; `/api/me` shows the
right person; a second browser profile that is not in the group gets "access denied".

---

## 9. Checklist

- [ ] No login, signup or password code anywhere in the app.
- [ ] Every route that serves data uses the guard; the guard checks **`X-Vibe-Ingress`** first.
- [ ] Constant-time comparison (`hmac.compare_digest` / `timingSafeEqual`).
- [ ] User rows keyed on `X-authentik-uid`; no email or name stored.
- [ ] SPA uses `redirect: 'manual'` and navigates to `/outpost.goauthentik.io/start?rd=<full URL>`.
- [ ] Logout navigates to `/outpost.goauthentik.io/sign_out`.
- [ ] Deployed with `--auth`, subdomain routing; group name relayed to the user.

---

## 10. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `unknown flag: --auth` | The server's vd predates forward auth. Use the [fallback](#fallback-server-side-oidc--only-when---auth-is-unavailable). |
| `AUTH_NOT_CONFIGURED` | The server was never set up for Authentik. A platform admin runs `vd init --authentik-url … --authentik-internal …`. Until then, use the fallback. |
| `AUTH_FAILED` | Authentik rejected the setup. **Nothing was deployed or changed.** Retry once; if it repeats, give the `details` to the platform admin. |
| `AUTH_REQUIRES_SUBDOMAIN` | Drop `--routing path`. |
| Every request to your API is `401` | The guard's secret check fails — you compared against a hardcoded value or a stale `.env`. Read `VIBE_INGRESS_SECRET` from the environment. |
| Everyone gets Authentik's "access denied" | Nobody is in `vibe-<name>` yet. That is the human step (§1). |
| SPA shows network errors after a while | Missing `redirect: 'manual'`, so the login bounce looks like an outage (§6). |
| `ROLLBACK_WOULD_UNPROTECT` | The previous version was public; rolling back would publish it. Fix forward and redeploy with `--auth`. |
| `vd status` says `auth.state: broken` | Something was removed in Authentik by hand. Redeploy — vd recreates it. |

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
