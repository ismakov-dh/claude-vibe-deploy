---
name: auth
description: Add "sign in with the platform account" to a vibe-deploy app. Use when the user wants login / accounts / per-user data in their vibecoded app.
---

# Add platform login to a vibe-deploy app

**IMPORTANT: Always communicate with the user in their language. Detect the language they use and respond in the same language throughout the session.**

You are adding "sign in with the platform account" to a vibe-deploy app. You never import the auth server's internals — the app **verifies a signed JWT** and keys its own data by the user id (`sub`). Follow this exactly: the user you're working with cannot debug auth, and the human-in-the-loop step in §1 must happen **before** you write any auth code, or every request returns 401.

This is one of vibe-deploy's normal patterns — load `/vibe` for platform constraints and `/deploy` for the deploy step. Auth is an **external integration**, not a new vd capability.

---

## 0. Three hard rules — do not negotiate

1. **NO signup screen.** Accounts are provisioned centrally by the platform admin. Public signup is disabled. Build a **sign-IN** form only. If the user asks for "register", explain that new users are added by the platform team (§1).
2. **Subdomain routing only.** Use the `vd deploy` default. **Never** pass `--routing path` — login depends on the browser origin being `https://<name>.apps.platform.REDACTED`, and path routing breaks the CORS/origin match. Every login will fail.
3. **One container, two jobs.** Serve UI + API from the **same** app (Express + static files, FastAPI + SPA, or Next.js full-stack). The browser does login against the auth host; **your backend verifies the JWT**. vibe-deploy has no inter-app networking — it must be one container.

---

## 1. The one human step — register the app

You can't do this yourself. The token only mints for a registered **origin**, and you can only verify tokens once you know the app's **audience**. Do this **before any code**.

**1.1 — Pick the app name now and never change it.** Lowercase, starts with a letter, 2–63 chars, `a-z 0-9 -`. The deployed origin will be exactly `https://<name>.apps.platform.REDACTED`, and `vd deploy --name <name>` must use the **same** name. Origin mismatch → login broken.

**1.2 — Have the human send this exact message to the platform admin** (fill in `<name>`):

> Please register a vibe-deploy app as a **public client** on the platform auth service:
> - **origin:** `https://<name>.apps.platform.REDACTED`
> - **app/audience name:** `<name>`
>
> Please reply with the **audience** string I should check (`client_aud`), and confirm whether email verification is required. Also: which users should be able to sign in? They must already have platform accounts — please provision any who don't.

**1.3 — Record the audience.** The reply will include an **audience** value (often just `<name>`). That becomes `AUTH_AUDIENCE` in `.env` (§2). Until you have it, **stop**. Do not guess.

> Why this matters: the auth service maps your origin to one audience and bars vibe apps from sensitive `reporting`/`prod` audiences. A token minted for your app carries `client_aud = <your audience>` and is useless against any other app — and cannot reach the medical/PHI backend. That isolation is the point; don't work around it.

---

## 2. Configuration — `.env` only, never in source

```bash
# .env  — pushed with the app, injected via `vd deploy --env-file`. NEVER commit to git.
AUTH_BASE_URL=https://auth.platform.REDACTED           # prod auth host
AUTH_ISSUER=https://auth.platform.REDACTED/auth         # exact `iss` in every token
AUTH_AUDIENCE=<the audience the admin gave you>             # what you check `client_aud` against
```

- `AUTH_BASE_URL`, `AUTH_ISSUER`, `AUTH_AUDIENCE` go in `.env` **only**. The deploy policy scan blocks hardcoded secrets — and these belong in env anyway.
- The **JWKS URL** (`${AUTH_BASE_URL}/auth/jwt/jwks.json`) is public and safe to keep in source.
- **Test host:** `https://auth.test.platform.REDACTED` (same paths). Use it while building if the admin points you there.
- Also create `.env.example` (committed) with placeholder values so the human knows what to fill in.
- Create `.gitignore` **first**, before any code, with at least: `.env`, `.env.*`, `*.pem`, `*.key`, `node_modules/`, `__pycache__/`, `.venv/`.

---

## 3. Token contract — what the backend checks

Your app receives a **Bearer JWT** (RS256) on API calls:

```jsonc
{
  "sub": "8cb2a552-…",            // stable user id — your link key
  "client_aud": "<your audience>", // YOU MUST check this == AUTH_AUDIENCE
  "roles": ["<app>-access"],       // app-access roles (may be empty)
  "email_verified": true,
  "flags": [],                     // advisory UI hints — NEVER use for authorization
  "iss": "https://auth.platform.REDACTED/auth",
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

The app and the auth host are different domains; cookies can't be shared. Use **bearer tokens**.

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

If the admin created an access role for your app (e.g. `<name>-access`), require it:

```python
from fastapi import Depends
def require_role(role: str):
    def dep(user: dict = Depends(current_user)):
        if role not in (user.get("roles") or []):
            raise HTTPException(403, "you don't have access to this app")
        return user
    return dep
```

If you don't need role gating, "a valid token for my audience" (§5) is enough to mean "a signed-in platform user." App-internal permission levels (admin vs viewer) you model yourself, keyed by `sub` — the platform does not manage those.

---

## 9. Deploy

Build the app inside the normal `/vibe` constraints, then deploy with `/deploy`. Auth-specific deploy rules:

- **`--name` MUST equal the registered name from §1** (otherwise the live origin won't match).
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
ssh vd-server "vd status <name> --json"   # → https://<name>.apps.platform.REDACTED
```

forwardauth / Traefik tricks are **not** available here — verify the JWT in your backend (§5). Reach the auth service only by its public `AUTH_BASE_URL`.

---

## 10. Pre-flight checklist

- [ ] App **name** chosen; same name used for the registered origin **and** `vd deploy --name`.
- [ ] Admin registered the origin and gave you the **audience** → it's in `.env` as `AUTH_AUDIENCE`.
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
| 401 "wrong audience" | The token's `client_aud` ≠ your `AUTH_AUDIENCE` — it's for a different app, or your origin isn't the one registered (did you deploy under a different `--name`?). |
| Login fails with a CORS / origin error | You used `--routing path`, or deployed under a name that isn't the registered origin. Use subdomain routing and the registered `--name`. |
| 403 from `/userinfo` for a real user | Their **email isn't verified** — have the admin/user verify it first. |
| Logged out every few minutes | No refresh loop — access tokens are ~5 min. Use supertokens-web-js (header mode). |
| SPA: token is `undefined` | You read the JSON body — tokens are in the `st-access-token` **response header**. |
| Intermittent 401 after running a while | You cached JWKS without refetch-on-unknown-`kid`. Use `PyJWKClient` / `createRemoteJWKSet` as shown. |
| Deploy blocked `POLICY_VIOLATION` | A secret is hardcoded — move it to `.env`, deploy with `--env-file`. (The "JWTs" *warning* is fine; silence with `--allow-external`.) |

---

*This skill mirrors the canonical source at `auth-service/docs/VIBE-AUTH.md`. Keep them in sync.*
