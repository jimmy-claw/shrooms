package state

import (
	"testing"

	"github.com/vpavlin/shrooms/internal/identity"
)

// A derived interface must not land on one a pinned mesh already holds.
//
// vps, 2026-09-16, and it took the node down. `config flatten` pinned home to
// logos01/51821. `mesh remove default` shifted office from index 2 to index 1.
// office had no pin — it was joined after the flatten — so it derived
// logos01/51821 from its new position, on top of home. The daemon crashed
// creating the second device and systemd restarted it 22 times, with nothing
// anywhere naming the collision.

func key(t *testing.T) string {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	return nk.String()
}

// The exact arrangement from vps.
func TestDerivedDoesNotCollideWithAPin(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Name = "vps"
	cfg.Interface = "logos0"
	cfg.ListenPort = 51820
	cfg.MeshSet = map[string]Mesh{
		// Pinned by flatten, when it sat at index 1 beside default.
		"home": {Label: "home", NetworkKey: key(t), Interface: "logos01", ListenPort: 51821},
		// Joined later, never pinned. At index 1 it derives logos01/51821.
		"office": {Label: "office", NetworkKey: key(t)},
	}

	seenIface := map[string]string{}
	seenPort := map[uint16]string{}
	for _, m := range cfg.Meshes() {
		if other, dup := seenIface[m.Interface]; dup {
			t.Errorf("meshes %q and %q both on interface %s", other, m.Label, m.Interface)
		}
		if other, dup := seenPort[m.ListenPort]; dup {
			t.Errorf("meshes %q and %q both on port %d", other, m.Label, m.ListenPort)
		}
		seenIface[m.Interface] = m.Label
		seenPort[m.ListenPort] = m.Label
	}

	// And the pin is still honoured, which is the property it exists for.
	for _, m := range cfg.Meshes() {
		if m.Label == "home" && (m.Interface != "logos01" || m.ListenPort != 51821) {
			t.Errorf("home moved to %s/%d despite its pin", m.Interface, m.ListenPort)
		}
	}
}

// With no pins anywhere, numbering is exactly what it always was. An upgrade
// must not rename a device's interfaces underneath it.
func TestUnpinnedNumberingIsUnchanged(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Name = "laptop"
	cfg.Interface = "logos0"
	cfg.ListenPort = 51820
	cfg.NetworkKey = key(t) // the original mesh, index 0 by label order
	cfg.MeshSet = map[string]Mesh{
		"home":   {Label: "home", NetworkKey: key(t)},
		"office": {Label: "office", NetworkKey: key(t)},
	}

	want := map[string]struct {
		iface string
		port  uint16
	}{
		"default": {"logos0", 51820},
		"home":    {"logos01", 51821},
		"office":  {"logos02", 51822},
	}
	for _, m := range cfg.Meshes() {
		w, ok := want[m.Label]
		if !ok {
			t.Fatalf("unexpected mesh %q", m.Label)
		}
		if m.Interface != w.iface || m.ListenPort != w.port {
			t.Errorf("%s = %s/%d, want %s/%d", m.Label, m.Interface, m.ListenPort, w.iface, w.port)
		}
	}
}

// Several pins, and the unpinned ones fill the gaps rather than stacking.
func TestDerivedFillsAroundSeveralPins(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Name = "node"
	cfg.Interface = "logos0"
	cfg.ListenPort = 51820
	cfg.MeshSet = map[string]Mesh{
		"a": {Label: "a", NetworkKey: key(t), Interface: "logos0", ListenPort: 51820},
		"b": {Label: "b", NetworkKey: key(t), Interface: "logos01", ListenPort: 51821},
		"c": {Label: "c", NetworkKey: key(t)},
		"d": {Label: "d", NetworkKey: key(t)},
	}

	seenIface := map[string]string{}
	seenPort := map[uint16]string{}
	for _, m := range cfg.Meshes() {
		if other, dup := seenIface[m.Interface]; dup {
			t.Errorf("meshes %q and %q both on %s", other, m.Label, m.Interface)
		}
		if other, dup := seenPort[m.ListenPort]; dup {
			t.Errorf("meshes %q and %q both on port %d", other, m.Label, m.ListenPort)
		}
		seenIface[m.Interface] = m.Label
		seenPort[m.ListenPort] = m.Label
	}
	if len(seenIface) != 4 || len(seenPort) != 4 {
		t.Errorf("got %d interfaces and %d ports for 4 meshes", len(seenIface), len(seenPort))
	}
}
