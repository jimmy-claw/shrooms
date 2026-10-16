package agent

import (
	"encoding/json"
	"testing"
	"time"
)

// The rate is output tokens over the time between pieces of the reply; a
// pause (a tool running) is left out, and a turn too short to say anything
// leaves the last rate as it was.
func TestTheModelsSpeedIsItsWritingTime(t *testing.T) {
	pieceGap = 200 * time.Millisecond
	t.Cleanup(func() { pieceGap = 3 * time.Second })
	s := &Session{}
	piece := json.RawMessage(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}}`)
	for i := 0; i < 6; i++ { // five gaps of ~120 ms: ~0.6 s writing
		s.timeDelta(piece)
		time.Sleep(120 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond) // a tool runs: not writing
	s.timeDelta(piece)
	s.turnRate(json.RawMessage(`{"type":"result","usage":{"output_tokens":60}}`))
	if r := s.tokRate; r < 70 || r > 115 {
		t.Fatalf("60 tokens in ~0.6 s of writing gave %.1f tokens a second", r)
	}
	kept := s.tokRate
	s.timeDelta(piece)
	s.turnRate(json.RawMessage(`{"type":"result","usage":{"output_tokens":3}}`))
	if s.tokRate != kept {
		t.Errorf("a turn of 3 tokens changed the rate to %.1f", s.tokRate)
	}
	sub := json.RawMessage(`{"type":"stream_event","parent_tool_use_id":"t1","event":{"type":"content_block_delta"}}`)
	s.timeDelta(sub)
	if !s.lastPiece.IsZero() {
		t.Error("a subagent's pieces are timed as this session's")
	}
}
