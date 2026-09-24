# auth-demo

Platform login with no login code: `main.py` is the `/auth` skill's guard and `api()` helper, running — a greeting from the identity headers, `/api/me`, silent renewal after the hourly expiry, and sign-out that returns to the app.

Deploy: `tar cf - -C examples/auth-demo . | ssh vd-server "vd push auth-demo --json"`, then `ssh vd-server "vd deploy /opt/vibe-deploy/push/auth-demo --name auth-demo --auth --json"`.

Access: add people to the Authentik group `vibe-auth-demo` (the deploy prints it as `auth.group`); everyone else gets Authentik's access-denied page.
