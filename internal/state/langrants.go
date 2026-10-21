package state

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

// The LAN host grants a node holds (ADR-068), kept so a restart does not wait
// for the next repetition to know which hosts exist. Stored as received and
// verified again on load, as relay advice is: the file is the same shape as a
// message from a peer.

type lanGrantsFile struct {
	Grants []string `json:"grants"`
}

// LanGrants returns the stored grants' wire forms.
func (s *State) LanGrants(networkID string) [][]byte {
	raw, err := os.ReadFile(s.meshFile("lan-grants-", networkID))
	if err != nil {
		return nil
	}
	var f lanGrantsFile
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	var out [][]byte
	for _, g := range f.Grants {
		if b, err := base64.StdEncoding.DecodeString(g); err == nil && len(b) > 0 {
			out = append(out, b)
		}
	}
	return out
}

// SetLanGrants replaces the stored grants.
func (s *State) SetLanGrants(networkID string, grants [][]byte) error {
	f := lanGrantsFile{Grants: make([]string, 0, len(grants))}
	for _, g := range grants {
		f.Grants = append(f.Grants, base64.StdEncoding.EncodeToString(g))
	}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal LAN host grants: %w", err)
	}
	if err := writeFileAtomic(s.meshFile("lan-grants-", networkID), append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write LAN host grants: %w", err)
	}
	return nil
}
