package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The reply streams: text arrives as unnumbered partial events before the
// whole message, and none of it is kept — the message itself is the record.
func TestTheReplyStreamsAndIsNotKept(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	_, ch := s.Since(0)
	defer s.Unsubscribe(ch)
	s.Send("one two three", "")

	var streamed strings.Builder
	var partialSeqs []uint64
	deadline := time.After(10 * time.Second)
loop:
	for {
		select {
		case e := <-ch:
			if e.Kind == "partial" {
				var d struct{ Text string }
				json.Unmarshal(e.Data, &d)
				streamed.WriteString(d.Text)
				partialSeqs = append(partialSeqs, e.Seq)
			}
			if claudeType(e) == "result/success" {
				break loop
			}
		case <-deadline:
			t.Fatal("no result")
		}
	}
	if streamed.String() != "echo: one two three" {
		t.Errorf("streamed %q", streamed.String())
	}
	kept, _ := s.Since(0)
	for i, e := range kept {
		if e.Kind == "partial" {
			t.Fatalf("a partial was kept: %+v", e)
		}
		if e.Seq != uint64(i+1) {
			t.Errorf("partials left a gap: event %d has seq %d", i, e.Seq)
		}
	}
	for _, q := range partialSeqs {
		if q > kept[len(kept)-1].Seq {
			t.Errorf("a partial claimed seq %d beyond the last kept %d", q, kept[len(kept)-1].Seq)
		}
	}
}

// The list says how full the context is and what was said last — from live
// messages, and again after a restart, from what was kept.
func TestTheListShowsContextAndTheLastReply(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("hello", "")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })

	check := func(in Info, when string) {
		if in.ContextUsed != 1210 || in.ContextWindow != 1000000 || in.Preview != "echo: hello" ||
			in.Model != "claude-opus-5[1m]" {
			t.Errorf("%s: context %d/%d, preview %q, model %q", when, in.ContextUsed, in.ContextWindow, in.Preview, in.Model)
		}
	}
	check(s.Info(), "live")
	s.stop()
	m2 := newTestManager(t, state)
	s2, _ := m2.Get("proj")
	check(s2.Info(), "after a restart")
}

// Auto-approve is the desktop's --dangerously-skip-permissions: a new process
// is started with it and asks nothing, a prompt already waiting is allowed when
// it is switched on, and the setting survives a restart.
func TestAutoApprove(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")

	s.Send("run make test", "phone")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "control_request" })
	if err := s.SetAutoApprove(true, "phone"); err != nil {
		t.Fatal(err)
	}
	answered := waitFor(t, s, 0, func(e Event) bool { return e.Kind == "answer" })
	if answered.By != "auto-approve" {
		t.Errorf("the waiting prompt was answered by %q", answered.By)
	}
	end := waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })

	s.stop()
	stopped := waitFor(t, s, end.Seq, func(e Event) bool { return e.Kind == "stopped" })
	s.Send("run make lint", "phone")
	init := waitFor(t, s, stopped.Seq, func(e Event) bool { return claudeType(e) == "system/init" })
	var h struct {
		Skip bool `json:"skip_permissions"`
	}
	json.Unmarshal(init.Data, &h)
	if !h.Skip {
		t.Error("a new process was started without --dangerously-skip-permissions")
	}
	waitFor(t, s, init.Seq, func(e Event) bool { return claudeType(e) == "result/success" })
	got, _ := s.Since(init.Seq)
	for _, e := range got {
		if claudeType(e) == "control_request" {
			t.Error("a prompt was raised with auto-approve on")
		}
	}

	m2 := newTestManager(t, state)
	if s2, _ := m2.Get("proj"); !s2.Info().AutoApprove {
		t.Error("auto-approve did not survive a restart")
	}
}

