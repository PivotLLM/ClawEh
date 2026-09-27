# WebUI authentication

The WebUI and everything under `/api/*` require a login. There is one admin
account, and it is created on the server — by the installer, or with
`claw admin`.

## Created by the installer

`claw install` and the one-line installer (`claw-online-install.sh`) make sure
the account exists before they register and start the service. After the
config is written, when `<CLAW_HOME>/credentials.json` does not exist:

1. If `CLAW_ADMIN_USER` and `CLAW_ADMIN_PASSWORD` are both set, the account is
   created from them (same rules as below). Setting only one is an error.
2. Otherwise the installer prompts for the username and the password on the
   terminal. When stdin is not a terminal — under `curl … | bash` stdin is the
   script — it opens `/dev/tty` instead; the password is never read from a pipe.
3. With neither, it stops with `no admin account and no terminal to create one:
   export CLAW_ADMIN_USER and CLAW_ADMIN_PASSWORD, or run `claw admin` on the
   server, then rerun the installer` and does not register or start the
   service. Rerunning the installer is safe.

An existing file is never changed; one that exists but cannot be used (wrong
permissions, damaged) stops the install with the fix. `--yes` skips only the
confirmation question, not the account. Run as root, the installer hands the
file to the service account. The summary ends with
`Admin account: <username> (<CLAW_HOME>/credentials.json)`.

For an unattended install, export the two variables in the same shell and
unset them afterwards; do not put them on the command line, where the password
lands in the shell history (and, in front of `curl … |`, reaches only `curl`).
Across `sudo`, keep them with
`sudo --preserve-env=CLAW_ADMIN_USER,CLAW_ADMIN_PASSWORD`.

```
read -r CLAW_ADMIN_USER; read -rs CLAW_ADMIN_PASSWORD
export CLAW_ADMIN_USER CLAW_ADMIN_PASSWORD
claw install          # or the curl … | bash one-liner
unset CLAW_ADMIN_USER CLAW_ADMIN_PASSWORD
```

## `claw admin`

```
claw admin            # prompts for the username and the password
claw admin alice      # username given, prompts for the password
```

The password is asked for twice without echo and must be at least 12
characters; a prompted username or password that is rejected is asked for
again, up to three times. Like the installer, `claw admin` prompts on
`/dev/tty` when stdin is not a terminal, and refuses when there is no terminal
at all. The result is `<CLAW_HOME>/credentials.json`:
`{"username","password_hash","updated"}` with an argon2id hash
(`$argon2id$v=19$m=65536,t=3,p=1$…`), mode 0600. `CLAW_HOME` is resolved the
way the service resolves it: the `CLAW_HOME` environment variable, else the
installed systemd unit or launchd plist, else `~/.claw`. On a system install
that is `/opt/claw/credentials.json`, and running the command as root hands the
file to the service account. Running `claw admin` again replaces the account.
There is no default password and no way to create or change the account from
the browser.

`claw status` shows whether an account exists and the URLs to open.

## Without an account

The gateway starts and every channel works, but the WebUI shows "No admin
account. On the server run: claw admin", every `/api/*` request answers 401
with `{"error":"no admin account","hint":"run: claw admin"}`, and startup logs a
warning. A credentials file that group or others can read is refused; the log
names the fix (`chmod 600 <path>`). The gateway re-checks the file every 60
seconds and signs every session out when it changes.

## Sessions

A login sets a random 256-bit session id in an `HttpOnly`, `SameSite=Strict`
cookie: `__Host-claw_session` (`Secure`) over HTTPS, `claw_session` over
loopback HTTP. Idle timeout 12 hours (sliding), absolute lifetime 7 days.
Sessions live in memory, so a gateway restart signs everyone out. Sign out is
the button next to the version in the sidebar.

## Failed logins

10 failures from one client address within 10 minutes lock that address for 5
minutes; 10 failures against one username (as typed, existing or not) within
10 minutes lock that username for 10 minutes. Every attempt during a lock is
refused with 429 and `Retry-After` and restarts the lock at its full length.
`claw admin` (any rewrite of `credentials.json`) or a gateway restart clears
every lock. Addresses in `gateway.lockout_exempt` (and loopback) are never
locked by address, but the username lock still applies to them. Behind a
proxy listed in `gateway.trusted_proxies`, the client address is the one the
proxy sends in `X-Real-IP` (else the first `X-Forwarded-For` entry), so the
address lock, the exemption, the logs and the audit log see the real client
rather than the proxy; from any other peer those headers are ignored. Each lock
start raises an alert. The full operator description is
the README's [Authentication failures](../README.md#authentication-failures)
section. Logins, failures, lockouts, refused attempts and logouts are logged
with the username and client address (never the password) and recorded in the
audit log (see `docs/audit.md`).

## What does not need a login

`/health`, `/ready`, `/ping` and `/api/v1/` (the MCPFusion OAuth API used by
`claw-auth`), `POST /api/message/{token}` (its own tokens), the signed LINE
webhook, and `/api/auth/*` itself. Loopback clients are **not** exempt: a
reverse proxy on the same host forwards to loopback, so exempting it would
exempt the internet. (With the proxy in `gateway.trusted_proxies` the client is
the forwarded address, not loopback.)

## Endpoints

| Method | Path | Result |
|---|---|---|
| `GET` | `/api/auth/status` | `{"configured","authenticated","username"}` |
| `POST` | `/api/auth/login` | body `{"username","password"}`; 204 and the cookie, 401 `invalid username or password`, or 429 |
| `POST` | `/api/auth/logout` | 204 |

## WebSocket chat

The browser connects to `/webui/ws` on the page's own origin with the session
cookie; the origin check is always same-origin. The WebUI channel token
(`channels.webui.token`) is for non-browser clients only, as
`Authorization: Bearer <token>` or the `claw-token` subprotocol; it is never
accepted in the URL.
