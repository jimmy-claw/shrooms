package disco

import (
	"net/netip"
	"testing"
	"time"
)

// A relay's report of where it saw us has to become an address we announce,
// because on a mesh with no public member it is the only one we will ever get.
//
// The office mesh, 2026-09-16: four machines and a phone behind one router,
// every announce containing nothing but 192.168.10.x. Reflexive discovery rides
// on disco pongs, and a pong only tells you something if the peer sending it is
// outside your NAT. None was.

func TestRelayObservationIsAnnounced(t *testing.T) {
	p := NewProber(Key{}, nil, func([]byte, netip.AddrPort) error { return nil })
	now := time.Now()
	seen := netip.MustParseAddrPort("85.160.39.54:11053")

	p.NoteReflexive(seen, "relay:203.0.113.9:31760", now)

	got := p.Reflexive(now)
	if len(got) != 1 || got[0] != seen {
		t.Fatalf("Reflexive = %v, want [%v]", got, seen)
	}
}

// One relay is one vantage point, and a single uncorroborated observation is
// kept — Reflexive says so itself: "no worse than having no candidate at all".
// That is what makes a single blind relay enough to unblock a mesh.
func TestOneRelayIsEnough(t *testing.T) {
	p := NewProber(Key{}, nil, func([]byte, netip.AddrPort) error { return nil })
	now := time.Now()
	p.NoteReflexive(netip.MustParseAddrPort("85.160.39.54:11053"), "relay:a", now)
	if len(p.Reflexive(now)) != 1 {
		t.Error("a lone relay observation was discarded")
	}
}

// Two observers disagreeing is a NAT that maps per destination, and neither
// address generalises. The existing rule must apply to a relay exactly as it
// does to a peer — a relay is not more trustworthy about this, it is just
// better placed.
func TestRelayAndPeerDisagreeingIsNotAnnounced(t *testing.T) {
	p := NewProber(Key{}, nil, func([]byte, netip.AddrPort) error { return nil })
	now := time.Now()
	p.NoteReflexive(netip.MustParseAddrPort("85.160.39.54:11053"), "relay:a", now)
	p.NoteReflexive(netip.MustParseAddrPort("85.160.39.54:22222"), "peer:b", now)

	for _, ap := range p.Reflexive(now) {
		t.Errorf("announced %v though two vantage points disagree", ap)
	}
}

// The same relay saying the same thing twice is one vantage point, not two —
// otherwise a single relay could corroborate itself past the agreement rule.
func TestARelayCannotCorroborateItself(t *testing.T) {
	p := NewProber(Key{}, nil, func([]byte, netip.AddrPort) error { return nil })
	now := time.Now()
	one := netip.MustParseAddrPort("85.160.39.54:11053")
	two := netip.MustParseAddrPort("85.160.39.54:22222")

	p.NoteReflexive(one, "relay:a", now)
	p.NoteReflexive(one, "relay:a", now.Add(time.Second))
	p.NoteReflexive(two, "peer:b", now)

	for _, ap := range p.Reflexive(now.Add(time.Second)) {
		t.Errorf("announced %v: one relay repeating itself is not corroboration", ap)
	}
}

// Addresses that cannot be how anybody reaches us are refused, whoever says so.
func TestRelayObservationIsFiltered(t *testing.T) {
	p := NewProber(Key{}, nil, func([]byte, netip.AddrPort) error { return nil })
	now := time.Now()
	for _, s := range []string{"127.0.0.1:51820", "0.0.0.0:51820", "169.254.1.2:51820"} {
		p.NoteReflexive(netip.MustParseAddrPort(s), "relay:a", now)
	}
	p.NoteReflexive(netip.MustParseAddrPort("198.51.100.7:51820"), "", now) // no observer
	if got := p.Reflexive(now); len(got) != 0 {
		t.Errorf("Reflexive = %v, want none", got)
	}
}
