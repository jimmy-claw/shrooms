package wg

import (
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun/tuntest"
)

func packetTo(dst netip.Addr) []byte {
	p := make([]byte, 16+40)
	p[16] = 6 << 4
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
