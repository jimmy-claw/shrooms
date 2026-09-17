package control

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
)

// The largest statement the caps allow — four IPv6 relays and a full-length
// token — must still fit a control message, or the admin's advice would be
// silently unsendable exactly when it says the most.
func TestTheLargestRelayAdviceFitsAndOpens(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	id, _ := identity.New()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	now := time.Now()

	relays := []netip.AddrPort{
		netip.MustParseAddrPort("[2001:db8:ffff:ffff:ffff:ffff:ffff:1]:65535"),
		netip.MustParseAddrPort("[2001:db8:ffff:ffff:ffff:ffff:ffff:2]:65535"),
		netip.MustParseAddrPort("[2001:db8:ffff:ffff:ffff:ffff:ffff:3]:65535"),
		netip.MustParseAddrPort("[2001:db8:ffff:ffff:ffff:ffff:ffff:4]:65535"),
	}
	ad, err := cred.AdviseRelaysWith(admin, auth, uint64(now.Unix()), relays,
		strings.Repeat("t", cred.MaxAdviceToken), now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ad.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	kr := NewKeyring(nk, nil)
	msg := &Advice{Kind: KindAdvice, DevicePub: id.DevicePub, Payload: raw, Timestamp: now.Unix()}
	sealed, err := kr.Seal(3, id.DevicePriv, msg)
	if err != nil {
		t.Fatalf("the largest relay advice does not fit a control message: %v", err)
	}

	got, err := kr.OpenAdvice(3, sealed, now)
	if err != nil {
		t.Fatal(err)
	}
	back, err := cred.UnmarshalRelayAdvice(got.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := cred.VerifyRelayAdviceBy(auth, back); err != nil {
		t.Errorf("advice did not verify after riding a control message: %v", err)
	}

	// The other openers must not mistake it for theirs: the receive path tries
	// them in turn, and a grant opener that took this would hand relay advice
	// to the credential code.
	if _, err := kr.OpenGrant(3, sealed, now); err == nil {
		t.Error("OpenGrant accepted relay advice")
	}
	if _, err := kr.OpenServices(3, sealed, now); err == nil {
		t.Error("OpenServices accepted relay advice")
	}
	if _, err := kr.OpenAnnounceWindow([]int64{3}, sealed, now); err == nil {
		t.Error("OpenAnnounceWindow accepted relay advice")
	}

	// And the other way round.
	g := &Grant{Kind: KindGrant, DevicePub: id.DevicePub, Payload: raw, Timestamp: now.Unix()}
	sealedGrant, _ := kr.Seal(3, id.DevicePriv, g)
	if _, err := kr.OpenAdvice(3, sealedGrant, now); err == nil {
		t.Error("OpenAdvice accepted a grant")
	}

	// A stale message is refused like any other.
	if _, err := kr.OpenAdvice(3, sealed, now.Add(2*MaxClockSkew)); err == nil {
		t.Error("OpenAdvice accepted a message far outside the clock window")
	}
}
