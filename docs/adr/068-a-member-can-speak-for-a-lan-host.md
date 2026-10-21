# 068. A member can speak for a named LAN host, never for a LAN

**Status:** accepted 2026-10-21 by the owner, as the scope; not built. Amended
the same day after the auditor's review (`08-adr-068-review.md` in the audit
folder). Narrows the "subnet routing" in `docs/an-exit-node-for-the-mesh.md`,
and doesn't change ADR-030's "no exit nodes".

## Context

Václav wants to print from home to a Bambu Lab printer in the office, in LAN
mode, without the cloud (2026-10-21). The printer can't run shrooms; neither
can most cameras, NAS boxes and other appliances he would want reachable. A
Bambu client needs:
- TCP 8883 (MQTT over TLS);
- TCP 990 (implicit FTPS) plus passive-mode data connections on high ports;
- the camera on TCP 6000 or RTSP 322.

So one forwarded port per service (ADR-023) isn't enough.

The general answer elsewhere is a subnet router: one member advertises
`192.168.10.0/24` and the whole mesh reaches the LAN. That was rejected here,
for what it would expose:
- **Every member reaches everything on the LAN.** That includes things never
  meant to be reachable (router admin pages, appliances with weak auth), and
  every agent session on every member machine. A prompt-injected agent could
  scan the office.
- **It links the mesh to a place.** Advertising a prefix tells every member what
  network sits behind which device, and lets them map it. Announces are
  encrypted, so outsiders don't see it, but every member does.
- **The LAN can't tell members apart.** Everything arrives from the gateway's
  address.
- **Address clashes.** Home and office LANs often use the same range.

Devices that *can* run shrooms stay members: each has its own keys, can be
revoked on its own, and is reached end to end with no gateway in the middle.
This is only for devices that can't.

### What the grant can and cannot contain

The review's first finding sets the honest frame:
- **A gateway already has its LAN.** A compromised one can point a granted
  name at any LAN address and port, and members can't check, since they never
  see the LAN address.
- **Any member can already proxy its LAN without a grant.** Today's `services`
  forwards to any `host:port` (`internal/service/service.go`: `Target`, e.g.
  `nas:80->192.168.1.5:8080`), and the target isn't announced. Any program a
  member runs could do the same.

So the grant is not a containment boundary. **The gateway is trusted for its
own LAN.** What this design adds is:
- an admin's say-so before a LAN host is *named and offered* to the mesh;
- a narrow, default-deny way to offer it;
- a refusal everywhere else.

## Decision

**A member can offer named LAN hosts, one at a time: each with its own mesh name
and address, its own list of ports, and a grant signed by the mesh admin. Every
member can reach them. Cages and anything acting for a cage can't.**

### The host

- **One host, never a prefix.** A LAN host is one LAN address behind one
  gateway member, in the gateway's config:
  `mesh.office.lan_hosts = ["printer=192.168.10.42/tcp:8883,990,6000,50000-50100"]`.
  There is no way to route a range of LAN addresses.
- **The LAN address is a literal, checked.** An exact IPv4 or IPv6 literal,
  never a name (no DNS, so no rebinding). It must be on a link the gateway is
  on, and must not be:
  - loopback, link-local, unspecified, multicast or broadcast;
  - one of the gateway's own addresses;
  - a mesh or synthetic range (ADR-005, ADR-021).
- **Limits.** At most 16 hosts per gateway and 256 ports per host, in at most
  8 ranges. A grant for 1–65535 is the whole device, so it's refused. These
  caps also bound the announce.

### Names and addresses

- **Members never see the LAN address.** A host is announced with a stable host
  ID, taken from its grant, and a derived overlay address. The derivation is
  ADR-005's, from the gateway's identity and the host ID, with a domain
  separator so it can never equal a member's address. A synthetic IPv4 follows
  (ADR-021).
- **A namespace of its own.** LAN hosts are named
  `<host>.lan.<gateway>.<mesh>.mesh`, e.g. `printer.lan.pi5.office.mesh`. There
  is no short form. Neither a member's name (ADR-066) nor a service's name
  (`<service>.<device>.<mesh>`) can take this shape: `lan` is reserved as a
  service and device label. A declaration that collides is refused, not
  resolved by first claim.

### The grant

- **Its own admin-signed object**, distributed like revocations, not a field in
  the credential: a credential is announced, renewed on its own schedule and
  size-bounded. The grant binds:
  - the mesh;
  - the gateway's device key;
  - the host ID and name;
  - the protocol and ports;
  - a commitment to the LAN address (a hash, so the grant itself doesn't
    publish it);
  - a serial, not-before and not-after;
  - a domain separator.
