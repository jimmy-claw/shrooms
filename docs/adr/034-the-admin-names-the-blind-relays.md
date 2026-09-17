# 034. The admin names the blind relays

**Status:** accepted, built 2026-09-17 — extends [ADR-014](014-relay-discovery-via-announce.md)

## Context

ADR-014 made member relays discoverable: a relay is a peer with a flag on its
announce, and nothing has to be distributed. Blind relays
([docs/blind-relays.md](../blind-relays.md)) are the case it cannot cover. They
hold no network key and run no delivery node, so they never announce, and every
device had to be given `relay_blind` by hand.

That cost was real. The `office` mesh on 2026-09-16 was four machines behind one
home router and a phone; none had a public address, and the blind relay that
fixed it — by carrying traffic and by telling each machine where it was seen —
had to be typed into every one. Then it was redeployed at a new port.

Two ways to distribute one were weighed
([distributing-a-blind-relay.md](../distributing-a-blind-relay.md)): any member
self-asserting a relay in its announce, or the admin signing a statement.
Pointing a mesh at a blind relay exposes every member's traffic tags, timing and
volume to a third party, which is not the same kind of act as a member offering
to forward. Vaclav chose the admin.

## Decision

**A mesh's admin signs a `cred.RelayAdvice` naming up to four blind relays and
an optional token, and members adopt it.**

- The statement carries the mesh id and a serial. Nodes verify it against the
  authority, keep the highest serial, persist it, and refuse anything lower.
  An empty list is a statement too; it withdraws the advice.
- It travels in its own control message, `relay-advice`, shaped like `revoke`
  and `grant` — any member may relay it, since the signature inside is what
  counts — but sealed under the current generation rather than generation zero,
  because it may carry a token.
- Nodes repeat it at each epoch and when a peer appears, under the
  `announce_revocations` switch.
- **It is advice.** A device whose config lists blind relays keeps them, and
  `relay_blind = "none"` (now accepted at the top level as well as per mesh)
  refuses it. Adopted relays come after configured ones in every ordered walk.
- `shrooms admin relay set|clear` signs through the Signer seam (key file or
  card) and publishes through the daemon's root-only `/relay-advice`, as
  `/revoke` does.

## Consequences

- A blind relay is configured once per mesh. Moving it is one command, from
  wherever the admin key is.
- Changing it needs the admin key. A device can still override locally, which
  is the escape hatch while the card holder is away.
- A member cannot redirect anybody's traffic: it can only repeat or withhold a
  statement the admin signed, and withholding is indistinguishable from being
  offline.
- Older nodes cannot read the message and ignore it as undecryptable; nothing
  changes for them.
- The endpoint is root-only today. By ADR-033's reasoning it is
  signature-gated and could move to the group tier with `/revoke` and `/grant`
  when those move.

## What would change our mind

Meshes run by several people who each want to offer a relay without holding the
admin key. That is the self-asserted design, and it could be added beside this
one — at a lower precedence than anything the admin signs.
