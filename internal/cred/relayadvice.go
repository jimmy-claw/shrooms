package cred

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// RelayAdvice names the blind relays a mesh's admin wants its members to use
// (docs/distributing-a-blind-relay.md).
//
// A blind relay is not a member, cannot announce itself, and used to have to be
// configured on every device by hand. This is the admin saying it once.
//
// Signed by the authority rather than asserted by any member, which is the
// decision recorded on 2026-09-17: pointing a mesh at a blind relay exposes
// every member's traffic tags, timing and volume to a third party, and the
// person who decides who is a member is the person who should decide that.
//
// It is advice, not an instruction. A device that configured relays of its own
// keeps them, and relay_blind = "none" refuses this outright — otherwise the
// statement would be a way to move somebody's traffic without their consent.
//
// Serial orders statements for one mesh: a node keeps the highest it has seen
// and refuses anything lower, so replaying an old statement cannot move a mesh
// back to a relay the admin has since abandoned. An empty relay list is a valid
// statement, and is how an admin withdraws the advice.
type RelayAdvice struct {
	MeshID MeshID

	// Serial is strictly increasing per mesh. Unix seconds by default, which
	// orders statements made from different machines without coordination.
	Serial uint64

	// Relays are the blind relays, in the order to prefer them.
	Relays []netip.AddrPort

	// Token authenticates to the relays, when their operator issued one. One
	// for the lot, like relay_token in a config: a list naming several relays
	// run by several operators with several tokens is not a thing the config
	// can express either.
	Token string

	Issued int64 // unix seconds
	Sig    []byte
}

const (
	adviceVersion byte = 1

	// MaxAdviceRelays caps the list. A device registers with at most two
	// blind relays; four leaves room to name spares without letting a
	// statement grow past what one control message carries.
	MaxAdviceRelays = 4

	// MaxAdviceToken caps the token. Relay tokens are short strings handed out
	// by an operator; the cap is what keeps the statement inside the padding.
	MaxAdviceToken = 128

	// An address is written as 16 bytes of IPv6 (IPv4 mapped) and a port.
	adviceAddrLen = 16 + 2

	adviceHead = 1 + MeshIDLen + 8 + 8 + 1 // version, mesh, serial, issued, count
)

func (a *RelayAdvice) signedBytes() ([]byte, error) {
	if len(a.Relays) > MaxAdviceRelays {
		return nil, fmt.Errorf("%d relays, at most %d", len(a.Relays), MaxAdviceRelays)
	}
	if len(a.Token) > MaxAdviceToken {
		return nil, fmt.Errorf("token is %d bytes, at most %d", len(a.Token), MaxAdviceToken)
	}
	if a.Serial == 0 {
		return nil, errors.New("serial zero is refused: it would lose to every statement ever made")
	}
	b := make([]byte, 0, adviceHead+len(a.Relays)*adviceAddrLen+1+len(a.Token))
	b = append(b, adviceVersion)
	b = append(b, a.MeshID[:]...)
	b = binary.BigEndian.AppendUint64(b, a.Serial)
	b = binary.BigEndian.AppendUint64(b, uint64(a.Issued))
	b = append(b, byte(len(a.Relays)))
	for _, r := range a.Relays {
		if !r.IsValid() || r.Port() == 0 || r.Addr().Zone() != "" {
			return nil, fmt.Errorf("relay %q is not a usable address", r)
		}
		ip := r.Addr().As16()
		b = append(b, ip[:]...)
		b = binary.BigEndian.AppendUint16(b, r.Port())
	}
	b = append(b, byte(len(a.Token)))
	b = append(b, a.Token...)
	return b, nil
}

// Digest is what is signed, fixed-size so a card can sign it.
func (a *RelayAdvice) Digest() ([32]byte, error) {
	body, err := a.signedBytes()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("shrooms/relay-advice/v1"), body...)), nil
}

// MarshalBinary renders a statement for the wire.
func (a *RelayAdvice) MarshalBinary() ([]byte, error) {
	body, err := a.signedBytes()
	if err != nil {
		return nil, err
	}
	if len(a.Sig) != sigLen {
		return nil, fmt.Errorf("signature is %d bytes, want %d", len(a.Sig), sigLen)
	}
	return append(body, a.Sig...), nil
}

