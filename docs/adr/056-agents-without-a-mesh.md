# 056. Agents without a mesh, and Shrooms as the upgrade

**Status:** accepted 2026-10-13.

## Context

Shrooms Agents could not be tried on its own. The agent waited for the
shrooms daemon until it reported a mesh, and served only on mesh addresses.
The Basecamp app found agents only among the mesh's peers, through
`shrooms_core`. So someone curious about the agents had to install a mesh
of one machine first: an overlay network, a daemon, an invite, firewall
rules. All of that before seeing a single session, and for nothing they
could see yet.

The mesh earns its place when there is a second machine or a phone. "My
laptop's agents, from my pocket" is where people get it. That is also
where a mesh is the only answer: no account, no relay of ours, the
WireGuard tunnel deciding who can reach an agent (ADR-041). Before that
point, it is an entry fee.

On a single machine, everything an agent does already happens locally:
sessions, cages (ADR-037), voice notes (ADR-053), and sessions asking each
other. What needs the mesh is other machines, and a way for this
machine's own clients to reach it.

## Decision

**The agent serves on a unix socket of its own, always**, at
`$XDG_RUNTIME_DIR/shrooms-agent/agent.sock`. The directory is 0700 and the
socket 0600, so only its user can open it. A request over the socket is
local: it is treated like one from this machine's own mesh address
(`from` in meshes.go), because file permissions have already decided who
can make it.

**Without a daemon, the agent starts anyway**, on the socket alone, with
no TCP at all, not even on loopback. Other users on the machine can reach
127.0.0.1, and so can any web page in the user's browser. In local mode:

- the machine is itself alone: `machines` has one entry, with the
  loopback address standing in for an overlay one;
- calls to "this machine" (a cage's proxy, a note to an asker, a sealed
  session's outbox) go through the socket;
- the MCP tools and `shrooms-agent a2a` find the socket when there is no
  daemon to ask.

**Shrooms is the upgrade, without a restart.** The agent keeps asking the
daemon, every 30 seconds while there is none and every minute after. When a
mesh appears, the agent starts serving on its address. The same happens
when this machine joins another mesh later; that used to need a restart.
Peers, names and per-mesh sessions (ADR-049) follow from the daemon as
before.

**The Basecamp app looks on the socket when there is no mesh.** The view
offers "this device" at the address `local`. `shrooms_core` connects to the
socket for that address, and only for that one; every other address must
still be a mesh address. Without a mesh, the view says what a mesh would
add (your other machines, your phone) instead of showing a daemon error.

**The installer no longer requires Shrooms.** `install-agent.sh` sets up
the socket ACL and the firewall only when it finds a shrooms daemon. If it
finds none, it says the agent will run on this machine only, and how to
add Shrooms later.

**Amended 2026-10-14: the tools are told which agent is theirs.** A
session's MCP tools (`shrooms-agent mcp`, started by Claude Code or pi) are
passed the agent's sockets as arguments (`--socket` for the daemon,
`--agent-socket` for the agent), in Claude's `--mcp-config` and in pi's
`mcp.json`. Before this, they used the default paths. A no-mesh agent beside
a running daemon (the stream's demo) had its sessions asking the daemon's
mesh instead, and one session's `ask_agent` to another on the same machine
went to the hostname over TCP and was refused. With the paths passed, the
same relay completed in 10 s.

## Consequences

- Trying the agents is one install: the agent, then Basecamp. The mesh
  comes in when someone wants another machine or the phone, which is the
  moment its value shows.
- With a mesh, nothing changes for peers. The socket adds a local way in
  for Basecamp and this machine's own tools.
- The phone app still needs the mesh: it is the other machine.
- Two ways in to keep consistent: a request over the socket and one from
  this machine's own mesh address are both local, and must stay equal.

## What would change our mind

If local mode leaves people stuck at one machine, the upgrade to Shrooms is
the part to fix, not the local mode. The signs would be the app's "add your
phone" prompt going unused, or people asking how to reach their agents
remotely.
