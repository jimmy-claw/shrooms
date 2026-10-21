package lanhost

import (
	"crypto/ed25519"
	"net/netip"
	"strings"
	"testing"
)

// A gateway's entry: a literal address, TCP, listed ports (ADR-068).
func TestASpecIsALiteralHostOnListedPorts(t *testing.T) {
	sp, err := ParseSpec("printer=192.168.10.42/tcp:8883,990,6000,50000-50100")
	if err != nil {
		t.Fatal(err)
	}
	if sp.Name != "printer" || sp.Addr != netip.MustParseAddr("192.168.10.42") || len(sp.Ports) != 4 {
		t.Fatalf("%+v", sp)
	}
	for _, bad := range []string{
		"printer=printer.local/tcp:80", // a name: DNS, so rebinding
		"printer=192.168.10.42/udp:80", // TCP first
		"printer=192.168.10.42/tcp:1-65535",
		"printer=127.0.0.1/tcp:80",       // loopback
		"printer=169.254.1.2/tcp:80",     // link-local
		"printer=224.0.0.1/tcp:80",       // multicast
		"printer=255.255.255.255/tcp:80", // broadcast
		"printer=0.0.0.0/tcp:80",
		"printer=fd12:3456:789a::1/tcp:80", // the overlay
		"printer=198.18.0.5/tcp:80",        // synthetic IPv4
		"printer=fe80::1%eth0/tcp:80",
		"lan=192.168.10.42/tcp:80", // the namespace's own label
		"printer=192.168.10.42",
	} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseSpecs([]string{"a=10.0.0.2/tcp:80", "a=10.0.0.3/tcp:80"}); err == nil {
		t.Error("a duplicate name accepted")
	}
	many := make([]string, MaxPerGateway+1)
	for i := range many {
		many[i] = "h" + strings.Repeat("x", i) + "=10.0.0.2/tcp:80"
	}
	if _, err := ParseSpecs(many); err == nil {
		t.Error("more than MaxPerGateway hosts accepted")
	}
}

// A host's overlay address is the mesh's, stable per gateway and name, and
// different from the gateway's own and from another host's.
func TestAHostsAddressIsItsOwn(t *testing.T) {
	pfx := netip.MustParsePrefix("fdaa:bbbb:cccc::/48")
	gw, _, _ := ed25519.GenerateKey(nil)
	a := Overlay(pfx, gw, "printer")
	if !pfx.Contains(a) || a != Overlay(pfx, gw, "printer") {
		t.Fatalf("%s not stable inside %s", a, pfx)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if a == Overlay(pfx, gw, "camera") || a == Overlay(pfx, other, "printer") {
		t.Error("two hosts share an address")
	}
	if string(AliasKey(gw, "printer")) == string(gw) {
		t.Error("the alias key is the gateway's own key")
	}
}