- **Members verify it.** A node offers a host only with a valid, unexpired,
  unrevoked grant naming it. Members reject a host announced without one, or
  with ports beyond its grant.
- **Revocable.** Revoking the grant drops that host; revoking the gateway's
  credential drops all its hosts. On a mesh with no admin, the feature doesn't
  exist.

### The gateway

- **A per-port TCP proxy, default deny.** The gateway accepts connections to the
  host's overlay address on its granted ports only, and opens its own
  connection to the LAN address. There is no IP forwarding and no broad DNAT,
  so nothing unlisted has anything to reach. Connections are capped per host
  and per member.
- **In its own network namespace.** The host's overlay address lives on a
  dedicated TUN in a namespace of its own. If it sat on the gateway's normal
  interface, a member could reach any service the gateway binds to all
  addresses through it.
- **TCP only, to start.** Under a TCP proxy, every connection has a start and an
  end, so limits, logging and refusals are simple. UDP would need a per-flow
  translation table (NAT-style state), and many UDP protocols carry addresses
  inside the traffic (RTP, SIP, discovery), the same problem as FTPS passive
  mode. Multicast and broadcast discovery can't be carried to one host at all.
  The printer doesn't need UDP: MQTT, FTPS and the 6000 camera are TCP, and the
  X1's RTSP runs over TCP. UDP waits for a device that needs it, as a per-flow
  relay under the same port lists and limits. ICMP is not carried.

### Who can reach a host

- **Every member of the mesh.** There is no per-member list. A compromised
  member, or an uncaged session on any member machine, can use the host's
  granted ports. That's the same trust as reaching any member's services today.
- **Not caged sessions, and not on their behalf.** "Not for cages" needs two
  things today's code doesn't do:
  - **Every cage blocks the whole mesh range,** members, services and LAN hosts
    alike. Today an unsealed cage blocks only the agent's port
    (`cageRules(false)`), and the cage's traffic leaves with its machine's
    mesh address, so the far side can't tell it came from a cage.
  - **A caged session can't make another session do it.** A task from a caged
    session to an uncaged one (`/a2a`, `ask_agent`) could ask it to fetch the
    host: a confused deputy no network rule stops. The agent marks such tasks
    as coming from a cage. A LAN host's gateway refuses connections made for a
    cage-originated task, and the receiving session is told its asker is
    caged, so it shouldn't fetch mesh resources for it.
- **Visible.** `shrooms status` and both apps list LAN hosts as "printer via
  pi5", with whether the gateway is online.

## Consequences

- **The printer's FTPS is a real risk, not just a gap.** The FTPS server's
  passive reply names its own LAN address, and that reply is inside TLS, so the
  gateway can't rewrite it. A client that obeys it, rather than reusing the
  control connection's host, connects to that address on *its own* LAN. If the
  member's home LAN uses the same range, credentials and the file go to
  whatever local device has that address. Before the printer case ships:
  - test Bambu Studio and OrcaSlicer against the real printer for which host
    they use;
  - if they obey the reply, gate the printer case on a client that uses EPSV
    (address-free) or reuses the control host;
  - an FTPS proxy that terminates TLS is the alternative, and isn't planned.
- **Two issues in today's code, recorded here and fixed on their own:**
  - **Unsealed cages reach the whole mesh.** They should block the mesh range
    whatever this ADR does: a caged session today can reach every member's
    services.
  - **`services` targets aren't restricted.** Today they may be any host. Either
    restrict them to this machine's own addresses (with LAN devices going
    through this ADR), or keep them open and say plainly that a member can
    always proxy its LAN. To be decided with the cage fix.
- **The gateway is the host's single point of failure**, and its compromise
  opens that host's granted ports, and, honestly, anything else on its LAN it
  chooses to proxy. The grant names the gateway, so moving a host to another
  member is a new grant.
- **The LAN host can't tell members apart:** everything comes from the
  gateway. Acceptable for an appliance; it's why members stay members.
- **Until it's built**, `sshuttle` from one machine to an office member over
  the mesh gives that machine the same reach, and nobody else.
- **Settled in the build:**
  - IPv6 hosts;
  - the synthetic IPv4 block for hosts;
  - how phones reach hosts (ADR-035);
  - one host offered by several gateways;
  - renaming a gateway;
  - renewing grants;
  - logging.
