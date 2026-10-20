package mesh

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
)

func credentialled(t *testing.T, admin *cred.Admin, auth *cred.Authority, id *identity.Identity, wg []byte, name string, now time.Time) []byte {
	t.Helper()
	c, err := admin.Issue(id.DevicePub, wg, name, 1, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.MeshID = auth.ID()
	d, _ := c.Digest()
	c.Sig = signWith(admin, d)
	raw, _ := c.MarshalBinary()
	return raw
}

// A remembered peer must not bring back a copy of this device's own tunnel
// key: the live announce is refused, and a roster written before that was
// held it (the auditor's lab, 2026-10-20; its test, from 07-ci-f03.md).
func TestATunnelKeyDoesNotComeBackFromMemory(t *testing.T) {
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	now := time.Now()

	first := rememberingMesh(t, dir, auth, nk)
	selfWG := first.st.Identity.WGPub
	thief, _ := identity.New()
	a := newAnnounce(t, thief, "thief", nil, 1)
	a.WGPub = append([]byte(nil), selfWG[:]...)
	a.Credential = credentialled(t, admin, auth, thief, selfWG[:], "thief", now)
	if err := first.checkMembership(a, now); err != nil {
		t.Fatal(err)
	}
	first.roster.Apply(a, now)
	first.saveRememberedPeers()

	second := rememberingMesh(t, dir, auth, nk)
	second.loadRememberedPeers(now)
	for _, p := range second.roster.Peers() {
		if p.ID() == hex.EncodeToString(thief.DevicePub) {
			t.Fatalf("remembered a copy of our own tunnel key: %s", p.Name)
		}
	}
}

// Two remembered devices with one key: the one that claimed it first keeps
// it, whichever the file lists first.
func TestOfTwoRememberedHoldersTheFirstClaimKeepsTheKey(t *testing.T) {
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	now := time.Now()

	first := rememberingMesh(t, dir, auth, nk)
	holder, _ := identity.New()
	thief, _ := identity.New()
	// Written as an older version would have: both in the roster.
	for _, x := range []struct {
		id   *identity.Identity
		name string
		at   time.Time
	}{{thief, "thief", now}, {holder, "lab-b", now.Add(-time.Hour)}} {
		a := newAnnounce(t, x.id, x.name, nil, 1)
		a.WGPub = append([]byte(nil), holder.WGPub[:]...)
		a.Credential = credentialled(t, admin, auth, x.id, holder.WGPub[:], x.name, now)
		if err := first.checkMembership(a, now); err != nil {
			t.Fatal(err)
		}
		first.roster.peers[hex.EncodeToString(x.id.DevicePub)] = PeerInfo{DevicePub: x.id.DevicePub, WGPub: holder.WGPub,
			Name: x.name, LastSeen: x.at, Since: x.at, Seq: 1}
	}
	first.saveRememberedPeers()

	second := rememberingMesh(t, dir, auth, nk)
	second.loadRememberedPeers(now)
	var got []string
	for _, p := range second.roster.Peers() {
		got = append(got, p.Name)
	}
	if len(got) != 1 || got[0] != "lab-b" {
		t.Fatalf("remembered %v, want only the first holder, lab-b", got)
	}
}
