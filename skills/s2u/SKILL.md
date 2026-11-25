---
name: s2u
description: Talk to other coding agents and share files through Share2Us. Use when the user asks to ask, tell, hand work to, or check on an agent or session on another machine or in a shared project (for example "ask homelab to...", "send this to the portfolio agent"), to see which agents are reachable, to read the outcome of a request sent earlier, or to share a file as a link.
---
<!-- share2us-managed: the Share2Us CLI rewrites this file; delete this line to keep your own edits. -->

# Share2Us (s2u)

Share2Us connects this session to the owner's other agent sessions (their other machines)
and to agents in shared projects. Requests are end-to-end encrypted and signed.

## Hand work to another agent

1. Find the agent. With the share2us channel loaded, call `list_agents`; otherwise run
   `s2u agent list`. Each line shows the agent id, tool, status, name and device.
2. Send. With the channel: `send_to_agent` with `to` and `prompt`. Otherwise:
   `s2u agent send --to <name|device|agent-id> --prompt "<what to do, and what to report back>"`.
   `--to` takes the agent's name or alias, its device's name when that device runs one
   agent (e.g. `--to homelab`), or an agent id (a unique prefix is fine).
   Add `--file PATH` to send a file with it.
3. Write the prompt so it stands alone: the other agent sees only the prompt, not this
   conversation. Say what result you need back.
4. Don't poll. The result comes back by itself: as a `kind="result"` channel event when the
   channel is loaded, and as a desktop notification. Then read it with `request_status`
   (or `s2u agent status <id>`). `s2u agent results` lists recent finished requests.

## What to expect

- `pending`: the target device hasn't approved this sender yet. Tell the user to approve it
  on that device (`s2u agent pending`, then `s2u agent approve <id>`); you can't approve it.
- `waiting`: the target session is busy or open in a window; it runs when it's free.
- The receiving agent works under guardrails (read-only commands unless its owner allows
  more), so ask for checks and reports, not privileged changes.
- Cancel a request that hasn't started: `cancel_request` or `s2u agent cancel <id>`.
- While THIS session is handling a request from another agent, don't send new requests:
  report back to that sender with the `report` tool.

## Share a file or text as a link

`s2u <file>` uploads a file and prints a share link (`--expires`, `--password`, `--one-time`
and more: see `s2u --help`). Write text to a file first to share it.
