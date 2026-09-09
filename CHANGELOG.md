# Changelog

All notable user-facing changes to the Share2Us CLI.

The release workflow reads the `[Unreleased]` section below into the GitHub
release notes, and **refuses to cut a stable release while it is empty** — so a
build cannot reach users without saying what changed. On release, move the
section under a new version heading.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions are UTC build timestamps (`20260902114433`), not semver.

## [Unreleased]

<!-- Add user-facing changes here as they merge. A stable release refuses to
     ship while this section is empty (HTML comments do not count). -->

### Added
- **`s2u devices` now tells you which machines can actually receive a file.** It
  led with a session ID nobody types and labelled everything "key" / "no-key".
  It now lists the name you pass to `--device`, when each machine was last seen,
  and whether it is ready to receive or still needs you to sign in on it.
- **Files sent to this device now land somewhere predictable.** `s2u receive`
  used to drop them into whatever directory you happened to be in. It now uses
  your receive folder (`s2u config set-receive-dir`, defaulting to Downloads),
  which the desktop app and the background service read too — previously all
  three disagreed and none of them could be configured.
- **`s2u receive` shows what is waiting, and lets you choose.** A bare `receive`
  lists the waiting files and when the oldest expires. `receive <folder>` offers
  a numbered picker, since several files can be waiting. `--all` and
  `--id <public-id>` are the forms for scripts.
- **`s2u daemon install` asks where files should go** and whether to save them
  automatically, because the background service itself has nobody to ask.
- **`--private` uploads a file without sharing it with anyone.** The share is
  yours alone: nobody else can open the link, even holding it. Passing that link
  back to `s2u` on any device you are signed in on gets the file, which is the
  point — it is a way to move something between your own machines without
  handing it to anybody. Composes with `--expires`, `--password` and `--keep`.
- **`s2u <url>` now works on your own private shares.** Previously an owner
  signed in on the CLI was refused their own file, because every download path
  was anonymous and the three ways to prove you are allowed all need a browser.

### Security
- **Updates now come only from where they are supposed to.** `s2u update`
  installed whatever archive the server pointed it at, over any protocol, from
  any host, and checked it against a checksum that arrived in the same reply.
  A compromised or impersonated server was therefore able to run code as you.
  The download is now restricted to HTTPS on Share2Us or GitHub at every
  redirect, a SHA-256 is required and verified, and an over-long download is cut
  off rather than allowed to fill the disk.
- **A file arriving from someone else can no longer overwrite yours.** The name
  came from the sender, so a file called `.bashrc` or `Makefile` replaced what
  was already there, silently. Arrivals now land beside an existing file as
  `name (1).ext`. A name you typed yourself with `--output` is still used
  exactly as you asked.
- **A download that fails halfway leaves nothing behind.** `s2u get` with a key
  wrote the decrypted file straight to its destination, so a truncated or
  tampered transfer — which is detected — left unverified content under the name
  you asked for. It is now written only after the whole file checks out.
- **A device on your network can no longer write into your approval prompt.**
  The sender chooses its own display name, and that text went to your terminal
  untouched, so it could rewrite the line you were about to approve. Names from
  the network are now stripped of anything that can move a cursor or reverse
  text.
- **Listing your devices needs a real sign-in.** A personal API token, meant for
  CI, could read every machine on the account with its name and last known
  address. `s2u devices` now says so plainly instead of failing obscurely.
- **The background service refuses to run a prompt it cannot verify.** With no
  device key it executed whatever the server sent, in your project directory,
  with edits pre-approved. It now declines and says why.
- **Retained encryption keys no longer outlive the session.** Keys kept to
  recover an in-flight transfer are removed when you log out, dropped from disk
  when they expire rather than merely ignored, and stored beside the rest of
  your credentials instead of a different directory on Windows.
