package state

import "testing"

// A gateway's LAN hosts and a member's LAN aliases are per mesh, survive a
// write and a read with the commas inside their items, and reach the mesh's
// own view of the config (ADR-068).
func TestLanHostsAndAliasesRoundTrip(t *testing.T) {
	text := `name = "laptop"
mesh.office.key = "` + aKey(t) + `"
mesh.office.lan_hosts = ["printer=192.168.10.143/tcp:990,2024,8883"]
mesh.office.lan_aliases = ["192.168.10.143=printer.lan.proteus"]
`
	c, err := parseConfig(text)
	if err != nil {
		t.Fatal(err)
	}
	m := c.MeshSet["office"]
	if len(m.LanHosts) != 1 || m.LanHosts[0] != "printer=192.168.10.143/tcp:990,2024,8883" ||
		len(m.LanAliases) != 1 || m.LanAliases[0] != "192.168.10.143=printer.lan.proteus" {
		t.Fatalf("parsed %+v", m)
	}
	out, err := RenderConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := parseConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	m2 := again.MeshSet["office"]
	if len(m2.LanAliases) != 1 || m2.LanAliases[0] != m.LanAliases[0] || m2.LanHosts[0] != m.LanHosts[0] {
		t.Errorf("after a round trip: %+v\n%s", m2, out)
	}
	if fm := c.ForMesh(m, 1); len(fm.LanAliases) != 1 || len(fm.LanHosts) != 1 {
		t.Errorf("ForMesh dropped them: %+v %+v", fm.LanHosts, fm.LanAliases)
	}
}
