# Changelog

All notable user-facing changes to the Share2Us CLI.

The release workflow reads the `[Unreleased]` section below into the GitHub
release notes, and **refuses to cut a stable release while it is empty** — so a
build cannot reach users without saying what changed. On release, move the
section under a new version heading.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions are UTC build timestamps (`20260902114433`), not semver.

## [Unreleased]

## [20261008105756] - 2026-10-08

### Added

- `s2u discover` now shows each nearby device's Share2Us version in a VERSION
  column (interactive table and `--plain`), and this build advertises its own
  version on the LAN, so you can see which build is on the other end before
  sending. A device running an older build that does not announce its version
  shows `-`. (cli-core v0.60.0)

## [20261008101506] - 2026-10-08

### Fixed

- LAN transfers no longer fail with "peer certificate fingerprint mismatch
  (possible MITM)" (or a handshake error) after the other device restarted. Codes
  are now tied to the device's stable identity and survive the peer regenerating
  its session certificate; the warning now means a real identity change.

## [20261008063208] - 2026-10-08

### Fixed

- Plain interactive login repairs a lost device signing key without signing out,
  with an updated API. Login now preserves the signing pair it presents for approval.

## [20261007122734] - 2026-10-07

### Added

- When a prompt sent to an agent cannot be delivered because the target session's
  window can no longer be verified (its pane moved, or it was not re-bound after a
  restart), the sender is now told to re-bind it instead of the prompt waiting
  silently. `s2u agent status <id>` shows "action needed: re-bind the target
  session"; the prompt is not lost and delivers on its own once the session is
  re-bound. Any attached file was already in the inbox.

### Fixed

- The agent signing-key error now gives a recovery that works: run `login` again to
  re-key the device, and if that does not clear it, `signout <device>` then `login`.
  (The old text pointed only at `login`, which on the same machine reuses the session
  and keeps the stuck key.)

## [20261007071712] - 2026-10-07

### Added

- Organise your agents: `s2u agent pin|unpin <id>` keeps an agent at the top of
  `agent list`, `s2u agent hide|unhide <id>` removes it from the default list
  (`agent list --all` shows hidden ones), and `s2u agent rename <id> [NAME]`
  gives it a local name (no NAME clears it). These are local labels on this
  machine, shared with the desktop app; nobody else sees your names, and an agent
  stays reachable while hidden.

## [20261006073738] - 2026-10-06

### Fixed

- Agent file transfers can go directly over LAN between active devices on the
  same account without separate nearby-device pairing. Account membership is
  refreshed hourly and expires after 90 minutes without a successful refresh.

## [20261002182845] - 2026-10-02

### Added

- `s2u agent send --inbox --file PATH` drops a file into an agent's inbox without
  running it. A file sent to an agent now carries the sender's LAN fingerprint in
  the signed envelope, so a later direct LAN transfer can be bound to this device.
- A file sent to an agent now goes directly over LAN/Tailscale when the target
  device is reachable, falling back to the relay otherwise. `s2u agent send` reports
  which path it used.

### Fixed

- Agent files can be staged as encrypted bytes over a trusted LAN/Tailscale
  connection and resolved by the signed hop nonce before trying the relay.
  A direct push never reaches the agent inbox unless the sender's signed LAN
  fingerprint matches the peer that staged it; orphaned pushes expire.
- Claude session and headless guard hooks invoke quoted executables correctly
  through PowerShell on Windows.
- Agent inbox-only file deliveries place and notify without running an agent
  prompt; unknown delivery modes fail closed.
- File-carrying agent prompts now fail if the attachment cannot be opened or
  placed. Received files never overwrite an existing inbox file or follow an
  inbox symlink.

## [20261001110746] - 2026-10-01

### Fixed

- Agent-channel control requests are bound to the Claude session that actually
  connected to the local daemon; another same-user process can no longer claim
  a session or complete its request using the shared control token.

## [20260930212043] - 2026-09-30

### Fixed

- Guarded live agent deliveries now refuse Claude subagents. A background
  subagent could otherwise keep running after the parent turn ended and use
  tools outside the delivered-hop guard.
