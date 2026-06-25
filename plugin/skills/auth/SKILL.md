---
name: auth
description: Add "sign in with the platform account" to a vibe-deploy app. Use when the user wants login / accounts / per-user data in their vibecoded app.
---

# Add platform login to a vibe-deploy app

**IMPORTANT: Always communicate with the user in their language. Detect the language they use and respond in the same language throughout the session.**

You are adding "sign in with the platform account" to a vibe-deploy app. You never import the auth server's internals — the app **verifies a signed JWT** and keys its own data by the user id (`sub`). Follow this exactly: the user you're working with cannot debug auth. There is **no registration step** — your audience is fixed by your app name (§1). The only human step is making sure the people who sign in have platform accounts (§1).

This is one of vibe-deploy's normal patterns — load `/vibe` for platform constraints and `/deploy` for the deploy step. Auth is an **external integration**, not a new vd capability.

---

## 0. Three hard rules — do not negotiate

1. **NO signup screen.** Accounts are provisioned centrally by the platform admin. Public signup is disabled. Build a **sign-IN** form only. If the user asks for "register", explain that new users are added by the platform team (§1).
2. **Subdomain routing only.** Use the `vd deploy` default. **Never** pass `--routing path` — login depends on the browser origin being `https://<name>.<apps-domain>`, and path routing breaks the CORS/origin match. Every login will fail.
3. **One container, two jobs.** Serve UI + API from the **same** app (Express + static files, FastAPI + SPA, or Next.js full-stack). The browser does login against the auth host; **your backend verifies the JWT**. vibe-deploy has no inter-app networking — it must be one container.

---

## 1. Your audience is fixed by your app name — no registration step

Your audience is derived from the app's origin, so there is nothing to register and no admin round-trip. Deployed at `https://<name>.<apps-domain>` (the live URL is returned by `vd status <name> --json` in the `url` field), your token's `client_aud` is always **`vibe:<name>`**. Pick the name, use the matching audience.

**Pick the app name now and keep it.** Lowercase, starts with a letter, 2–63 chars, `a-z 0-9 -`. You must later `vd deploy --name <name>` with this **same** name — the origin, and therefore the audience, is derived from it. Change the name and login breaks.

So your audience is simply:

```
AUTH_AUDIENCE = vibe:<name>      # e.g. app "trip-notes" → "vibe:trip-notes"
```

Put it in `.env` (§2). No need to ask anyone for it.

**The one human step that remains: accounts.** Public signup is disabled, so the people who will sign in must already have platform accounts. Have the user ask the platform admin to provision anyone who doesn't — and, if the app gates on a specific role (§8), to grant it. You can build and deploy without this; users just can't actually log in until their accounts exist.

> Why this is safe: `vibe:<name>` is bound to your own origin — app `foo` can only ever mint `vibe:foo`, never another app's audience, and the `vibe:` namespace can never be a PHI (`reporting`/`prod`) audience. A token for your app is useless against any other app or the medical backend (separate keys + pool). That isolation is the point; don't work around it.

---

## 2. Configuration — `.env` only, never in source

```bash
# .env  — pushed with the app, injected via `vd deploy --env-file`. NEVER commit to git.
AUTH_BASE_URL=https://<auth-host>             # ask the platform operator — this is the auth server's public host
AUTH_ISSUER=https://<auth-host>/auth          # exact `iss` in every token — same host as AUTH_BASE_URL, with /auth appended
AUTH_AUDIENCE=vibe:<name>                     # your app name, prefixed — what you check `client_aud` against
```

- **Ask the user (or the platform operator) for the auth host.** `AUTH_BASE_URL` and `AUTH_ISSUER` aren't hardcoded in this skill on purpose — they're platform-specific. Don't guess; ask. A test host may also be available — ask if you need one.
- `AUTH_BASE_URL`, `AUTH_ISSUER`, `AUTH_AUDIENCE` go in `.env` **only**. The deploy policy scan blocks hardcoded secrets — and these belong in env anyway.
- The **JWKS URL** (`${AUTH_BASE_URL}/auth/jwt/jwks.json`) is public and safe to keep in source.
- Also create `.env.example` (committed) with placeholder values so the human knows what to fill in.
- Create `.gitignore` **first**, before any code, with at least: `.env`, `.env.*`, `*.pem`, `*.key`, `node_modules/`, `__pycache__/`, `.venv/`.

---

