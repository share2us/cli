<div align="center">

# Share2Us

**Share a file from your terminal and get back a link. Or send it straight to
another machine, with nothing in between.**

[Documentation](https://docs.share2.us) · [Desktop app](https://github.com/share2us/gui) · [share2.us](https://share2.us)

</div>

```console
$ s2u rca.md
Link: s.share2.us/7Kf9aQ2m

$ s2u get s.share2.us/7Kf9aQ2m
saved to ./rca.md
```

The binary is `share2us`, and every install also gives you `s2u`.

## Install

**Linux and macOS.** Installs to `~/.local/bin`, verifies a checksum, needs no
root:

```sh
curl -fsSL https://share2.us/install.sh | sh
```

**Debian and Ubuntu.** A system package, upgraded by `apt` with everything else:

```sh
curl -fsSL https://apt.share2.us/share2us-apt.gpg | sudo tee /usr/share/keyrings/share2us-apt.gpg >/dev/null
echo "deb [signed-by=/usr/share/keyrings/share2us-apt.gpg] https://apt.share2.us stable main" | sudo tee /etc/apt/sources.list.d/share2us.list
sudo apt update && sudo apt install s2u
```

**Windows.**

```powershell
winget install share2us.cli
```

> The Windows binary is not code-signed yet. With **Smart App Control** on,
> Windows may refuse to run it. Until a signed build ships, use WSL
> (`sudo apt install s2u`) or turn Smart App Control off, which is one-way.

**From source**, needing Go 1.25 or newer:

```sh
git clone https://github.com/share2us/cli && cd cli && go build -o s2u .
```

Other ways to install, and how to uninstall:
[docs.share2.us/start/install](https://docs.share2.us/start/install/).

## The shape of it

```sh
s2u login                          # authenticate this machine
s2u report.pdf                     # upload, get a link
s2u report.pdf --to alex@acme.dev --expires 7d    # only them, for a week
s2u secret.env --device laptop     # straight to your own machine, encrypted
s2u photos.zip --dest 192.168.1.5  # straight across the network, nothing uploaded
s2u get s.share2.us/7Kf9aQ2m       # fetch one
s2u ls                             # what you have out there
s2u revoke 7Kf9aQ2m                # kill it
```

Three genuinely different things happen there, and it is worth knowing which is
which. A **link** is stored by the service and opens for whoever holds it, unless
you narrow it. A **device send** is encrypted before it leaves your machine, so
the service relays something it cannot read. A **direct transfer** never touches
the service at all, needs no account, and works with no internet.

`s2u help` prints the complete command reference for the version you have.

## Everything else

**[docs.share2.us](https://docs.share2.us)** has the rest:

| | |
| --- | --- |
| [Share a link](https://docs.share2.us/guides/share-a-link/) | Passwords, expiry, view limits, one-time, QR codes |
| [Receive files](https://docs.share2.us/guides/receive/) | Your inbox, approvals, per-sender policy |
| [Encryption](https://docs.share2.us/guides/encryption/) | What the service can and cannot see |
| [Devices and trust](https://docs.share2.us/guides/devices-and-trust/) | Safety numbers, and why it asks for a code |
| [Your own network](https://docs.share2.us/guides/local-network/) | Direct transfer, serving a folder, `--bind` |
| [Background service](https://docs.share2.us/guides/background-service/) | Receiving with nothing open |
| [AI agents](https://docs.share2.us/guides/agents/) | Tools for an agent, and the agent bridge |
| [Command reference](https://docs.share2.us/reference/cli/) | Every command, flag and environment variable |

There is also a [desktop app](https://github.com/share2us/gui) that does the same
things from a window and a right-click menu. Both are built on the same library,
so they behave identically.

## Privacy

Share2Us processes what you upload only to run the service. It does not sell your
content or train models on it. See the [Terms](https://share2.us/terms) and the
[Privacy policy](https://share2.us/privacy).

The CLI scans for secrets locally before uploading anything. `--no-scan` skips it,
`--allow-secrets` proceeds past findings.

## Contributing

Bug reports and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).
The shared logic lives in [share2us/cli-core](https://github.com/share2us/cli-core).

## Licence

[GNU General Public License v3.0 only](LICENSE) © 2026 Hassan Khurram. Use it,
study it, share it, change it. If you pass it on, pass on the same freedoms and
make your source available. Building your own copy for yourself carries no
obligation.

Releases published before 2026-09-07 remain under the MIT licence they were issued
with; a licence already granted cannot be withdrawn. Dependency licences are in
[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
