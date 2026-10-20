package mesh

import (
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

func TestResolveFindsPeerByName(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.roster.Apply(newAnnounce(t, peer, "laptop", nil, 1), time.Now())

	addr, ok := m.Resolve("laptop")
	if !ok {
		t.Fatal("did not resolve a peer that exists")
	}
	if addr != identity.OverlayAddr(nk, peer.DevicePub) {
		t.Errorf("resolved to %v", addr)
	}
}

// Names are self-asserted, so two entries can claim one. The name stays with
// the key that has claimed it longest, online or not: one gone quiet is a
// laptop asleep as often as a device that joined again, and "the one still
// announcing" let a member wait for a laptop to sleep and take its ssh (audit
// F-18, 2026-10-20). The later key answers to its fingerprint meanwhile.
func TestANameStaysWithItsHolder(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	holder, _ := identity.New()
	later, _ := identity.New()
	now := time.Now()
	h, l := identity.OverlayAddr(nk, holder.DevicePub), identity.OverlayAddr(nk, later.DevicePub)

	// Order of arrival must not decide it.
	for _, holderFirst := range []bool{true, false} {
		m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
		apply := []func(){
			func() { m.roster.Apply(newAnnounce(t, holder, "box", nil, 1), now.Add(-2*time.Hour)) },
			func() { m.roster.Apply(newAnnounce(t, later, "box", nil, 1), now) },
		}
		if !holderFirst {
			apply[0], apply[1] = apply[1], apply[0]
		}
		apply[0]()
		apply[1]()
		if addr, ok := m.Resolve("box"); !ok || addr != h {
			t.Errorf("box resolved to %v, want the holder %v (holder first: %v)", addr, h, holderFirst)
		}
		if addr, ok := m.Resolve("box-" + lastGroup(l)); !ok || addr != l {
			t.Errorf("the later claimant does not answer to its fingerprint: %v", addr)
		}
		// Revoked or forgotten, the name passes on.
		m.roster.Forget(holder.DevicePub)
		if addr, _ := m.Resolve("box"); addr != l {
			t.Errorf("with the holder gone, box resolved to %v", addr)
		}
	}
}

// A heartbeat keeps a claim; a rename starts a new one.
func TestAClaimIsKeptAcrossAnnouncesAndRestarts(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	holder, _ := identity.New()
	later, _ := identity.New()
	now := time.Now()
	r := NewRoster(nk, self.DevicePub)
	r.Apply(newAnnounce(t, holder, "box", nil, 1), now.Add(-time.Hour))
	r.Apply(newAnnounce(t, later, "box", nil, 1), now.Add(-time.Minute))
	r.Apply(newAnnounce(t, holder, "box", nil, 2), now)
	if !Holders(r.Peers())[PeerInfo{DevicePub: holder.DevicePub}.ID()] {
		t.Fatal("a heartbeat gave up the holder's claim")
	}
	// Remembered and restored, as on a restart: the claim's age comes back.
	r2 := NewRoster(nk, self.DevicePub)
	for _, p := range r.Peers() {
		r2.Restore(p)
	}
	if !Holders(r2.Peers())[PeerInfo{DevicePub: holder.DevicePub}.ID()] {
		t.Fatal("a restart gave the name to the later claimant")
	}
	r.Apply(newAnnounce(t, holder, "crate", nil, 3), now.Add(time.Second))
	r.Apply(newAnnounce(t, holder, "box", nil, 4), now.Add(2*time.Second))
	if Holders(r.Peers())[PeerInfo{DevicePub: holder.DevicePub}.ID()] {
		t.Error("renaming away and back kept the old claim")
	}
}

// What resolves must match what the hosts file writes, or a name works in one
// and not the other.
func TestResolveMatchesHostsSanitising(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.roster.Apply(newAnnounce(t, peer, "My Laptop", nil, 1), time.Now())

	for _, q := range []string{"my-laptop", "MY-LAPTOP", "My_Laptop"} {
		if _, ok := m.Resolve(q); !ok {
			t.Errorf("did not resolve %q", q)
		}
	}
}

func TestResolveUnknownName(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	if _, ok := m.Resolve("nosuch"); ok {
		t.Error("resolved a name nobody claims")
	}
}

// A service name is <service>.<device>, which is the same shape as the
// mesh-qualified <device>.<mesh> form. Lookup must handle both, and must not
// resolve a service by treating its leftmost label as a device.
func TestLookupResolvesServiceOnPeer(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub), cfg: state.Config{Name: "laptop"}}
	m.self = identity.OverlayAddr(nk, self.DevicePub)
	m.roster.Apply(newAnnounce(t, peer, "home-server", nil, 1), time.Now())
	server := identity.OverlayAddr(nk, peer.DevicePub)

	cases := []struct {
		name string
		want any // netip.Addr, or nil for "must not resolve"
	}{
		{"home-server", server},          // a device
		{"immich.home-server", server},   // a service on a device
		{"jellyfin.home-server", server}, // any service name; none are announced
		{"laptop", m.self},               // ourselves
		{"immich.laptop", m.self},        // a service on ourselves
		// The mesh-qualified form is NOT this function's job. A Mesh does not
		// know its own label, so it could not tell "home" from any other word
		// and resolved dev.<anything> — which is how a name qualified with one
		// mesh's label fell through to a device on another. Both production
		// callers wrap this in a resolver that does know the labels.
		{"home-server.home", nil},
		{"immich.nonesuch", nil}, // the device does not exist
		{"nonesuch", nil},        // nor does this
	}
	for _, c := range cases {
		addr, ok := m.Lookup(c.name)
		if c.want == nil {
			if ok {
				t.Errorf("Lookup(%q) resolved to %v, want no answer", c.name, addr)
			}
			continue
		}
		if !ok {
			t.Errorf("Lookup(%q) did not resolve", c.name)
			continue
		}
		if addr != c.want {
			t.Errorf("Lookup(%q) = %v, want %v", c.name, addr, c.want)
		}
	}
}