## 3. Token contract — what the backend checks

Your app receives a **Bearer JWT** (RS256) on API calls:

```jsonc
{
  "sub": "8cb2a552-…",            // stable user id — your link key
  "client_aud": "vibe:<name>",     // YOU MUST check this == AUTH_AUDIENCE
  "roles": ["<app>-access"],       // app-access roles (may be empty)
  "email_verified": true,
  "flags": [],                     // advisory UI hints — NEVER use for authorization
  "iss": "https://<auth-host>/auth",   // exactly matches your AUTH_ISSUER
  "iat": 1782133336,
  "exp": 1782133636                // short-lived (~5 min)
}
```

**Verify, in order:**

1. **RS256 signature** via JWKS (key chosen by `kid`)
2. **`iss` == `AUTH_ISSUER`**
3. **`exp` not passed**
4. **`client_aud` == `AUTH_AUDIENCE`**

Reject any `alg` other than `RS256` (never `none`, never `HS256`).

> `client_aud` is a **custom claim**, not the standard `aud`. Your JWT library will NOT check it automatically — you compare it yourself (one line). That check is what stops another app's token from being replayed against yours. Don't skip it.
>
> **PII is not in the token.** No email/name. If you need to display them, call `/userinfo` (§6).

---

## 4. Browser — sign in and send the token

