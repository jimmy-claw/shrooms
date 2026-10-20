package mesh

import (
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/state"
)

// Only a node worth bootstrapping from publishes an address, and each condition
// is load-bearing (ADR-031).
//
// Tested through bootAddr's own preconditions rather than a live node: the peer
// id needs an FFI call, and what matters here is which nodes decline to
// publish, not what the string looks like when they do.
func TestOnlyACoreRelayWithAPinnedPortPublishes(t *testing.T) {
	base := state.Config{
		Mode:         "Core",
		Relay:        true,
		DeliveryPort: 39777,
		Advertise:    []string{"128.140.55.128:51820"},
	}

	for _, tc := range []struct {
		name string
		edit func(c *state.Config)
	}{
		{"an Edge node has no gossip to share", func(c *state.Config) { c.Mode = "Edge" }},
		{"a non-relay is probably not reachable", func(c *state.Config) { c.Relay = false }},
		{"an unpinned port changes on restart", func(c *state.Config) { c.DeliveryPort = 0 }},
		{"no public address to name", func(c *state.Config) { c.Advertise = nil }},
	} {
		cfg := base
		tc.edit(&cfg)
		m := &Mesh{cfg: cfg, prober: nil}
		if got := m.bootAddrFor(cfg, time.Time{}); got != "" {
			t.Errorf("%s: published %q", tc.name, got)
		}
	}
}

// A public address is used and a private one is refused: an address only this
// LAN can reach is useless to members, who are mostly not on it.
func TestOnlyRoutableAddressesAreOffered(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"128.140.55.128:51820", true},
		{"192.168.0.152:51820", false},
		{"10.0.0.1:51820", false},
		{"127.0.0.1:51820", false},
		{"0.0.0.0:51820", false},
	} {
		cfg := state.Config{Mode: "Core", Relay: true, DeliveryPort: 1, Advertise: []string{tc.addr}}
		m := &Mesh{cfg: cfg}
		got := m.publicIPFrom(cfg, time.Time{}).IsValid()
		if got != tc.want {
			t.Errorf("%s: routable=%v, want %v", tc.addr, got, tc.want)
		}
	}
}

// A bootstrap address must be one the delivery library can dial, and it listens
// on /ip4 only — the fleet's own entry nodes are /dns4/ multiaddrs. Publishing
// a v6 address would cost every peer that stored it a wasted startup dial.
//
// This is only true of bootstrap. The data plane reaches IPv6 peers directly.
func TestBootstrapAddressesAreIPv4(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"128.140.55.128:51820", true},
		{"[2a01:4f8:c015:d234::1]:51820", false},
		{"[fd3b:ffe9:f81::1]:51820", false},
	} {
		cfg := state.Config{Mode: "Core", Relay: true, DeliveryPort: 1, Advertise: []string{tc.addr}}
		m := &Mesh{cfg: cfg}
		if got := m.publicIPFrom(cfg, time.Time{}).IsValid(); got != tc.want {
			t.Errorf("%s: offered=%v, want %v", tc.addr, got, tc.want)
		}
	}
}

// What a peer publishes is kept only if it is a public, well-formed address
// (audit F-22): no loopback, private, reserved or odd forms for every restart
// to dial, and no names, which say nothing until resolved.
func TestOnlyWellFormedPublicBootAddressesAreKept(t *testing.T) {
	id := "16Uiu2HAmQ9X2xDfPG3uL77V9piYDhjq14JhKCtcmNYsTMKNqrKCj"
	for _, a := range []string{
		"/ip4/128.140.55.128/tcp/30304/p2p/16Uiu2HAmNQQC8L8T55KoLiCRD75bSDehn2evQ1vEEG8sqSkbk2uh",
		"/ip4/8.8.8.8/tcp/60000/p2p/" + id,
		"/ip4/8.8.8.8/tcp/60000/p2p/12D3KooWD3eckifWpRn9wQpMG9R9hX3sD158z7EqHWmweQAJU5SA",
	} {
		if !ValidBootAddr(a) {
			t.Errorf("refused %s", a)
		}
	}
	for _, a := range []string{
		"/ip4/127.0.0.1/tcp/60000/p2p/" + id,
		"/ip4/192.168.1.2/tcp/60000/p2p/" + id,
		"/ip4/255.255.255.255/tcp/60000/p2p/" + id,
		"/ip4/100.64.0.1/tcp/60000/p2p/" + id,
		"/ip4/198.18.0.1/tcp/60000/p2p/" + id,
		"/ip4/192.0.2.1/tcp/60000/p2p/" + id,
		"/ip4/203.0.113.7/tcp/60000/p2p/" + id,
		"/ip4/240.0.0.1/tcp/60000/p2p/" + id,
		"/ip4/8.8.8.8/tcp/0/p2p/" + id,
		"/ip4/8.8.8.8/tcp/+123/p2p/" + id,
		"/ip4/8.8.8.8/tcp/0123/p2p/" + id,
		"/ip4/8.8.8.8/tcp/65536/p2p/" + id,
		"/ip4/8.8.8.8/udp/60000/p2p/" + id,
		"/dns4/node-01.do-ams3.logos.test.status.im/tcp/30303/p2p/" + id,
		"/dns4/127.0.0.1.nip.io/tcp/60000/p2p/" + id,
		"/ip6/2001:db8::1/tcp/60000/p2p/" + id,
		"/ip4/8.8.8.8/tcp/60000/p2p/" + strings.Repeat("a", 40),
		"/ip4/8.8.8.8/tcp/60000/p2p/" + id[:len(id)-1],
		"/ip4/8.8.8.8/tcp/60000/p2p/" + id + "/p2p-circuit",
	} {
		if ValidBootAddr(a) {
			t.Errorf("kept %s", a)
		}
	}
}
