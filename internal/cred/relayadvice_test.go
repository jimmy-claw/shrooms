package cred

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func adviceRelays() []netip.AddrPort {
	return []netip.AddrPort{
		netip.MustParseAddrPort("222.167.212.15:31760"),
		netip.MustParseAddrPort("[2001:db8::7]:32100"),
	}
}

func TestRelayAdviceRoundTripsAndVerifies(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)

	a, err := AdviseRelaysWith(admin, auth, 1789640000, adviceRelays(), "tok-abc", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalRelayAdvice(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Serial != 1789640000 || back.Token != "tok-abc" || len(back.Relays) != 2 ||
		back.Relays[0] != adviceRelays()[0] || back.Relays[1] != adviceRelays()[1] {
		t.Errorf("round trip lost fields: %+v", back)
	}
	// An IPv4 relay must come back as IPv4, or it would never compare equal to
	// the same relay written in a config.
	if !back.Relays[0].Addr().Is4() {
		t.Errorf("IPv4 relay came back as %s", back.Relays[0])
	}
	if err := VerifyRelayAdviceBy(auth, back); err != nil {
		t.Errorf("good advice did not verify: %v", err)
	}
}

// An empty list is how an admin withdraws the advice, so it must be signable
// and readable like any other statement.
func TestEmptyRelayAdviceIsAStatement(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	a, err := AdviseRelaysWith(admin, auth, 2, nil, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := a.MarshalBinary()
	back, err := UnmarshalRelayAdvice(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Relays) != 0 || VerifyRelayAdviceBy(auth, back) != nil {
		t.Errorf("empty advice did not survive: %+v", back)
	}
}

func TestRelayAdviceFromAnotherMeshIsRefused(t *testing.T) {
	ours, _ := NewAdmin()
	theirs, _ := NewAdmin()
	ourAuth, _ := NewAuthority(ours.Pub)
	theirAuth, _ := NewAuthority(theirs.Pub)

	a, err := AdviseRelaysWith(theirs, theirAuth, 5, adviceRelays(), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRelayAdviceBy(ourAuth, a); err != ErrWrongMesh {
		t.Errorf("advice for another mesh: got %v, want ErrWrongMesh", err)
	}
	// Same mesh id, wrong signer: a member forging advice for our mesh.
	a.MeshID = ourAuth.ID()
	d, _ := a.Digest()
	a.Sig, _ = theirs.SignDigest(d)
	if err := VerifyRelayAdviceBy(ourAuth, a); err != ErrBadSignature {
		t.Errorf("forged advice: got %v, want ErrBadSignature", err)
	}
}

// Swapping the relay after signing is the attack that matters: a member
// redirecting everyone's traffic to a relay of its choosing.
func TestTamperedRelayAdviceIsRefused(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	a, _ := AdviseRelaysWith(admin, auth, 5, adviceRelays(), "tok", time.Now())
	raw, _ := a.MarshalBinary()

	// The first relay's last address byte.
	at := adviceHead + 15
	raw[at] ^= 0xff
	back, err := UnmarshalRelayAdvice(raw)
	if err != nil {
		t.Fatalf("tampered bytes should still parse, got %v", err)
	}
	if VerifyRelayAdviceBy(auth, back) == nil {
		t.Error("advice with a swapped relay verified")
	}
}

func TestRelayAdviceLimits(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	five := append(adviceRelays(), adviceRelays()...)
	five = append(five, adviceRelays()[0])
	if _, err := AdviseRelaysWith(admin, auth, 1, five, "", time.Now()); err == nil {
		t.Error("five relays were accepted")
	}
	if _, err := AdviseRelaysWith(admin, auth, 1, nil, strings.Repeat("x", MaxAdviceToken+1), time.Now()); err == nil {
		t.Error("an oversized token was accepted")
	}
	if _, err := AdviseRelaysWith(admin, auth, 0, adviceRelays(), "", time.Now()); err == nil {
		t.Error("serial zero was accepted")
	}
	bad := []netip.AddrPort{netip.MustParseAddrPort("203.0.113.1:0")}
	if _, err := AdviseRelaysWith(admin, auth, 1, bad, "", time.Now()); err == nil {
		t.Error("port zero was accepted")
	}
}

// Every length is checked before use: these bytes come from any member.
func TestMalformedRelayAdviceIsRefusedNotPanicked(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	a, _ := AdviseRelaysWith(admin, auth, 5, adviceRelays(), "tok", time.Now())
	raw, _ := a.MarshalBinary()

	for n := 0; n < len(raw); n++ {
		if _, err := UnmarshalRelayAdvice(raw[:n]); err == nil {
			t.Fatalf("a %d-byte prefix parsed", n)
		}
	}
	long := append(append([]byte(nil), raw...), 0)
	if _, err := UnmarshalRelayAdvice(long); err == nil {
		t.Error("trailing bytes were accepted")
	}
	count := append([]byte(nil), raw...)
	count[adviceHead-1] = 200
	if _, err := UnmarshalRelayAdvice(count); err == nil {
		t.Error("a relay count over the cap was accepted")
	}
	ver := append([]byte(nil), raw...)
	ver[0] = 9
	if _, err := UnmarshalRelayAdvice(ver); err == nil {
		t.Error("an unknown version was accepted")
	}
}