Your app runs on a different origin than the auth host, and the auth session cookie is **host-only** (never sent to your app's subdomain) — so your app authenticates with **bearer tokens**, not cookies.

**Strongly preferred: `supertokens-web-js` in header mode.** It stores the access token, runs the refresh-on-401 retry loop, and keeps the user signed in. Access tokens expire in ~5 minutes; **if you hand-roll this and skip refresh, the user is logged out every few minutes** — this is the #1 thing that breaks.

Configure it with:
- `apiDomain = AUTH_BASE_URL`
- `apiBasePath = "/auth"`
- recipes: `EmailPassword` + `Session`
- token transfer: `header`

**If you call the auth API directly instead**, this is the exact contract:

```
POST {AUTH_BASE_URL}/auth/signin
Headers: st-auth-mode: header,  rid: emailpassword   (browser sends Origin automatically)
Body:    {"formFields":[{"id":"email","value":"…"},{"id":"password","value":"…"}]}
```

- On success the tokens arrive in **response headers** `st-access-token` and `st-refresh-token` — **not** in the JSON body. The server exposes them via CORS.
- Keep the access token **in memory** (variable / React state). **Not** localStorage.
- Send on every call to your own API: `Authorization: Bearer <access-token>`.
- On a `401` from your API: `POST {AUTH_BASE_URL}/auth/session/refresh` with the refresh token, get new tokens, retry the original call **once**.
- Logout: `POST {AUTH_BASE_URL}/auth/signout`.

---

## 5. Backend — verify the token

Use a JWKS client that caches keys **in memory** and refetches on an unknown `kid` (keys rotate). That's exactly right for vibe-deploy — no Redis, no disk cache, don't add your own.

### Python (FastAPI / Flask)

```python
import os, jwt                       # requirements.txt:  pyjwt[crypto]
from fastapi import Depends, Header, HTTPException

AUTH_ISSUER  = os.environ["AUTH_ISSUER"]
AUTH_AUD     = os.environ["AUTH_AUDIENCE"]
JWKS_URL     = os.environ["AUTH_BASE_URL"] + "/auth/jwt/jwks.json"
_jwks = jwt.PyJWKClient(JWKS_URL)    # in-memory cache + auto refetch on new kid

def current_user(authorization: str = Header(None)) -> dict:
    if not authorization or not authorization.startswith("Bearer "):
        raise HTTPException(401, "missing bearer token")
    token = authorization[7:]
    try:
        key = _jwks.get_signing_key_from_jwt(token).key
        claims = jwt.decode(
            token, key, algorithms=["RS256"], issuer=AUTH_ISSUER,
            options={"verify_aud": False},          # client_aud is custom; checked below
        )
    except Exception:
        raise HTTPException(401, "invalid token")
    if claims.get("client_aud") != AUTH_AUD:        # <-- the audience check (REQUIRED)
        raise HTTPException(401, "wrong audience")
    claims["token"] = token                          # keep raw token for /userinfo (§6)
    return claims                                    # claims["sub"], ["roles"], ["flags"]
```

### Node (Express / Fastify / Hono / Next.js route handler)

```js
// package.json:  "jose"
import { createRemoteJWKSet, jwtVerify } from 'jose'
const JWKS = createRemoteJWKSet(new URL(process.env.AUTH_BASE_URL + '/auth/jwt/jwks.json'))
const ISSUER = process.env.AUTH_ISSUER
const AUD    = process.env.AUTH_AUDIENCE

export async function currentUser(req, res, next) {
  const h = req.headers.authorization || ''
  if (!h.startsWith('Bearer ')) return res.status(401).end()
  try {
    const { payload } = await jwtVerify(h.slice(7), JWKS, { algorithms: ['RS256'], issuer: ISSUER })
    if (payload.client_aud !== AUD) return res.status(401).end()   // audience check (REQUIRED)
    req.user = payload                                             // payload.sub, .roles, .flags
    next()
  } catch { return res.status(401).end() }
}
```

(Go: `github.com/lestrrat-go/jwx/v2/jwk` with `jwk.NewCachedSet`. Same in-memory cache. Verify `RS256`/`iss`/`exp`, then compare `client_aud` yourself.)

---

## 6. Your own user data — keyed by `sub`, via a migration

Store per-user data keyed by `sub`. **Do not copy email/name into your table** — they go stale. Read them from `/userinfo` when you need to display them (§7).

vibe-deploy requires a **migration tool** (Prisma for Node, Alembic for Python, Django's built-in). Never raw `CREATE TABLE`. Define the table in a **reversible** migration:

```prisma
// Prisma — prisma/schema.prisma  (run `npx prisma migrate deploy` on container start)
model AppUser {
  sub         String   @id          // the token's `sub`
  preferences Json     @default("{}")
  createdAt   DateTime @default(now())
}
```

```python
# Alembic — a migration's upgrade():  (run `alembic upgrade head` on container start)
op.create_table("app_users",
    sa.Column("sub", sa.Text, primary_key=True),         # the token's `sub`
    sa.Column("preferences", postgresql.JSONB, server_default="{}", nullable=False),
    sa.Column("created_at", sa.TIMESTAMP(timezone=True), server_default=sa.text("now()")),
)
# downgrade(): op.drop_table("app_users")
```

Provision the row **lazily** in your handler after `current_user()` succeeds — using the auto-injected `DATABASE_URL` (you get it with `--db postgres`):

```python
await db.execute(
    "INSERT INTO app_users (sub) VALUES (%s) ON CONFLICT (sub) DO NOTHING", (user["sub"],)
)
```

---

## 7. Display name / email — `/userinfo` (pull + short cache)

```python
import httpx, time
_cache: dict[str, tuple[float, dict]] = {}

async def userinfo(sub: str, token: str, ttl: int = 300) -> dict | None:
    hit = _cache.get(sub)
    if hit and time.monotonic() - hit[0] < ttl:
        return hit[1]
    async with httpx.AsyncClient() as c:
        r = await c.get(f"{AUTH_BASE_URL}/userinfo", headers={"Authorization": f"Bearer {token}"})
    if r.status_code == 200:
        _cache[sub] = (time.monotonic(), r.json())
        return r.json()
    return None     # 403 ⇒ email not verified — degrade gracefully (show "verify your email")
```

`/userinfo` returns `sub`, `email`, `email_verified`, `name`, `roles`, `flags`, `status`, and a shared `profile` (`avatar_url`, `phone`, `title`, `department`, `locale`). The user's display profile = this `/userinfo` (shared, central) **joined with** your local `sub` row (app-specific).

> **Never call `/admin/*` from your app.** Provisioning, password resets, and role grants are server-to-server admin operations done centrally — your app must not hold an admin key.

---

## 8. Roles (optional) — gate access if the admin gave you one

The `roles` claim on the token lists names the platform admin attached to the user. **You don't choose the names** — ask the admin which roles exist for your app (the account step in §1), then check for them. Two common naming conventions:

**Per-app roles** — scoped to your app by `<name>`, created/granted by the admin on request:
- `<name>-access` — basic access gate
- `<name>-admin`  — app-internal admin
- `<name>-viewer` — read-only

**Cross-app roles** — platform-wide, not tied to one app:
- `admin`, `staff`, etc.

Whichever names you're given, check them like this:

```python
from fastapi import Depends
def require_role(role: str):
    def dep(user: dict = Depends(current_user)):
        if role not in (user.get("roles") or []):
            raise HTTPException(403, "you don't have access to this app")
        return user
    return dep

# usage: @app.get("/admin", dependencies=[Depends(require_role("auth-demo-admin"))])
```

If you don't need role gating, "a valid token for my audience" (§5) is enough to mean "a signed-in platform user." More fine-grained, app-internal permissions (document ACLs, team scoping, per-resource ownership) you still model yourself, keyed by `sub` — the platform doesn't manage those.

---

## 9. Deploy

Build the app inside the normal `/vibe` constraints, then deploy with `/deploy`. Auth-specific deploy rules:

- **`--name` MUST equal the name from §1** — the live origin (and thus your `vibe:<name>` audience) is derived from it.
- **Subdomain routing only** — do **not** pass `--routing path`.
- Use **`--db postgres`** to get `DATABASE_URL` for your `sub`-keyed table.
- Pass `.env` with **`--env-file`** (it holds `AUTH_*`). Never commit `.env`.
- The policy scan will emit a **warning about "JWTs"** — that's **expected** (you verify a JWT). Pass **`--allow-external`** to silence it.
- If the scan **blocks** with `POLICY_VIOLATION`, you hardcoded a secret — move it to `.env` and re-deploy.

```bash
# push (exclude build artifacts)
tar cf - --exclude='node_modules' --exclude='.git' --exclude='__pycache__' --exclude='.venv' --exclude='venv' --exclude='.next' ./<name> \
  | ssh vd-server "vd push <name> --json"

# deploy: own DB, env file, allow the JWT warning, subdomain routing (default)
ssh vd-server "vd deploy /opt/vibe-deploy/push/<name> --name <name> --db postgres \
  --env-file /opt/vibe-deploy/push/<name>/.env --allow-external --json"

# verify
ssh vd-server "vd status <name> --json"   # the `url` field in the JSON response is your app's live origin
```

forwardauth / Traefik tricks are **not** available here — verify the JWT in your backend (§5). Reach the auth service only by its public `AUTH_BASE_URL`.

---

## 10. Pre-flight checklist

- [ ] App **name** chosen; the **same** name used for `vd deploy --name`.
- [ ] `AUTH_AUDIENCE=vibe:<name>` in `.env` (derived from the name — no registration needed).
- [ ] The users who'll sign in have platform accounts (ask the admin to provision any missing).
- [ ] `.gitignore` exists and lists `.env`; `.env` is **not** committed.
- [ ] Sign-**in** form only — no signup page.
- [ ] Backend verifies signature + `iss` + `exp` **and** `client_aud == AUTH_AUDIENCE`.
- [ ] Browser uses bearer tokens with a **refresh-on-401** loop (supertokens-web-js).
- [ ] `sub`-keyed table created via a **reversible migration**, run on container start.
- [ ] No email/name copied into your DB; read from `/userinfo`. No admin key in the app.
- [ ] Deployed with `--db postgres --env-file … --allow-external`, **subdomain** routing.

---

## 11. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Every request 401, token "looks fine" | Wrong `AUTH_BASE_URL` (so `iss` mismatch), or you didn't compare `client_aud`, or clock skew on `exp`. |
| 401 "wrong audience" | `client_aud` ≠ your `AUTH_AUDIENCE`. Check it's exactly `vibe:<name>` and that you deployed under that same `--name` (the audience is derived from the origin). |
| Login fails with a CORS / origin error | You used `--routing path`, or deployed under a `--name` whose origin doesn't match your `AUTH_AUDIENCE`. Use subdomain routing and keep `--name` == the `<name>` in `vibe:<name>`. |
| 403 from `/userinfo` for a real user | Their **email isn't verified** — have the admin/user verify it first. |
| Logged out every few minutes | No refresh loop — access tokens are ~5 min. Use supertokens-web-js (header mode). |
| SPA: token is `undefined` | You read the JSON body — tokens are in the `st-access-token` **response header**. |
| Intermittent 401 after running a while | You cached JWKS without refetch-on-unknown-`kid`. Use `PyJWKClient` / `createRemoteJWKSet` as shown. |
| Deploy blocked `POLICY_VIOLATION` | A secret is hardcoded — move it to `.env`, deploy with `--env-file`. (The "JWTs" *warning* is fine; silence with `--allow-external`.) |

---

*This skill mirrors the canonical source at `auth-service/docs/VIBE-AUTH.md`. Keep them in sync.*
