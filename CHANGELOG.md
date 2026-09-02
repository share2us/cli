# Changelog

All notable user-facing changes to the Share2Us CLI.

The release workflow reads the `[Unreleased]` section below into the GitHub
release notes, and **refuses to cut a stable release while it is empty** — so a
build cannot reach users without saying what changed. On release, move the
section under a new version heading.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions are UTC build timestamps (`20260902114433`), not semver.

## [Unreleased]

### Added
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
- `s2u --serve` no longer serves or lists dotfiles (`.env`, `.git/config`) from
  within a shared directory, and refuses to follow a symlink that points outside
  the served tree.

## [20260812102534] - 2026-08-12

Releases before this changelog existed. See the GitHub releases list for the
build history.
