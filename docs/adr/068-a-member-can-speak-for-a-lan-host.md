# 068. A member can speak for a named LAN host, never for a LAN

**Status:** accepted 2026-10-21 by the owner, as the scope; not built. Narrows
the "subnet routing" in `docs/an-exit-node-for-the-mesh.md`, and doesn't
change ADR-030's "no exit nodes".

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

## Decision

**A member can speak for named LAN hosts, one at a time, each with its own mesh
name and address, its own list of ports, and the mesh admin's say-so.**

- **One host, never a prefix.**
  - A LAN host is one LAN address behind one member, the gateway, written in
    the gateway's config: `mesh.office.lan_hosts = ["printer=192.168.10.42/tcp:8883,990,6000,50000-50100"]`.
  - There is no way to route a range of LAN addresses.
- **Members never see the LAN address.**
  - A LAN host gets its own overlay address, derived like a member's (ADR-005)
    from the gateway's identity and the host's name.
  - It gets a synthetic IPv4 address too (ADR-021), and a name:
    `printer.pi5.office.mesh`, or `printer.office.mesh` when unambiguous.
  - The announce carries the name and that derived address, never
    `192.168.10.42`.
  - The gateway translates, so members can't map the LAN, and two LANs with
    the same range never clash.
- **Only the listed ports, only TCP to start.** The gateway accepts connections
  to the host's overlay address on its listed ports and ranges, and refuses the
  rest. UDP waits for a device that needs it.
- **Off unless the admin says so.**
  - Being a gateway for a host is a right the mesh admin signs: which member,
    which host name, which ports.
  - Without that grant, a lan_hosts entry stays local, logged and unannounced,
    so a member can't start speaking for devices on its own.
  - On a mesh with no admin, the option doesn't exist.
- **Not for cages.** Caged and sealed sessions get no route to LAN hosts.
  Their cage already reaches only the agent, and this keeps it that way.
- **Visible.** `shrooms status` and both apps list LAN hosts with their gateway
  ("printer via pi5") and whether the gateway is online.

## Consequences

- **The printer:** MQTT, FTPS with a passive range, and the camera, by name.
  One known gap: the FTPS server's passive reply names its own LAN address. A
  client that obeys it, rather than reusing the control connection's host,
  dials an address it can't reach. Most clients reuse the host (curl has by
  default for years); Bambu Studio and OrcaSlicer need checking against the
  real printer. No protocol rewriting at the gateway is planned.
- **The gateway is the host's single point of failure**, and its compromise
  opens that host's listed ports, nothing more. The admin's grant names the
  gateway, so moving a host to another member is a new grant.
- **The LAN host still can't tell members apart:** everything comes from the
  gateway. Acceptable for an appliance; it's why members stay members.
- **Revocation:** revoking the gateway's credential drops its hosts. Revoking a
  host's grant drops just that host.
- **Until it's built**, `sshuttle` from one machine to an office member over
  the mesh gives that machine the same reach, and nobody else.
- **Not settled here, for the build:**
  - the grant's wire form (a field in the credential, or its own signed
    object like a revocation);
  - how the derived address is computed;
  - how a phone reaches LAN hosts (it already routes overlay addresses,
    ADR-035).