// UnmarshalRelayAdvice reads one, checking every length before using it: these
// bytes arrive from a mesh member, and a member may be hostile.
func UnmarshalRelayAdvice(b []byte) (*RelayAdvice, error) {
	if len(b) < adviceHead+1+sigLen {
		return nil, fmt.Errorf("relay advice is %d bytes, too short", len(b))
	}
	if b[0] != adviceVersion {
		return nil, fmt.Errorf("relay advice version %d is not supported", b[0])
	}
	a := &RelayAdvice{}
	i := 1
	copy(a.MeshID[:], b[i:i+MeshIDLen])
	i += MeshIDLen
	a.Serial = binary.BigEndian.Uint64(b[i : i+8])
	i += 8
	a.Issued = int64(binary.BigEndian.Uint64(b[i : i+8]))
	i += 8
	n := int(b[i])
	i++
	if n > MaxAdviceRelays {
		return nil, fmt.Errorf("relay advice names %d relays, at most %d", n, MaxAdviceRelays)
	}
	if len(b) < i+n*adviceAddrLen+1 {
		return nil, errors.New("relay advice is truncated")
	}
	for k := 0; k < n; k++ {
		var ip [16]byte
		copy(ip[:], b[i:i+16])
		port := binary.BigEndian.Uint16(b[i+16 : i+18])
		i += adviceAddrLen
		ap := netip.AddrPortFrom(netip.AddrFrom16(ip).Unmap(), port)
		if port == 0 || !ap.Addr().IsValid() || ap.Addr().IsUnspecified() {
			return nil, fmt.Errorf("relay advice names an unusable address %s", ap)
		}
		a.Relays = append(a.Relays, ap)
	}
	tl := int(b[i])
	i++
	if tl > MaxAdviceToken {
		return nil, fmt.Errorf("relay advice token is %d bytes, at most %d", tl, MaxAdviceToken)
	}
	if len(b) != i+tl+sigLen {
		return nil, fmt.Errorf("relay advice is %d bytes, want %d", len(b), i+tl+sigLen)
	}
	a.Token = string(b[i : i+tl])
	i += tl
	a.Sig = append([]byte(nil), b[i:]...)
	if a.Serial == 0 {
		return nil, errors.New("relay advice has serial zero")
	}
	return a, nil
}

// VerifyRelayAdviceBy checks a statement against a mesh's authority.
//
// The mesh id first, then the signature: advice for another mesh signed by
// that mesh's admin is valid and simply not ours, and accepting it would let
// anyone who runs a mesh redirect ours.
func VerifyRelayAdviceBy(auth *Authority, a *RelayAdvice) error {
	if auth == nil {
		return errors.New("no authority")
	}
	if a == nil {
		return errors.New("no relay advice")
	}
	if a.MeshID != auth.ID() {
		return ErrWrongMesh
	}
	d, err := a.Digest()
	if err != nil {
		return fmt.Errorf("relay advice is malformed: %w", err)
	}
	for _, k := range auth.Keys {
		if verifyKey(k, d[:], a.Sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// AdviseRelaysWith signs a statement through the Signer seam, so the admin key
// can be a file or a card. Relays may be empty, which withdraws the advice.
func AdviseRelaysWith(s Signer, auth *Authority, serial uint64,
	relays []netip.AddrPort, token string, now time.Time) (*RelayAdvice, error) {

	if s == nil {
		return nil, errors.New("no signer")
	}
	if auth == nil {
		return nil, errors.New("no authority to sign for")
	}
	a := &RelayAdvice{
		MeshID: auth.ID(),
		Serial: serial,
		Relays: append([]netip.AddrPort(nil), relays...),
		Token:  token,
		Issued: now.Unix(),
	}
	d, err := a.Digest()
	if err != nil {
		return nil, err
	}
	if a.Sig, err = s.SignDigest(d); err != nil {
		return nil, err
	}
	// Checked before it leaves, as a revocation is: a card can return
	// something well-formed and wrong, and every peer would silently discard it.
	if err := VerifyRelayAdviceBy(auth, a); err != nil {
		return nil, fmt.Errorf("the signer produced relay advice this mesh will not accept: %w", err)
	}
	return a, nil
}
