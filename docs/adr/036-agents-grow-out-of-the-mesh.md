# 036. Agents grow out of the mesh

**Status:** accepted, built 2026-10-03 — details in [docs/agents.md](../agents.md).
Amended: voice notes by Parakeet, not Whisper ([ADR-053](053-voice-notes-by-parakeet-in-pieces.md));
harnesses other than `claude -p` ([ADR-054](054-harnesses-speak-claude-codes-events.md))

## Context

The owner runs Claude Code on several machines, reached from a phone through
tmux over SSH (`cl`), whose phone interface is a terminal. Claude Code's own
Remote Control reaches a session from the Claude app, through a cloud relay.
The mesh already provides what such a thing needs — every machine reachable by
name, behind any NAT, and every packet's source address derived from a device
key — and the point was privacy and independence: nothing leaving the mesh.

## Decision

**A separate agent process, reached over the mesh, with clients of their own.**

- `shrooms-agent` runs on each machine as its user (a systemd user unit, never
  root) and drives `claude -p` over stream-json: sessions, streamed replies,
  permission prompts answered from elsewhere, history, files, voice notes
  transcribed on that machine (whisper.cpp), and taking over a conversation
  started in a terminal. It is not part of the shrooms daemon: something that
  runs code on request does not belong in the network's most privileged
  process.
- **The bind is the access control** (ADR-026): it listens only on the
  machine's overlay addresses, so only mesh members reach it, and each request
  is attributed to the device whose key its source address is derived from.
  Plain HTTP inside the tunnel.
- **Shrooms Agents is its own Android app and its own Basecamp module**, not
  screens of shrooms. The two change at very different speeds — the network
  should be boring, the agents UI changed a dozen times on its first day — and
  an app that records audio and drives machines that run code is a bigger
  target bundled into a VPN. The dependency runs one way: agents need the mesh
  for addresses, access and discovery; the mesh does not need agents. The
  Android app has its own signing key, kept outside the repository.

## Consequences

- Everyone who can reach the mesh can reach the agents on it. Today every
  device on the owner's meshes is the owner's; a shared mesh would need an
  allowlist by device key, which the attribution already makes possible.
- Two installs instead of one. The shrooms app opens Shrooms Agents and hands
  it the peers it can reach, since the agents app is no mesh client itself.
- In Basecamp the view can make no network call (the sandbox), so the shared
  `shrooms_core` does all of it, on threads of its own: a synchronous call that
  waited on the network would freeze the window.
- Auto-approve (the desktop's --dangerously-skip-permissions) is per session
  and reachable from any mesh member's device, which is exactly as safe as
  the mesh membership is.

## What would change our mind

Claude Code's own remote access becoming available without a third party in
the path, or a mesh shared with people who are not the owner — which would
make per-device authorization a requirement rather than an option.
