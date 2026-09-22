# Design: `vd deploy --auth` — forward auth via Authentik

**Status:** proposed, 2026-09-22. Decision: auth-service ADR-005 §Consequences.
**Goal:** `vd deploy --auth` puts the app behind the Authentik embedded outpost. The app writes
no auth code — it reads identity from request headers. The only human action is adding people
to the group `vibe-<app>`.

Target IdP is **prod** (`auth.platform.acuradai.com`): vibe apps serve people from the prod
pool. Every vd action is additive and touches no user object.

## 1. What vd creates in Authentik, per app

Idempotent: look up by name/slug, then create or patch. All five, in this order:

| Object | Key | Settings |
|---|---|---|
| Group | `vibe-<app>` | created if absent; vd never patches or deletes it (it only holds `add`+`view`) |
| Proxy provider | `vibe-<app>` | `mode=forward_single`, `external_host=https://<app>.<domain>`, `access_token_validity=days=7`, `sub_mode=user_uuid`, authorization flow `default-provider-authorization-implicit-consent`, invalidation flow `default-provider-invalidation-flow` |
| Application | slug `vibe-<app>` | bound to that provider |
| Policy binding | application → group | so only group members pass `authorize` |
| Embedded outpost | `providers += <pk>` | **without this the outpost does not serve the provider at all** |

**The outpost's provider list belongs to vd.** The stacks blueprint no longer manages
`providers`, so vd is the only writer and must not assume it owns the contents. Always
read-modify-write: `GET` the outpost, and if our pk is absent, `PATCH` the **full** list —
everything already there, plus ours. `vd destroy` removes ours the same way. Never `PATCH`
the list with only our own provider: that silently unpublishes every other app.

**Group names are derived, never passed through.** The only name vd may create is
`vibe-<app>`, where `<app>` is the already-validated app name (lowercase, starts with a letter,
2–63 chars, `a-z0-9-`). vd re-checks the composed name against that pattern before the call and
aborts if it does not match — a group name that does not fit is a bug in vd, not a
configuration option, and the token's `add` right makes a mistake here permanent.

`vd destroy` removes the binding, application and provider, and drops the provider from the
outpost. The group stays — vd has no right to delete it, and re-deploying must not silently
restore access somebody revoked.

`access_token_validity=days=7` is the owner's decision for vibe apps (non-PHI, no browser
tokens). Consequence, recorded deliberately: **removing someone from the group takes up to a
week to bite.** The immediate lever is deactivating the account, which kills the SSO session at
once. `--auth-ttl` overrides it per app — use a short one for `--db prod-ro` apps, which read
production data.

The outpost's `providers` list is one shared object and read-modify-write is not atomic, so two
concurrent `vd deploy --auth` runs can drop each other's provider. vd takes a lock file in
`$VD_HOME` around the whole Authentik phase.
<!-- ponytail: one global lock; per-app locking if deploys ever run in parallel for real -->

### API surface and token permissions (needed from stacks)

`/api/v3/core/groups/`, `/api/v3/providers/proxy/`, `/api/v3/core/applications/`,
`/api/v3/policies/bindings/`, `/api/v3/outposts/instances/`, `/api/v3/flows/instances/`.

| Model | Rights |
|---|---|
| `authentik_core.group` | view, add |
| `authentik_providers_proxy.proxyprovider` | view, add, change, delete |
| `authentik_core.application` | view, add, change, delete |
| `authentik_policies.policybinding` | view, add, change, delete |
| `authentik_outposts.outpost` | view, change |
| `authentik_flows.flow` | **view** — to resolve the two flow pks by slug |

`authentik_flows.flow` read is the one addition to the list we were given; without it the
provider cannot be created, since both flows are foreign keys. **No user model at any level** —
vd can never create, read, modify or disable an account, by construction.

Host and token come from vd config (`config.json` + an env var for the token, same shape as the
postgres password), never hardcoded.

## 2. Traefik wiring

vd-traefik gains a **file provider** (`--providers.file.directory=/etc/traefik/dynamic`,
written by `vd init`) because the docker provider cannot express an upstream that is not a
container.

### Reaching Authentik: overlay, not the public URL

The outpost identifies the provider by the app's host, which reaches it as `X-Forwarded-Host`.
Routing either the forwardAuth call or the `/outpost.goauthentik.io/*` paths through
`https://auth.platform.acuradai.com` destroys that value:

- Host nginx's `auth.platform.acuradai.com` vhost does not set `X-Forwarded-Host`, so it would
  pass ours through — but it proxies to the **swarm Traefik**, which has no `forwardedHeaders`
  configured. Traefik's default is to distrust and **overwrite** incoming `X-Forwarded-*`, so
  the header arrives at the outpost as `auth.platform.acuradai.com` and no provider matches.
- Preserving Host instead (`passHostHeader=true`) is worse: nginx then matches its own
  `*.<apps domain>` server block and sends the request back into vd-traefik — an infinite loop.

Making the public URL work would need `forwardedHeaders.trustedIPs` on the swarm Traefik — a
security-relevant stacks change on an entrypoint published on the host, to let a client assert
its own identity. Not worth it.

