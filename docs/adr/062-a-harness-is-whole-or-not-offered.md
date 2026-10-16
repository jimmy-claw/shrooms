# 062. A harness comes with everything a session has, cages included

**Status:** accepted 2026-10-16, and built the same day. `Cageable` is the
interface, Claude Code and pi moved behind it unchanged (their tests, untouched,
are what shows it), and OpenCode implements it, `CageEnv` and `Gatewayed`, with
a real-podman test.

## Context

OpenCode came in as a harness (ADR-061) that runs sessions well and cannot be
caged. With the small free models it is offered with, the cage matters more,
not less: a model that may do random things is the one to keep in its folder.

The plug-in point (ADR-054) made a harness's *conversation* pluggable: a codec,
the event shape, the model, the mesh's tools. A session is more than that. It
can be caged (ADR-037) and sealed (ADR-045), reach its model through the agent
with no key in the cage (ADR-059), keep a private home (ADR-060), show usage
and say when it is out of quota. Each of those was built for Claude Code and pi
by name, in `cage.go`, `limited.go` and `usage.go`. A third harness gets none of
them without editing those files, and OpenCode didn't.

## Decision

**A harness is offered with every feature a session has, or the missing feature
is refused by name with this ADR's number.** It is never offered and then fails
partway. What a harness must provide:

| Feature | Required | How a harness provides it |
|---|---|---|
| Sessions, streamed turns, interrupt | yes | `Harness`, `Codec` (ADR-054) |
| The mesh's tools and the session's note | yes | `StartOptions.MCP`, `Note` |
| The model, per session | yes | `StartOptions.Model` |
| Usage: tokens, cost (0 when free) | yes | the `result` event's `usage` |
| **Caged** | **yes** | `Cageable` (below), `CageEnv` |
| **Sealed** | **yes** | `Gatewayed` (ADR-059): no key in the cage |
| Out of quota, and until when | yes where the provider says so | `Limits` (to come, out of `limited.go`) |
| Approve before a tool runs | where the harness can ask | `Caps.Approve`; if it can't, the docs say so |
| Takeover of a terminal's conversation | where the harness keeps them | `Transcripts`, `Caps.Takeover` |

**Caging moves behind an interface.** What `cage.go` knows about each harness
by name becomes the harness's to say:

- its home on the host, which of it is the owner's settings (mounted read-only
  into the private home) and where one project's conversations live (mounted
  read-write);
- the program to run in the cage, and what of the host to mount for it;
- what the owner has decided about this project, carried into the private
  home (`carryProjectTrust`);
- the variables it needs (`CageEnv`) and how it is pointed at the gateway
  (`Gatewayed`).

A harness that doesn't implement it is refused caging. That replaces
`cageableHarness`'s check by name. Claude Code and pi move behind it first,
unchanged, with their tests showing they are; then OpenCode.

**A new harness isn't merged without caged and sealed.** OpenCode came in before
this rule and was the exception; it is caged now, and it is the worked example
of what the interface asks for.

## Consequences

- The harness guide (docs/agents-harnesses.md) lists the table above as what
  a harness must do, and the pi walk-through gains the cage steps.
- A harness author learns the cage from the interface and the guide, not by
  reading `cage.go`.
- Until OpenCode is caged, its sessions run uncaged only, and the apps say so.
- Each caged harness needs a real-cage test (podman, as the shared folder's
  has), not only the fake: a cage is where fakes have lied before.
- **A harness that keeps every project in one database gets a database of its
  own in the cage.** OpenCode does, so a caged OpenCode session's conversations
  are the cage's: they are not in the owner's database and they go when the cage
  does. ADR-060 rejected copies, and mounting the owner's would put every other
  project's conversation in the cage, so this is the only way to keep its line.
  For Claude Code and pi the conversation is still the owner's file.
