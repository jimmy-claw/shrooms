package lanhost

import "testing"

func TestAnAliasIsAPrivateAddressForAHost(t *testing.T) {
	al, err := ParseAlias("192.168.10.143=printer.lan.proteus")
	if err != nil || al.LAN.String() != "192.168.10.143" || al.Host != "printer" || al.Gateway != "proteus" {
		t.Fatalf("%+v, %v", al, err)
	}
	for _, bad := range []string{
		"8.8.8.8=printer.lan.proteus",         // public: would take a piece of the internet
		"198.19.1.1=printer.lan.proteus",      // the mesh's own
		"fd12::1=printer.lan.proteus",         // IPv6
		"192.168.10.143=printer.proteus",      // not a LAN host's name
		"192.168.10.143=printer.lan.",         // no gateway
		"192.168.10.143=lan.lan.proteus",      // a reserved host name
		"192.168.10.143",                      // no host
		"printer=printer.lan.proteus",         // no address
		"127.0.0.1=printer.lan.proteus",       // loopback
		"192.168.10.143=printer.lan.p.office", // the mesh is not part of it
	} {
		if _, err := ParseAlias(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := ParseAliases([]string{"10.0.0.5=a.lan.gw", "10.0.0.5=b.lan.gw"}); err == nil {
		t.Error("one address for two hosts was accepted")
	}
}