// The name a peer is told to use has to reach the mesh the thing is on. The
// short form is answered by the primary mesh alone, so on a multi-mesh node it
// names an address on another network — which is what `shrooms bound`, the
// desktop panel and the phone all showed before this existed.
func TestQualifiedDNSName(t *testing.T) {
	for _, tc := range []struct{ name, label, suffix, want string }{
		// No mesh, no name: the resolver answers only qualified names, so a
		// short one printed here would be one that does not resolve.
		{"laptop", "", "mesh", ""},
		{"laptop", "test", "mesh", "laptop.test.mesh"},
		{"laptop", "home", "", "laptop.home.mesh"},
		{"Living Room NAS", "shared", "mesh", "living-room-nas.shared.mesh"},
		{"laptop", "test", ".lan.", "laptop.test.lan"},
		{"", "test", "mesh", ""},
	} {
		if got := QualifiedDNSName(tc.name, tc.label, tc.suffix); got != tc.want {
			t.Errorf("QualifiedDNSName(%q,%q,%q) = %q, want %q",
				tc.name, tc.label, tc.suffix, got, tc.want)
		}
	}
}

// The rule of 2026-09-29, against the real Mesh.Lookup: a name resolves on the
// mesh its last label names, and a name with no mesh resolves nowhere — on a
// device with one mesh exactly as on a device with several.
func TestResolveQualifiedOnly(t *testing.T) {
	now := time.Now()
	mk := func(names ...string) *Mesh {
		nk, _ := identity.NewNetworkKey()
		self, _ := identity.New()
		m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
		for _, n := range names {
			id, _ := identity.New()
			m.roster.Apply(newAnnounce(t, id, n, nil, 1), now)
		}
		return m
	}
	office := mk("vps", "atlas")
	home := mk("vps")
	meshes := map[string]*Mesh{"office": office, "home": home}
	find := func(label string) (func(string) (netip.Addr, bool), bool) {
		m, ok := meshes[label]
		if !ok {
			return nil, false
		}
		return m.Lookup, true
	}
	want := func(m *Mesh, n string) netip.Addr {
		a, _ := m.Resolve(n)
		return a
	}

	for _, tc := range []struct {
		host string
		ok   bool
		addr netip.Addr
	}{
		{"atlas.office", true, want(office, "atlas")},
		{"vps.office", true, want(office, "vps")},
		{"vps.home", true, want(home, "vps")},        // same name, the other mesh
		{"immich.vps.home", true, want(home, "vps")}, // a service on a device
		{"atlas", false, netip.Addr{}},               // the short form
		{"atlas.home", false, netip.Addr{}},          // not on that mesh
		{"atlas.work", false, netip.Addr{}},          // a mesh this node is not on
		{"immich.vps", false, netip.Addr{}},          // a service with no mesh
		{"", false, netip.Addr{}},
		{"atlas.", false, netip.Addr{}},
	} {
		got, ok := ResolveQualified(tc.host, find)
		if ok != tc.ok || got != tc.addr {
			t.Errorf("%q resolved to %v (%v), want %v (%v)", tc.host, got, ok, tc.addr, tc.ok)
		}
	}
	if want(office, "vps") == want(home, "vps") {
		t.Fatal("the two vps devices share an address, so the test proves nothing")
	}

	// And with one mesh: still no short form.
	only := map[string]*Mesh{"office": office}
	one := func(label string) (func(string) (netip.Addr, bool), bool) {
		m, ok := only[label]
		if !ok {
			return nil, false
		}
		return m.Lookup, true
	}
	if _, ok := ResolveQualified("atlas", one); ok {
		t.Error("a device with one mesh answered the short form")
	}
	if a, ok := ResolveQualified("atlas.office", one); !ok || a != want(office, "atlas") {
		t.Errorf("atlas.office on a one-mesh device resolved to %v (%v)", a, ok)
	}
}

// Two online devices claiming one name: the one that claimed it first has it,
// and the other its fingerprint, so a member can neither take a name nor
// make it answer nothing (audit F-18).
func TestANameTwoOnlineDevicesClaimStaysWithTheFirst(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	a, _ := identity.New()
	b, _ := identity.New()
	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	now := time.Now()
	m.roster.Apply(newAnnounce(t, a, "laptop", nil, 1), now)
	m.roster.Apply(newAnnounce(t, b, "laptop", nil, 1), now.Add(time.Second))
	m.roster.Apply(newAnnounce(t, b, "laptop", nil, 2), now.Add(2*time.Second))
	aa, bb := identity.OverlayAddr(nk, a.DevicePub), identity.OverlayAddr(nk, b.DevicePub)
	if got, ok := m.Resolve("laptop"); !ok || got != aa {
		t.Fatalf("laptop: %v %v, want the first claimant", got, ok)
	}
	if got, ok := m.Resolve("laptop-" + lastGroup(bb)); !ok || got != bb {
		t.Errorf("laptop-%s: %v %v", lastGroup(bb), got, ok)
	}
}
