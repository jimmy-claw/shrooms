# Distributing a blind relay

**Status:** built 2026-09-17, admin-signed — see
[ADR-034](adr/034-the-admin-names-the-blind-relays.md). Vaclav, 2026-09-16: *"I
still feel like there could/should be a way for us to distribute the blind relay
we want to use among the mesh peers — I feel like we talked about this?"*

We did, in the shape of its opposite. [ADR-014](adr/014-relay-discovery-via-announce.md)
says a relay is found by announcing itself, and concludes that **"any node can
become a relay by setting one config value, with nothing to distribute
afterwards."**

That holds for a *member* relay. A blind relay is not a member, has never sent
an announce, and cannot send one — so it is precisely the case ADR-014's
conclusion does not cover, and it was the only relay that had to be configured
on every node by hand.

## How to use it

On the machine that holds the mesh's admin key, or with its Keycard:

    shrooms admin relay set 222.167.212.15:31760 [--token T] [--mesh office]
    shrooms admin relay clear [--mesh office]

That signs a statement and hands it to the local daemon, which publishes it.
Every member:

- verifies it against the mesh's admin keys and the mesh id;
- keeps it only if its serial is higher than the one held (serials default to
  unix seconds, bumped past the held one if needed), so an old statement
  replayed later changes nothing;
- writes it to `relay-advice-<mesh>.json` in the state directory and verifies
  it again on the next start;
- repeats it at every epoch rotation and when a peer appears (two-minute
  cooldown), under the same `announce_revocations` switch as revocations;
- **uses the relays only if its own config lists no blind relays and does not
  say `relay_blind = "none"`**. Otherwise it holds the statement, logs that it
  is not using it, and still passes it on.

An adopted relay behaves exactly like a `relay_blind` entry, after any
configured ones: it is registered with (at most two, as always), selected by the
same order (DESIGN §8), and may report where it saw us (`relay.TypeObserved`).
`shrooms status` marks it "(named by the mesh's admin)", and the status JSON
carries `relay_advised` and `relay_advice_serial` per mesh.

On the wire the statement is `cred.RelayAdvice` — version, mesh id, serial,
issue time, up to four relays, one token of at most 128 bytes, and the admin
signature over a domain-separated digest, so a card can sign it. It travels in
a `relay-advice` control message sealed under the current announce generation:
it may carry a token, and a device rotated out of the mesh should not learn a
new one.

**The phone cannot issue one** — issuing needs the admin key — but it adopts
them like any member, and typing `none` in its blind relay field refuses them.

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

**Decided 2026-09-17: the admin, signed by the authority.** Both options below
were put to Vaclav; he chose this one.

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
Chosen. There is precedent and machinery: `cred.Rotation` is an admin-signed
statement already distributed on the control plane, and `publishGrant` already
ships signed things to members. It puts the choice of third party with the
person who already decides membership, which is the same kind of decision.

The cost is that it needs the admin key to change, so a relay cannot be swapped
while the person holding the card is away — the situation `relay_blind` on a
single device remains the escape hatch for.

**A recommendation is not an instruction.** As built: `relay_blind = "none"`
refuses it, and a device given relays by hand keeps them. Otherwise this would
be a way to move somebody's traffic without their consent.

## A correction worth keeping

The blind relay at the centre of this — the one three machines were pointed at
in September — was repeatedly described in this session's notes as dead, on the
evidence of a laptop sending 143 KB into it and receiving nothing. It **was
never dead**. It was carrying traffic for other devices throughout, and Vaclav
reached pi5 through it from a phone on 2026-09-16.

The silence had a different cause: the laptop had registered with that relay
while k11 had registered with vps, and a relay forwards only between peers
registered with IT. Two working ends, two working relays, no path. See
`registerWithRelay`, which used to register with whatever was CONFIGURED while
`selectRelay` routed through whatever it had CHOSEN.

"The relay is dead" is the tempting reading of one-way traffic and it is usually
wrong. Check which relay each end registered with first.

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

The distribution question above was the last piece, and is built.
