# 066. A device name is held by the key that claimed it first

**Status:** accepted 2026-10-20 by the owner, after the security audit (F-18)
and the auditor's re-check; built.

## Context

Device names are announced by the device itself (ADR-008). When two keys
claim one name, something has to decide which of them `laptop.home.mesh`, the
hosts file and every service named under the device mean. The answer was
changed three times, and each one had a hole:

- **The freshest announce.** This was chosen because the usual way a name is
  claimed twice is one device that joined again under a new key while the
  old entry lived out its six hours (`k11.mesh` answering nothing). But a
  member could take any name by announcing more often (audit F-18,
  2026-10-19).
- **The one online claimant, and nothing when two are online.** A member
  could wait for a laptop to sleep and be given its ssh. Claiming a name
  while its owner was online made it answer nothing (the auditor's re-check,
  2026-10-20).

Announces alone cannot tell a device that joined again from a member taking
its name: both look like a new key with an old name while the old key is
quiet.

There is also a second way in. On a mesh with an admin, the credential
carries a name, but the announced one was used. Renewal signs the names the
roster shows, so a claimed name would have been signed at the next renewal.

## Decision

- **A name is held by the key that has claimed it longest, online or not.**
  - The holder keeps it until its entry is forgotten (`ForgetAfter`, six
    hours of silence) or revoked.
  - A later claimant answers to its name and the last group of its address
    (`laptop-c54c`), in the resolver, the hosts file, the daemon's status and
    the phone.
  - Within a claim's lifetime, a heartbeat does not change who holds it, and
    neither does a restart: the claim's start is remembered with the roster.
    A rename starts a new claim.
  - `mesh.Holders` is the rule; everything that names a device uses it.
- **On a mesh with an admin, a device is called what its credential says.**
  - The announced name is ignored there, so renewal keeps the admin's name.
  - Renaming a device on such a mesh means the admin issuing it a credential
    with the new name.
- **This device keeps its own name in its own hosts file**, as before.

## Consequences

- A member can no longer take a sleeping device's name, nor make a name
  answer nothing by claiming it too.
- **A device that joins again under a new key does not get its name back at
  once.** It answers to `name-xxxx` until the old key is revoked, which
  frees the name at once, or until it is forgotten six hours later. Revoking
  the old key is part of re-enrolling a device.
- `Roster.Current` no longer hides an old entry that holds its name, so the
  lists show both until the old one goes.
- A rename on a mesh with an admin waits for the admin.
- **On a mesh without an admin, a name is only first come, first served.**
  The auditor's re-check (2026-10-20) showed what is left there:
  - A member can claim a name before the device that wants it joins.
  - It can claim one while its device has been off long enough to be
    forgotten, which a night can be, and keep it while it announces.
  - Each node times claims by its own clock, so a member can make its claim
    on some nodes and not others.

  None of these can be closed without someone whose word on names counts, and
  on such a mesh nobody's does: whoever holds the network key is a member as
  good as any other (ADR-008). That is what an admin is for. A mesh whose
  names matter should have one; home and office do, `default` does not.
  Credentials always carry a name: every way of issuing one refuses an empty
  name, so on a mesh with an admin a name never falls back to the announced
  one.
