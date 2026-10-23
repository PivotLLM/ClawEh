# Installing ClawEh

This page installs ClawEh as a background service and creates the admin account you sign in with.

There are three ways to install. All three end with the same command, `claw install`, which copies the binary, registers the service, creates the admin account and starts ClawEh.

## Before you start

- A Linux or macOS machine on amd64 or arm64 (Linux 386 is also available).
- A terminal on that machine.
- Decide who runs the service:
  - **As your user** (no `sudo`): ClawEh starts when you log in. The data directory is `~/.claw`.
  - **As a system service** (with `sudo`): ClawEh starts at boot without anyone logging in. It still runs as your user, never as root.

## Option 1: the one-line installer

This downloads the latest release, verifies its checksum, and runs `claw install`.

```bash
curl -fsSL https://raw.githubusercontent.com/PivotLLM/ClawEh/main/claw-online-install.sh | bash
```

For a system service, end the line with `sudo bash` instead of `bash`.

When it finishes, it opens the WebUI in your browser if it can, or prints the addresses to open.

To install a specific release, set `CLAW_VERSION` (for example `CLAW_VERSION=0.6.0`) before the command.

## Option 2: a release archive

1. Download the archive for your platform from the [GitHub releases](https://github.com/PivotLLM/ClawEh/releases). The names are `claw-<os>-<arch>.tar.gz`, for example `claw-linux-amd64.tar.gz` or `claw-darwin-arm64.tar.gz`.
2. Unpack it and run the installer:

   ```bash
   tar -xzf claw-linux-amd64.tar.gz
   ./claw install
   ```

   For a system service, run `sudo ./claw install`.

## Option 3: build from source

You need Git, Go, Node.js and pnpm.

```bash
git clone https://github.com/PivotLLM/ClawEh.git
cd ClawEh
make build
./build/claw install
```

## The admin account

The WebUI requires a login, and there is no default account. When none exists, `claw install` asks for one before it starts the service:

- a username (no spaces, at most 64 characters);
- a password, typed twice, at least 12 characters. It is not shown as you type.

This works under `curl … | bash` too: the installer reads from your terminal, not from the pipe.

An existing account is kept. To replace it later, run `claw admin` on the server. See [Signing in](../admin/login.md).

### Unattended install

Without a terminal, export the account in the same shell, run the installer, then remove the variables:

```bash
read -r CLAW_ADMIN_USER; read -rs CLAW_ADMIN_PASSWORD
export CLAW_ADMIN_USER CLAW_ADMIN_PASSWORD
./claw install
unset CLAW_ADMIN_USER CLAW_ADMIN_PASSWORD
```

With `sudo`, keep them across it: `sudo --preserve-env=CLAW_ADMIN_USER,CLAW_ADMIN_PASSWORD ./claw install`.

::: warning
Do not write the password on the command line in front of the installer. It would be saved in your shell history.
:::

If there is no account, no terminal and no variables, the installer stops without starting the service and says how to continue.

## What the installer does

| | User service | System service (`sudo`) |
|---|---|---|
| Binary | `~/bin` if it exists, else `~/.local/bin` | `/usr/local/bin` |
| Linux service | `~/.config/systemd/user/claw.service` | `/etc/systemd/system/claw.service` |
| macOS service | `~/Library/LaunchAgents/com.pivotllm.claweh.plist` | `/Library/LaunchDaemons/com.pivotllm.claweh.plist` |

It also:

- creates the data directory, `~/.claw` by default (`CLAW_HOME` overrides it; an existing `/opt/claw` is used if present);
- writes a complete `config.json` there on first start;
- creates an `openclaw` link next to `claw`, which the Rabbit R1 needs (see [Devices](../channels/devices.md));
- prints the addresses where the WebUI can be opened.

`--bin-dir` installs the binary somewhere else. `--user` picks the account the system service runs as (default: the user who ran `sudo`). `-y` skips the confirmation question but never the admin account.

Re-run `claw install` with the same privilege as the first time: `sudo` for a system service, none for a user service. The installer refuses the other one.

## Opening the WebUI

By default ClawEh listens on:

- `http://127.0.0.1:18790/` — plain HTTP, this machine only;
- `https://<this machine>:18443/` — HTTPS on every interface, with a self-signed certificate. Your browser warns about it once.

Other machines are refused until you allow their network, even over HTTPS (see [HTTPS and certificates](../admin/https.md)). `claw status` prints the current addresses at any time.

## Installing on a headless or remote host

To reach the WebUI from another machine, allow your network when you install:

```bash
./claw install --allowed-cidrs 192.168.1.0/24
```

`--allowed-cidrs` takes a comma-separated list of networks, or `private` (the private LAN ranges) or `any` (any address). Prefer the narrowest range that works. Then open the HTTPS address from the other machine.

`--host 0.0.0.0` also serves plain, unencrypted HTTP on the network. It is not recommended, and it requires `--allowed-cidrs`.

The same options work with the one-line installer:

```bash
curl -fsSL https://raw.githubusercontent.com/PivotLLM/ClawEh/main/claw-online-install.sh | bash -s -- --allowed-cidrs 192.168.1.0/24
```

You can change network access later on the **Network** page or with `claw network`. See [Network access](../admin/network.md).

## Running without a service

You can also run `claw` in a terminal. It starts ClawEh in the foreground until you press Ctrl+C. Create the admin account first with `claw admin`.

## Removing the service

`claw uninstall` stops and removes the service. The binary and the data directory are left in place. Use `sudo` if the service was installed with `sudo`.

## Next step

[Quick start](./quickstart.md): sign in and set up your first agent.
