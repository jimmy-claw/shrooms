package lanhost

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/vpavlin/shrooms/internal/cred"
)

// An Alias is a member's own say that a LAN address means a LAN host:
// "192.168.10.143=printer.lan.proteus". Packets this device sends to the LAN
// address go to the host over the mesh, and the replies come back from it.
//
// For applications that use the address a device gave them rather than the
// one they were told. Bambu Studio is told the host's mesh address, then
// opens the camera and the FTPS data connection to the address the printer
// reports, which is its LAN one: from another LAN, that went nowhere or, on
// a LAN with the same range, to whatever local device had that address (audit
// F-29). The alias is local config on the member, an opt-in, and changes
// nothing for anyone else: the gateway sees the same connections to the
// host's overlay address it always does, under the same grant.
type Alias struct {
	LAN     netip.Addr // the address applications use
	Host    string     // the host's name: printer
	Gateway string     // its gateway's name on the mesh: proteus
}

// MaxAliases caps a member's list, as MaxPerGateway caps a gateway's.
const MaxAliases = 16

// ParseAlias reads one entry: "ADDRESS=HOST.lan.GATEWAY".
func ParseAlias(s string) (Alias, error) {
	s = strings.TrimSpace(s)
	addr, name, ok := strings.Cut(s, "=")
	if !ok {
		return Alias{}, fmt.Errorf("%q: want address=host.%s.gateway", s, Label)
	}
	a, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return Alias{}, fmt.Errorf("%q: %q is not an address", s, addr)
	}
	a = a.Unmap()
	// A private IPv4 address only. The alias routes the address into the
	// mesh on this device, and a public one would quietly take a piece of
	// the internet with it; IPv6 LANs have not needed it yet.
	if !a.Is4() || !a.IsPrivate() {
		return Alias{}, fmt.Errorf("%q: %s is not a private IPv4 address (10/8, 172.16/12, 192.168/16)", s, a)
	}
	if err := CheckAddr(a); err != nil {
		return Alias{}, fmt.Errorf("%q: %w", s, err)
	}
	labels := strings.Split(strings.ToLower(strings.TrimSpace(name)), ".")
	if len(labels) != 3 || labels[1] != Label {
		return Alias{}, fmt.Errorf("%q: want address=host.%s.gateway, the host's name without the mesh", s, Label)
	}
	if err := cred.CheckLanHostName(labels[0]); err != nil {
		return Alias{}, fmt.Errorf("%q: %w", s, err)
	}
	// The gateway's name is a DNS label like the host's: one that is not
	// could never match a gateway, and the alias would sit there silently
	// inactive (audit F-45).
	if err := cred.CheckLanHostName(labels[2]); err != nil {
		return Alias{}, fmt.Errorf("%q: the gateway: %w", s, err)
	}
	return Alias{LAN: a, Host: labels[0], Gateway: labels[2]}, nil
}

// ParseAliases reads a member's list, refusing an address listed twice.
func ParseAliases(list []string) ([]Alias, error) {
	if len(list) > MaxAliases {
		return nil, fmt.Errorf("%d LAN aliases, at most %d", len(list), MaxAliases)
	}
	seen := map[netip.Addr]bool{}
	var out []Alias
	for _, s := range list {
		al, err := ParseAlias(s)
		if err != nil {
			return nil, err
		}
		if seen[al.LAN] {
			return nil, fmt.Errorf("LAN alias %s is listed twice", al.LAN)
		}
		seen[al.LAN] = true
		out = append(out, al)
	}
	return out, nil
}
