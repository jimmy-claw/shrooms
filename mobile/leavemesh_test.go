package mobile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

// Leaving the mesh a device was built around.
//
// A phone's first mesh is written in the single-mesh config form — top-level
// network_key, no [mesh.<label>] block — and LeaveMesh only knew how to delete
// from MeshSet. So the app offered no way to leave it at all, and the error
// said "or it is the original one", which from the outside reads as the option
// not existing.
//
// Vaclav, 2026-09-16, retiring the default mesh across every device: "I cannot
// leave default on phone - that option does not exist:-)". The desktop could,
// because `shrooms config flatten` rewrites the config first. The phone has no
// such command, so the same operation was possible on one and not the other
// for a reason that is a config FORMAT rather than anything about the mesh.

// twoMeshPhone writes the arrangement a phone is actually in: an original mesh
// in the top-level fields, and a second one joined by invite.
func twoMeshPhone(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	first, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}

	cfg := state.DefaultConfig()
	cfg.Name = "nothing"
	cfg.NetworkKey = first.String() // the original: no [mesh.x] block
	cfg.MeshSet = map[string]state.Mesh{
		"office": {Label: "office", NetworkKey: second.String()},
	}
	cfgPath, _ := paths(dir)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	return dir
}

func meshLabels(t *testing.T, dir string) map[string]bool {
	t.Helper()
	cfgPath, _ := paths(dir)
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range cfg.Meshes() {
		out[m.Label] = true
	}
	return out
}

func TestLeaveTheOriginalMesh(t *testing.T) {
	dir := twoMeshPhone(t)

	if err := LeaveMesh(dir, state.DefaultLabel); err != nil {
		t.Fatalf("leaving the original mesh: %v", err)
	}

	got := meshLabels(t, dir)
	if got[state.DefaultLabel] {
		t.Error("the original mesh is still there")
	}
	if !got["office"] {
		t.Error("leaving one mesh took the other with it")
	}
}

// The mesh joined by invite still leaves, unchanged.
func TestLeaveAnAdditionalMesh(t *testing.T) {
	dir := twoMeshPhone(t)

	if err := LeaveMesh(dir, "office"); err != nil {
		t.Fatalf("leaving an additional mesh: %v", err)
	}

	got := meshLabels(t, dir)
	if got["office"] {
		t.Error("office is still there")
	}
	if !got[state.DefaultLabel] {
		t.Error("the original mesh went too")
	}
}

// The last mesh is not leavable, however it is written. A device with no mesh
// is not a device that has left one, it is a device with no configuration —
// and on a phone there would be no way back without clearing app data.
func TestTheOnlyMeshCannotBeLeft(t *testing.T) {
	dir := t.TempDir()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := state.DefaultConfig()
	cfg.NetworkKey = nk.String()
	cfgPath, _ := paths(dir)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	if err := LeaveMesh(dir, state.DefaultLabel); err == nil {
		t.Error("the only mesh was left, which leaves the device unconfigured")
	}
}

// A label nobody has still says so, rather than flattening and then failing
// with something confusing.
func TestLeaveAMeshThatIsNotThere(t *testing.T) {
	dir := twoMeshPhone(t)
	if err := LeaveMesh(dir, "nowhere"); err == nil {
		t.Error("left a mesh that does not exist")
	}
	if got := meshLabels(t, dir); len(got) != 2 {
		t.Errorf("meshes = %v, want both still there", got)
	}
}
