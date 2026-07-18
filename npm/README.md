# @share2us/cli

The [Share2Us](https://share2.us) command-line client (`s2u`), distributed via npm.

Share files and text from your terminal, the browser, or an AI agent, and get back
one opaque, expiring link. Also does offline LAN / Tailscale / WireGuard direct
transfers with no account and no cloud.

## Install

```sh
npm install -g @share2us/cli
```

This downloads the prebuilt binary for your platform (linux/macOS, x64/arm64) from
[GitHub Releases](https://github.com/share2us/cli/releases). Then:

```sh
s2u login
s2u rca.md            # upload -> get a share link
s2u get s.share2.us/7Kf9aQ2m
s2u help              # full command reference
```

Prefer a script installer instead? `curl -fsSL https://share2.us/install.sh | sh`

## Notes

- The version you get is always the latest release. Pin one with
  `SHARE2US_VERSION=v20260718173923 npm i -g @share2us/cli`.
- This is a thin wrapper around the Go binary; the source and full docs live at
  **[github.com/share2us/cli](https://github.com/share2us/cli)**.

## License

[MIT](https://github.com/share2us/cli/blob/main/LICENSE.md) © Share2Us
