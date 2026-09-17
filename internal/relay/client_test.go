package relay

import (
	"net/netip"
	"testing"
	"time"
)

// A relay that is also a member reaches its own clients where they registered
// from, so it must be able to ask. The answer is the observed source — the
// client's tunnel socket as its NAT presents it — and it stops being given the
// moment the registration would stop being forwarded to.
func TestClientReportsWhereADeviceRegistered(t *testing.T) {
	k := testKey(t)
	s := NewServer(k, nil)
	now := time.Now()

	phone := wgKey(0xaa)
	observed := netip.MustParseAddrPort("198.51.100.20:40123")
	s.Handle(EncodeRegister(k, phone, regKey(t), now), observed, now)

	at, ok := s.Client(phone, now)
	if !ok || at != observed {
		t.Fatalf("Client = %v, %v; want %v, true", at, ok, observed)
	}
	if _, ok := s.Client(wgKey(0xbb), now); ok {
		t.Error("reported a device that never registered")
	}
	if _, ok := s.Client(phone, now.Add(RegistrationTTL+time.Second)); ok {
		t.Error("reported a registration that has expired")
	}
}
