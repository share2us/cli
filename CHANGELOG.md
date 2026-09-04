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