- **A refused transfer no longer floods your desktop.** Anything on the network
  could be declined as fast as it liked and each refusal raised a notification,
  which buried the one message that mattered. Repeated refusals from the same
  device are now collapsed.

### Fixed
- **Files sent to your device land in your receive folder, not as a file named
  after it.** If the folder did not exist yet, the first arrival was written *as*
  the folder: you were told "Received report.txt -> ~/Downloads" and got a file
  called Downloads. Found by running a real transfer between two machines.
- **Prompts no longer appear when nothing can answer them.** A command run with
  its input redirected from `/dev/null` was treated as interactive, because
  `/dev/null` is technically a character device. Anything that asks a question —
  the new receive picker, the daemon installer, confirmation prompts — could
  therefore stop and wait in a script or a CI job. Terminal detection is now a
  real check.

### Changed
- **`s2u receive` with no arguments will stop downloading everything.** It lists
  what is waiting instead. This release still downloads when run from a script
  and prints a warning; add `--all` to keep that behaviour permanently. Nothing
  changes for `--watch`.
- **`discover --download` now checks who is offering the file.** A device's name
  on the network can be claimed by anything, so before pulling an offer the
  command shows the verify code and asks whether the other device is displaying
  the same one. In a script, where nobody can compare anything, it refuses and
  tells you to pass `--yes` if you accept that risk.

## [20260908103258] - 2026-09-08

### Added
- **`--serve` can require a password.** `-p` (or `--password`) gates the served
  folder; the bare flag prompts so it never reaches your shell history. Any
  username works, since there is only one secret to get right.

### Changed
- **`--serve` now says when it is completely open**, because it is: unlike
  `--receive`, nothing approves a request, so anyone who can reach the address can
  browse and download everything under the served path. The banner says so and
  names the flag that fixes it. With a password set it says what that does and does
  not buy you, since plain HTTP carries both the password and the files in the
  clear.

## [20260908065959] - 2026-09-08

### Changed
- **`--receive --no-password` now says that you get to approve each transfer.**
  It always did, but nothing on screen mentioned it, and the warning above said
  any device "may send you a file" — which stopped being true when the approval
  prompt was added. Nothing is written unless you accept it, and the banner says
  so. With `--yes` there is no prompt, so the warning now points at `--yes`
  rather than at open mode.
- **`--serve` tells you it is reachable on every address, and that `--bind`
  exists.** It listens on all interfaces by default, which on a machine with a
  VPN, a tailnet or container bridges is more places than people expect. `--bind`
  has always accepted a single address; it was documented nowhere.

### Added
- **`share2us discover` now finds devices on your tailnet**, not just the local
  segment. Tailnet peers are looked up directly (never scanned), so machines in
  different places — which mDNS can never reach — show up alongside nearby ones.
  `--scan` additionally probes every address on the local subnet, for a receiver
  whose announcement is being lost; it is off by default because sweeping a
  segment is traffic some networks would rather not see.

### Added
- **The background service now runs on Windows.** `share2us daemon install`
  registers a per-user scheduled task that starts at logon, so the inbox and LAN
  receiver keep running with no terminal open — the same thing Linux and macOS
  already had. `daemon status|start|stop|logs|uninstall` all work, and
  `uninstall` now stops the running daemon rather than leaving it going until
  you log out.

### Changed
- The embedded MCP server (`s2u mcp serve`) is now GPLv3 too, so the whole
  shipped client is under one licence. Same code as before — the module was
  relicensed, not changed.
- **Share2Us is now free software under the GPLv3.** The CLI was MIT; it is now
  [GPL-3.0-only](LICENSE). You may use, study, share and modify it, and if you
  distribute it you must pass on those same freedoms with the source. Building
  your own copy for your own use carries no obligation. Releases before this one
  stay MIT — a licence already granted cannot be withdrawn.

