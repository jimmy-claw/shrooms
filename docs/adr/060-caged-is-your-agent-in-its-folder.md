# 060. Caged is your agent, kept in its folder

**Status:** accepted 2026-10-16, built the same day.

## Context

A session can run in three ways: not caged, caged (ADR-037) and sealed
(ADR-045). The difference between caged and sealed was clear in the code
and blurred in practice. "Caged" said "only its project", yet a caged
session's cage had the owner's whole `~/.claude` (or `~/.pi/agent`) mounted
read-write, for its conversations. With it came:
- every other session's conversations;
- `history.jsonl`, every prompt typed on the machine;
- running sessions' keys, and backups of the config;
- an ssh key someone kept there;
- the owner's Claude login, until ADR-059;
- pi's `auth.json`, which can hold provider keys.

A private `.claude` per cage, with conversations copied in and out, was
considered and rejected: two copies of a conversation, kept in step, can
lose part of one when the copy fails.

## Decision

**One sentence each, kept true:**
- **Not caged:** your agent, with your machine.
- **Caged:** your agent, kept in its folder. It sees its project and its own
  conversations, and nothing else of yours: no credentials, no other
  conversations, nothing else of your home. Network open, memory and CPUs
  limited. Against a careless agent, not a hostile one.
- **Sealed:** for code or content nobody vouches for. The internet and
  nothing local.

**A caged session's harness home is private, built from what is let in.**
The cage's `~/.claude` (or `~/.pi/agent`) is a directory of the cage's own,
deleted with it. Into it come:
- the owner's settings, skills and plugins, read-only (as before);
- the session's own conversation folder, read-write, from the real one:
  `~/.claude/projects/<its folder>` or pi's `sessions/<its folder>`.

Nothing else comes in, so whatever lands in the owner's harness directory
later is out by default.

**Its credentials come through the agent** (ADR-059), never into the cage.

**Caged stops here.** Anything that needs more than this is sealed's job:
no further narrowing of caged, no network limits on it.

## Consequences

- **The conversation is still the owner's real file,** so `claude --resume`
  in a terminal, the apps' history and taking a session out of its cage
  work as before. Tested live: a caged Claude session remembered a word
  after a restart in the cage, and again after being taken out of it.
- **Sessions in the same working folder** share their conversation folder,
  and see each other's conversations, as outside a cage.
- **What a harness writes besides its conversation** (prompt history, todos,
  caches, pi's own `auth.json`) stays with the cage and goes when it is
  deleted.
- **The owner's user-level MCP servers** (kept in `~/.claude.json`) are not
  in a cage. Its shrooms tools are passed in as before.
- **A cage made before this** keeps the old mounts until it is made again.
- **The apps say it in the same words:** the cage note in Basecamp and on the
  phone is the caged sentence, ending "For code you do not trust, seal it".
