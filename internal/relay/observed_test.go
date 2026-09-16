package relay

import (
	"net/netip"
	"testing"
	"time"
)

// A relay telling a device where it was seen from.
//
// The case it exists for, from the office mesh on 2026-09-16: four machines and
// a phone, every one behind the same router, not one announcing a public
// address and none that ever would. Reflexive discovery arrives on disco pongs
// from peers, and no peer was ever outside. A relay always is.

func TestObservedRoundTrips(t *testing.T) {
	k := testKey(t)
	for _, s := range []string{
		"198.51.100.10:51820",
		"85.160.39.54:11053",
		"[2001:db8::1]:51820",
		"10.77.57.173:51821", // private is kept: a peer on our LAN sees us there
	} {
		want := netip.MustParseAddrPort(s)
		raw, err := EncodeObserved(k, want)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		f, err := Decode(k, raw)
		if err != nil {
			t.Fatalf("%s: decode: %v", s, err)
		}
		if f.Type != TypeObserved {
			t.Fatalf("%s: type = %d", s, f.Type)
		}
		if f.Observed != want {
			t.Errorf("observed = %v, want %v", f.Observed, want)
		}
	}
}

// Every frame on this channel is authenticated, and this one is not special: a
// relay we are not talking to must not be able to tell us where we are, because
// the answer is announced to peers.
func TestObservedRejectsAForeignKey(t *testing.T) {
	raw, err := EncodeObserved(testKey(t), netip.MustParseAddrPort("198.51.100.10:51820"))
	if err != nil {
		t.Fatal(err)
	}
	var other Key
	other[0] = 0xAA
	if _, err := Decode(other, raw); err == nil {
		t.Error("a frame from a foreign key was accepted")
	}
}

func TestObservedRejectsNonsense(t *testing.T) {
	k := testKey(t)
	if _, err := EncodeObserved(k, netip.AddrPort{}); err == nil {
		t.Error("encoded an invalid address")
	}
	if _, err := EncodeObserved(k, netip.AddrPortFrom(netip.MustParseAddr("198.51.100.1"), 0)); err == nil {
		t.Error("encoded port 0")
	}
	raw, _ := EncodeObserved(k, netip.MustParseAddrPort("198.51.100.10:51820"))
	if _, err := Decode(k, raw[:len(raw)-1]); err == nil {
		t.Error("a truncated frame was accepted")
	}
}

// A client that predates this refuses the frame rather than misreading it,
// which is what let it be added without a flag day: Decode's default case
// returns an error for an unknown type and the caller drops it.
func TestAnUnknownTypeIsRefused(t *testing.T) {
	k := testKey(t)
	raw, _ := EncodeObserved(k, netip.MustParseAddrPort("198.51.100.10:51820"))
	raw[0] = 99
	if _, err := Decode(k, raw); err == nil {
		t.Error("an unknown frame type was accepted")
	}
}

// It is counted, so an operator can tell "nobody asked" from "we never told
// anybody" — the two look identical from a device that learns nothing.
func TestTheRelayCountsWhatItTold(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	now := time.Now()

	out, _, _ := s.Handle(EncodeRegister(k, wg, priv, now), here, now)
	f, _ := Decode(k, out)
	if got := s.Stats().Told; got != 0 {
		t.Errorf("Told = %d before any registration completed, want 0", got)
	}
	s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), here, now)
	if got := s.Stats().Told; got != 1 {
		t.Errorf("Told = %d after a confirmed registration, want 1", got)
	}
}