### Security
- **Sending to a device found on the network now asks you to confirm it.** Device
  discovery is unauthenticated — any machine on the same network can advertise
  another device's name — so `s2u <file> --dest=<name>` could hand your file to
  whoever answered to that name. The receiver now prints a short **verify code**,
  and a password-less send to a discovered device asks you to confirm the same
  code before anything is sent. If there is no terminal to ask on, it refuses
  rather than guessing.

  Unaffected: sending to an IP, to a pasted pairing string, or with a password —
  those already identify the receiver, and none of them prompt.

- **Trusting a nearby device no longer depends on its IP address.** A receiver
  that had set a password would still accept a transfer with *no* password from
  any device at a "trusted" IP — and an IP is something another machine on the
  same network can take. Trust is now keyed on the device's verified identity,
  the same MFA-gated trust used everywhere else, so taking an address gets you
  nothing.

  `share2us config set device trusted <alias|ip>` has been **removed** as a
  result; it can no longer grant anything. Existing entries are inert and can be
  cleared with `share2us config delete device trusted <alias|ip>`. To let a
  device send without your password, trust it when a transfer arrives (press `t`
  and enter your verification code) and see it in `share2us lan trusted`.

### Added
- **Send a file and a prompt to a coding-agent session on another machine.**
  With the daemon running as `share2us daemon run --agent-bridge`, a session on
  this machine can be listed from your other devices (`share2us agent list`) and
  sent work: `share2us agent send --device <id> --session <id> --file shot.png
  --prompt "what is wrong here?"`. Claude Code, Codex and Gemini are supported.
  The prompt and the file are end-to-end encrypted to the target device — the
  server never sees either.

  A device you have not sent to before is **not** trusted automatically: the
  first request waits for the target machine to approve it
  (`share2us agent pending`, then `share2us agent approve <id>` for that one
  request, or `share2us agent allow <device>` for standing access, which
  `share2us agent revoke <device>` withdraws and `share2us agent allowed` lists).

  Injected runs are **guardrailed by default, with no setup**: pushing,
  deleting, and outbound network are blocked, and a remote prompt can never edit
  your rules or your Claude settings. Write a plain-text `.s2u.rules` (start one
  with `share2us setup`) to block more, or opt out per item with a line like
  `allow network`. `share2us agent rules` shows which of your rules are hard
  enforced and which are advisory, and how each tool enforces them.

  Requires a Pro or Max plan, and is off unless you pass `--agent-bridge`.

- `share2us daemon` can now run **LAN receive without an account**: start it
  while logged out to receive account-free LAN transfers (unknown senders are
  still declined). Log in to also receive account device shares.

- Background service: `share2us daemon install` runs an optional, off-by-default
  per-user service that keeps receiving device and LAN shares (with desktop
  notifications) while no terminal or app is open, and refreshes the trusted-
  device list and checks for updates on a schedule. `share2us daemon
  run|status|stop|logs|uninstall` manage it. Linux (systemd --user) in this
  release, plus **macOS** (launchd LaunchAgent) — note that macOS support has
  not yet been exercised on a real Mac, so treat `daemon install` there as
  provisional and report anything that misbehaves. Windows to follow. Honors the
  same device-trust rules as
  the CLI: it never trusts a new device on its own, and unknown senders are
  declined.

## [20260904112641] - 2026-09-04

### Added
- Windows Package Manager: `winget install share2us.cli` (installs the `s2u` and
  `share2us` commands). An winget-installed `s2u update` points at
  `winget upgrade Share2Us.CLI` instead of self-replacing.

## [20260904061335] - 2026-09-04

### Security
- Trusting a device now shows its **safety number** (five groups of four
  digits, derived from the device key) in the prompt and in the verification
  email, to compare with `s2u lan id` on that device. The six-digit code stays
  for per-transfer prompts, but it is short enough for a determined attacker to
  forge a matching device; the safety number is not. `s2u lan id` and
  `s2u lan trusted list` print it too.

## [20260903182054] - 2026-09-03

