# 061. OpenCode is a harness, over its ACP server

**Status:** accepted 2026-10-16 — built the same day

## Context

ADR-054 made harnesses pluggable and named OpenCode as one of the two open ones
worth having ("the open ones matter — pi and OpenCode"). pi followed; OpenCode
did not.

The question was never whether it could be added — the plug-in point exists — but
**which of OpenCode's transports fits a session**. A session is a long-lived
process that takes turn after turn, streams what it is doing, and can be
interrupted. Measured on opencode 1.19.2:

- `opencode run` is **one-shot**: the message is a command-line argument and the
  process exits. It cannot take a second turn and has no stdin to interrupt.
  Its `--format json` also emits one `text` event per completed part — a
  twenty-word answer arrived as a single event, so a phone would see replies
  appear whole rather than grow.
- `opencode acp` is a **long-lived JSON-RPC server on stdin/stdout**, like
  `claude -p --input-format stream-json` and `pi --mode rpc`. It streams the
  reply as `agent_message_chunk` deltas, reports tool calls as they run, and
  answers each turn with `stopReason` and usage.

So ACP is the transport, and this ADR records the three things that followed
from that, none of which were obvious before trying it.

## Decision

**OpenCode is a harness, spoken to over ACP.**

### 1. The codec writes while decoding — the interface had to say so

Every other harness here writes at one of three moments: when the process starts
(`Start`), for a turn (`Turn`), or to answer a prompt (`Respond`). `Decode` only
ever produced events for the apps.

ACP cannot work that way. The session does not exist until `initialize` is
answered with `session/new`, and the session id that comes back is what every
`session/prompt` needs. Worse, a device may send a message the moment a session
is up — `Session.SendID` writes it at once — so a turn can arrive *before* there
is a session to put it in.

`Decode` therefore returns messages **and** anything that must be written back
to the process, and the read loop writes that first. Claude Code and pi return
`nil` there and are otherwise untouched.

### 2. The session is created by a round trip, so a turn can be queued

The turn that arrives before the session exists is kept and goes out with the
`session/new` request. Dropping it would be a message the person watched leave
and never arrive.

### 3. OpenCode raises no permission request, so auto-approve is not offered

Claude Code is started with `--permission-prompt-tool stdio`, which is what turns
a permission prompt into a control request a session answers — the basis of the
apps' approve button and of `Caps.Approve`.

OpenCode has no equivalent. Measured twice: asked to write a file, it wrote it
with no prompt, both through `run` without `--auto` and over ACP. Its
`permission` config decides per action — run, block, or ask inside a TUI — and
nothing reaches a client. So `Caps.Approve` is **false**, rather than offered and
doing nothing.

Its `session/new` also ignores a system prompt (a `systemPrompt` param asking for
BANANA was ignored and the model answered Paris), so the session's own note goes
on the first prompt as a block of its own, once.

## Consequences

- A session can be started on OpenCode from the apps, with the mesh's agents as
  MCP tools and the session's note delivered, and its turns reach the apps in the
  same shape Claude Code's do.
- **Auto-approve is not offered for OpenCode sessions**, and the desktop and
  phone must not imply otherwise. This is a real difference from a Claude
  session, not a detail: what OpenCode may do is decided by its own config.
- **Takeover is not built.** OpenCode keeps its conversations and ACP can list,
  load and fork them, but adopting another terminal's conversation is not
  implemented, so `Caps.Takeover` is false.
- The model is a session setting (`-opencode-model`), defaulting to
  `opencode/deepseek-v4.1-flash-free`. Most of OpenCode's free models are
  **stealth** — unreleased and anonymous, free "for a limited time" — which is
  fine for what they are for, but a session meant to outlive one should name a
  model that is not a codename.
- Nothing about Claude Code or pi changes except the `Decode` signature.

## What was not verified

- `opencode serve`'s HTTP/SSE API, which might stream more finely than ACP. Not
  needed: ACP already streams, which was the point.
- Cages. A caged session on OpenCode would need the binary and its own config
  inside the image; that is a separate piece of work, and the generic
  `missingHarness` path already covers a machine without it.
