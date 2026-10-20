package waku

import (
	"strings"
	"testing"
)

// The source build answers with the PeerId's Nim repr; the id is the base58
// of its bytes (2026-10-20). The text form passes through unchanged.
func TestPeerIDTextReadsBothForms(t *testing.T) {
	repr := "(data: @[0, 37, 8, 2, 18, 33, 2, 93, 235, 203, 227, 211, 254, 104, 240, 28, 112, 248, 180, 49, 78, 37, 127, 188, 216, 212, 191, 94, 219, 227, 217, 143, 151, 28, 24, 204, 41, 18, 142])"
	got, err := peerIDText(repr)
	if err != nil || !strings.HasPrefix(got, "16Uiu2") || len(got) != 53 {
		t.Fatalf("peerIDText(repr) = %q, %v", got, err)
	}
	if got2, _ := peerIDText(got); got2 != got {
		t.Errorf("the text form changed: %q", got2)
	}
	if _, err := peerIDText("(data: @[1, x])"); err == nil {
		t.Error("a broken repr was read")
	}
}
