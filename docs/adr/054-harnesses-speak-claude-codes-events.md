# 054. Harnesses are pluggable, and speak Claude Code's events

**Status:** accepted 2026-10-04, built the same day with pi as the second
harness; pi pinned in cages 2026-10-11 — how to add one in
[docs/agents-harnesses.md](../agents-harnesses.md)

## Context

The agent drove only `claude -p` (ADR-036). The owner wanted the open coding
agents too, pi and OpenCode first, then Codex: a machine with a GPU and no
Claude account could then serve sessions to the whole mesh on a local model.
The agents on pi5 (Jimmy), proteus and scribe were already pi, kept in tmux
by a service of their own.

Every app, every test and every event log already on disk read Claude Code's
stream-json.

## Decision

- **A harness is a plug-in** (`internal/agent/harness.go`): it says how to
  start its process and what it can do (`Caps`), and makes a codec per
  process that translates both ways. Each session records its harness;
  `GET /v1/harnesses` lists what the machine has.
- **The event shape stays Claude Code's stream-json**, the subset written down
  in docs/agents-harnesses.md. Claude Code's codec passes lines through; every
  other harness translates into it. A new neutral format would have meant
  changing both apps and every stored log; this costs one translator per
  harness. The price is a format named after one vendor.
- **pi is the second harness**, driven in its RPC mode (`pi --mode rpc`):
  - every message goes as a follow-up, because pi refuses a plain prompt while
    it works, also while it waits to retry a 429;
  - it resumes by its conversation's file, not its id: given an id, pi looks
    only in the directory it runs in;
  - it resumes nothing pi never wrote. pi writes a conversation only once it
    has a message, and exits on an id it cannot find; a session kept running
    was restarted every 15 s, failing each time (proteus, 2026-10-11);
  - the shrooms tools reach it through pi's own MCP configuration,
    `~/.pi/agent/mcp.json`, which the agent keeps an entry in. pi reads that
    file only from 1.0 on;
  - a pi agent that ran on its own is taken over with
    `install-agent.sh --adopt-pi`, its conversation continued as a session
    kept running (`keep_running`), and turns pi starts itself are shown as
    coming from their source, not from the owner.
- **The cage's pi is pinned**: `@earendil-works/pi-coding-agent@1.1.0` in the
  workbench image. The package moved from `@mariozechner`, which stopped at
  0.73.1 and read a `models.json` key of `"$VAR"` literally (every caged pi
  session on proteus failed with 401). 0.85.1 ignored `mcp.json`, so caged pi
  had none of the mesh's tools. Pinned, so a pi release cannot change the RPC
  under us. pi on the host is whatever its owner installed.

## Consequences

- The apps needed no change for pi beyond offering the choice, naming the
  harness on its sessions, and hiding auto-approve where `caps.approve` is
  false (pi runs its tools without asking).
- pi's model, provider and keys are pi's own settings; the agent adds only
  `--pi-args`. Its cost is pi's figure, 0 for a local model (ADR-038).
- Sealed cages run Claude Code only: pi would need the owner's model keys
  (ADR-045).
- Moving the pin is a deliberate step, checked against the RPC and both MCP
  servers, as 1.1.0 was.
- OpenCode and Codex are not built. OpenCode is an HTTP server, not stdio,
  and will need a second kind of process behind the same codec idea.

## What would change our mind

A harness whose events cannot be expressed in Claude Code's shape without
losing something the apps should show. That would be the time for a format
of our own.