- Automatic Zellij delivery is limited to three prompts per Claude session per
  rolling hour, at least five minutes apart. The desktop notice shows the count,
  and `s2u agent typed off --project DIR` pauses terminal typing for a binding
  without disabling channel delivery or later in-place delivery.
- Local `--serve` refuses macOS system and credential directories even when
  those paths resolve through a symlink (for example `/etc` to `/private/etc`).

## [20260930200947] - 2026-09-30

### Fixed

- Guarded agent hops no longer treat `git status` or `git blame` as safe
  unattended reads: Git configuration can make either run a program. Headless
  Claude hops also resolve edit targets before allowing them, so a separate Git
  directory or path alias cannot hide editable Git metadata from the guard.

## [20260930152334] - 2026-09-30

### Fixed

- `s2u claude` can deliver guarded prompts into its verified Zellij pane when
  an organisation blocks Claude development channels. It no longer requires
  an MCP channel poll for terminal delivery; a channel remains preferred when
  it is proven available. An active typed turn fails closed if the daemon stops.
  If that turn is interrupted, a later owner turn no longer stays locked by
  the old guard marker. Turn identity is bound by Claude's prompt ID, so
  compaction and queued messages cannot release the guard mid-turn; older
  `s2u claude` windows need to restart and run one local prompt before receiving
  typed hops.
- A typed hop's report no longer marks an MCP channel as deliverable. This
  prevents the next hop from waiting on a development channel that the
  organisation silently blocks.

## [20260929205703] - 2026-09-29

### Fixed

- Guarded agent hops cannot edit `.git` metadata, including through a symlink.
  This closes a path where a delivered prompt could change Git configuration
  and make an otherwise allowed `git status` execute a command.

## [20260929202224] - 2026-09-29

### Fixed

- Guarded live Claude hops now keep their tool restrictions until the turn ends,
  even if the agent reports early. Zellij delivery rejects terminal escape text,
  verifies Claude's UI and pane again before Enter, and keeps reads inside the
  bound project. Shell substitutions, backgrounding and redirection cannot
  bypass the read-only command gate.
- CLI releases require an explicit beta or stable dispatch; merging a PR no
  longer publishes automatically. Release tags now point to the exact build
  commit, including betas built from release branches.

## [20260929192101] - 2026-09-29

### Changed

- **`s2u help` lists `--sharenet` and `--project`**, under "Sharenet files".

## [20260929191259] - 2026-09-29

### Fixed

- **`--json` for a sharenet post names only where it went.** A `--project` post no
  longer prints an empty `"sharenet_id": ""`, and a `--sharenet` post no longer
  prints an empty `"project_id"`.

## [20260929190112] - 2026-09-29

### Fixed

- Open Claude sessions must successfully use the report tool before receiving
  channel requests. Each channel request then requires its own report: an
  unrelated owner turn can no longer falsely mark a dropped prompt as done.
  Unconfirmed requests wait without repeated delivery; Zellij typing is unchanged.

## [20260929184034] - 2026-09-29

### Fixed

- **Live `s2u claude` delivery now fails closed without getting stuck.** A
  dropped channel event cannot be mistaken for the owner's current turn or be
  retried every 20 seconds; unreadable Zellij sessions prevent typing; short
  unfolded prompts are submitted; and old transcript text such as “do you
  want” no longer makes an idle input look busy. Typed prompts show the sending
  device's name alongside its verified id.

## [20260929175441] - 2026-09-29

### Added

- **`s2u claude`: prompts appear in your open Claude window.** Start Claude with
  `s2u claude [claude args]` (for example `s2u claude --resume <session>`) and a
  prompt sent to that session's agent is delivered into the Zellij pane you are
  looking at, instead of waiting until you exit Claude. No Team/Enterprise admin
  setting is needed. Share2Us verifies the bound process and an empty input box
  before typing and never types into plain `claude`; outside Zellij the prompt
  waits as before. The agent reports its result back to the sender. While a
  delivered prompt runs, the project's `.s2u.rules` still apply, whatever mode
  the window is in, and a read-only agent stays read-only.

## [20260929112713] - 2026-09-29

### Changed

