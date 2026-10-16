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

So ACP is the transport — **not `opencode serve`**, which this was expected to
need. The earlier guess was that a harness for OpenCode would have to be a
server: start `opencode serve`, speak HTTP to it, and add a second kind of
process behind the same Codec idea. It was wrong, and nothing like it was
needed: ACP is stdio, the shape the plug-in point already had.

This ADR records the things that followed from using it, none of which were
obvious before trying it.

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

So a codec must be able to answer its process while reading from it. That is a
capability, not something every harness has, so it is **opt-in** — the way
`Transcripts`, `Gatewayed` and `CageEnv` are: `Answering`, whose `Decoding()`
returns what the codec queued to write, and the read loop writes that before the
messages from the same line. **Claude Code and pi do not implement it and their
codecs are unchanged** — they do not carry a return value they would never use,
which is what the first draft of this change did to them.

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
- **The model is the session's to choose**, from the apps' model setting, as
  for any harness. `-opencode-model` is only this machine's default for sessions
  that name none, and it is **empty** out of the box so OpenCode's own config
  decides — deliberately, because most of OpenCode's free models are **stealth**
  (unreleased and anonymous, free "for a limited time") and a flag default
  naming one would rot into a support call.
- Nothing about Claude Code or pi changes at all: `Codec` gains nothing, and
  neither of their codecs implements `Answering`.
- **OpenCode cannot be caged yet**, and asking for it is refused where it is
  asked for — `CreateCaged` and `SetCage` — rather than making a cage that fails
  at exec. Its image has no `opencode`, and it has no private home or gateway
  there (ADR-059 and ADR-060 are by name, for Claude Code and pi). `AdoptCaged`
  needs no check of its own: it requires `Transcripts`, which OpenCode is not.

## What was not verified

- `opencode serve`'s HTTP/SSE API, which might stream more finely than ACP. Not
  needed: ACP already streams, which was the point.
- Cages. A caged session on OpenCode would need the binary and its own config
  inside the image, and a private home or a gateway to reach its model. That is
  a separate piece of work; until then it is refused (see Consequences). That is
  not the same as `missingHarness`, which covers a machine without the binary,
  not a cage without it.