// History comes from Claude Code's own transcript: only what was typed and
// what the model said, before a given time, newest kept when limited — read
// from the end of the file, however large it is.
func TestHistoryFromTheTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dir := filepath.Join(home, "projects", "-home-someone-proj")
	os.MkdirAll(dir, 0o700)

	at := func(min int) string { return time.Date(2026, 10, 3, 12, min, 0, 0, time.UTC).Format(time.RFC3339Nano) }
	lines := []string{
		`{"type":"user","timestamp":"` + at(0) + `","message":{"role":"user","content":"first question"}}`,
		`{"type":"assistant","timestamp":"` + at(1) + `","message":{"content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"first answer"}]}}`,
		`{"type":"assistant","timestamp":"` + at(2) + `","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}]}}`,
		`{"type":"user","timestamp":"` + at(3) + `","message":{"content":[{"type":"tool_result","content":"output"}]}}`,
		`{"type":"user","timestamp":"` + at(4) + `","message":{"content":"<system-reminder>not typed</system-reminder>"}}`,
		`{"type":"user","isMeta":true,"timestamp":"` + at(5) + `","message":{"content":"meta"}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"` + at(6) + `","message":{"content":[{"type":"text","text":"a subagent"}]}}`,
		`{"type":"user","timestamp":"` + at(7) + `","message":{"content":"second question"}}`,
		`{"type":"assistant","timestamp":"` + at(8) + `","message":{"content":[{"type":"text","text":"second answer"}]}}`,
	}
	// Padding ahead of them, so the reader has to start from the end.
	pad := strings.Repeat(`{"type":"summary","summary":"`+strings.Repeat("x", 1000)+`"}`+"\n", (historyTail/1000)+50)
	os.WriteFile(filepath.Join(dir, "conv-1.jsonl"), []byte(pad+strings.Join(lines, "\n")+"\n"), 0o600)

	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.convID = "conv-1"

	all, err := s.History(time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range all {
		got = append(got, h.Role+":"+h.Text)
	}
	want := "user:first question|assistant:first answer|user:second question|assistant:second answer"
	if strings.Join(got, "|") != want {
		t.Errorf("history %v\nwant %s", got, want)
	}
	before, _ := time.Parse(time.RFC3339, at(7))
	if h, _ := s.History(before, 1); len(h) != 1 || h[0].Text != "first answer" {
		t.Errorf("before %s, limit 1: %+v", at(7), h)
	}
}

// Switched on while a process runs that was started without it: that process
// still asks, and the session answers for you.
func TestAutoApproveAnswersARunningProcess(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("hello", "")
	first := waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
	s.SetAutoApprove(true, "phone")

	s.Send("run make test", "phone")
	end := waitFor(t, s, first.Seq, func(e Event) bool { return claudeType(e) == "result/success" })
	got, _ := s.Since(first.Seq)
	var asked, auto bool
	for _, e := range got {
		asked = asked || claudeType(e) == "control_request"
		auto = auto || (e.Kind == "answer" && e.By == "auto-approve")
	}
	if !asked || !auto {
		t.Errorf("asked %v, auto-answered %v (turn ended at %d)", asked, auto, end.Seq)
	}
}

// A file from the phone lands under the agent's own directory whatever its
// name says, keeps a recognisable name, and its path comes back to be quoted
// in the next message.
func TestUploadsLandWhereTheAgentDecides(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Create("proj", t.TempDir())
	srv := localServer(Handler(slog.New(slog.DiscardHandler), m, nil))
	t.Cleanup(srv.Close)

	for name, wantBase := range map[string]string{
		"screenshot 1.png":       "screenshot_1.png",
		"../../../.bashrc":       "bashrc",
		"":                       "file",
		"notes/../../etc/passwd": "passwd",
	} {
		resp, err := http.Post(srv.URL+"/v1/sessions/proj/files?name="+url.QueryEscape(name), "application/octet-stream",
			strings.NewReader("contents"))
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ Path string }
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%q: %s", name, resp.Status)
		}
		dir := filepath.Join(state, "uploads", "proj")
		if filepath.Dir(out.Path) != dir || !strings.HasSuffix(out.Path, "-"+wantBase) {
			t.Errorf("%q landed at %s, want %s/<time>-%s", name, out.Path, dir, wantBase)
		}
		if b, _ := os.ReadFile(out.Path); string(b) != "contents" {
			t.Errorf("%q: kept %q", name, b)
		}
	}
	// The same name twice in one second: two files, not one overwritten.
	var paths []string
	for i := 0; i < 2; i++ {
		resp, _ := http.Post(srv.URL+"/v1/sessions/proj/files?name=same.txt", "text/plain", strings.NewReader(fmt.Sprint(i)))
		var out struct{ Path string }
		json.NewDecoder(resp.Body).Decode(&out)
		paths = append(paths, out.Path)
	}
	if paths[0] == paths[1] || paths[0] == "" {
		t.Errorf("same name twice: %v", paths)
	}
	resp, _ := http.Post(srv.URL+"/v1/sessions/proj/files?name=big", "application/octet-stream",
		io.LimitReader(zeros{}, MaxUpload+1))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an oversized upload: %s", resp.Status)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { return len(p), nil }
