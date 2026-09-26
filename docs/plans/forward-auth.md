# Design: `vd deploy --auth` — forward auth via Authentik

**Status:** implemented and spiked end to end on **prod** 2026-09-23 (owner's decision: test skipped). Logout-to-app pending `vibe-provider-invalidation-flow` from stacks. Decision: auth-service ADR-005 §Consequences.
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
| Proxy provider | `vibe-<app>` | `mode=forward_single`, `external_host=https://<app>.<domain>`, `access_token_validity=hours=1`, `intercept_header_auth=false`, authorization flow `default-provider-authorization-implicit-consent`, invalidation flow `default-provider-invalidation-flow` |
| Application | slug `vibe-<app>` | bound to that provider |
| Policy binding | application → group | so only group members pass `authorize`. **Load-bearing:** an application with no binding is open to any signed-in account, and the prod pool holds invited accounts with no groups at all. If the binding cannot be made, `Ensure` fails closed — provider off the outpost, an application created in the same run deleted. A disabled or negated binding counts as none and is repaired in place; `vd status` reports `binding` and is `broken` without it |
| Embedded outpost | `providers += <pk>` | **without this the outpost does not serve the provider at all** |

### What the API actually does — measured on test, 2026-09-22

Four behaviours that all fail silently, so the client is written against these, not
against the shape the documentation suggests:

- **`sub_mode` cannot be set through the proxy endpoint** — posting `sub_mode=user_uuid` there
  is accepted and dropped, and the field is absent from the response. It is not unsettable,
  though: `ProxyProvider` inherits `OAuth2Provider` by multi-table inheritance, one row and one
  pk, so it is written against the parent (`PATCH /providers/oauth2/<pk>/`, or a blueprint entry
  on `authentik_providers_oauth2.oauth2provider`). That is how reporting's provider has
  `sub_mode=user_uuid` despite the proxy serializer never showing it (stacks-1e, read from the
  database). We do not need it: we key on `X-authentik-uid`, and reporting needs it only because
  it links identity by the directory UUID in `sub`. So it stays out of the spec above.
  `X-authentik-uid` is authentik's `user.uid` —
  `sha256("<user id>-<install id>")` — which is stable per installation and identical across
  providers, so it works as the app's user key and survives a provider being recreated. (Same
  formula the reporting team verified against a live instance.) It differs between test and
  prod, as two installations should.
- **Unknown query parameters are ignored, not rejected.** `?bogus=x` returns the whole list.
  `?name=` works on groups but is *not* supported on proxy providers: `providers/proxy/?name=nope`
  returned reporting's provider. An existence check written that way would find a stranger's
  provider and either skip creation or patch theirs. Use `?name__iexact=` (verified to filter)
  and re-compare the name client-side before believing a hit.
- **Application lists are filtered by the policy engine, counts are not.** `core/applications/`
  reports `count=1` with an empty `results`. Re-measured 2026-09-23 with a throwaway app: once
  an application is bound to a group the service account is not in — which is every `vibe-<app>`
  — it disappears from the list **for its own creator too**, `superuser_full_list=true` makes no
  difference, and `GET /core/applications/<slug>/` still answers `200`. vd therefore looks
  applications up by detail only; a list-based check would re-create them on every deploy. So
  "not found" does not mean "free to create" — a slug collision must be handled as a `400` on
  create, with a clear error, not as a crash. Note the sharp edge: `if count > 0 then it exists`
  lies precisely when the token lacks the rights to see the object, which is the case that
  matters. Decide on `results`, never on `count`.
- **Always re-read after create and compare.** Unknown fields vanish quietly, and defaults fill
  in around them; a provider can look configured without being it. This is what cost the
  reporting rollout three days.

Verified negatively too: deleting a group returns `403`, as the token's rights intend.

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

`access_token_validity=hours=1` is the owner's decision for vibe apps (2026-09-24; it
replaced `days=7`, which he rejected as too long): removing someone from the group takes effect
within the hour, like reporting. Deactivating the account remains the immediate lever. The
expiry is invisible to people: a page load renews through the 30-day SSO session without a
password, and SPA calls renew through the `/auth` skill's `api()` helper — one `no-cors` round
to the outpost's `start`, the mechanism ADR-005 measured on reporting. `--auth-ttl` still
overrides per app; anything over a day produces a deploy warning.

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

Host comes from `config.json`; the token from `vd init --authentik-token-stdin`, stored 0600 in
`$VD_HOME/authentik.token`. stdin rather than a flag keeps it out of `ps` and the ssh wrapper's
log, and works through that wrapper, so installing or rotating it needs no root.

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

**Therefore vd-traefik must reach Authentik over a swarm overlay** and address it at
`http://authentik_server:9000`. This is possible at all because — verified on the host — vd and
the swarm are **one Docker daemon on one machine** (`hostname` = `nashville`, single daemon ID,
`vd-traefik` and `authentik_server` side by side, `vd-net` and the overlays in one
`docker network ls`). The NAT question is settled: same host.