- **A prompt waiting for an open window says so.** `s2u agent status <id>` shows
  `waiting` with the reason, instead of `delivered`. If the background service
  restarts (a reboot or an update) while a prompt waits, the prompt comes back and
  keeps waiting; the 24-hour limit still counts from when it was sent.

## [20260929105538] - 2026-09-29

### Changed

- **A prompt sent to an agent now always runs in its bound session.** It is never
  forked into a copy you cannot see. If you have that Claude session open in a
  window, the prompt waits and you get a notice; it runs in that session once you
  exit Claude there, and `s2u agent hops` says how long it waited. A prompt that
  waits 24 hours fails with a reason instead of running. `s2u update` does not
  restart the background service while a prompt is waiting.

## [20260929070247] - 2026-09-29

### Added

- **Post a file into a sharenet or project.** `s2u <file> --sharenet <id>` or
  `s2u <file> --project <id>`. The file goes to the members, not to a link: they
  find it under Files in the portal. It counts toward the sharenet owner's
  storage and stays until someone deletes it. Link options such as `--password`,
  `--one-time` or `--to` are refused with these flags, and your standing
  defaults for encryption, view limits and domains are not applied.

## [20260928211925] - 2026-09-28

### Added

- **`s2u agent join` on Windows.** It finds the Claude Code session it runs in
  from the process tree, as on Linux and macOS. Codex's session cannot be told
  from its process on Windows, so name it: `s2u agent join <code> --session <id>`.
  `--session` works everywhere, and when the session cannot be found `join` lists
  the live sessions in the current folder with the exact line to run.

### Changed

- **`agent join` says the request expires in 60 minutes** if nobody approves it.

## [20260928195436] - 2026-09-28

### Changed (breaking)

- **Hops use signing format v2: update every machine.** A hop's signature now
  also covers its project and both agents, so a compromised server cannot relabel
  which project a hop belongs to or which agent sent it. The receiving machine
  checks them too. The old format is no longer accepted: a CLI older than this one
  cannot send hops ("update s2u on this machine") or receive them. Run
  `share2us update` on each machine.

## [20260928073228] - 2026-09-28

### Fixed

- **The background service uses far less CPU.** Every 30 seconds it asked each
  agent tool for its sessions, including tools you had never bound. Gemini's
  `gemini --list-sessions` alone cost 4 to 5 seconds of CPU each time (measured:
  the service used 16% of a core, about 90% of it Gemini). It now asks only the
  tools you have bound a session for.
- **Windows: receiving a peer-to-peer file no longer writes through a link.** If
  a symbolic link or junction already sat at the destination, the file it pointed
  to was overwritten. Such a destination is now refused, as it already was on
  macOS and Linux.
