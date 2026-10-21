package mesh

import (
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/lanhost"
	"github.com/vpavlin/shrooms/internal/state"
)

func (f *adviceFixture) lanGrant(t *testing.T, gw []byte, host, ports string, serial uint64, withdrawn bool) []byte {
	t.Helper()
	var rs []cred.PortRange
	if ports != "" {
		var err error
		if rs, err = cred.ParseLanPorts(ports); err != nil {
			t.Fatal(err)
		}
	}
	g, err := cred.GrantLanHostWith(f.admin, f.auth, gw, host, rs, serial, time.Now(), time.Hour, withdrawn)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := g.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A grant naming a member is a host on the mesh, at its derived address; it
// survives a restart; a newer serial narrows it and a withdrawal removes it,
// and an older serial changes nothing (ADR-068).
func TestALanGrantMakesAHostOfAMembersDevice(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	gw, _ := identity.New()
	f.m.roster.Apply(newAnnounce(t, gw, "pi5", nil, 1), time.Now())

	if err := f.m.LanGrant(f.lanGrant(t, gw.DevicePub, "printer", "8883,990,50000-50100", 5, false)); err != nil &&
		err.Error() != "no rendezvous connection" {
		t.Fatal(err)
	}
	hosts := f.m.LanHosts(time.Now())
	if len(hosts) != 1 || hosts[0].Name != "printer" || hosts[0].GatewayName != "pi5" ||
		hosts[0].Overlay != lanhost.Overlay(f.nk.Prefix(), gw.DevicePub, "printer") || hosts[0].Self {
		t.Fatalf("hosts: %+v", hosts)
	}

	// Restarted: the grant comes back from disk.
	again := adviceMesh(t, f.dir, f.nk, f.auth, state.Config{})
	again.loadLanGrants()
	again.roster.Apply(newAnnounce(t, gw, "pi5", nil, 1), time.Now())
	if got := again.LanHosts(time.Now()); len(got) != 1 {
		t.Fatalf("not kept across a restart: %+v", got)
	}

	// An older serial is ignored; a newer one narrows; a withdrawal removes.
	f.m.handleLanGrant(f.lanGrant(t, gw.DevicePub, "printer", "1-200", 4, false), time.Now())
	if got := f.m.LanHosts(time.Now()); cred.FormatLanPorts(got[0].Ports) != "990,8883,50000-50100" {
		t.Errorf("an older grant changed the ports: %s", cred.FormatLanPorts(got[0].Ports))
	}
	f.m.handleLanGrant(f.lanGrant(t, gw.DevicePub, "printer", "8883", 6, false), time.Now())
	if got := f.m.LanHosts(time.Now()); cred.FormatLanPorts(got[0].Ports) != "8883" {
		t.Errorf("a newer grant did not narrow: %s", cred.FormatLanPorts(got[0].Ports))
	}
	f.m.handleLanGrant(f.lanGrant(t, gw.DevicePub, "printer", "", 7, true), time.Now())
	if got := f.m.LanHosts(time.Now()); len(got) != 0 {
		t.Errorf("a withdrawn host is still listed: %+v", got)
	}
}

// A host whose gateway is not a member is not listed; a grant from another
// mesh's admin is refused; a mesh with no admin has no LAN hosts.
func TestALanHostNeedsAMemberGatewayAndThisMeshsAdmin(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	stranger, _ := identity.New()
	f.m.handleLanGrant(f.lanGrant(t, stranger.DevicePub, "printer", "80", 1, false), time.Now())
	if got := f.m.LanHosts(time.Now()); len(got) != 0 {
		t.Errorf("a host behind a non-member is listed: %+v", got)
	}

	other := newAdviceFixture(t, state.Config{})
	if _, err := f.m.applyLanGrant(other.lanGrant(t, stranger.DevicePub, "camera", "80", 1, false), false); err == nil {
		t.Error("another mesh's grant was accepted")
	}

	f.m.authority = nil
	if _, err := f.m.applyLanGrant(f.lanGrant(t, stranger.DevicePub, "x", "80", 9, false), false); err == nil {
		t.Error("a mesh with no admin accepted a grant")
	}
}

// One gateway offers at most lanhost.MaxPerGateway hosts.
func TestAGatewayOffersAHandfulOfHosts(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	gw, _ := identity.New()
	for i := 0; i < lanhost.MaxPerGateway; i++ {
		if _, err := f.m.applyLanGrant(f.lanGrant(t, gw.DevicePub, "h"+string(rune('a'+i)), "80", 1, false), false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.m.applyLanGrant(f.lanGrant(t, gw.DevicePub, "one-more", "80", 1, false), false); err == nil {
		t.Error("a gateway was granted more than MaxPerGateway hosts")
	}
}
