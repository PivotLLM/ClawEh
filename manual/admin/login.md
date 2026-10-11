# Signing in

The WebUI has one admin account, created on the server, and every page needs it.

## The admin account

There is no default account and no default password. The account cannot be created or changed from the browser.

`claw install` (and the one-line installer) creates it before it starts the service (see [Installing ClawEh](../getting-started/install.md#the-admin-account)). If it was not created then, or you need a new password, use `claw admin` on the server.

The account lives in `<CLAW_HOME>/credentials.json` (on a system install, `/opt/claw/credentials.json`). The password is stored only as a hash.

## Create or replace the account

1. Open a terminal on the server, as the user ClawEh runs as.
2. Run one of:

   ```
   claw admin            # asks for the username and the password
   claw admin alice      # username given, asks for the password
   ```

3. Type the password twice. It is not shown, and it must be at least 12 characters.

Running `claw admin` again replaces the account. A running ClawEh picks up the change within a minute and signs every browser out.

Run as root on a system install, `claw admin` hands the file to the service account.

### Unattended install

The installer can take the account from two environment variables, `CLAW_ADMIN_USER` and `CLAW_ADMIN_PASSWORD`. Set both or neither. Read them in rather than typing them on the command line, so the password does not land in the shell history:

```
read -r CLAW_ADMIN_USER; read -rs CLAW_ADMIN_PASSWORD
export CLAW_ADMIN_USER CLAW_ADMIN_PASSWORD
claw install
unset CLAW_ADMIN_USER CLAW_ADMIN_PASSWORD
```

With `sudo`, keep them with `sudo --preserve-env=CLAW_ADMIN_USER,CLAW_ADMIN_PASSWORD`.

## No account yet

ClawEh still starts and every channel works. The WebUI shows **No admin account** with the command to run (`claw admin`) and a **Check again** button.

If the credentials file can be read by other users, ClawEh refuses it and the log names the fix: `chmod 600 <path>`.

## Sign in and sign out

1. Open the WebUI address (`claw status` prints it).
2. Enter **Username** and **Password**, then **Sign in**.

To sign out, use **Sign out** next to the version at the bottom of the sidebar.

Signing in is required from this machine too; localhost is not exempt.

## Sessions

| | |
|---|---|
| Idle timeout | 12 hours, reset by each use |
| Longest session | 7 days |
| After a restart of ClawEh | Everyone is signed out |
| After `claw admin` | Everyone is signed out |

## Failed sign-ins

Failed sign-ins are counted two ways:

| Counted by | Limit | Lock |
|---|---|---|
| Client address | 10 failures in 10 minutes | That address, 5 minutes |
| Username (as typed, whether it exists or not) | 10 failures in 10 minutes | That username from every address, 10 minutes |

While locked, every attempt is refused, even with the right password, and the WebUI shows "Too many failed sign-ins. Try again in N seconds." Each attempt during a lock starts the lock again at its full length, so wait without trying.

Each lock that starts raises an [alert](./alerts.md) ("WebUI login address locked out" or "WebUI login account locked out"). Logins, failures, lockouts and logouts are written to the log and the [audit log](./audit.md), with the username and address, never the password.

### End a lock now

Do one of these on the server:

- Run `claw admin` (a new password, or the same one).
- Restart ClawEh.

Both clear every lock at once.

### Addresses that are never locked

On the **Network** page, under **Allowed networks**, **Never locked out** lists addresses that are never locked by address. Localhost never is. The username lock still applies to them. See [Network access](./network.md#never-locked-out).

## Forgot the password

Run `claw admin` on the server and set a new one. See also [If you are locked out](./network.md#if-you-are-locked-out).
