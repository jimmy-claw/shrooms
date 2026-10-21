package wg

import (
	"net/netip"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// SelfOnly wraps the device WireGuard writes into, so that only packets
// addressed to this device's own overlay address reach the operating system.
//
// WireGuard checks a packet's source against the sending peer's AllowedIPs and
// nothing else: the destination is whatever the peer wrote. Handed to the
// kernel unfiltered, a member's packet to this machine's LAN or public IPv6
// address reached any service bound to it, and with forwarding on, the LAN
// and the internet beyond, as though this node were an exit (ADR-030 says it
// is not). The audit found it on a LAN host gateway (F-26, 2026-10-21), but
// nothing about it was the gateway's: every node did it.
//
// A peer has no business sending anything else. Its packets to other peers go
// to them; the resolver answers on a read-path intercept, never on what
// arrives from the tunnel; neighbour discovery has no meaning on a TUN.
func SelfOnly(t tun.Device, self netip.Addr) *SelfOnlyTun {
	return &SelfOnlyTun{Device: t, self: self}
}

// SelfOnlyTun is a tun.Device that drops inbound packets for anyone else.
type SelfOnlyTun struct {
	tun.Device
	self    netip.Addr
	dropped atomic.Uint64
}

// Write is WireGuard's inbound path.
func (t *SelfOnlyTun) Write(bufs [][]byte, offset int) (int, error) {
	kept := bufs[:0:0]
	for _, b := range bufs {
		if ForSelf(b[offset:], t.self) {
			kept = append(kept, b)
		} else {
			t.dropped.Add(1)
		}
	}
	if len(kept) > 0 {
		if _, err := t.Device.Write(kept, offset); err != nil {
			return 0, err
		}
	}
	return len(bufs), nil
}

// Dropped is how many packets were not for this device.
func (t *SelfOnlyTun) Dropped() uint64 { return t.dropped.Load() }

// ForSelf reports whether p is an IPv6 packet addressed to self that carries
// TCP, UDP or ICMPv6 and nothing that could send it further.
//
// The destination alone was not enough (audit F-39): a routing header names
// the next hop inside the packet, so a packet "for" this node could ask its
// kernel to pass it on, which only a kernel default (accept_source_route=0)
// refused. So no extension header passes but a fragment header, which large
// UDP needs and which names the protocol it carries too; and no ICMPv6 that
// steers the node's routing or neighbours, which have no meaning on a tunnel.
func ForSelf(p []byte, self netip.Addr) bool {
	if len(p) < 40 || p[0]>>4 != 6 {
		return false
	}
	var a [16]byte
	copy(a[:], p[24:40])
	if netip.AddrFrom16(a) != self {
		return false
	}
	next, at, first := p[6], 40, true
	if next == protoFragment {
		if len(p) < 48 {
			return false
		}
		next, at = p[40], 48
		first = p[42] == 0 && p[43]&0xf8 == 0 // fragment offset zero
	}
	switch next {
	case protoTCP, protoUDP:
		return true
	case protoICMPv6:
		if !first {
			return true // the type is in the first fragment, checked there
		}
		if len(p) < at+1 {
			return false
		}
		t := p[at]
		// Router solicitation and advertisement, neighbour solicitation and
		// advertisement, redirect.
		return t < 133 || t > 137
	}
	return false
}

const (
	protoTCP      = 6
	protoUDP      = 17
	protoFragment = 44
	protoICMPv6   = 58
)
