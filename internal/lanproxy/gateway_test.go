package lanproxy

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun/netstack"
	"golang.zx2c4.com/wireguard/tun/tuntest"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/lanhost"
)

// echo is the LAN appliance: it answers whatever it is sent.
func echo(t *testing.T) (*net.TCPListener, uint16) {
	t.Helper()
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln, uint16(ln.Addr().(*net.TCPAddr).Port)
}

// rig is a member and a gateway wired together as WireGuard would wire them:
// what the member's stack sends is what the gateway's Write receives, and what
// the gateway's Read yields is delivered to the member.
type rig struct {
	member *netstack.Net
	gw     *Gateway
	host   netip.Addr
}

func newRig(t *testing.T, sp lanhost.Spec, grant GrantFunc) *rig {
	t.Helper()
	prefix := netip.MustParsePrefix("fdaa:bbbb:cccc::/48")
	gwKey, _, _ := ed25519.GenerateKey(nil)
	self := netip.MustParseAddr("fdaa:bbbb:cccc::1")
	memberAddr := netip.MustParseAddr("fdaa:bbbb:cccc::2")

	inner := tuntest.NewChannelTUN() // the gateway's kernel side, unused here
	g, err := NewGateway(inner.TUN(), []lanhost.Spec{sp}, prefix, gwKey, self, 1280, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	g.onLink = func(netip.Addr) error { return nil } // the LAN is loopback in a test
	if grant != nil {
		g.SetGrants(grant)
	}
	t.Cleanup(func() { g.Close() })

	mdev, mnet, err := netstack.CreateNetTUN([]netip.Addr{memberAddr}, nil, 1280)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mdev.Close() })
	// member -> gateway
	go func() {
		bufs := [][]byte{make([]byte, packetOffset+65535)}
		sizes := []int{0}
		for {
			n, err := mdev.Read(bufs, sizes, packetOffset)
			if err != nil {
				return
			}
			if n == 1 {
				p := append(make([]byte, packetOffset), bufs[0][packetOffset:packetOffset+sizes[0]]...)
				g.Write([][]byte{p}, packetOffset)
			}
		}
	}()
	// gateway -> member
	go func() {
		bufs := [][]byte{make([]byte, packetOffset+65535)}
		sizes := []int{0}
		for {
			n, err := g.Read(bufs, sizes, packetOffset)
			if err != nil {
				return
			}
			for i := 0; i < n; i++ {
				p := append(make([]byte, packetOffset), bufs[i][packetOffset:packetOffset+sizes[i]]...)
				mdev.Write([][]byte{p}, packetOffset)
			}
		}
	}()
	return &rig{member: mnet, gw: g, host: lanhost.Overlay(prefix, gwKey, sp.Name)}
}

func grantFor(ports string) GrantFunc {
	rs, _ := cred.ParseLanPorts(ports)
	g := &cred.LanGrant{Host: "printer", Ports: rs, NotBefore: 0, NotAfter: time.Now().Add(time.Hour).Unix()}
	return func(host string, now time.Time) *cred.LanGrant {
		if host == "printer" {
			return g
		}
		return nil
	}
}

// talk dials the host from the member and reports what came back.
func (r *rig) talk(t *testing.T, port uint16) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := r.member.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(r.host, port))
	if err != nil {
		return "dial: " + err.Error()
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("hello")); err != nil {
		return "write: " + err.Error()
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil {
		return "read: " + err.Error()
	}
	return string(buf)
}

// A member reaches the LAN host on a granted port, through the gateway's own
// stack, by the host's overlay address (ADR-068).
func TestAMemberReachesAGrantedLanHost(t *testing.T) {
	_, port := echo(t)
	rs := []cred.PortRange{{From: port, To: port}}
	r := newRig(t, lanhost.Spec{Name: "printer", Addr: netip.MustParseAddr("127.0.0.1"), Ports: rs},
		grantFor(cred.FormatLanPorts(rs)))
	if got := r.talk(t, port); got != "hello" {
		t.Fatalf("through the gateway: %q", got)
	}
}

// Without a grant in force, or on a port the grant does not open, the
// connection is refused even though the gateway's config lists it.
func TestALanHostWithoutAGrantIsRefused(t *testing.T) {
	_, port := echo(t)
	rs := []cred.PortRange{{From: port, To: port}}
	sp := lanhost.Spec{Name: "printer", Addr: netip.MustParseAddr("127.0.0.1"), Ports: rs}

	if got := newRig(t, sp, nil).talk(t, port); got == "hello" {
		t.Error("served with no grants known")
	}
	other := port + 1
	if other == 0 {
		other = port - 1
	}
	if got := newRig(t, sp, grantFor(cred.FormatLanPorts([]cred.PortRange{{From: other, To: other}}))).talk(t, port); got == "hello" {
		t.Error("served on a port the grant does not open")
	}
}

// The LAN check refuses this gateway's own addresses and anything off its
// links.
func TestOnLinkRefusesTheGatewayAndElsewhere(t *testing.T) {
	if OnLink(netip.MustParseAddr("203.0.113.9")) == nil {
		t.Error("a documentation address counted as on-link")
	}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 || meshInterface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				own, _ := netip.AddrFromSlice(ipn.IP.To4())
				if OnLink(own) == nil {
					t.Errorf("this machine's own %s counted as a LAN host", own)
				}
				return
			}
		}
	}
}
