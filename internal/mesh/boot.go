package mesh

import (
	"math/big"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/state"
)

// Publishing an address other members can bootstrap their rendezvous
// connection from (ADR-031).
//
// The problem this solves is narrow and was expensive: on 2026-08-20 five of
// the six public entry nodes refused connections. Every machine already
// connected stayed up; the one that had restarted went to zero peers and could
// not get back in, because bootstrap addresses are read when the delivery node
// is constructed and it had nowhere else to look. A mesh that publishes its own
// entry points does not have that failure.

// bootPeerID caches the delivery node's peer id.
//
// One FFI call, not one per announce: the id is fixed for the life of the node
// and announces go out every 45 seconds, per mesh, forever.
var (
	bootOnce sync.Once
	bootID   string
)

// bootAddr is the multiaddr to publish, or "" when this node is not one others
// should bootstrap from.
//
// Three conditions, all necessary:
//
//   - Core. An Edge node subscribes and forwards nothing, so bootstrapping from
//     it buys a connection to a node with no gossip to share.
//   - relay. The operator's statement that this node is meant to be dialled.
//     It is a statement, not a measurement — selectRelay stopped treating it as
//     proof of reachability on 2026-09-17, after k11 — which is why the public
//     address below is also required: the flag says "meant to be", the address
//     says where.
//   - a pinned delivery port. The library will not say which port it chose, so
//     an unpinned node cannot describe itself; and a random port would change
//     on the next restart, publishing an address that quietly stops working.
//
// The address is the same public IP this node already advertises for
// WireGuard, with the delivery port rather than the tunnel port. A node that
// cannot name a public address for itself publishes nothing rather than
// guessing: a bootstrap address that does not answer is worse than none, since
// a peer that keeps it wastes its first dial on it.
func (m *Mesh) bootAddr(now time.Time) string { return m.bootAddrFor(m.cfg, now) }

// bootAddrFor is bootAddr with the config passed in, so the conditions can be
// exercised without standing up a delivery node.
func (m *Mesh) bootAddrFor(cfg state.Config, now time.Time) string {
	if cfg.Mode != "Core" || !cfg.Relay || cfg.DeliveryPort == 0 {
		return ""
	}
	ip := m.publicIPFrom(cfg, now)
	if !ip.IsValid() {
		return ""
	}
	bootOnce.Do(func() {
		if id, err := m.node.PeerID(); err == nil {
			bootID = strings.TrimSpace(id)
		}
	})
	if bootID == "" {
		return ""
	}
	return "/ip4/" + ip.String() +
		"/tcp/" + strconv.Itoa(int(cfg.DeliveryPort)) +
		"/p2p/" + bootID
}

// publicIP is a globally routable address this node is reachable at.
//
// Configured first, because an operator who wrote it down knows better than we
// do; then what peers report seeing us at. Private and loopback addresses are
// refused — they are useless to anybody off this LAN, and a mesh's members are
// mostly off it.
func (m *Mesh) publicIPFrom(cfg state.Config, now time.Time) netip.Addr {
	for _, a := range cfg.Advertise {
		if ap, err := netip.ParseAddrPort(a); err == nil && routable(ap.Addr()) {
			return ap.Addr()
		}
	}
	if m.prober != nil {
		for _, ap := range m.prober.Reflexive(now) {
			if routable(ap.Addr()) {
				return ap.Addr()
			}
		}
	}
	return netip.Addr{}
}

// routable reports whether an address is one another member could dial for
// bootstrap.
//
// IPv4 only, which is not a simplification. The delivery library listens on
// /ip4/0.0.0.0 and nothing else, and the fleet's own entry nodes are /dns4/
// multiaddrs — A records by construction. So a /ip6/ bootstrap address is one
// the library cannot use, and publishing it would cost every peer that stored
// it a wasted dial at startup, in the order ahead of addresses that work.
//
// The *data plane* has no such limit: the WireGuard socket binds [::], disco
// carries 16-byte addresses with v4 mapped into v6, and two members with IPv6
// reach each other directly with nothing to punch. This function is about
// bootstrap, which is the one place that asymmetry bites.
//
// Revisit when delivery listens on v6 and the fleet publishes /dns6/ or /ip6/
// entries. Until then an IPv6-only machine can be a fine mesh member and cannot
// be a bootstrap node.
func routable(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.Is4() && !a.IsPrivate() && !a.IsLoopback() &&
		!a.IsLinkLocalUnicast() && !a.IsUnspecified() && !a.IsMulticast()
}

// ValidBootAddr is whether a bootstrap address a peer published may be kept:
// /ip4/<public address>/tcp/<port>/p2p/<peer id>, exactly the form bootAddr
// publishes, and nothing else. A member could otherwise point every restart
// at a loopback, private, reserved or malformed address (audit F-22,
// 2026-10-19), and an entry the library cannot parse might cost the whole
// list.
//
// No /dns4: no node of ours publishes one, and a name is checked only once
// resolved: "127.0.0.1.nip.io" is loopback with a public-looking name
// (auditor, 2026-10-20).
func ValidBootAddr(a string) bool {
	parts := strings.Split(a, "/")
	if len(parts) != 7 || parts[0] != "" || parts[1] != "ip4" || parts[3] != "tcp" || parts[5] != "p2p" {
		return false
	}
	ip, err := netip.ParseAddr(parts[2])
	if err != nil || !ip.Is4() || !routable(ip) || reserved4(ip) {
		return false
	}
	if p := parts[4]; p == "" || p[0] == '0' {
		return false
	}
	if port, err := strconv.ParseUint(parts[4], 10, 16); err != nil || port == 0 {
		return false
	}
	return validPeerID(parts[6])
}

// reserved4 is the IPv4 space routable leaves in that is still not anybody's
// public address: shared, documentation, benchmarking, protocol, relay,
// future use and broadcast.
func reserved4(a netip.Addr) bool {
	for _, p := range reservedV4 {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

var reservedV4 = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// validPeerID is a libp2p peer id: base58btc of a multihash, identity
// (0x00) or sha2-256 (0x12), whose stated length is what follows.
func validPeerID(id string) bool {
	if len(id) < 32 || len(id) > 64 {
		return false
	}
	n := new(big.Int)
	for _, r := range id {
		d := strings.IndexRune(base58, r)
		if d < 0 {
			return false
		}
		n.Mul(n, big.NewInt(58)).Add(n, big.NewInt(int64(d)))
	}
	b := n.Bytes()
	for _, r := range id {
		if r != '1' {
			break
		}
		b = append([]byte{0}, b...) // a leading '1' is a leading zero byte
	}
	if len(b) < 2 || b[0] != 0x00 && b[0] != 0x12 {
		return false
	}
	return int(b[1]) == len(b)-2 && (b[0] != 0x12 || b[1] == 32)
}

const base58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
