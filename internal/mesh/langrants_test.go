package mesh

import (
	"encoding/hex"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/lanhost"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/v4"
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

// A host answers to <host>.lan.<gateway>, and nothing else answers to that
// shape: a device that names itself "lan" does not get every LAN host's name.
func TestALanHostHasANameNobodyElseCanTake(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	gw, _ := identity.New()
	squat, _ := identity.New()
	f.m.roster.Apply(newAnnounce(t, gw, "pi5", nil, 1), time.Now())
	f.m.roster.Apply(newAnnounce(t, squat, "lan", nil, 1), time.Now())
	f.m.handleLanGrant(f.lanGrant(t, gw.DevicePub, "printer", "8883", 1, false), time.Now())

	want := lanhost.Overlay(f.nk.Prefix(), gw.DevicePub, "printer")
	if got, ok := f.m.Lookup("printer.lan.pi5"); !ok || got != want {
		t.Fatalf("printer.lan.pi5 = %v %v, want %v", got, ok, want)
	}
	if got, ok := f.m.Lookup("camera.lan.pi5"); ok {
		t.Errorf("an ungranted host resolved to %v", got)
	}
	if got, ok := f.m.Lookup("printer.lan.other"); ok {
		t.Errorf("a host resolved under the wrong gateway: %v", got)
	}
}

// The admin's statement that is not newer than the one held is refused, not
// reported as done: a withdrawal at the grant's own serial used to print
// "Withdrew" and leave the host granted (F-28). The very statement held may
// be sent again.
func TestAStaleOrEqualSerialIsRefused(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	gw, _ := identity.New()
	f.m.roster.Apply(newAnnounce(t, gw, "pi5", nil, 1), time.Now())
	noBus := func(err error) bool { return err == nil || err.Error() == "no rendezvous connection" }

	grant := f.lanGrant(t, gw.DevicePub, "printer", "8883", 10, false)
	if err := f.m.LanGrant(grant); !noBus(err) {
		t.Fatal(err)
	}
	if err := f.m.LanGrant(f.lanGrant(t, gw.DevicePub, "printer", "", 10, true)); !IsStaleLanGrant(err) {
		t.Errorf("a withdrawal at the grant's serial: %v", err)
	}
	if err := f.m.LanGrant(f.lanGrant(t, gw.DevicePub, "printer", "", 9, true)); !IsStaleLanGrant(err) {
		t.Errorf("a withdrawal at a lower serial: %v", err)
	}
	if len(f.m.LanHosts(time.Now())) != 1 {
		t.Error("a refused withdrawal changed what is held")
	}
	if err := f.m.LanGrant(grant); !noBus(err) {
		t.Errorf("the same statement again: %v", err)
	}

	// The repeat is only of what is still held (F-38).
	if !f.m.holdsLanGrant(grant) {
		t.Fatal("the grant is not held")
	}
	if err := f.m.LanGrant(f.lanGrant(t, gw.DevicePub, "printer", "", 11, true)); !noBus(err) {
		t.Fatal(err)
	}
	if f.m.holdsLanGrant(grant) {
		t.Error("a superseded grant would still be repeated")
	}
}

// A gateway whose credential has run out is still in the roster for a while,
// but its hosts are not listed: they would have no route (F-31).
func TestAnExpiredGatewaysHostsAreNotListed(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	gw, _ := identity.New()
	f.m.roster.Apply(newAnnounce(t, gw, "pi5", nil, 1), time.Now())
	f.m.handleLanGrant(f.lanGrant(t, gw.DevicePub, "printer", "8883", 1, false), time.Now())
	if len(f.m.LanHosts(time.Now())) != 1 {
		t.Fatal("not listed while the gateway is in force")
	}
	f.m.mu.Lock()
	if f.m.expiry == nil {
		f.m.expiry = map[string]int64{}
	}
	f.m.expiry[hex.EncodeToString(gw.DevicePub)] = time.Now().Add(-time.Minute).Unix()
	f.m.mu.Unlock()
	if got := f.m.LanHosts(time.Now()); len(got) != 0 {
		t.Errorf("an expired gateway's host is listed: %+v", got)
	}
}

// A LAN alias points at its host while the host is on the mesh, and is let go
// when the grant is withdrawn, so the address belongs to the LAN again; the
// daemon hears of each change (ADR-068).
func TestALanAliasFollowsItsHost(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	gw, _ := identity.New()
	f.m.roster.Apply(newAnnounce(t, gw, "proteus", nil, 1), time.Now())
	f.m.v4 = v4.NewTableIn(netip.MustParsePrefix("198.19.128.0/19"), v4.Entry{}, nil)
	var err error
	if f.m.lanAliases, err = lanhost.ParseAliases([]string{"192.168.10.143=printer.lan.proteus", "192.168.10.9=camera.lan.proteus"}); err != nil {
		t.Fatal(err)
	}
	var heard []map[netip.Addr]netip.Addr
	f.m.OnLanAliases(func(a map[netip.Addr]netip.Addr) { heard = append(heard, a) })
	lan := netip.MustParseAddr("192.168.10.143")

	f.m.handleLanGrant(f.lanGrant(t, gw.DevicePub, "printer", "8883", 1, false), time.Now())
	f.m.syncLanAliases(f.m.LanHosts(time.Now()))
	want := lanhost.Overlay(f.nk.Prefix(), gw.DevicePub, "printer")
	if got := f.m.LanAliases(); len(got) != 1 || got[lan] != want {
		t.Fatalf("active: %v", got)
	}
	if o, ok := f.m.v4.Overlay(lan); !ok || o != want {
		t.Errorf("the table does not send the LAN address to the host: %v %v", o, ok)
	}
	f.m.syncLanAliases(f.m.LanHosts(time.Now()))
	if len(heard) != 1 {
		t.Errorf("heard %d changes, want 1: the same set again is not a change", len(heard))
	}

	f.m.handleLanGrant(f.lanGrant(t, gw.DevicePub, "printer", "", 2, true), time.Now())
	f.m.syncLanAliases(f.m.LanHosts(time.Now()))
	if got := f.m.LanAliases(); len(got) != 0 {
		t.Errorf("still active after the withdrawal: %v", got)
	}
	if _, ok := f.m.v4.Overlay(lan); ok {
		t.Error("the table still sends the LAN address to the mesh")
	}
	if len(heard) != 2 || len(heard[1]) != 0 {
		t.Errorf("the daemon was not told: %v", heard)
	}
}
