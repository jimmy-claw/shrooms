package mobile

import "testing"

// A phone can refuse blind relays outright — including ones the mesh's admin
// names — and then let the admin choose again by clearing the list.
func TestSetBlindRelaysNoneAndBack(t *testing.T) {
	dir := twoMeshPhone(t)

	if err := SetBlindRelays(dir, "none", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := BlindRelays(dir); got != "none" {
		t.Fatalf("after none, BlindRelays = %q", got)
	}
	if got := BlindRelayToken(dir); got != "" {
		t.Errorf("a refused relay kept a token %q", got)
	}

	if err := SetBlindRelays(dir, "203.0.113.10:31760", ""); err != nil {
		t.Fatal(err)
	}
	if got := BlindRelays(dir); got != "203.0.113.10:31760" {
		t.Errorf("after naming one, BlindRelays = %q", got)
	}

	if err := SetBlindRelays(dir, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := BlindRelays(dir); got != "" {
		t.Errorf("after clearing, BlindRelays = %q; want empty, which lets the admin choose", got)
	}
}
