package relay

import (
	"crypto/hmac"
	"encoding/binary"
	"errors"
	"net/netip"
)

// Telling a device where it was seen from.
//
// A node behind NAT cannot learn its own external address; something outside
// has to say. Today the only thing that does is a mesh peer's disco pong, which
// carries "where I saw you" — so a mesh whose every member sits behind one
// router never learns anything. Seen on the office mesh, 2026-09-16: four
// machines and a phone, not one announcing a public address, and none that ever
// would. A phone on 5G had nothing to dial and nothing to punch toward.
//
// A relay is outside by definition and has the answer already: `from`, on every
// frame it receives. It simply never said so. A blind relay is the useful case,
// because a device can talk to one WITHOUT being a member of anything — so this
// gives reflexive discovery to a mesh with no public member at all, and the
// relay carries no traffic unless a direct path cannot be found.
//
// A new frame type rather than a field on the challenge: that frame is a fixed
// length and covered by a MAC, so extending it would break every client that
// has not been updated. An unknown type is refused by Decode and dropped, which
// is exactly the right behaviour for an old client receiving this.

// TypeObserved is the relay reporting the address a frame arrived from.
//
// 7 because 1-6 are taken (register, forward, challenge, confirm, MTU probe and
// echo).
const TypeObserved Type = 7

// observedLen is the frame: type, address family, 16 address bytes, port, mac.
// The address is always stored as 16 bytes so the frame is one size.
const observedLen = 1 + 1 + 16 + 2 + macLen

// EncodeObserved says where a frame came from.
//
// Authenticated with the same key as everything else on this channel. That is
// not a secret — a blind relay's key is derived from its operator's token, or
// public when it is open — so this proves the relay we are talking to sent it,
// not that the address is true. It is treated as a candidate and probed like
// any other, which is what makes an honest answer useful and a dishonest one
// harmless.
func EncodeObserved(k Key, seen netip.AddrPort) ([]byte, error) {
	a := seen.Addr()
	if !a.IsValid() || seen.Port() == 0 {
		return nil, errors.New("no address to report")
	}
	buf := make([]byte, 0, observedLen)
	buf = append(buf, byte(TypeObserved))
	if a.Is4() {
		buf = append(buf, 4)
	} else {
		buf = append(buf, 6)
	}
	b16 := a.As16()
	buf = append(buf, b16[:]...)
	buf = binary.BigEndian.AppendUint16(buf, seen.Port())
	return append(buf, mac(k, buf)...), nil
}

func decodeObserved(k Key, pkt []byte) (*Frame, error) {
	if len(pkt) != observedLen {
		return nil, errors.New("short observed frame")
	}
	body := pkt[:observedLen-macLen]
	if !hmac.Equal(mac(k, body), pkt[observedLen-macLen:]) {
		return nil, errors.New("observed frame failed its mac")
	}
	var b16 [16]byte
	copy(b16[:], body[2:18])
	addr := netip.AddrFrom16(b16)
	if body[1] == 4 {
		addr = addr.Unmap()
	}
	port := binary.BigEndian.Uint16(body[18:20])
	f := &Frame{Type: TypeObserved}
	f.Observed = netip.AddrPortFrom(addr, port)
	return f, nil
}
