package state

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

// The admin's choice of blind relays is kept on disk per mesh, like
// revocations and for the same reasons (docs/distributing-a-blind-relay.md):
// a node that forgot it at every restart would run without a relay until a
// peer happened to repeat it, and a replayed older statement could then win.
//
// Only the signed wire bytes are stored. The caller verifies them on load, so a
// hostile or corrupt file costs nothing an unverified message would not.
type relayAdviceFile struct {
	Advice string `json:"advice"`
}

// RelayAdvice returns the stored statement for one mesh, or nil.
func (s *State) RelayAdvice(networkID string) []byte {
	raw, err := os.ReadFile(s.meshFile("relay-advice-", networkID))
	if err != nil {
		return nil
	}
	var f relayAdviceFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(f.Advice)
	if err != nil || len(b) == 0 {
		return nil
	}
	return b
}

// SetRelayAdvice replaces the stored statement for one mesh.
func (s *State) SetRelayAdvice(networkID string, raw []byte) error {
	body, err := json.MarshalIndent(relayAdviceFile{Advice: base64.StdEncoding.EncodeToString(raw)}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal relay advice: %w", err)
	}
	if err := writeFileAtomic(s.meshFile("relay-advice-", networkID), append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write relay advice: %w", err)
	}
	return nil
}
