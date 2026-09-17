package mesh

import (
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
	"github.com/vpavlin/shrooms/internal/state"
)

// The admin names a blind relay once and every member uses it
// (docs/distributing-a-blind-relay.md). These drive the receive path —
// handleRelayAdvice, which is what a message off the bus reaches — on a mesh
// with no rendezvous node, so the repeat it attempts fails quietly and what is
// left to check is what the node decided.

type adviceFixture struct {
	m     *Mesh
	admin *cred.Admin
	auth  *cred.Authority
	dir   string
	nk    identity.NetworkKey
}

func newAdviceFixture(t *testing.T, cfg state.Config) *adviceFixture {
	t.Helper()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	return &adviceFixture{m: adviceMesh(t, dir, nk, auth, cfg), admin: admin, auth: auth, dir: dir, nk: nk}
}

// adviceMesh is a mesh as New leaves it, as far as advice is concerned: state
// on disk, an authority, a config, and whatever was stored loaded back.
func adviceMesh(t *testing.T, dir string, nk identity.NetworkKey, auth *cred.Authority, cfg state.Config) *Mesh {
	t.Helper()
	st, err := state.LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := newRelayFixture(t)
	m := f.m
	m.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	// The same mesh on every call, so a second call is this node restarted.
	m.nk = nk
	m.relayKey = relay.DeriveKey(nk)
	m.st = st
	m.cfg = cfg
	m.authority = auth
	m.networkID = state.NetworkID(m.nk)
	m.resync = make(chan struct{}, 1)
	m.loadRelayAdvice()
	return m
}

