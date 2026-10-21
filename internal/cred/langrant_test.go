package cred

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

func lanGateway(t *testing.T) []byte {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// A grant names a gateway, a host and its ports; it round-trips, verifies
// under its own mesh only, and refuses tampering (ADR-068).
func TestALanGrantRoundTripsAndVerifies(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	gw := lanGateway(t)
	ports, err := ParseLanPorts("8883,990,6000,50000-50100")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	g, err := GrantLanHostWith(admin, auth, gw, "printer", ports, 7, now, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := g.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalLanGrant(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyLanGrantBy(auth, back); err != nil {
		t.Fatal(err)
	}
	if back.Host != "printer" || back.Serial != 7 || FormatLanPorts(back.Ports) != "990,6000,8883,50000-50100" ||
		!back.Active(now) || back.Active(now.Add(25*time.Hour)) {
		t.Errorf("round trip: %+v", back)
	}
	if !back.Contains(50050) || back.Contains(22) || back.Contains(50101) {
		t.Error("Contains is wrong")
	}

	// Another mesh's admin, or a flipped byte, is refused.
	other, _ := NewAdmin()
	otherAuth, _ := NewAuthority(other.Pub)
	if err := VerifyLanGrantBy(otherAuth, back); err == nil {
		t.Error("a grant verified under another mesh")
	}
	bad := append([]byte(nil), raw...)
	bad[len(bad)-sigLen-30] ^= 1
	if g2, err := UnmarshalLanGrant(bad); err == nil && VerifyLanGrantBy(auth, g2) == nil {
		t.Error("a tampered grant verified")
	}

	// Withdrawn at a higher serial: valid, and opens nothing.
	w, err := GrantLanHostWith(admin, auth, gw, "printer", nil, 8, now, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyLanGrantBy(auth, w); err != nil || w.Active(now) {
		t.Errorf("withdrawal: %v, active %v", err, w.Active(now))
	}
}

// What one grant may open is capped: a host's ports, not the whole device.
func TestALanGrantIsAHostNotADevice(t *testing.T) {
	for _, bad := range []string{"1-65535", "1-300", "1,3,5,7,9,11,13,15,17", "0", "70000", "90-80", ""} {
		if _, err := ParseLanPorts(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if rs, err := ParseLanPorts("80,81,82,1000-1010"); err != nil || len(rs) != 2 {
		t.Errorf("adjacent ports not merged: %v %v", rs, err)
	}
	for _, bad := range []string{"lan", "", "-x", "x-", "Printer", "a.b", strings.Repeat("a", 33)} {
		if CheckLanHostName(bad) == nil {
			t.Errorf("host name %q accepted", bad)
		}
	}
}
