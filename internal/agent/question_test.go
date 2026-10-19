package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A question from the model waits for the person even with auto-approve on —
// auto-approve answering it is what made one read to the model as "the user
// did not answer" (2026-10-03) — and the answer given reaches the model.
func TestAQuestionWaitsForItsAnswerEvenWithAutoApprove(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	if err := s.SetAutoApprove(true, ""); err != nil {
		t.Fatal(err)
	}
	s.Send("ask Deploy now?", "nothing")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "control_request" })
	time.Sleep(200 * time.Millisecond)
	if in := s.Info(); in.State != Waiting || in.Pending != 1 {
		t.Fatalf("the question did not wait: %+v", in)
	}
	// Switching auto-approve on again (it allows what is waiting) leaves it too.
	s.SetAutoApprove(true, "")
	if in := s.Info(); in.Pending != 1 {
		t.Fatalf("auto-approve answered the question: %+v", in)
	}

	srv := localServer(Handler(slog.New(slog.DiscardHandler), m, nil))
	t.Cleanup(srv.Close)
	post := func(body any) int {
		b, _ := json.Marshal(body)
		r, err := http.Post(srv.URL+"/v1/sessions/proj/prompts/req-q", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if code := post(map[string]any{"allow": true}); code != http.StatusConflict {
		t.Errorf("allowed without answers: %d", code)
	}
	if code := post(map[string]any{"allow": true, "answers": map[string]string{"Deploy now?": "yes"}}); code != http.StatusNoContent {
		t.Fatalf("answering: %d", code)
	}
	got := waitFor(t, s, 0, func(e Event) bool {
		t := assistantText(e)
		return t == "answered: yes" || t == "The user did not answer the questions."
	})
	if assistantText(got) != "answered: yes" {
		t.Errorf("the model got %q", assistantText(got))
	}
	ans := waitFor(t, s, 0, func(e Event) bool { return e.Kind == "answer" })
	var d struct {
		Answers map[string]string
	}
	json.Unmarshal(ans.Data, &d)
	if ans.By == "auto-approve" || d.Answers["Deploy now?"] != "yes" {
		t.Errorf("the answer event: by %q, %s", ans.By, ans.Data)
	}
}

// Declining a question works as declining any prompt.
func TestAQuestionCanBeDeclined(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("ask Deploy now?", "")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "control_request" })
	time.Sleep(100 * time.Millisecond)
	if err := s.Answer("req-q", false, "not tonight", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return assistantText(e) == "denied: not tonight" })
}

// Background work: the turn ends, heartbeats and thinking keep coming, and
// Claude Code resumes by itself. The session is working again while it does,
// and the turns counted are the two that ended — not the events in between,
// which is what notified a phone every fifteen seconds (2026-10-04).
func TestATurnClaudeResumesByItselfIsWorkAndOneTurn(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "go-on")
	t.Setenv("FAKE_HOLD", hold)
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("background", "")
	waitFor(t, s, 0, func(e Event) bool { return assistantText(e) == "started it in the background" })
	waitFor(t, s, 0, func(e Event) bool {
		var d struct {
			Message struct{ Content []struct{ Type, Name string } }
		}
		json.Unmarshal(e.Data, &d)
		return len(d.Message.Content) > 0 && d.Message.Content[0].Name == "Bash"
	})
	time.Sleep(100 * time.Millisecond)
	if in := s.Info(); in.State != Working || in.Turns != 1 {
		t.Errorf("resumed by itself: %s after %d turns; want working after 1", in.State, in.Turns)
	}
	os.WriteFile(hold, nil, 0o600)
	waitFor(t, s, 0, func(e Event) bool { return assistantText(e) == "the build passed" })
	time.Sleep(100 * time.Millisecond)
	if in := s.Info(); in.State != Idle || in.Turns != 2 {
		t.Errorf("after: %s, %d turns", in.State, in.Turns)
	}
	// Counted from the log too, so a restarted agent does not renotify.
	m2, _ := NewManager(t.Context(), slog.New(slog.DiscardHandler), m.dir, "claude")
	if s2, _ := m2.Get("proj"); s2.Info().Turns != 2 {
		t.Errorf("turns after a restart: %d", s2.Info().Turns)
	}
}