### Removed
- The pre-verification local trust file (`lan_trusted.json`) is deleted at
  startup. Its entries were never confirmed with a code, so they are not
  migrated; trust each device again once (see README, "Trusting a device").

## [20260903164522] - 2026-09-03

### Security
- Trusting a nearby device now requires verification through your account
  (ADR-034). When you answer `t` (or use `discover --trust`), Share2Us emails a
  6-digit code to your account address (or asks for your authenticator code once
  you enrol one) and only then records the device — on your account, not in a
  local file. The list of trusted devices is signed by the server and synced to
  your signed-in machines; a hand-edited copy is ignored. This stops an
  automation or AI agent driving the CLI from trusting devices on its own.
  Devices you trusted before this release were never verified this way and are
  **not carried over**: trust them again once. Switching a device to "auto" also
  needs the code; switching back to "ask" and revoking do not. Trusting needs a
  signed-in interactive login: personal API tokens cannot trust.
- New: `s2u lan trusted reset` clears the cached list and pinned server key.

## [20260903161047] - 2026-09-03

### Changed
- Trusted devices now have a mode. When you answer `t` to trust a sender, the
  CLI asks "Ask before each transfer from this device?": **Y** (default) keeps a
  one-tap approval per file without the code compare; **n** saves its files
  automatically. Devices trusted before this release are treated as "ask".
  `s2u lan trusted list` shows the mode; `s2u lan trusted mode <fingerprint>
  ask|auto` changes it. `--receive` now honours trust too (it used to prompt
  trusted devices like strangers).
- Direct sends (`s2u <file> --dest=…`) now present the device identity and
  name, like broadcasts already did. Before, a direct sender was anonymous, so
  the receiver could never trust it, only accept once.

## [20260903150825] - 2026-09-03

### Added
- Debian/Ubuntu packages. `sudo apt install s2u` from the signed repository at
  https://apt.share2.us (suites `stable` and `beta`); `.deb` files are also
  attached to every GitHub release. An apt-installed `s2u update` prints the
  apt command instead of replacing the file dpkg owns.

## [20260903124432] - 2026-09-03

### Added
- `s2u update --channel beta` follows prerelease builds; `--channel stable`
  returns to stable releases. The choice is saved, so the passive update
  notice follows it too. `SHARE2US_UPDATE_CHANNEL=beta` overrides it for one
  run (CI). Stable users never see a beta.
- `s2u --receive` in open mode (`--no-password`) now asks before accepting each
  transfer, showing the sender, the file, and the sender's 6-digit verify code.
  Answer `t` to trust that device by its key, and future transfers from it are
  accepted without asking. `--yes` accepts without prompting. Modes that already
  authenticate the sender (`--password`, `--allow-ip`) are unchanged.
- `s2u --receive --keep` — one listener accepts many sequential transfers
  instead of exiting after the first.
- `s2u <file> --dest=… --resume` — an interrupted send restarts where it
  stopped rather than from zero. Opt-in: it costs a full read pass to hash the
  source before sending.

### Changed
- The LAN receive banner no longer prints the passphrase inside the sender
  command it tells you to copy. It advertises the bare `--password` flag, which
  prompts with terminal echo off, keeping the passphrase out of shell history
  and `ps` output.

### Fixed
- On Windows, a receiver kept advertising itself over mDNS while the firewall
  silently dropped its port — so another device could see it by name and then
  time out connecting, which looks like a broken feature rather than a missing
  firewall rule. `--receive`, `--broadcast` and `--serve` now notice when no
  inbound rule names the program and print the exact command to add one. It is
  advisory only: it never blocks the listener and never changes firewall state.
- `s2u --serve` no longer serves or lists dotfiles (`.env`, `.git/config`) from
  within a shared directory, and refuses to follow a symlink that points outside
  the served tree.

## [20260812102534] - 2026-08-12

Releases before this changelog existed. See the GitHub releases list for the
build history.
