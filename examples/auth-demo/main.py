# auth-demo — the /auth skill's guard, verbatim, plus one page.
import hmac
import html
import os

from fastapi import Depends, FastAPI, HTTPException, Request
from fastapi.responses import HTMLResponse

INGRESS_SECRET = os.environ["VIBE_INGRESS_SECRET"]


def header(request: Request, name: str) -> str:
    raw = request.headers.get(name, "")
    try:
        return raw.encode("latin-1").decode("utf-8")
    except (UnicodeEncodeError, UnicodeDecodeError):
        return raw


def current_user(request: Request) -> dict:
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


app = FastAPI()


PAGE = """<!doctype html><meta charset=utf-8><title>auth-demo</title>
<style>body{font:16px system-ui;max-width:40em;margin:2em auto}#log{font:13px ui-monospace,monospace;white-space:pre-wrap;background:#f4f4f4;padding:1em}</style>
<h1>Привет, __WHO__</h1><p>__EMAIL__</p>
<p><button id=b>Проверить /api/me</button> <a href="/outpost.goauthentik.io/sign_out">Выйти</a></p>
<div id=log></div>
<script>
// api.js — the only frontend auth code the app needs.
const RETRYABLE = new Set(['GET', 'HEAD'])

async function api(path, init = {}) {
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

const out = document.getElementById('log')
const t0 = new Date()
const say = m => { out.textContent = new Date().toLocaleTimeString() + '  ' + m + '\\n' + out.textContent }
say('страница открыта — перезагрузок с этого момента не было')
document.getElementById('b').onclick = async () => {
  const mins = ((new Date() - t0) / 60000).toFixed(1)
  try { const r = await api('/api/me'); const j = await r.json(); say('через ' + mins + ' мин после открытия: ' + r.status + ' ' + j.email) }
  catch (e) { say('ошибка: ' + e.message) }
}
</script>"""


@app.api_route("/", methods=["GET", "HEAD"], response_class=HTMLResponse)
def index(request: Request):
    # The container health check calls this from inside, without the secret.
    try:
        user = current_user(request)
    except HTTPException:
        return "ok"
    return (PAGE.replace("__WHO__", html.escape(user["name"] or user["email"]))
                .replace("__EMAIL__", html.escape(user["email"])))


@app.get("/api/me")
def me(user: dict = Depends(current_user)):
    return user
