package main

import (
	"strings"
	"testing"
)

func TestParseAdvisedRelays(t *testing.T) {
	got, err := parseAdvisedRelays("203.0.113.10:31760, 198.51.100.7:32100")
	if err != nil || len(got) != 2 || got[1].String() != "198.51.100.7:32100" {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"", "203.0.113.10", "relay.example:1",
		"1.1.1.1:1,1.1.1.2:1,1.1.1.3:1,1.1.1.4:1,1.1.1.5:1"} {
		if _, err := parseAdvisedRelays(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := parseAdvisedRelays(""); err == nil || !strings.Contains(err.Error(), "clear") {
		t.Errorf("an empty list should point at `clear`, got %v", err)
	}
}

// With no daemon to ask, the serial is the time; that is what orders statements
// signed on different machines.
func TestNextAdviceSerialWithoutADaemon(t *testing.T) {
	if got := nextAdviceSerial(t.TempDir()+"/none.sock", "", 1789640000); got != 1789640000 {
		t.Errorf("got %d", got)
	}
}
