# Share2Us CLI

The command-line client for [Share2Us](https://share2.us) — share files and text
from your terminal and get back a link, a QR code, or an end-to-end encrypted
transfer straight to another device. It also does **offline** direct transfers
over your LAN, Tailscale, or WireGuard with no account and no cloud at all.

The binary is `share2us`; most people alias it to `s2u`.

```console
$ s2u rca.md
Link: s.share2.us/7Kf9aQ2m

$ s2u get s.share2.us/7Kf9aQ2m
saved to ./rca.md
```

## Install

**One-liner (Linux / macOS)** — downloads the prebuilt binary to `~/.local/bin`,
symlinks `s2u`, and adds it to your PATH:

```sh
curl -fsSL https://share2.us/install.sh | sh
```

The script ([`scripts/install.sh`](scripts/install.sh)) verifies a CRC32 checksum
and falls back to GitHub Releases. Tune it with env vars: `SHARE2US_INSTALL_DIR`
(default `~/.local/bin`), `SHARE2US_VERSION` (default `latest`).

**Debian / Ubuntu (apt)** — system-wide install that upgrades with the rest of the
system. Two lines once, then `sudo apt install s2u`:

```sh
curl -fsSL https://apt.share2.us/share2us-apt.gpg | sudo tee /usr/share/keyrings/share2us-apt.gpg >/dev/null
echo "deb [signed-by=/usr/share/keyrings/share2us-apt.gpg] https://apt.share2.us stable main" | sudo tee /etc/apt/sources.list.d/share2us.list
sudo apt update && sudo apt install s2u
```

amd64 and arm64. Pre-release builds are in the `beta` suite (`beta main` instead of
`stable main`). The `.deb` files are also attached to every
[release](https://github.com/share2us/cli/releases) for `sudo apt install ./s2u_<version>_amd64.deb`
without adding the repository. An apt-installed `s2u` upgrades with
`sudo apt upgrade`; `s2u update` tells you so instead of replacing the file.

**Windows (winget)** — `winget install share2us.cli` installs the `s2u` and
`share2us` commands. Note: the CLI is not code-signed yet, so on a machine with
**Smart App Control** on, Windows may block the unsigned binary from running;
until a signed build ships, run the CLI under WSL (`sudo apt install s2u`) or
turn Smart App Control off (one-way). Signing is tracked.

**Which one?** The one-liner needs no root and lives in your home directory —
right for a personal machine, WSL, or anywhere you cannot install packages. apt
is right for servers and for machines you already keep current with `apt upgrade`.
Use one or the other on a machine; a `~/.local/bin` copy shadows `/usr/bin/s2u`.

**One-liner (Windows, PowerShell)** — downloads `share2us.exe`, installs `s2u` +
`share2us` under `%LOCALAPPDATA%\Share2Us\bin`, and adds it to your PATH:

```powershell
irm https://share2.us/install.ps1 | iex
```

The script ([`scripts/install.ps1`](scripts/install.ps1)) verifies a SHA-256
checksum and falls back to GitHub Releases. Tune it with the same env vars
(`SHARE2US_INSTALL_DIR`, `SHARE2US_VERSION`).

**From source** (requires **Go 1.25+**):

```sh
git clone https://github.com/share2us/cli
cd cli
go build -o s2u .
sudo mv s2u /usr/local/bin/        # or anywhere on your PATH
```

`go install github.com/share2us/cli@latest` also works, but it installs the
binary as `cli` — rename it to `s2u` (or `share2us`) afterwards.

Once installed, **`s2u update`** keeps a script- or source-installed copy current
from prebuilt releases (apt-installed copies are updated by apt).

### Uninstall

```sh
curl -fsSL https://share2.us/uninstall.sh | sh
```

[`scripts/uninstall.sh`](scripts/uninstall.sh) removes the binary + `s2u` symlink,
undoes the PATH edit, and deletes your config + credentials. Keep your config with
`SHARE2US_KEEP_CONFIG=1 sh` (append to the pipe).

## Quick start

```sh
s2u login                          # authenticate this device (opens a browser)
s2u rca.md                         # upload → get a share link
s2u rca.md --to alex@acme.dev --expires 7d   # recipient-restricted, expiring link
s2u get s.share2.us/7Kf9aQ2m       # download a share to the current directory
s2u ls                             # list your shares
s2u revoke 7Kf9aQ2m                # kill a share
```

Prefer a shorter verb? `alias share=s2u`.

## What it does

**Share to a link**

```sh
s2u report.pdf                     # upload → link
s2u "some quick text" --qr         # QR of the text itself, fully offline (nothing uploaded)
s2u report.pdf --qrl               # upload, then show a QR of the link
s2u app.log --live                 # stream text updates to the link until Ctrl-C
s2u secret.env --password          # gate the share behind a password
s2u photo.jpg --one-time           # dies after a single download
s2u notes.md --max-views 5         # stops working after 5 views
```

**Send directly to a device (end-to-end encrypted)**

```sh
s2u devices                        # list your logged-in devices
s2u secret.env --device laptop     # E2E to your own device
s2u brief.pdf --contact alex@acme.dev   # E2E to another user's device (contacts only)
```

**Receive**

```sh
s2u receive                        # pull files sent to this device
s2u inbound approvals              # require approval before accepting incoming files
s2u incoming approve <id>          # review what's waiting
s2u trust|block|require-approval alex@acme.dev   # per-sender policy
```

**Offline / LAN direct transfer** (no account, no cloud — works on any plan)

```sh
# On the receiver:
s2u --receive                      # prints its IP and waits for one transfer
s2u --receive --keep               # stay open for many transfers (Ctrl-C to stop)
s2u --receive --no-password        # open mode: prompts you to accept each transfer
                                   #   (add --yes to accept without asking)

# On the sender:
s2u photos.zip --dest 192.168.1.5  # send directly over LAN/Tailscale/WireGuard
s2u big.iso --dest 192.168.1.5 --resume   # restart an interrupted send where it stopped

# Or serve over HTTP to any browser on the network:
s2u ./folder --serve --qr
```

**Trusting a device.** When an open-mode prompt asks about a sender, answer `t` to
trust it: you choose whether it should still **ask** before each transfer (default,
no code to compare) or save its files **automatically**. Trust is an account
feature verified with a second factor: Share2Us emails a 6-digit code to your
account address, or asks for your authenticator code once you set one up under
**Account → Security** in the portal. The trusted list lives on your account,
signed by the server and synced to your signed-in machines, so nothing on the
local disk (and no automation or AI agent driving the CLI) can widen it.
Before you enter the code, the prompt (and the email) shows the device's **safety
number**, five groups of four digits: compare it with `s2u lan id` on that device
and cancel if it differs, because that is how an impersonator would be caught.
`s2u lan trusted list|mode <fingerprint> ask|auto|revoke <fingerprint>|reset`
manages it; switching to auto asks for a code, revoking does not. Not signed in,
or using a personal API token? The transfer is accepted once and nothing is trusted.


Transfers are secured with TLS 1.3 and a PAKE handshake; peers can be discovered
by mDNS and saved as aliases/trusted peers.

**Other**

```sh
s2u tui                            # interactive terminal UI
s2u mcp serve                      # run as an MCP server for AI agents
s2u install-agent-tools            # wire s2u into Codex / Claude Code / Gemini CLI
s2u whoami | s2u logout | s2u signout <device>
```

Run `s2u help` for the complete, always-current command reference.

## Configuration

Config and credentials live under **`~/.config/share2us/`** (`config.json` +
`credentials.json`; override the directory with `XDG_CONFIG_HOME`). Manage it with
`s2u config`:

```sh
s2u config show                    # current base URL, API/share hosts, and their source
s2u config defaults                # standing upload defaults and where each came from
s2u config set-base-url example.com   # point at a self-hosted / different environment
```

**Standing upload defaults** — apply automatically when you omit the flag; an
explicit flag always wins (use `--no-encrypt` / `--scan` to override a `true` default):

```sh
s2u config set-default expires 30d      # keys: expires, reshare, encrypt, max-views,
s2u config set-default encrypt true      #       no-scan, allow-domains, deny-domains
s2u config unset-default encrypt         # clear one (falls back to the built-in default)
```

Only *safe* options are defaultable. Footguns — `--password`, `--one-time`,
recipients, visibility, `--allow-secrets`, `--device`/`--contact` — are deliberately
per-command and can't be set as a default.

### Environment variables

| Variable | Purpose |
| --- | --- |
| `SHARE2US_BASE_URL` | Environment apex domain (`api.`/`s.` are derived). Default `share2.us`. |
| `SHARE2US_API_BASE` | Advanced API base URL override. |
| `SHARE2US_SHARE_BASE_URL` | Advanced share-link display base override. |
| `SHARE2US_DEFAULT_EXPIRY` | Default upload expiry. Default `7d`. |
| `SHARE2US_API_TOKEN` | Personal access token for non-interactive auth (CI/automation). Overrides the saved login; cannot perform device/contact E2E sends. |

## Privacy

Share2Us processes the files and text you upload only to operate the service. It
does not sell your content or train models on it. See the
[Terms](https://share2.us/terms) and [Privacy policy](https://share2.us/privacy).

The CLI runs a local secret scan before uploading; use `--no-scan` to skip it or
`--allow-secrets` to proceed past findings.

## Contributing

Bug reports and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).
This repo is the CLI itself; the shared logic lives in
[share2us/cli-core](https://github.com/share2us/cli-core).

## License

[MIT](LICENSE.md) © Share2Us
