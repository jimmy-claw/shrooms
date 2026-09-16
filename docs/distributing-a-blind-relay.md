# Distributing a blind relay

**Status:** not built. Vaclav, 2026-09-16: *"I still feel like there could/should
be a way for us to distribute the blind relay we want to use among the mesh
peers — I feel like we talked about this?"*

We did, in the shape of its opposite. [ADR-014](adr/014-relay-discovery-via-announce.md)
says a relay is found by announcing itself, and concludes that **"any node can
become a relay by setting one config value, with nothing to distribute
afterwards."**

That holds for a *member* relay. A blind relay is not a member, has never sent
an announce, and cannot send one — so it is precisely the case ADR-014's
conclusion does not cover, and the only relay that has to be configured on every
node by hand.

## Why it matters more than it looks

An `office` mesh on 2026-09-16: four machines and a phone, every one of them
behind the same home router.

    mesh office   peers 4   no relay
    atlas    192.168.10.59:51820
    pi5      192.168.10.219:51820
    proteus  192.168.10.13:51820
    scribe   192.168.10.233:51820

Not one announces a public address, and none ever will — **reflexive discovery
needs a peer outside the NAT**, and there isn't one. `HandlePong` records "where
the peer saw us"; with every member behind one router, nobody is ever seen from
outside. So the phone on 5G has nothing to dial and nothing to punch toward,
and NAT traversal is not weak here so much as unfed.

A blind relay fixes both halves at once, without adding a member: it is outside
the NAT, so it can say where it saw you, and it can carry traffic on the days
punching fails. But every node has to be told about it separately, and a mesh is
exactly the thing that should not need that.

## The decision: who may say which relay to use

**Any member, self-asserted — like `relay = true` today.** ADR-014 already
accepts self-assertion for relay willingness, reasoning that a member "could
drop traffic anyway" and that relays are probe-confirmed before use. Simple, and
symmetric with what exists.

The argument does not carry over cleanly, though. A member relaying for you is
someone already inside. A **blind** relay is a third party with no access at
all, and being pointed at one grants a stranger the tags, timing and volume of
your traffic. It still cannot read anything — the mesh key never leaves, and
handles are per-relay tags — but it is new exposure created by somebody else's
config.

**The admin, signed by the authority — like a credential or a revocation.**
There is precedent and machinery: `cred.Rotation` is an admin-signed statement
already distributed on the control plane, and `publishGrant` already ships
signed things to members. It puts the choice of third party with the person who
already decides membership, which is the same kind of decision.

The cost is that it needs the admin key to change, so a relay cannot be swapped
while the person holding the card is away — the situation `relay_addr` exists as
an escape hatch for.

**A recommendation is not an instruction.** Whichever signs it, the receiving
node should treat it as a candidate, not an order: `relay_blind = "none"` must
keep overriding it, and a node that has been given one by hand should keep it.
Otherwise this becomes a way to move somebody's traffic without their consent.

## What it would take

Small, on either choice:

- a field on the announce (self-asserted) or a new signed statement (admin),
  carrying one or more `addr:port` and optionally a token
- adopt it into the same list `relay_blind` fills, at lower precedence than
  anything configured locally
- nothing else: selection, registration, tags and first-claim-wins already work
  once an address is in that list

## Related, and worth doing either way

**A blind relay reports where it saw you — built 2026-09-16.** It has `from` on every
register and never says so. One new frame type — the challenge frame is
fixed-length and MAC'd, so it cannot be extended without breaking every client
— and a node pointed at a blind relay gets reflexive discovery without any
public member. `Prober.Reflexive` already keeps a single uncorroborated
observation ("no worse than having no candidate at all"), so one relay is
enough.

That is the piece that makes `office` work *directly* rather than through the
relay: each machine learns its public address, announces it, and the phone dials
it. The relay carries nothing unless punching fails.

Shipped as `relay.TypeObserved` (frame type 7) and `Prober.NoteReflexive`. A
relay answers every accepted registration — member or blind — with the address
it arrived from; registrations already refresh on a timer, so a NAT rebinding
changes the answer without needing anything new. The client takes it only from a
relay it configured, and treats it as a candidate: it is probed like any other
address, and `Reflexive` weighs it against what peers report, so one relay
repeating itself cannot corroborate itself past the agreement rule.

Only the distribution question above is left.