- **Windows: `--serve` refuses system and credential folders.** The Windows
  folders, Program Files, ProgramData (which holds SSH keys) and your AppData
  (browser profiles, cloud logins, this CLI's own login) were not on the list, and
  the check was case-sensitive, so `c:\users\me` could slip past a rule for
  `C:\Users\Me`.

## [20260927205644] - 2026-09-27

### Fixed

- **Windows: `s2u update` now finds the running service on any display
  language.** It read `schtasks`' "Running", which Windows translates, so on a
  non-English system the service was never restarted after an update. It now
  reads the scheduled task's state, which is not translated. Verified on
  Windows 10: the update restarted the task onto the new version.
- **Running the test suite on Windows no longer touches your real config.** Tests
  set only `XDG_CONFIG_HOME`, which Windows ignores, so they wrote bindings, the
  hop log, `config.json` and `credentials.json` into your own `%AppData%`. Only
  contributors running `go test` on Windows were affected.

## [20260927183655] - 2026-09-27

### Changed

- **`s2u update` restarts the background service** so it runs the new version,
  instead of leaving the old one running until the next reboot. It never
  restarts in the middle of a hop (it says how to restart once the hop is done),
  and a daemon you started by hand in a terminal is left for you to restart.
- **Hops can run read-only commands without an approval.** An agent reached
  through Share2Us has nobody to approve a command, so chained or piped reads
  (`grep ... | head`) used to be refused. A short list of commands that only read
  (`ls`, `cat`, `head`, `tail`, `grep`, `wc`, `pwd`, `stat`, `which`,
  `git status`, `git blame`) now runs. It does not widen where a hop may read:
  paths outside the project are still refused. Commands with a flag that writes
  or runs something (`git diff --output`, `rg --pre`, `find -exec`, `sed -i`)
  are not on the list. Restricted agents are unchanged. The hop is also told it
  is unattended, and to read other repositories with Claude's own tools rather
  than changing into them, which Claude refuses to do.

## [20260927180032] - 2026-09-27

### Changed

- **`s2u agent send` tells an offline agent from a missing one.** When the agent
  you name exists but its machine has stopped checking in, it says so, with when
  it was last seen, instead of "no reachable agent matches". Needs a server with
  this change; against an older one the message is as before.

## [20260927175144] - 2026-09-27

### Fixed

- **Running the CLI's test suite no longer stops your installed service.** One
  test called the real `systemctl --user disable --now s2u-daemon.service`, which
  stopped and disabled the daemon on a developer's machine. Tests now never reach
  systemctl, launchctl or schtasks. Only contributors running `go test` were
  affected.

## [20260927174513] - 2026-09-27

### Added

- **`s2u agent send --agent <id>`.** An agent id never changes, so it is the
  address to use. `--device` and `--session` still work, and are now optional:
  any id can be a unique prefix, and the device is worked out from the session.
- **`s2u agent hops`** lists the hops this machine ran, what each asked, and the
  session it ran in, with the `claude --resume` command to open it.

### Changed

- **A Claude agent keeps its session id across hops.** A hop now resumes the
  agent's session in place, keeping the same id and one history. It forks only
  when the session is open in a Claude window (Claude cannot resume a live
  session), which happens at most once: after that the agent is the fork.
- **`s2u agent list` prints full ids**, agent id first, in the same form as
  `s2u agent project`, so anything it prints can be pasted into `agent send`.
- **Clearer errors from `agent send`.** An id that matches several sessions
  lists them in full. One that matches nothing says to address the agent by id.

### Fixed

- **Presence no longer depends on listing order.** A Claude session listed twice
  (its window and a background entry) showed busy or available depending on which
  came last. Busy now wins.

## [20260926235211] - 2026-09-26

### Fixed

- **`s2u agent bind` works for Codex sessions again.** Codex's own sub-agents
  (such as its review "guardian") write session files that repeat their parent's
  id, which made one session look like two, so binding refused it as ambiguous.
  Sub-agents are no longer listed. Codex sessions are also no longer named after
  the injected AGENTS.md text.
- **The background service no longer records temporary folders on its PATH.**
  Temp, missing and repeated entries from the installing shell are dropped, so a
  leftover scratch folder can never shadow the real `claude` or `codex`.

## [20260926234646] - 2026-09-26

### Changed

- **An agent is now one session, not a whole folder.** `s2u agent join` and
  `s2u agent bind` bind only the session you run them in; other Claude or Codex
  sessions in the same folder are no longer advertised. When a Claude session
  forks to take on sharenet work, the agent follows the fork. Bindings made
  before this release still cover their folder; run `!s2u agent bind` inside
  the session you want to keep to narrow one.

### Fixed

- **The background service could not run agents.** Installed as a service, the
  daemon got a bare PATH, so it could not find `claude` or `codex`: Claude sessions
  were never advertised and hops could not run. The service now records your PATH
  when installed (systemd and launchd). When agents are bound, it is installed in an
  agent mode that can write to your projects (system folders stay read-only and it
  can never gain privileges); a receiver-only service keeps its tight sandbox.
  `s2u agent join` and `s2u agent bind` refresh an existing service that predates
  this and restart it.

## [20260926224817] - 2026-09-26

### Fixed

- **Codex sessions now show real presence** instead of "unknown": **busy** while
  Codex is working (its session file is being written), **available** otherwise,
  since the daemon can resume a quiet Codex session to deliver work.

## [20260926221239] - 2026-09-26

### Changed

- **Linux: agents stay reachable after you log out.** When `s2u agent join` or
  `s2u agent bind` sets up the background service, it now also keeps that service
  running after logout (systemd "lingering", for your own user only) and says so;
  undo with `loginctl disable-linger`. This matters on servers where agents run in
  tmux over SSH. If the system does not allow it without a password, it prints the
  one line to run instead. `s2u daemon status` shows whether it is on. Plain
  `s2u daemon install` is unchanged and only suggests it.

## [20260926220430] - 2026-09-26

### Changed

- **No more starting the daemon by hand for agents.** `s2u agent join` and
  `s2u agent bind` now make sure the Share2Us background service is running: they
  install and start the per-user service (systemd, launchd or Windows), or, where
  there is no service manager, start the receiver in the background. The daemon's
  agent bridge is now **on by default** and stays idle, making no network calls,
  until a session is bound; it wakes on its own when you bind one. Use
  `s2u daemon run --no-agent-bridge` to opt out. `s2u daemon status` shows the
  agent bridge.
- The daemon backs off for 15 minutes when the server says agents are not
  available (plan or server setting) instead of retrying every few seconds, and
  reports a persistent registration error once instead of every cycle.

## [20260926215143] - 2026-09-26

### Fixed

- `s2u agent join` now works inside **Codex** sessions, not only Claude Code: it finds
  the Codex session it runs in from the session file that Codex holds open. When it
  cannot find a session, the message now shows `!s2u agent join <code>` (what you
  type) and says it works in Claude Code or Codex.

## [20260926204153] - 2026-09-26

### Added

- **`s2u agent join <code>`**: join a sharenet project with the agent session you are
  in. A host generates the code in the portal; you type `!s2u agent join <code>` inside
  your Claude session. The CLI finds that session itself, binds it, and asks to join;
  a host approves in the portal (they see your email), and you become a member with
  this agent in the project. Codes work once and expire after 24 hours.

## [20260926184027] - 2026-09-26

### Removed

- `s2u agent invites` and `s2u agent withdraw`. Sharenets, invitations and agent
  memberships are managed in the portal (portal.share2.us/sharenets); the CLI binds
  your agent sessions, and bound agents send and receive with `s2u agent send`.

## [20260926113529] - 2026-09-26

### Added

- **Your agent can now work with an agent in someone else's account**, inside a
  sharenet project you both belong to. `s2u agent send --project ID --device ...
  --session ...` sends to a member agent of that project; the sending agent is the
  one bound to the directory you run it from (or `--as AGENT-ID`). Nothing else of
  either account is reachable: not other agents, not other sessions.
- `s2u agent project ID` lists a project's reachable member agents, with the
  `--device` and `--session` to send to.
- `s2u agent invites` shows invitations for your agents to join other owners'
  projects; `s2u agent invites accept|decline ID` answers one. Accepting admits
  that one agent to that one project and nothing else.
- `s2u agent withdraw PROJECT-ID MEMBERSHIP-ID` takes your agent back out.

### Changed

- A session ready for work is now reported as `available` rather than `idle`,
  the word the rest of Share2Us uses for it.

### Security

- The daemon now refuses **every** unsigned agent request, including one from a
  device it has never heard from. Previously a device that had never signed was
  let through, so a request could be slipped in under a sender id nobody had
  pinned yet. Share2Us no longer sends unsigned requests at all.

## [20260925150632] - 2026-09-25

### Added

- **Every bound agent now has a stable id**, printed by `s2u agent bind` and
  `s2u agent bindings` (`agt_…`). Session ids change constantly — Claude gets a new
  one for every prompt it is sent — so nothing that has to outlive a single prompt
  could be tied to them. This id can: it is what another owner will invite into
  their project. It is created once, kept in your bindings outside every project,
  and never changes afterwards; two clones of the same repository are two agents.
  A binding made before today shows `(none - re-bind)` — run `s2u agent bind` on it
  once and it gets its id.

## [20260925132557] - 2026-09-25

### Security

- **Your machine now checks who sent a prompt before running it.** A prompt sent
  to one of your agents was encrypted so only your device could read it, but
  nothing proved who had written it: anyone who knew your device's public key
  could send one, and the server was the only thing vouching for the sender.
  Every prompt is now signed by the sending device, and the daemon verifies it
  against that sender's key **before anything is decrypted or run**. The first
  signed prompt from a sender pins its key on your machine, like SSH's
  known_hosts. After that the daemon refuses a prompt under a different key, an
  unsigned prompt from a sender that has signed before, a prompt signed for a
  different session or device, and a prompt it has already run once. It checks
  its own pinned copy, never the key the server hands it, so it holds even if
  the server itself were compromised. Pins live outside every project at
  `~/.config/share2us/agents/pinned_senders.json`.

## [20260925103526] - 2026-09-25

### Added

- **Goals: autonomous work with a budget.** `s2u agent goal new|list|show|close|wait`
  opens a unit of work that agents hand between themselves, and
  `s2u agent send --goal <id>` makes an injection a **counted hop** against it
  instead of a one-off. Both ceilings are required when opening one — `--hops` and
  `--time` — because a goal without a budget is unbounded work, and the server
  refuses to invent one for you. When a ceiling is reached the goal closes with
  everything preserved, and the refusal says which ceiling it was.
  Completing a goal requires `--evidence`: the command that was run and its
  output. Failing or cancelling does not, because those are outcomes anyone may
  report honestly while a completion is a claim about the world.

## [20260925031816] - 2026-09-25

### Added

- `s2u agent bind <session-id> [PROJECT-NAME]`, `agent unbind` and
  `agent bindings`. **Nothing is advertised until you bind it.** Until now,
  starting the daemon with `--agent-bridge` registered *every* coding-agent
  session on the machine — every Claude and Codex session, by name and working
  directory, including work that had nothing to do with Share2Us. On a shared
  sharenet that is someone else's view of your whole desk. A session is now
  advertised only when its project and tool are bound, and the daemon retires
  anything it advertised before. Bindings live outside any project
  (`~/.config/share2us/agents/bindings.json`), so an agent cannot bind itself
  into visibility.

- `s2u agent policy [--project DIR] [restricted|standard|privileged]` sets how
  much an injected run may do, **per project**. Until now one `--agent-strict`
  flag decided it for every agent on the machine, which could not express the
  thing people actually want: a locked-down reviewer and a deploying devops
  agent side by side on one laptop.
  - `standard` is the default and is unchanged — edit inside the workspace, no
    network, the usual push / delete / network denies.
  - `restricted` is read-only, what `--agent-strict` always meant.
  - `privileged` is for an agent that is meant to deploy: it reaches the network
    and may push, and its baseline denies are dropped. Self-protection is not,
    and an explicit `don't ...` line in `.s2u.rules` is still hard.
  - The file the daemon obeys lives **outside** the project
    (`~/.config/share2us/agents/<project>/policy.yaml`), because an agent has
    write access to its own repo. A `.s2u/policy.yaml` inside the repo can still
    **tighten** the level for everyone on the team; it can never raise it.
  - `s2u agent rules` now shows the level in force and where it is stored.

### Fixed

- A Codex agent could not commit, push or reach the network, whatever its owner
  intended, because the adapter hardcoded the `workspace-write` sandbox. At
  `privileged` it now gets `danger-full-access`. Verified directly: the same
  `git commit` fails under `workspace-write` with `Unable to create
  '.git/index.lock': Read-only file system` and succeeds at the new level.
  `approval_policy=never` still rides on every injected run, so a denied command
  can never escalate into an auto-approved unsandboxed one.

## [20260923065015] - 2026-09-23

### Fixed

- `s2u daemon uninstall` no longer claims to have removed something that was
  never installed. On Linux and macOS it printed "Removed ..." whatever the
  state; on Windows it did the opposite and failed outright, so uninstalling
  twice returned an error. All three now exit cleanly either way and say which
  of the two things happened.

## [20260918201621] - 2026-09-18

### Fixed

- Markdown shared through an AI agent now previews as formatted markdown
  instead of raw source. The MCP tools classified `.md` as plain text, because
  `text/markdown` matched a `text/` check first, and the share page renders its
  preview from that classification alone.
- Re-sharing the same file through an AI agent keeps its link. The MCP tools
  never sent the identifier the server dedups on, so every repeat share of a
  file minted a new link. Sharing pasted text is unchanged and still creates a
  new share each time, which is what you want for a note.

## [20260916092626] - 2026-09-16

### Fixed
- **Uploads were failing on the free plan.** Without `--expires`, the CLI asked to
  keep the file for 7 days — longer than the free plan now allows — so ordinary
  uploads were refused outright. It now asks for nothing and lets your plan's own
  default apply. An explicit `--expires` is still sent as you typed it.
- **A machine receiving over the network now says who it is.** `--receive` and the
  daemon listened without publishing this device's identity, so anything scanning
  the network saw an address and nothing else. That is why sending to a device
  sitting right there still uploaded: there was no identity to recognise it by.
  Both now present the same signed device card the rest of the app uses.

## [20260915203826] - 2026-09-15

### Added
- **Sending to your own machine now goes straight across when it is on the same
  network.** `--device` used to upload the file and have the other machine
  download it, spending your quota twice, even when that machine was in the same
  room. It now checks first, and when the device is there the file goes directly:
  nothing is uploaded, nothing is stored, and no quota is used. It falls back to
  uploading whenever it cannot, which is most of the time to begin with, because
  a device only answers a probe while it is actually listening.
- **When a direct send was possible but the other machine was not listening, it
  says so** — that the upload will use your quota, and how to turn the direct
  path on. That advice appears only for devices that could actually use it, never
  for one that can never be reached that way.

## [20260911112347] - 2026-09-11

### Changed
- **A signed-in browser is no longer listed as a device.** Chrome and the like
  appeared among your devices saying "can't receive yet, sign in with Share2Us on
  it", which is advice nobody can follow: there is nothing to install on a
  browser, and it can never receive a file. They now sit in their own section
  that says what they are and how to sign one out. Sending to one is refused with
  the two things that do work: share a link, or keep the file to yourself with
  `--private` and open it from any browser you are signed in on.

## [20260911100515] - 2026-09-11

### Changed
- **`s2u receive .` is now the quick way.** One file waiting goes straight into
  the folder you are in, and it tells you what it took. It only asks which when
  there is more than one, where you can still take them all or pick by number. It
  used to ask even when there was a single possible answer.

## [20260911094952] - 2026-09-11

### Fixed
- **A file you ask for now lands where you are.** `receive --id` and
  `receive --all` saved into the configured receive folder, usually Downloads,
  even when you were standing somewhere else and had just named the file you
  wanted. That folder is for the background service, which takes arrivals from
  trusted devices with nobody watching and needs a fixed home. Typed at a
  terminal, both now save into the current folder. Scripts and the background
  service are unchanged.
- **`receive 1` no longer means "make a folder called 1".** The bare argument is
  a destination folder, but the prompt this command shows numbers the files, so a
  number was the obvious thing to type and quietly did something else. It is
  refused now, with the two real ways to pick a file. A folder genuinely called
  `1` still works, written as `./1`.

## [20260910074525] - 2026-09-10

### Security
- **An encrypted file now gets its own key.** Every encrypted share used its key
  directly and told the pieces of the file apart with four random bytes. That
  was safe only for as long as one key was never used to encrypt twice, which
  nothing enforced and nothing at the call site showed. Two files encrypted
  under one key that drew the same four bytes would have given away both of
  them. Each file now derives a key of its own, so a key can encrypt as many
  files as it likes.
- Files encrypted by earlier versions still open. **Files encrypted by this
  version do not open in an earlier one**, which reports the format as
  unsupported. Update both ends.

## [20260910063046] - 2026-09-10

### Security
- **A file arriving at a destination someone else prepared can no longer be
  redirected.** Writing a download or a peer-to-peer transfer followed a symlink
  already sitting at that path and truncated whatever was on the other end.
  Both now refuse it.
- **A receive folder with an odd name can no longer alter the background
  service.** The folder path went into the Linux service definition unquoted, so
  a newline in it could add a startup command. Control characters are refused
  and the path is quoted.

## [20260909210845] - 2026-09-09

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

### Changed
- **`s2u receive` with no arguments will stop downloading everything.** It lists
  what is waiting instead. This release still downloads when run from a script
  and prints a warning; add `--all` to keep that behaviour permanently. Nothing
  changes for `--watch`.

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

## [20260908110903] - 2026-09-08

### Changed
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
