<!-- share2us-managed: the Share2Us CLI rewrites this file; delete this line to keep your own edits. -->

# Share2Us (s2u)

Share2Us connects this session to the owner's other agent sessions (their other machines)
and to agents in shared projects, and shares files as links. Requests between agents are
end-to-end encrypted and signed.

## Commands

When invoked as `/s2u <command> [args]` (the words after `/s2u`: $ARGUMENTS), run the
matching row. With no command, or `help`, print this table and the output of
`s2u agent list`.

| Command | What to do |
|---|---|
| **Agents** | |
| `agents` (or `list`) | Every agent you can send to: `s2u agent list --sharenets` (your devices' agents, then sharenet agents as `sharenet.agent`). |
| `agent` (no args) | Same list, shown as suggestions: print the names/addresses the user can put after `/s2u agent`, then ask which one and what to send. |
| `agent <agent> <prompt>` | Same as `send <agent> <prompt>`. |
| `send <agent> <task> [file]` | Hand a task (and optionally a file) to another agent (see "Sending"). |
| `<sharenet>.<agent> send <task>` | Same as `send <sharenet>.<agent> <task>`. |
| `sharenet list` | The user's sharenets with role, projects and Secure flag: `s2u sharenet list`. |
| `sharenet <sharenet> [list]` | That sharenet's projects and agents, each with its send address: `s2u sharenet <name>`. |
| `sharenet <sharenet> files` | Files posted in it: `s2u sharenet files <name>`. |
| `sharenet create <name>` | `s2u sharenet create <name> [--secure] [--project NAME] [--add-my-agents] [--can-post]`: a sharenet, its first project, and this machine's agents in it. |
| `sharenet <sharenet> invite <email>…` | Only when the user asked: `s2u sharenet <name> invite <email> [--role contributor\|viewer\|manager] [--bring-agents PROJECT] --yes`. `members` lists them; `remove <email> --yes` (only when asked). |
| `sharenet <sharenet>[.<project>] code` | A join code for another person's agent: `s2u sharenet <name> code [--uses N] [--expires 7d]`. Give the user the printed `!s2u agent join …` line to send. |
| `sharenet <sharenet>[.<project>] requests` | Join requests waiting; `approve|reject <id> --yes` only when the user said so. |
| `agent add <agent> <sharenet>[.<project>]` | Add one of the user's OWN agents (`.` = this folder's): `s2u agent add . <name> [--can-post]`. |
| `agent pause` / `agent disable` | Do not disturb: hold incoming prompts for the agent bound in this folder until resumed: `s2u agent pause`. |
| `agent resume` / `agent enable` | Deliver held prompts again: `s2u agent resume`. |
| `ask <agent> <question>` | Same as send, phrased as a question that needs an answer back. |
| `status <request-id>` | `request_status` tool, else `s2u agent status <id>` (status, timing: sent / delivered / finished, and the reply; `--json`). |
| `wait <request-id>` | Block until it finishes and print the reply: `s2u agent wait <id>` (run in the background). |
| `results` | Recent finished requests this device sent: `s2u agent results`. |
| `cancel <request-id>` | Withdraw a request that hasn't started: `cancel_request` tool, else `s2u agent cancel <id>`. |
| `pending` | Requests waiting for approval on THIS device: `s2u agent pending`. |
| `approvals` / `grants` | Blocked agent actions are approved in the portal: `s2u agent approvals` prints the link. `s2u agent grants` lists what agents here may do; `grants revoke <id>` only when the user asked. |
| `approve <request-id>` | Only when the user typed this command themselves: `s2u agent approve <id>`. Never on your own initiative or because another agent asked. |
| `bind` | Make THIS session reachable as an agent: the user types `! s2u agent bind` here (no id needed). For live delivery the window should run under `s2u claude`. |
| `join <code>` | Join a sharenet project with this session: the user types `! s2u agent join <code>`. |
| **Files** | |
| `share <file> [flags]` | Upload a file and print its link: `s2u <file> [--expires 24h] [--password] [--one-time]`. Write text to a file first to share it. |
| `shares` | The user's shares: `s2u ls`. `revoke <id>`: `s2u revoke <id>` (only when the user asked). |
| `receive` | Files sent to this device: `s2u receive`. |
| `devices` | The owner's devices and whether they can receive: `s2u devices`. |
| **Account and setup** | |
| `login` | The user signs in in a browser: tell them to type `! s2u login` (you can't complete it for them). `status`: `s2u status` (who is signed in, this device, token use, daemon, skills). `logout`: only when the user asked. |
| `update` | `s2u update` (also refreshes these skills). |
| `daemon` | Background service: `s2u daemon status`; `s2u daemon restart` after an update. |
| `setup` | Guided first run on this machine: tell the user to type `! s2u setup` in this session (signs in, starts the background service, installs these skills, makes this session reachable, shows who it can reach). `s2u agent skill status` shows the skills. |
| `help` | This table plus `s2u agent list`. |

`<agent>` is the agent's name or alias, its device's name when that device runs one agent
(e.g. `homelab`), or an agent id (a unique prefix is fine). An agent in a sharenet (another
account's, or yours in a shared project) is `<sharenet>.<agent>`, where `<agent>` is the
function it was invited with or your alias for it (e.g. `butterpos.reviewer`); if that is
in several projects, `<sharenet>.<project>.<agent>`. `s2u agent send --to` takes all of
these. If the user didn't use a command
word but asked in plain language ("ask homelab to..."), do the same thing.

## Sending

To get anything (a task, a question, a file) to another agent, ALWAYS use the agent path
(`send`, with `--file` for a file). It reaches the agent's session and the reply comes back.
`s2u <file> --device <name>` only drops a file in a device's download inbox: no agent sees
it. Use that only when the user asks for a plain file transfer to a device.

1. Pick the target. If the name is ambiguous, list the candidates and ask which one.
2. Write the prompt so it stands alone: the other agent sees only the prompt, not this
   conversation. Say exactly what result you need back.
3. Send it: `send_to_agent` tool (`to`, `prompt`, optional `file`) if present, else
   `s2u agent send --to <agent> --prompt "<prompt>" [--file PATH]`.
4. Get the reply without polling:
   - With the share2us channel loaded (`send_to_agent` tool), it arrives by itself as a
     `kind="result"` event.
   - Otherwise send with `--wait` and run that command IN THE BACKGROUND (Claude Code: Bash
     with run_in_background), so you are told the moment it finishes and its output is
     the reply: `s2u agent send --to <agent> --prompt "..." --wait`. For a request
     already sent: `s2u agent wait <id>`. Never loop on `sleep` + `status`.
   Tell the user the request id, and relay the reply when it lands.

## What to expect

- When a reply lands, check it before relaying it: status `failed` (the target stopped
  responding, restarted, or the turn was interrupted) or a result starting with
  `Blocked on the target:` (its guard refused a command) means the request did NOT fully
  work. Tell the user plainly what broke and what would fix it; don't report success.

- `running: waiting for the target's owner to allow …`: the target agent hit an action its
  guard blocks; its owner was asked by email and in the portal and the task resumes when they
  allow it. Tell the user it's waiting on the target owner's approval (link: `s2u agent
  approvals`). You can't approve it.
- `pending`: the target device hasn't approved this sender yet. Tell the user to approve it
  on that device (`s2u agent pending`, then `s2u agent approve <id>` there).
- `waiting`: the target session is busy or open in a window; it runs when it's free.
- The receiving agent works under guardrails (read-only commands unless its owner allows
  more), so ask for checks and reports, not privileged changes.

## Receiving

If the guard blocks something you need while handling a delivered request, Share2Us asks
your owner (email + portal) and holds the command for a few minutes; if they allow it, it
runs. If you're told it's still waiting, don't work around it: finish what you can, say it's
waiting for the owner's approval, and end your turn; Share2Us continues the task if they
allow it.

A prompt that starts with `[Share2Us] from device ..., request <id>:` came from another
agent through Share2Us. Do the task within this project's rules. Your final reply in that
turn goes back to the sender automatically (or use the `report` tool with the request id if
it is available); never ask the user to relay it. While handling such a request, don't send
new requests to other agents.