**Which overlay: a dedicated `authentik-forward`, not `traefik-gateway`** (stacks-1e's
proposal, adopted). `traefik-gateway` would work and needs no new network, but it carries 13
services including the prod reporting API, its ASR and `postgres-mcp`. An overlay is L3
connectivity with no authorization between members, so joining it would hand vd-traefik direct
network access to prod backends and a database MCP — bypassing Traefik, forward auth and the
ingress secret entirely. The access radius should match the task, and the task is one hostname.

A dedicated attachable overlay holding only `authentik_server` (plus `authentik-test_server`
for the spike) and `vd-traefik` gives identical mechanics — preserved `X-Forwarded-Host`,
forwardAuth on the internal address — with none of that reach. Cost on the stacks side is one
network and a line in two stacks; on ours, one `networks:` entry in `infrastructure.yml`.

The overlay is `external: true` everywhere, so no `docker stack deploy` creates or removes it
and vd-traefik stays attached across deploys. `vd init` re-attaching is belt-and-braces for the
one case that does break it — the network being recreated wholesale — and stacks warns first.

### The scheme must survive to the outpost

Host nginx terminates TLS and proxies to vd-traefik over plain http on `127.0.0.1:8080`, so
the outpost learns the scheme only from `X-Forwarded-Proto`. nginx already sets it
(`proxy_set_header X-Forwarded-Proto $scheme` in `vd-proxy.conf` — no change needed from the
servers side), but Traefik's default is to distrust and overwrite incoming `X-Forwarded-*`,
which would turn it back into `http`. The outpost would then build its callback as
`http://<app>.<domain>/outpost.goauthentik.io/callback`.

`vd init` therefore sets `--entrypoints.web.forwardedHeaders.trustedIPs=127.0.0.1/32`. Safe
here because vd-traefik is published on loopback only, so nothing but nginx can reach it.

Related, one line: `vd-proxy.conf` serves `:80` and `:443` from one server block with no
redirect, so `http://<app>.<domain>` is reachable and would propagate `X-Forwarded-Proto: http`
all the way. Add a `:80 → :443` redirect to the nginx template in `deploy.sh`.

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

The outpost's cookie is `authentik_proxy_<hash>`, host-only on the provider's `external_host`,
so there is one per app and no sharing across vibe apps. Signing out of one app therefore does
not log the others out immediately: it kills the SSO session, and each other app keeps working
on its own cookie until that expires — up to the TTL — then lands on the login page at its next
round trip. Worth saying out loud in the skill; "log out" is not an instant global eviction.

## 4. vd surface

- `--auth` on `vd deploy`; `--auth-ttl` (default `hours=1`).
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
   provider without `docker service update --force`, and the `start`/`sign_out` paths. **First
   check of all: the `Location` in the outpost's `302` must be `https://`** — if the scheme is
   lost anywhere it shows up here and nowhere else. Prod config only after it passes.
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

## Spike on prod, 2026-09-23 — what it found

Throwaway app `authspike`, `--auth --auth-ttl minutes=2`, then destroyed. Verified: sign-in with
a real account; `302` to `https://…/authorize/` with an `https` `redirect_uri` on the app host;
outpost cookie `HttpOnly; Secure; SameSite=Lax`; forged identity headers from outside never
reach the app; a neighbouring container calling the app directly gets `401`; removing the
owner from the group denies him within the TTL; a second deploy creates no duplicates; destroy
returns Authentik to zero providers and an empty outpost list. The other 10 apps and 7 MCP
endpoints answered exactly as in the baseline throughout.

Found on the way, all fixed in this branch unless noted:

- **`vd init` force-recreated `vd-postgres`.** `ComposeUp` passes `--force-recreate`, right for
  an app, wrong for infrastructure. The init that added forward auth recreated postgres; three
  apps logged a dropped connection, one returned a single `500`. `vd init` now uses
  `ComposeApply` (`up -d`).
- **FastAPI `@app.get("/")` answers `HEAD` with `405`**, and the health check is
  `wget --spider`. The first spike deploy failed `UNHEALTHY`. The skill now declares `/` for
  `HEAD` as well.
- **Identity headers are UTF-8, read as latin-1** by Starlette and Node — the owner's name
  rendered as mojibake. Bytes captured on his real request through Traefik were single-layer
  UTF-8 (`d0 94 d0 b0 …`); the skill's `header()` helper undoes exactly that layer.
- **A new provider is served only after up to 5 minutes**: the prod embedded outpost's
  websocket to the core has been failing since 2026-09-22 07:27 UTC, so it hears about changes
  only on its 5-minute refresh. The app answers `404` meanwhile (closed, but confusing).
  Reported to stacks; documented in the skill.
- **Logout lands on Authentik's login page, then its portal.** The outpost's `sign_out` sends
  only `id_token_hint` to end-session, never a `post_logout_redirect_uri`, and
  `SessionEndStage` falls back to the root once `user_logout` has run. Fix, agreed: a separate
  `vibe-provider-invalidation-flow` whose `user_logout` binding (`re_evaluate_policies: true`,
  so neither the plan cache nor the policy cache skips it) carries an expression policy that
  sets `goauthentik.io/providers/oauth2/post_logout_redirect_uri` to
  `application.get_launch_url()`. vd prefers that flow when it exists and falls back to
  `default-provider-invalidation-flow` with a warning, so no reinstall is tied to its creation.
  vd now also sets `meta_launch_url` to the app URL.

