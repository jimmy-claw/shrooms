#!/usr/bin/env python3
# icmp6probe.py — is ICMPv6 to a local address really refused?
#
# cageRules puts `meta l4proto ipv6-icmp accept` ABOVE the ip6 daddr reject, so ICMPv6 to any
# address — mesh, link-local, multicast — is accepted. The ADR says those ranges are "refused"
# and that a hostile review "cannot touch the LAN". This measures which is true.
#
# Usage: icmp6probe.py <destination-ipv6> [udp-port-for-the-control]
import socket, struct, sys

dst = sys.argv[1]
ctl_port = int(sys.argv[2]) if len(sys.argv) > 2 else 9999


def csum(payload):
    if len(payload) % 2:
        payload += b"\x00"
    s = sum(struct.unpack("!%dH" % (len(payload) // 2), payload))
    s = (s >> 16) + (s & 0xFFFF)
    s += s >> 16
    return ~s & 0xFFFF


# The source the kernel would choose, via a connected UDP socket (no `ip` in this image).
u = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
u.connect((dst, 53))
src = u.getsockname()[0]
u.close()
print("  source the kernel chose:", src)

# CONTROL: UDP to the same address on a port that is not 53. The reject must bite here.
c = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
c.settimeout(3)
try:
    c.sendto(b"control", (dst, ctl_port))
    try:
        c.recvfrom(2048)
        print("CONTROL udp/%d: got something back (unexpected)" % ctl_port)
    except socket.timeout:
        print("CONTROL udp/%d: sent, no answer (a reject shows up as an error, a drop as silence)" % ctl_port)
except OSError as e:
    print("CONTROL udp/%d: BLOCKED at sendto — %s" % (ctl_port, e))
c.close()

# THE QUESTION: an ICMPv6 echo request to the same address.
# Two ways in. A raw socket needs CAP_NET_RAW, which a sealed cage does not have; the "ping
# socket" (SOCK_DGRAM + IPPROTO_ICMPV6) needs no capability, only the group range. Try both,
# because "I could not send ICMPv6" and "ICMPv6 was refused" are different answers.
for kind, typ in (("raw", socket.SOCK_RAW), ("ping-socket", socket.SOCK_DGRAM)):
    try:
        s = socket.socket(socket.AF_INET6, typ, socket.IPPROTO_ICMPV6)
    except OSError as e:
        print("ICMPv6 echo via %s: could not even make the socket — %s" % (kind, e))
        continue
    break
else:
    sys.exit(0)
if typ == socket.SOCK_DGRAM:
    # A ping socket: the kernel writes the ICMPv6 header and checksum. Send an empty payload.
    try:
        s.sendto(b"shrooms-cage-probe", (dst, 0))
        s.settimeout(4)
        pkt, addr = s.recvfrom(2048)
        print("ICMPv6 echo via ping-socket: REPLY from %s — ICMPv6 to this address is ALLOWED" % addr[0])
    except OSError as e:
        print("ICMPv6 echo via ping-socket: failed — %s" % e)
    sys.exit(0)
s.settimeout(4)
s.settimeout(4)
ident, seq, data = 0x5A17, 1, b"shrooms-cage-probe"
hdr = struct.pack("!BBHHH", 128, 0, 0, ident, seq) + data
ph = (socket.inet_pton(socket.AF_INET6, src) + socket.inet_pton(socket.AF_INET6, dst)
      + struct.pack("!I3xB", len(hdr), 58))
icmp = struct.pack("!BBHHH", 128, 0, csum(ph + hdr), ident, seq) + data
try:
    s.sendto(icmp, (dst, 0))
except OSError as e:
    print("ICMPv6 echo: BLOCKED at sendto — %s" % e)
    sys.exit(0)
try:
    while True:
        pkt, addr = s.recvfrom(2048)
        if pkt[0] == 129 and pkt[4:6] == struct.pack("!H", ident):
            print("ICMPv6 echo: ECHO-REPLY from %s — ICMPv6 to this address is ALLOWED" % addr[0])
            sys.exit(0)
except socket.timeout:
    print("ICMPv6 echo: sent, no reply within 4s (allowed but unanswered, or dropped)")
