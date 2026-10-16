package agent

// How fast a session's model writes: output tokens per second of generating.
// For Claude it hardly matters; for a local model on the mesh (K11) or a
// free one, it says what one is dealing with.
//
// Measured here, the same for every harness, from what any of them already
// sends: the pieces of its reply as they stream (stream_event), and its
// output tokens when the turn ends (result). Generating time is the time
// between pieces; a gap longer than pieceGap is not generating (a tool
// running, the next request being sent) and is left out. The wait for the
// first piece (the model reading the prompt) is left out too, so this is the
// writing speed, not the turn's.

import (
	"encoding/json"
	"time"
)

var pieceGap = 3 * time.Second

// timeDelta notes one piece of a reply as it streams.
func (s *Session) timeDelta(raw json.RawMessage) {
	var m struct {
		Event struct {
			Type string `json:"type"`
		} `json:"event"`
		ParentTool *string `json:"parent_tool_use_id"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Event.Type != "content_block_delta" || (m.ParentTool != nil && *m.ParentTool != "") {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lastPiece.IsZero() {
		if gap := now.Sub(s.lastPiece); gap < pieceGap {
			s.genTime += gap
		}
	}
	s.lastPiece = now
}

// turnRate works out the turn's rate when it ends: its output tokens over
// the time spent generating. Kept only when there is enough of both to mean
// something; the counts start again for the next turn.
func (s *Session) turnRate(raw json.RawMessage) {
	var m struct {
		Usage struct {
			Output uint64 `json:"output_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(raw, &m)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.genTime >= 500*time.Millisecond && m.Usage.Output >= 20 {
		s.tokRate = float64(m.Usage.Output) / s.genTime.Seconds()
	}
	s.genTime, s.lastPiece = 0, time.Time{}
}
