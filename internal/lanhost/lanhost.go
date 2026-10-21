// Package lanhost is the part of ADR-068 both sides share: a LAN host's config
// entry, the checks on its LAN address, and the overlay address and name a
// host is known by on the mesh.
//
// A LAN host is one device on a gateway member's LAN, offered to the mesh on
// listed TCP ports under an admin's grant (cred.LanGrant). Members never see
// its LAN address: they reach a derived overlay address, and the gateway
// proxies.
package lanhost

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"strings"

	"github.com/vpavlin/shrooms/internal/cred"
)

// Spec is one host in a gateway's config:
// "printer=192.168.10.42/tcp:8883,990,6000,50000-50100".
type Spec struct {
	Name  string
	Addr  netip.Addr // the LAN address: only ever on the gateway
	Ports []cred.PortRange
}

// MaxPerGateway caps how many hosts one gateway offers on one mesh. It bounds
// what a gateway's grants add to every member's tunnel config, and keeps a
// LAN host a device, not a LAN (ADR-068).
const MaxPerGateway = 16

// ParseSpec reads one config entry.
func ParseSpec(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	name, rest, ok := strings.Cut(s, "=")
	if !ok {
		return Spec{}, fmt.Errorf("%q: want name=address/tcp:ports", s)
	}
	name = strings.TrimSpace(name)
	if err := cred.CheckLanHostName(name); err != nil {
		return Spec{}, err
	}
	addr, proto, ok := strings.Cut(rest, "/")
	if !ok {
		return Spec{}, fmt.Errorf("%q: want name=address/tcp:ports", s)
	}
	proto, ports, ok := strings.Cut(proto, ":")
	if !ok || proto != "tcp" {
		return Spec{}, fmt.Errorf("%q: only tcp is supported (ADR-068: TCP first)", s)
	}
	a, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return Spec{}, fmt.Errorf("%q: %q is not an address literal: a LAN host is named by its address, never a host name (no DNS, so no rebinding)", s, addr)
	}
	if err := CheckAddr(a); err != nil {
		return Spec{}, fmt.Errorf("%q: %w", s, err)
	}
	rs, err := cred.ParseLanPorts(ports)
	if err != nil {
		return Spec{}, fmt.Errorf("%q: %w", s, err)
	}
	return Spec{Name: name, Addr: a.Unmap(), Ports: rs}, nil
}

// ParseSpecs reads a gateway's list, refusing duplicate names and more than
// MaxPerGateway hosts.
func ParseSpecs(list []string) ([]Spec, error) {
	if len(list) > MaxPerGateway {
		return nil, fmt.Errorf("%d LAN hosts, at most %d per gateway", len(list), MaxPerGateway)
	}
	seen := map[string]bool{}
	var out []Spec
	for _, s := range list {
		sp, err := ParseSpec(s)
		if err != nil {
			return nil, err
		}
		if seen[sp.Name] {
			return nil, fmt.Errorf("LAN host %q is listed twice", sp.Name)
		}
		seen[sp.Name] = true
		out = append(out, sp)
	}
	return out, nil
}

// CheckAddr refuses an address no LAN host may have: loopback, link-local,
// unspecified, multicast, broadcast, and the mesh's own ranges (the overlay
// fd00::/8 and the synthetic IPv4 198.18.0.0/15). Whether it is on a link the
// gateway is on, and not one of the gateway's own addresses, is the gateway's
// check at the time it serves (OnLink), because interfaces change.
func CheckAddr(a netip.Addr) error {
	a = a.Unmap()
	switch {
	case !a.IsValid() || a.IsUnspecified():
		return fmt.Errorf("%s is not a host", a)
	case a.IsLoopback():
		return fmt.Errorf("%s is loopback", a)
	case a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast():
		return fmt.Errorf("%s is link-local", a)
	case a.IsMulticast() || a.IsInterfaceLocalMulticast():
		return fmt.Errorf("%s is multicast", a)
	case a.Is4() && a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return fmt.Errorf("%s is broadcast", a)
	case a.Zone() != "":
		return fmt.Errorf("%s carries a zone", a)
	}
	for _, p := range meshRanges {
		if p.Contains(a) {
			return fmt.Errorf("%s is inside the mesh's own range %s", a, p)
		}
	}
	return nil
}

var meshRanges = []netip.Prefix{
	netip.MustParsePrefix("fd00::/8"),        // every mesh's overlay (ADR-005)
	netip.MustParsePrefix("198.18.0.0/15"),   // synthetic IPv4 (ADR-021)
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT: tunnels and carriers, never a LAN appliance
	netip.MustParsePrefix("169.254.0.0/16"),  // IPv4 link-local, also checked above
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("ff00::/8"),        // IPv6 multicast, also checked above
	netip.MustParsePrefix("::ffff:0:0/96"),   // mapped: written as IPv4 instead
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
}

// Overlay is the host's address on a mesh: the mesh's /48, then 80 bits from
// the gateway's key and the host's name, under its own domain so it can never
// be a member's address (identity.OverlayAddr derives from "mesh/v1/addr").
func Overlay(meshPrefix netip.Prefix, gateway ed25519.PublicKey, name string) netip.Addr {
	h := sha256.Sum256(append(append([]byte("mesh/v1/lanhost\x00"), gateway...), name...))
	p := meshPrefix.Addr().As16()
	var ip [16]byte
	copy(ip[:6], p[:6])
	copy(ip[6:], h[:10])
	return netip.AddrFrom16(ip)
}

// AliasKey stands in for a device key when the synthetic IPv4 table gives the
// host an alias (v4.Entry.DevicePub): stable per gateway and name, and never
// a real device's key.
func AliasKey(gateway ed25519.PublicKey, name string) []byte {
	h := sha256.Sum256(append(append([]byte("mesh/v1/lanhost-v4\x00"), gateway...), name...))
	return h[:]
}

// Label is the reserved label that marks the namespace:
// <host>.lan.<gateway>.<mesh>.mesh.
const Label = "lan"
