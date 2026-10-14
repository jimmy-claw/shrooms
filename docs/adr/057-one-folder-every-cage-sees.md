# 057. A folder caged sessions share

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
(sealed.go). Neither had any notion of a folder that belongs to no single session.

The first version gave every cage one machine-wide folder. Review found that it
crossed two boundaries already drawn: sessions limited to different meshes
(ADR-049) would have shared it, and sealed cages, which get "the internet and
nothing local" (ADR-045), could have read it and copied it out.

## Decision

**A per-cage option, "shared folder", like nix and the GitHub login: a podman
named volume at `/shared`, read-write, one per set of meshes, never for a sealed
cage.**

- **Asked for, not given.** `Cage.Shared`, chosen in "+ session" or the cage
  dialog, off by default. Anything that widens a cage is chosen; existing cages do
  not change on upgrade.
- **One per set of meshes.** `shrooms-shared` for sessions on every mesh;
  `shrooms-shared-LABELS-HASH` for a session limited to some (the labels for a
  person reading `podman volume ls`, the hash so two sets that look alike in a
  volume name stay apart). A session shares only with sessions seen from the same
  meshes, as ADR-049 sees them.
- **Never in a sealed cage**, not even read-only: for code nobody vouches for the
  danger is exfiltration, which read-only does not stop. `newCage` drops the
  option for a sealed cage, as it drops nix and the GitHub login.
- **A named volume, not a path on the machine.** A cage must not write into the
  owner's filesystem. A rootless podman volume lives under the user's own podman
  storage, and the owner can read it directly.
- **`/shared` in the cage, and `SHROOMS_SHARED=/shared` in its environment**, only
  in a cage that has it. Told, not discovered.
- **The cage is labelled with its volume** (`xyz.vpavlin.shrooms.shared`). A cage
  whose label is not what its session asks for now is made again on its next
  start. Changing a session's meshes stops it, so it does not keep the old
  meshes' folder until it next restarts.
- **The agent makes the volume, and checks one that is there.** Before a cage is
  made with it, the volume must be driver `local` with no options. The names are
  fixed, so whoever makes one first decides what it is, and
  `podman volume create --opt o=bind --opt device=DIR` would hand DIR read-write to
  every cage given it. Such a volume is refused, with how to remove it, and no cage
  is made.

## Consequences

- **No generation bump.** A cage made before this has no `Shared`, needs no mount,
  is not told `SHROOMS_SHARED`, and is kept with what was installed in it. A cage
  made again (the option ticked later, or its meshes changed) loses its container
  layer: installs and caches. Conversations survive, since `~/.claude`, pi's agent
  directory and a sealed cage's `.claude` are directories on the machine.
- **`nocopy`, after a colon.** An empty named volume is seeded from the image's
  content at the mount point on first mount, even on a read-only mount; `nocopy`
  stops that. The options follow a colon (`VOL:/shared:nocopy`): with a comma
  straight after the path, podman mounts at a directory called `/shared,nocopy`
  and the cage has no `/shared` at all (found with real podman, 2026-10-14).
- **`podman volume prune` can delete it.** A volume used by any container, even a
  stopped one, survives a prune; once every cage using it is removed, the next
  prune takes the folder and everything in it.
- **On the host side, treat it as untrusted.** A cage can leave a symlink there
  pointing at the owner's home: harmless inside cages, followed by the owner or an
  uncaged agent reading the folder on the machine.
- **Where it is on the machine.** `GET /v1/harnesses` gives podman's volume
  directory (`shared_root`) and the list gives each session's volume
  (`cage.shared`); the folder is `shared_root/VOLUME/_data`, shown in the cage
  dialog with OPEN. There is no path until the session's first start makes the
  volume, and none off Linux, where a podman machine reports a path inside its VM.
- **Nothing is shared across machines.** A volume is per user and per machine;
  handing something to another machine is still ADR-048.
- **No locking.** Several read-write containers on one volume are not
  coordinated. Fine for a clone several agents read and build from; a hazard for
  two agents writing the same file.
- **Do not add `:Z`.** `--security-opt label=disable` and no relabel is load
  bearing: a `:Z` relabel on a mount shared between containers takes it away from
  the others on an SELinux host. Every cage runs as container root (no `USER` in
  the workbench image), which is why files written there map to the one host user
  and stay writable by all of them; an owner image with its own `USER` would break
  that.
- **Tested with podman** by `TestARealSharedFolder` (`SHROOMS_REAL_CAGE=1`): two
  cages see each other's file at `/shared` and nowhere else, and a bind volume is
  refused.