**Therefore: attach vd-traefik to the `traefik-gateway` overlay and address Authentik at
`http://authentik_server:9000`.** This is not an infrastructure change: `traefik-gateway` is
already `attachable: true`, `authentik_server` is already on it, and — verified on the host —
vd and the swarm are **one Docker daemon on one machine** (`hostname` = `nashville`, single
daemon ID, `vd-traefik` and `authentik_server` running side by side, `vd-net` and
`traefik-gateway` in the same `docker network ls`). The NAT question is settled: same host.
Cost is one `networks:` entry in `infrastructure.yml`; `vd init` reconnects if the overlay was
recreated. It is also exactly how reporting is wired.

### Per-app labels (`app.yml.tmpl`)

```
# outpost's own paths must reach the outpost, above the app router
router vd-<app>-outpost:  Host(`<app>.<domain>`) && PathPrefix(`/outpost.goauthentik.io/`)
                          priority 500  →  service authentik (file provider)

# the app router, chain outermost-first
router vd-<app>:          middlewares = strip-identity, authentik-fa, ingress-secret
```

- **strip-identity** — blanks every `X-authentik-*` header *and* `X-Vibe-Ingress` on the way
  in. The list must match `authResponseHeaders` name for name, or an unset header travels.
- **authentik-fa** — `forwardauth.address=http://authentik_server:9000/outpost.goauthentik.io/auth/traefik`,
  `trustForwardHeader=true`,
  `authResponseHeaders=X-authentik-uid,X-authentik-username,X-authentik-email,X-authentik-name,X-authentik-groups`.
- **ingress-secret** — last, so it cannot be stripped: sets `X-Vibe-Ingress: <secret>`.
  vd generates the secret per app, stores it in the manifest, injects it as
  `VIBE_INGRESS_SECRET`, and carries it through backup/rollback.

The secret is what makes the headers trustworthy. Every vd app shares the `vd-net` bridge and
resolves its neighbours by name, so without it any app could spoof `X-authentik-uid` at any
other app directly, never touching Traefik.

**Health checks are unaffected.** `WaitHealthy` reads the container's own Docker healthcheck,
which runs `wget` against `127.0.0.1:<port>` inside the container. It never traverses Traefik,
so the forward-auth `302` cannot reach it. No change needed.

## 3. What the app sees

| Header | Use |
|---|---|
| `X-Vibe-Ingress` | must equal `VIBE_INGRESS_SECRET`, else `401` — the only trust check |
| `X-authentik-uid` | stable user key (`sub_mode=user_uuid`); own tables key on this |
| `X-authentik-email`, `X-authentik-name` | display only, never stored |
| `X-authentik-groups` | informational; access was already decided at `authorize` |

Browser: on a `401` or an unreadable network error (the forward-auth `302` ends cross-origin,
so `fetch` cannot report it), do a **top-level navigation** to
`/outpost.goauthentik.io/start?rd=<path>`. Logout is a navigation to
`/outpost.goauthentik.io/sign_out`, and it is **global** — it ends the Authentik session for
every SSO application. With a 7-day assertion both are rare.

## 4. vd surface

- `--auth` on `vd deploy`; `--auth-ttl` (default `days=7`).
- `vd status` reports auth on/off, group name, provider health.
- `vd destroy` tears down as in §1.
- Deploy fails closed: if the Authentik phase fails, no partially-protected app is published.
- `--auth` requires subdomain routing; reject `--routing path` with a clear error.

## 5. Prerequisites from stacks, before any prod provider exists

1. **Fill `authentik_host` on the prod embedded outpost.** It is empty. An empty value made the
   outpost build redirects to `http://localhost:9000` on test and cost a day and a half. This
   is the single highest-value item on the list and must land before our first provider.
2. **Fill the prod invalidation flow** (`default-provider-invalidation-flow`), as on test.
3. Issue the API token with exactly the six models above.

Prod Authentik currently has **zero providers** — ours would be the first. That makes the
ordering below mandatory, not cautious.

## 6. Order of work

1. **Spike on test first** (`auth.test.platform.acuradai.com`, synthetic pool): one throwaway
   app end to end, proving the overlay attachment, that the outpost picks up a newly added
   provider without `docker service update --force`, and the `start`/`sign_out` paths. Prod
   config only after it passes.
2. Authentik client + templates + flag.
3. `auth-demo` (FastAPI, `/` greeting + `/api/me`) on prod: sign in; remove from group → denied
   at the next round trip; `docker exec` from a neighbouring container without the secret → `401`.
4. Rewrite `/auth` for headers, keeping today's server-side OIDC as a fallback section.
5. PR.

## Open questions

- Does the embedded outpost pick up a newly added provider without a forced service update?
  ADR-005 saw caching on `access_token_validity` changes. Answered by step 1.
- Cookie domain of the outpost's `authentik_proxy` cookie across `*.<apps domain>` — one cookie
  per app host, or one shared by every vibe app? Decides whether signing out of one app drops
  the others' assertions too.
