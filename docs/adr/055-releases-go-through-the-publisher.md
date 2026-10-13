# 055. Releases go through the publisher, and public ones wait for the owner

**Status:** accepted 2026-10-08, in use since — every Shrooms and Shrooms
Agents release from 0.41.0 on. The publisher's own rules live with it, on
jimmy-crib; this records what this repository does about them.

## Context

The apps reach people through package repositories: on the LAN, two for
Basecamp (lan02, lan03) and one F-Droid repository (fdroid-loam), all on
jimmy-crib; in public, one of each on apps.vpavlin.xyz. Whichever session
built a release published it, with scripts that ssh to jimmy-crib
(`scripts/publish-lan.sh`, `scripts/publish-fdroid.sh`). The traps were
quiet: a repository nothing read, a versionCode not above the last, a `-dev`
build Basecamp lists as not available. Each publish "succeeded" and devices
kept the old version.

On 2026-10-08 jimmy-crib/vpavlin made an agent session for this,
jimmy-crib/publisher, over the agent API. The mesh is its authentication
(ADR-041).

## Decision

**Every release is asked of jimmy-crib/publisher**, with the files, the
targets, and each package's expected name and version. It checks them,
publishes, reads back what is actually served, and answers with the URLs.

- **The LAN repositories first**, without asking anyone: lan02 and lan03 for
  Basecamp, fdroid-loam for the APKs. They are the owner's devices on the
  owner's network.
- **The public ones need the owner's phone.** A publish to `public-basecamp`
  or `fdroid-public` waits for an approval code from the owner, single-use,
  one per publish. Writing "approved" in a request does nothing.
- **The publisher never signs.** An APK arrives signed with its app's own key,
  which never leaves where it is kept: `SIGN_ONLY=1 scripts/publish-fdroid.sh`
  builds and signs on the host that holds the key and leaves the APK for the
  publisher. An APK's signature is its identity: Android refuses an update
  signed by another key, and for shrooms the way past that loses the phone's
  mesh identity.
- **Basecamp packages are multi-arch**: one `.lgx` per module carrying
  linux-amd64 and linux-arm64 (`lgx merge` of the two portable builds), since
  an arm64 tablet runs Basecamp. The view and its core go together: a view
  without the core it expects fails as buttons that do nothing.
- **The agent binary is not a release of the app.** It ships in its own image,
  `ghcr.io/vpavlin/shrooms-agent` (amd64 and arm64), pushed by CI after the
  tests, not by hand. It is not a layer of the shrooms image: nodes follow
  `shrooms:latest` with podman auto-update, and publishing the agent there
  would roll a new daemon onto all of them.

## Consequences

- A release can be left to any session on the mesh, and a public one still
  cannot happen without the owner seeing it on the phone.
- A release with several public publishes needs several approvals.
- The Agents APK is signed v1 as well as v2, because the publisher's check
  verifies at `--min-sdk-version 19`, which demands v1.
- `make basecamp-publish` and the direct scripts remain, as the way when the
  publisher cannot do it, and a release made that way says so.
- The publisher's checks, targets and approval flow are not in this
  repository. A change there changes how releases are made here, without a
  commit here.

## What would change our mind

A release pipeline in CI that signs with a key it cannot leak and publishes to
the public repositories on a tag, with the same owner approval. Then the
publisher would be one more step, not the gate.
