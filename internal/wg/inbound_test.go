package wg

import (
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun/tuntest"
)

func packetTo(dst netip.Addr) []byte {
	p := make([]byte, 16+40+20)
	p[16] = 6 << 4
	p[16+6] = protoTCP
	d := dst.As16()
	copy(p[16+24:16+40], d[:])
	return p
}

// A peer's packet reaches the operating system only when it is addressed to
// this device; one for the machine's LAN or public address, or for anyone
// else, is dropped (F-26).
func TestSelfOnlyDeliversOnlyToSelf(t *testing.T) {
	self := netip.MustParseAddr("fdaa:bbbb:cccc::1")
	ch := tuntest.NewChannelTUN()
	f := SelfOnly(ch.TUN(), self)
	got := make(chan []byte, 8)
	go func() {
		for p := range ch.Inbound {
			got <- p
		}
	}()

	v4 := make([]byte, 16+20)
	v4[16] = 4 << 4
	for _, p := range [][]byte{packetTo(netip.MustParseAddr("2001:db8::7")),
		packetTo(netip.MustParseAddr("fdaa:bbbb:cccc::2")), v4, make([]byte, 16+3)} {
		if n, err := f.Write([][]byte{p}, 16); err != nil || n != 1 {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	f.Write([][]byte{packetTo(self)}, 16)
	select {
	case p := <-got:
		if !ForSelf(p, self) {
			t.Errorf("a packet for someone else came through")
		}
	case <-time.After(time.Second):
		t.Fatal("the packet for this device did not come through")
	}
	select {
	case <-got:
		t.Error("more than the one packet came through")
	case <-time.After(50 * time.Millisecond):
	}
	if f.Dropped() != 4 {
		t.Errorf("dropped %d, want 4", f.Dropped())
	}
}

// A packet for this device that could be sent on, or that steers its routing
// or neighbours, is dropped too: a routing header names a further hop inside
// the packet, and only a kernel default kept it from being followed (F-39).
func TestSelfOnlyRefusesWhatCouldGoFurther(t *testing.T) {
	self := netip.MustParseAddr("fdaa:bbbb:cccc::1")
	peer := netip.MustParseAddr("fdaa:bbbb:cccc::2")
	other := netip.MustParseAddr("fdaa:bbbb:cccc::3")
	// icmpErr is an error from `from` about a packet self sent to `to`.
	icmpErr := func(typ byte, src, to, from netip.Addr) []byte {
		p := make([]byte, 40+8+40)
		p[0], p[6] = 6<<4, protoICMPv6
		f, d := from.As16(), self.As16()
		copy(p[8:24], f[:])
		copy(p[24:40], d[:])
		p[40] = typ
		in := p[48:]
		in[0], in[6] = 6<<4, protoTCP
		s, t := src.As16(), to.As16()
		copy(in[8:24], s[:])
		copy(in[24:40], t[:])
		return p
	}
	with := func(next byte, rest ...byte) []byte {
		p := packetTo(self)[16:]
		p[6] = next
		copy(p[40:], rest)
		return p
	}
	for name, c := range map[string]struct {
		p    []byte
		want bool
	}{
		"TCP":                             {with(protoTCP), true},
		"UDP":                             {with(protoUDP), true},
		"ping":                            {with(protoICMPv6, 128), true},
		"too big, forged":                 {with(protoICMPv6, 2), false},
		"too big, genuine":                {icmpErr(2, self, peer, peer), true},
		"unreachable, genuine":            {icmpErr(1, self, peer, peer), true},
		"unreachable, about another peer": {icmpErr(1, self, other, peer), false},
		"router renumbering":              {with(protoICMPv6, 138), false},
		"routing header":                  {with(43, protoTCP), false},
		"hop-by-hop":                      {with(0, protoTCP), false},
		"redirect":                        {with(protoICMPv6, 137), false},
		"router advertisement":            {with(protoICMPv6, 134), false},
		"neighbour solicitation":          {with(protoICMPv6, 135), false},
		"first fragment of UDP":           {with(protoFragment, protoUDP, 0, 0, 1), true},
		"later fragment of UDP":           {with(protoFragment, protoUDP, 0, 0x05, 0x00), true},
		"fragment hiding routing":         {with(protoFragment, 43, 0, 0, 1), false},
		"fragmented redirect":             {with(protoFragment, protoICMPv6, 0, 0, 1, 0, 0, 0, 0, 137), false},
	} {
		if got := ForSelf(c.p, self); got != c.want {
			t.Errorf("%s: ForSelf = %v, want %v", name, got, c.want)
		}
	}
}
