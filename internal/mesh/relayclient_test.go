package mesh

import (
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/tuntest"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/wg"
)

// A relay that also uses a relay must still reach its own clients directly.
//
// Relays used to select no relay at all, so this never came up. Now that they
// do, a device registered with this node — whose only way in may be the
// pinhole its registrations keep open — would otherwise be routed through the
// OTHER relay, which it is not registered with and which drops everything
// addressed to it. Driven through syncPeers and read back from a real device,
// because the endpoint WireGuard ends up holding is the whole point.
func TestARelayReachesItsOwnClientsWhereTheyRegistered(t *testing.T) {
	f := newRelayFixture(t)
	m := f.m
	m.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	m.relaySrv = relay.NewServer(m.relayKey, nil)

	priv, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	dev, err := wg.NewDevice(tuntest.NewChannelTUN().TUN(), priv.WGPriv, 0, device.NewLogger(device.LogLevelSilent, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close() })
	dev.Bind.SetRelayIdentity(m.relayKey, priv.WGPub)
	m.dev = dev
	m.st = &state.State{Identity: priv}

	// A phone, announced and online, with no probed path from here.
	phone, _ := identity.New()
	m.roster.Apply(newAnnounce(t, phone, "phone", []string{"10.9.9.9:51820"}, 1), f.now)
	// Registered with this node's relay, from behind a carrier NAT.
	registered := netip.MustParseAddrPort("198.51.100.20:40123")
	m.relaySrv.Handle(relay.EncodeRegister(m.relayKey, phone.WGPub, phone.DevicePriv, f.now), registered, f.now)

	if rl := m.selectRelay(f.now); !rl.ok {
		t.Fatal("the fixture's relay was not selected; the test would prove nothing")
	}
	if err := m.syncPeers(); err != nil {
		t.Fatal(err)
	}

	stats, err := dev.PeerStats()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := stats[phone.WGPub.String()]
	if !ok {
		t.Fatal("the phone was not configured at all")
	}
	// The control: the fixture's laptop, not registered here, still goes
	// through the relay. Without it this test would pass for a mesh that had
	// simply stopped relaying.
	for _, p := range m.roster.Peers() {
		if p.Name != "laptop" {
			continue
		}
		if lap := stats[p.WGPub.String()]; !strings.HasPrefix(lap.Endpoint, "relay:") {
			t.Errorf("laptop endpoint = %q, want it relayed", lap.Endpoint)
		}
	}
	if got.Endpoint != registered.String() {
		t.Errorf("phone endpoint = %q, want where it registered, %s", got.Endpoint, registered)
	}
}