func (f *adviceFixture) advise(t *testing.T, serial uint64, token string, relays ...string) []byte {
	t.Helper()
	var aps []netip.AddrPort
	for _, r := range relays {
		aps = append(aps, netip.MustParseAddrPort(r))
	}
	a, err := cred.AdviseRelaysWith(f.admin, f.auth, serial, aps, token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func adopted(m *Mesh) []string {
	var out []string
	for _, a := range m.AdoptedRelays() {
		out = append(out, a.String())
	}
	return out
}

func TestAdviceIsAdoptedByADeviceWithNoBlindRelays(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	f.m.handleRelayAdvice(f.advise(t, 10, "tok", "222.167.212.15:31760"), time.Now())

	if got := adopted(f.m); len(got) != 1 || got[0] != "222.167.212.15:31760" {
		t.Fatalf("adopted %v, want the advised relay", got)
	}
	// It is a blind relay, spoken to under the advised token.
	ts := f.m.allRelays()
	last := ts[len(ts)-1]
	if !last.blind || last.key != relay.TokenKey("tok") {
		t.Errorf("adopted relay is %+v, want blind under the advised token", last)
	}
	// And the rest of the relay machinery sees it: it is registered with, and
	// is the fallback when nothing else is answering.
	if !hasRelay(f.m.registerWith(time.Now()), last.addr) {
		t.Error("the adopted relay is not registered with")
	}
	bare := bareMesh(t)
	bare.adopted.Store(f.m.adopted.Load())
	if got := bare.selectRelay(time.Now()); !got.ok || got.addr != last.addr {
		t.Errorf("with nothing else available selectRelay chose %+v", got)
	}
}

// A relay the device chose itself wins. The advice is still held, so this node
// can pass it on to a member that wants it.
func TestLocallyConfiguredBlindRelaysWin(t *testing.T) {
	f := newAdviceFixture(t, state.Config{RelayBlind: []string{"198.51.100.7:32100"}})
	f.m.handleRelayAdvice(f.advise(t, 10, "", "222.167.212.15:31760"), time.Now())

	if got := adopted(f.m); len(got) != 0 {
		t.Errorf("a device with its own blind relays adopted %v", got)
	}
	if f.m.RelayAdviceSerial() != 10 {
		t.Error("the advice was not held for passing on")
	}
}

func TestRelayNoneRefusesAdvice(t *testing.T) {
	f := newAdviceFixture(t, state.Config{RelayNone: true})
	f.m.handleRelayAdvice(f.advise(t, 10, "", "222.167.212.15:31760"), time.Now())
	if got := adopted(f.m); len(got) != 0 {
		t.Errorf(`relay_blind = "none" adopted %v`, got)
	}
}

// Replaying an older statement must not move the mesh back to a relay the admin
// has since abandoned.
func TestOlderAdviceIsIgnored(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	older := f.advise(t, 10, "", "203.0.113.1:1000")
	newer := f.advise(t, 20, "", "203.0.113.2:2000")

	f.m.handleRelayAdvice(newer, time.Now())
	f.m.handleRelayAdvice(older, time.Now())
	if got := adopted(f.m); len(got) != 1 || got[0] != "203.0.113.2:2000" {
		t.Errorf("after a replay, adopted %v; want the newer relay", got)
	}
	if err := f.m.RelayAdvice(older); err == nil {
		t.Error("the admin path accepted a statement older than the one held")
	}
}

func TestAdviceFromAnyoneButTheAdminIsRefused(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	mallory := &adviceFixture{admin: func() *cred.Admin { a, _ := cred.NewAdmin(); return a }()}
	mallory.auth, _ = cred.NewAuthority(mallory.admin.Pub)
	forged := mallory.advise(t, 99, "", "203.0.113.66:6666")

	f.m.handleRelayAdvice(forged, time.Now())
	if got := adopted(f.m); len(got) != 0 {
		t.Errorf("adopted a relay from advice another mesh's admin signed: %v", got)
	}
	if err := f.m.RelayAdvice(forged); err == nil {
		t.Error("the admin path accepted forged advice")
	}
}

// An empty statement is how the admin withdraws the relay.
func TestEmptyAdviceWithdrawsTheRelay(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	f.m.handleRelayAdvice(f.advise(t, 10, "", "203.0.113.1:1000"), time.Now())
	f.m.handleRelayAdvice(f.advise(t, 11, ""), time.Now())
	if got := adopted(f.m); len(got) != 0 {
		t.Errorf("after withdrawal, still using %v", got)
	}
}

// A restart must come back on the relay, and must still refuse the replay the
// held serial protects against.
func TestAdviceSurvivesARestart(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	f.m.handleRelayAdvice(f.advise(t, 20, "tok", "203.0.113.2:2000"), time.Now())

	again := adviceMesh(t, f.dir, f.nk, f.auth, state.Config{})
	if got := adopted(again); len(got) != 1 || got[0] != "203.0.113.2:2000" {
		t.Fatalf("after a restart, adopted %v", got)
	}
	again.handleRelayAdvice(f.advise(t, 10, "", "203.0.113.1:1000"), time.Now())
	if got := adopted(again); got[0] != "203.0.113.2:2000" {
		t.Errorf("a replay after restart moved the relay to %v", got)
	}

	// A stored statement another authority signed is not believed for being
	// on our disk.
	stranger, _ := cred.NewAdmin()
	strangerAuth, _ := cred.NewAuthority(stranger.Pub)
	if got := adopted(adviceMesh(t, f.dir, f.nk, strangerAuth, state.Config{})); len(got) != 0 {
		t.Errorf("a mesh with a different authority adopted %v from disk", got)
	}
}

// The relay tells us where it saw us, and that is only taken from relays we
// chose. One the admin chose counts: it is the whole reason a mesh behind one
// router can learn its public address.
func TestAnAdoptedRelayMayReportWhereItSawUs(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	relayAt := netip.MustParseAddrPort("198.51.100.9:31760")
	f.m.handleRelayAdvice(f.advise(t, 10, "", relayAt.String()), time.Now())

	// A prober that has heard nothing, so a single observation is kept rather
	// than weighed against what the fixture's relay already reported.
	fresh := func() {
		id, _ := identity.New()
		f.m.prober = disco.NewProber(f.m.discoKey, id.DevicePriv,
			func([]byte, netip.AddrPort) error { return nil })
	}

	// A relay nobody named is ignored.
	fresh()
	other := netip.MustParseAddrPort("203.0.113.41:51820")
	frame, _ := relay.EncodeObserved(relay.OpenKey(), other)
	f.m.handleRelayFrame(frame, netip.MustParseAddrPort("198.51.100.10:31760"))
	if hasAddr(f.m.prober, other) {
		t.Error("an observation from an unnamed relay was recorded")
	}

	// The one the admin named is heard.
	fresh()
	seen := netip.MustParseAddrPort("203.0.113.40:51820")
	frame, err := relay.EncodeObserved(relay.OpenKey(), seen)
	if err != nil {
		t.Fatal(err)
	}
	f.m.handleRelayFrame(frame, relayAt)
	if !hasAddr(f.m.prober, seen) {
		t.Errorf("the adopted relay's observation was not recorded")
	}
}

func hasAddr(p *disco.Prober, ap netip.AddrPort) bool {
	for _, r := range p.Reflexive(time.Now()) {
		if r == ap {
			return true
		}
	}
	return false
}

func TestAdviceRepeatsOnDiscoveryWithACooldown(t *testing.T) {
	f := newAdviceFixture(t, state.Config{})
	now := time.Now()
	if f.m.shouldRepeatAdvice(now) {
		t.Error("repeating with nothing held")
	}
	f.m.handleRelayAdvice(f.advise(t, 10, "", "203.0.113.1:1000"), now)
	if !f.m.shouldRepeatAdvice(now) {
		t.Error("not repeating advice to a peer that just appeared")
	}
	if f.m.shouldRepeatAdvice(now.Add(time.Second)) {
		t.Error("repeated inside the cooldown")
	}
	if !f.m.shouldRepeatAdvice(now.Add(RevocationRepublishCooldown + time.Second)) {
		t.Error("did not repeat after the cooldown")
	}

	quiet := newAdviceFixture(t, state.Config{QuietRevocations: true})
	quiet.m.handleRelayAdvice(quiet.advise(t, 10, "", "203.0.113.1:1000"), now)
	if quiet.m.shouldRepeatAdvice(now) {
		t.Error(`announce_revocations = "false" did not silence the repeat`)
	}
}
