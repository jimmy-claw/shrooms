package mesh

import (
	"net/netip"
	"testing"
)

// Which announced address is worth dialling before anything has been probed.
//
// The guess used to take the first private candidate in the list, whatever it
// was. On 2026-09-11 and 12 that had a laptop dialling a pi5 at 10.77.57.173
// and then 10.222.140.253 — carrier-NAT addresses the pi5's router had handed
// it, first in its announce, unreachable from anywhere — while the pi5 sat on
// the same mesh with a perfectly good public path back. Each time the tunnel
// went quiet the guess overwrote the endpoint WireGuard had LEARNED, so it
// could not recover.

func addrs(t *testing.T, ss ...string) []netip.Addr {
	t.Helper()
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		out = append(out, a)
	}
	return out
}

func TestBootstrapGuess(t *testing.T) {
	// This laptop, as it actually was: one LAN address and two docker bridges.
	mine := addrs(t, "192.168.0.151", "172.17.0.1", "172.18.0.1")

	for _, tc := range []struct {
		name  string
		cands []string
		want  string
	}{{
		name:  "the case from the field: a carrier-NAT address first, LAN second",
		cands: []string{"10.222.140.253:51821", "192.168.10.219:51820", "172.17.0.1:51820"},
		want:  "", // nothing here is reachable, and saying so beats guessing
	}, {
		name:  "a public address wins outright, wherever it sits in the list",
		cands: []string{"10.222.140.253:51821", "178.213.45.235:51822", "192.168.0.9:51820"},
		want:  "178.213.45.235:51822",
	}, {
		name:  "a peer on our own LAN is exactly what the guess is for",
		cands: []string{"10.222.140.253:51821", "192.168.0.9:51820"},
		want:  "192.168.0.9:51820",
	}, {
		name:  "our own docker bridge is not a way to reach anybody else",
		cands: []string{"172.17.0.1:51820"},
		want:  "",
	}, {
		name:  "loopback and link-local are never candidates",
		cands: []string{"127.0.0.1:51820", "169.254.1.2:51820"},
		want:  "",
	}, {
		name:  "nothing announced at all — a phone that cannot list its addresses",
		cands: nil,
		want:  "",
	}, {
		name:  "garbage is skipped, not fatal",
		cands: []string{"not-an-address", "192.168.0.9:51820"},
		want:  "192.168.0.9:51820",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bootstrapFrom(tc.cands, mine); got != tc.want {
				t.Errorf("bootstrapFrom(%v) = %q, want %q", tc.cands, got, tc.want)
			}
		})
	}
}

// A node that cannot enumerate its own addresses — Android denies it — must
// still take a public candidate. Without this a phone would refuse every guess.
func TestBootstrapGuessWithNoLocalAddresses(t *testing.T) {
	if got := bootstrapFrom([]string{"178.213.45.235:51822"}, nil); got != "178.213.45.235:51822" {
		t.Errorf("got %q, want the public address", got)
	}
	// And a private one is not plausible when we know nothing about ourselves,
	// which is correct: there is no evidence it is reachable.
	if got := bootstrapFrom([]string{"192.168.0.9:51820"}, nil); got != "" {
		t.Errorf("got %q, want none", got)
	}
}

// A relay's report of where it saw us is only worth having when the relay is
// genuinely outside. Measured against the blind relay on Akash, 2026-09-16:
//
//	first   device seen by the relay at 10.42.9.1:39733
//	second  device seen by the relay at 10.42.9.1:10096
//
// The provider NATs inbound UDP, so the relay sees its own ingress rather than
// the client — two devices, one address. Believing it would have every node
// announce a private address shared with strangers, which is the failure that
// cost two days in September.
func TestARelayBehindItsOwnNATIsIgnored(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"10.42.9.1:39733", false},    // the Akash provider's pod network
		{"192.168.10.1:51820", false}, // a relay on somebody's LAN
		{"100.64.3.7:51820", false},   // carrier shared space
		{"127.0.0.1:51820", false},
		{"169.254.1.1:51820", false},
		{"85.160.39.54:11053", true}, // a relay that can actually see us
		{"[2001:db8::1]:51820", true},
	} {
		ap := netip.MustParseAddrPort(tc.addr)
		if got := relayObservationIsUseful(ap); got != tc.want {
			t.Errorf("relayObservationIsUseful(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
	if relayObservationIsUseful(netip.AddrPort{}) {
		t.Error("the zero address was accepted")
	}
}
