package cred

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LanGrant is the mesh admin's say-so for one gateway member to offer one LAN
// host to the mesh, on the listed TCP ports (ADR-068).
//
// It does not carry the LAN address, not even as a commitment. A hash of a
// private address is brute-forced in moments (there are 65 536 in a /16), so
// a commitment would hide nothing; and the gateway is trusted for its LAN
// regardless (the review's first finding), so binding the address would
// contain nothing either. The address stays in the gateway's config, and the
// grant says who may offer which name on which ports.
//
// Serial orders statements for one gateway and host: a node keeps the highest
// it has seen, so a replayed older grant cannot reopen ports the admin has
// since narrowed. A grant with Withdrawn set, at a higher serial, is how the
// admin takes a host away before its grant runs out.
type LanGrant struct {
	MeshID MeshID

	// Gateway is the member's device key: the grant is its, and no other
	// member's announce of the same name is believed.
	Gateway []byte

	// Host is the host's name, one DNS label: "printer" in
	// printer.lan.pi5.office.mesh. With the gateway, it is the host's
	// identity, from which its overlay address is derived.
	Host string

	// Ports are the TCP ports the host may be reached on.
	Ports []PortRange

	Serial    uint64
	NotBefore int64 // unix seconds
	NotAfter  int64
	Withdrawn bool

	Sig []byte
}

// PortRange is a closed range of TCP ports; From == To for one port.
type PortRange struct{ From, To uint16 }

const (
	lanGrantVersion byte = 1

	// MaxLanRanges and MaxLanPorts cap what one grant opens. A grant for
	// 1-65535 is the whole device, which is a LAN exposure, not a host
	// (ADR-068); 256 covers a printer's passive FTPS range and its other
	// ports with room to spare.
	MaxLanRanges = 8
	MaxLanPorts  = 256

	// MaxLanHostName is one DNS label's worth, kept short so a grant fits a
	// control message with room.
	MaxLanHostName = 32

	lanGrantHead = 1 + MeshIDLen + ed25519.PublicKeySize + 1 // version, mesh, gateway, name length
)

// Count is how many ports the ranges open.
func (rs portRanges) Count() int {
	n := 0
	for _, r := range rs {
		n += int(r.To) - int(r.From) + 1
	}
	return n
}

type portRanges []PortRange

// Contains reports whether the grant opens port p.
func (g *LanGrant) Contains(p uint16) bool {
	for _, r := range g.Ports {
		if p >= r.From && p <= r.To {
			return true
		}
	}
	return false
}

// CheckLanHostName is the rule for a host's name: one label of a-z, 0-9 and
// "-", not starting or ending with "-", and not "lan", which names the
// namespace itself.
func CheckLanHostName(s string) error {
	if s == "" || len(s) > MaxLanHostName {
		return fmt.Errorf("host name %q: 1 to %d characters", s, MaxLanHostName)
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return fmt.Errorf("host name %q: only a-z, 0-9 and -", s)
		}
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return fmt.Errorf("host name %q: may not start or end with -", s)
	}
	if s == "lan" {
		return errors.New(`host name "lan" is reserved`)
	}
	return nil
}

