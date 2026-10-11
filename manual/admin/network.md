# Network access

By default ClawEh can be reached only from the machine it runs on; this page shows how to open it to your network, and to devices, deliberately.

## Two things decide access

Another machine reaches the WebUI only when both are true:

1. **A listener is on the network.** HTTPS listens on every interface by default; HTTP listens on this machine only. See [HTTPS and certificates](./https.md).
2. **Its network is allowed.** The allowed networks list is empty by default, which means localhost only.

Every request still needs the [admin sign-in](./login.md).

From a machine that is not allowed, ClawEh answers "Forbidden" (403), or the port looks closed.

## Allow your network

### On the Network page

1. Open **Network** in the **Services** group.
2. Under **Allowed networks**, enter each network in **Allowed network CIDRs**, one per line, for example `192.168.1.0/24`. Enter `*` for any address.
3. The page saves by itself ("Saved ✓"). No restart is needed.

Prefer the narrowest network that works. Use `*` rather than `0.0.0.0/0` for "any address": `0.0.0.0/0` still refuses IPv6 clients.

### From the server

`claw network` changes the same setting without the WebUI, which helps when the allowlist is what keeps you out. It is safe to run while ClawEh is running; the change applies within about 15 seconds.

```
claw network --show             # what is allowed now
claw network                    # allow the private LAN ranges
claw network 192.168.1.0/24     # allow one subnet (comma-separate several)
claw network any                # any address
claw network none               # back to localhost only
```

### Where the listeners listen

`claw network` also sets where each listener listens. These take effect after a restart.

```
claw network --http localhost|network
claw network --https all|localhost|off
claw network --device localhost|network
```

On the **Network** page the same settings are **HTTP** → **Scope**, **HTTPS** → **Listen on**, and **Device listener** → **Scope**. After a change the page shows "Restart required to apply changes." with **Restart now**.

## The Addresses card

At the top of the **Network** page, **Addresses** lists where ClawEh can be opened right now: **On this host**, **On the network (HTTPS)**, and **On the network (plain HTTP)** when HTTP is on the network. `claw status` prints the same.

## Never locked out

**Never locked out** (under **Allowed networks**) lists addresses or networks that are never locked out by address after failed sign-ins, at the WebUI and at the device listener. Localhost never is. A lock on a username still applies to them. See [Signing in](./login.md#failed-sign-ins).

## Behind a reverse proxy

When a proxy such as nginx sits in front of ClawEh, every request seems to come from the proxy. To have ClawEh see the real client:

1. Have the proxy send the client address in the `X-Real-IP` header. For nginx: `proxy_set_header X-Real-IP $remote_addr;`
2. On the **Network** page, under **Allowed networks**, add the proxy's address to **Trusted proxies**, for example `127.0.0.1` for a proxy on the same host.
3. Set **Allowed network CIDRs** to cover the real clients (`*` for anyone the proxy lets through).

The real client address is then what the allowed networks, the lockouts, the logs and the [audit log](./audit.md) see. Requests from any other address cannot set these headers.

For a proxy on the same host you can keep HTTP on localhost and set HTTPS **Listen on** to **Localhost only** or **Off**. Set **Hostname / external URL** to the public address so ClawEh answers to that name.

## The device listener

External devices (Rabbit R1, voice apps) connect to a separate listener with its own pairing. It is configured under **Device listener** on the **Network** page:

| Setting | What it does |
|---|---|
| **Scope** | **Localhost only (default)** or **Network**. |
| **Port** | Default 18791. |
| **External address** | Where devices connect from outside, for example through a reverse proxy. |
| **Allowed network CIDRs** | Networks that may reach the device listener. Empty allows any; localhost is always allowed. Note this is the opposite of the WebUI list. |
| **Auto-approve pairings** | Pairs new devices without approval. |
| **Protocol** | **ws (unencrypted)** or **wss (HTTPS)**, using the WebUI certificate. **wss** needs HTTPS to be on. |

Pair and manage devices on the **Devices** page.

ClawEh refuses a configuration that puts the device listener on the network with **Auto-approve pairings** on, or without a device token set.

A client address that fails device sign-in 5 times in 10 minutes is locked out for one minute, doubling with each further lockout up to an hour. The first lockout of an address raises the "Device authentication locked out" [alert](./alerts.md).

The device listener and the WebUI are independent: opening one to the network does not open the other.

## If you are locked out

Run these on the server, as the user ClawEh runs as (on a system install, `sudo -u <service user> CLAW_HOME=/opt/claw claw …`).

| What you see | Fix |
|---|---|
| "Forbidden" (403) from another machine | `claw network`, or `claw network 192.168.1.0/24`. Applies within about 15 seconds. |
| Connection refused from another machine | `claw network --https all` (or `--http network`, `--device network`), then restart the service. |
| Wrong or forgotten password | `claw admin`. |
| "Too many failed sign-ins" | Wait without trying, or run `claw admin`, or restart the service. |

## Check the result

The [Check Up](./checkup.md) page shows one row per listener, saying whether it is on for localhost or the network, encrypted or not, and which networks are allowed.
