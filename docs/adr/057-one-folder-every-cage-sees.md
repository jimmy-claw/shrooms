# 057. One folder every cage sees

**Status:** accepted 2026-10-14 — built the same day

## Context

Caged agents kept needing to work on the same thing: one clone of a repository, a
dataset, a set of fixtures, several agents building and testing against it. ADR-048
(transfer) is the wrong shape for that — it copies a file to one receiver, and
copying a repository to five cages means five copies that immediately diverge, no
way for a cage to know the set is complete, and five turns spent announcing
arrival. A cage that is told to work in a folder it cannot see is not working in
the folder.

The mounts a cage gets are built in `cageMounts` (cage.go) and `sealedMounts`
(sealed.go). Neither is configurable, and neither had any notion of a folder that
belongs to no single session.

## Decision

**One podman named volume, `shrooms-shared`, mounted at `/shared` in every cage —
read-write for a caged session, read-only for a sealed cage.**

- **A named volume, not a path on the machine.** A cage must not write into the
  owner's filesystem, and a host directory shared between cages would be exactly
  that. A rootless podman volume lives under the agent's own storage, is owned by
  the user that owns it, and the owner and the agent can read it directly to see
  what a cage produced.
- **`/shared` in the cage, the same path in every cage.** One path to document and
  one to remember, and a session that moves between caged and sealed does not
  have to relearn where data lives.
- **Read-write only for caged sessions.** A caged session is the owner's own agent,
  which is already trusted with the machine. A sealed cage holds code nobody
  vouches for (ADR-045): a folder every cage may write is a folder any one of them
  may poison for the others, so a sealed cage reads it and cannot write it.
- **`mount.volume`, and the filter keeps it.** Both mount builders drop what is not
  there (`os.Stat`), and a volume name is not a path — so without this the shared
  folder would be dropped from every cage silently. One filter (`keepMounts`) is
  used by both builders, so the rule cannot be right in one and wrong in the other.
- **No configuration.** A fixed volume and a fixed path, like the cage proxy's own
  directory. A cage is not the place to discover a mount list at runtime.

## Consequences

- **A cage made before this does not have it, and says it does.** The mount is set
  at `podman create` time and an existing container is reused, so cages must be
  removed and made again. Worse than a missing feature: the cage is still told
  `SHROOMS_SHARED=/shared`, and a caged session's own rootfs is writable, so it
  would `mkdir /shared`, write into its private layer, and believe it was sharing.
  **The cage generation is therefore BUMPED**, not merely documented — the remake
  path on a generation mismatch is what makes the bump enough.
- **The volume is not copied into.** An empty named volume is seeded from the
  image's content at the mount point on first mount, from the image side, even on
  a read-only mount. `:nocopy` is set on every volume mount, so whichever image a
  cage first uses cannot leave anything in the shared folder.
- **The name is constant; the definition is not.** Whoever creates
  `shrooms-shared` first decides what it is, and
  `podman volume create --opt type=none --opt o=bind --opt device=<host dir>`
  is honoured silently — every caged session would then have that host directory
  read-write. The agent therefore reports no host path unless the volume is driver
  `local` with no options. A cage cannot do this itself: it has no podman socket.
- **`podman volume prune` can delete it.** A volume referenced by any container,
  even a stopped one, survives a prune — but once every cage is removed, the next
  prune takes the folder and everything in it. If that matters, keep a holder
  container or back the folder up; this is documented rather than prevented.
- **On the host side, treat `/shared` as untrusted.** A read-write cage can leave
  a symlink there pointing at the owner's home, which is harmless inside cages (it
  resolves in each container's own filesystem) but is followed by the owner, or by
  an uncaged agent told to read the folder at the host path.
- **No host path off Linux.** A podman machine reports a path inside its VM, which
  does not exist where the app runs, so none is reported rather than a wrong one.
- **Nothing is shared across machines.** A volume is per-user and per-machine;
  handing something to another machine is still ADR-048.
- **No locking.** Several read-write containers on one volume are not
  coordinated. Fine for a clone several agents read and build from; a hazard for
  two agents writing the same file.
- **Do not add `:Z`.** `--security-opt label=disable` and no relabel is load
  bearing: a `:Z` relabel on a mount shared between containers takes it away from
  the others on an SELinux host. Every cage also runs as container root (no `USER`
  in the workbench image), which is why files written there map to the one host
  user and stay mutually writable — an owner image with its own `USER` would break
  that silently.
- **The owner can look.** The volume's directory is readable by the user the agent
  runs as, so what a cage wrote is inspectable without going through a cage.