// ParseLanPorts reads "8883,990,6000,50000-50100": ports and ranges, merged
// and sorted, within MaxLanRanges and MaxLanPorts.
func ParseLanPorts(s string) ([]PortRange, error) {
	var rs []PortRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		from, err := strconv.ParseUint(lo, 10, 16)
		if err != nil || from == 0 {
			return nil, fmt.Errorf("%q is not a port", lo)
		}
		to := from
		if isRange {
			if to, err = strconv.ParseUint(hi, 10, 16); err != nil || to == 0 {
				return nil, fmt.Errorf("%q is not a port", hi)
			}
		}
		if to < from {
			return nil, fmt.Errorf("range %s runs backwards", part)
		}
		rs = append(rs, PortRange{uint16(from), uint16(to)})
	}
	rs = mergeRanges(rs)
	if len(rs) == 0 {
		return nil, errors.New("no ports")
	}
	if err := checkRanges(rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func mergeRanges(rs []PortRange) []PortRange {
	sort.Slice(rs, func(i, j int) bool { return rs[i].From < rs[j].From })
	var out []PortRange
	for _, r := range rs {
		if n := len(out); n > 0 && int(r.From) <= int(out[n-1].To)+1 {
			if r.To > out[n-1].To {
				out[n-1].To = r.To
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func checkRanges(rs []PortRange) error {
	if len(rs) > MaxLanRanges {
		return fmt.Errorf("%d port ranges, at most %d", len(rs), MaxLanRanges)
	}
	if n := portRanges(rs).Count(); n > MaxLanPorts {
		return fmt.Errorf("%d ports, at most %d: a grant names a host's ports, not the whole device", n, MaxLanPorts)
	}
	for i, r := range rs {
		if r.From == 0 || r.To < r.From {
			return fmt.Errorf("port range %d-%d is not usable", r.From, r.To)
		}
		if i > 0 && r.From <= rs[i-1].To {
			return errors.New("port ranges overlap or are out of order")
		}
	}
	return nil
}

// FormatLanPorts writes ranges back as ParseLanPorts reads them.
func FormatLanPorts(rs []PortRange) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.From == r.To {
			parts = append(parts, strconv.Itoa(int(r.From)))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", r.From, r.To))
		}
	}
	return strings.Join(parts, ",")
}

func (g *LanGrant) signedBytes() ([]byte, error) {
	if len(g.Gateway) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("gateway key is %d bytes, want %d", len(g.Gateway), ed25519.PublicKeySize)
	}
	if err := CheckLanHostName(g.Host); err != nil {
		return nil, err
	}
	if !g.Withdrawn {
		if err := checkRanges(g.Ports); err != nil {
			return nil, err
		}
	} else if len(g.Ports) > MaxLanRanges {
		return nil, errors.New("too many port ranges")
	}
	if g.Serial == 0 {
		return nil, errors.New("serial zero is refused: it would lose to every statement ever made")
	}
	b := make([]byte, 0, lanGrantHead+len(g.Host)+1+4*len(g.Ports)+8+8+8+1)
	b = append(b, lanGrantVersion)
	b = append(b, g.MeshID[:]...)
	b = append(b, g.Gateway...)
	b = append(b, byte(len(g.Host)))
	b = append(b, g.Host...)
	b = append(b, byte(len(g.Ports)))
	for _, r := range g.Ports {
		b = binary.BigEndian.AppendUint16(b, r.From)
		b = binary.BigEndian.AppendUint16(b, r.To)
	}
	b = binary.BigEndian.AppendUint64(b, g.Serial)
	b = binary.BigEndian.AppendUint64(b, uint64(g.NotBefore))
	b = binary.BigEndian.AppendUint64(b, uint64(g.NotAfter))
	if g.Withdrawn {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	return b, nil
}

// Digest is what is signed, fixed-size so a card can sign it.
func (g *LanGrant) Digest() ([32]byte, error) {
	body, err := g.signedBytes()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("shrooms/lan-grant/v1"), body...)), nil
}

// MarshalBinary renders a grant for the wire.
func (g *LanGrant) MarshalBinary() ([]byte, error) {
	body, err := g.signedBytes()
	if err != nil {
		return nil, err
	}
	if len(g.Sig) != sigLen {
		return nil, fmt.Errorf("signature is %d bytes, want %d", len(g.Sig), sigLen)
	}
	return append(body, g.Sig...), nil
}

// UnmarshalLanGrant reads one, checking every length before using it: these
// bytes arrive from a mesh member, and a member may be hostile.
func UnmarshalLanGrant(b []byte) (*LanGrant, error) {
	if len(b) < lanGrantHead+1+1+8*3+1+sigLen {
		return nil, fmt.Errorf("LAN host grant is %d bytes, too short", len(b))
	}
	if b[0] != lanGrantVersion {
		return nil, fmt.Errorf("LAN host grant version %d is not supported", b[0])
	}
	g := &LanGrant{}
	i := 1
	copy(g.MeshID[:], b[i:i+MeshIDLen])
	i += MeshIDLen
	g.Gateway = append([]byte(nil), b[i:i+ed25519.PublicKeySize]...)
	i += ed25519.PublicKeySize
	hl := int(b[i])
	i++
	if hl == 0 || hl > MaxLanHostName || len(b) < i+hl+1 {
		return nil, errors.New("LAN host grant has a bad host name length")
	}
	g.Host = string(b[i : i+hl])
	i += hl
	n := int(b[i])
	i++
	if n > MaxLanRanges || len(b) < i+4*n+8*3+1+sigLen {
		return nil, errors.New("LAN host grant is truncated or names too many ranges")
	}
	for k := 0; k < n; k++ {
		g.Ports = append(g.Ports, PortRange{binary.BigEndian.Uint16(b[i:]), binary.BigEndian.Uint16(b[i+2:])})
		i += 4
	}
	g.Serial = binary.BigEndian.Uint64(b[i:])
	g.NotBefore = int64(binary.BigEndian.Uint64(b[i+8:]))
	g.NotAfter = int64(binary.BigEndian.Uint64(b[i+16:]))
	i += 24
	switch b[i] {
	case 0:
	case 1:
		g.Withdrawn = true
	default:
		return nil, errors.New("LAN host grant has a bad withdrawn flag")
	}
	i++
	if len(b) != i+sigLen {
		return nil, fmt.Errorf("LAN host grant is %d bytes, want %d", len(b), i+sigLen)
	}
	g.Sig = append([]byte(nil), b[i:]...)
	// Re-derive the signed form, so every rule signedBytes enforces (name,
	// ranges, serial) holds for what came off the wire too.
	if _, err := g.signedBytes(); err != nil {
		return nil, fmt.Errorf("LAN host grant: %w", err)
	}
	return g, nil
}

// VerifyLanGrantBy checks a grant against a mesh's authority: the mesh, then
// the signature. Whether it is in force at a moment is Active's question.
func VerifyLanGrantBy(auth *Authority, g *LanGrant) error {
	if auth == nil {
		return errors.New("no authority")
	}
	if g == nil {
		return errors.New("no LAN host grant")
	}
	if g.MeshID != auth.ID() {
		return ErrWrongMesh
	}
	d, err := g.Digest()
	if err != nil {
		return fmt.Errorf("LAN host grant is malformed: %w", err)
	}
	for _, k := range auth.Keys {
		if verifyKey(k, d[:], g.Sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// Active is whether a verified grant opens its host at now.
func (g *LanGrant) Active(now time.Time) bool {
	t := now.Unix()
	return !g.Withdrawn && t >= g.NotBefore && t < g.NotAfter
}

// GrantLanHostWith signs a grant through the Signer seam, so the admin key can
// be a file or a card. withdrawn takes the host away at this serial.
func GrantLanHostWith(s Signer, auth *Authority, gateway []byte, host string, ports []PortRange,
	serial uint64, now time.Time, life time.Duration, withdrawn bool) (*LanGrant, error) {

	if s == nil {
		return nil, errors.New("no signer")
	}
	if auth == nil {
		return nil, errors.New("no authority to sign for")
	}
	if !withdrawn && life <= 0 {
		return nil, errors.New("a grant with no lifetime is already expired")
	}
	g := &LanGrant{
		MeshID:    auth.ID(),
		Gateway:   append([]byte(nil), gateway...),
		Host:      host,
		Ports:     append([]PortRange(nil), ports...),
		Serial:    serial,
		NotBefore: now.Add(-time.Minute).Unix(),
		NotAfter:  now.Add(life).Unix(),
		Withdrawn: withdrawn,
	}
	d, err := g.Digest()
	if err != nil {
		return nil, err
	}
	if g.Sig, err = s.SignDigest(d); err != nil {
		return nil, err
	}
	// Checked before it leaves, as a revocation is: a card can return
	// something well-formed and wrong, and every peer would silently discard it.
	if err := VerifyLanGrantBy(auth, g); err != nil {
		return nil, fmt.Errorf("the signer produced a LAN host grant this mesh will not accept: %w", err)
	}
	return g, nil
}
