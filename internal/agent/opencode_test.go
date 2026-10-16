package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeOpenCodeBin(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_OPENCODE", "1")
	return self
}

func newOpenCodeSession(t *testing.T) (*Manager, *Session) {
	t.Helper()
	m := newTestManager(t, t.TempDir())
	m.Register(OpenCode{Model: "opencode/deepseek-v4.1-flash-free"}, fakeOpenCodeBin(t))
	if _, err := m.CreateWith("oc-proj", t.TempDir(), "opencode"); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("oc-proj")
	return m, s
}

// runTurn sends a message and collects the events until the turn ends.
func runTurn(t *testing.T, s *Session, text string) ([]Event, string) {
	t.Helper()
	var partial strings.Builder
	_, ch := s.Since(0)
	defer s.Unsubscribe(ch)
	if err := s.Send(text, "nothing"); err != nil {
		t.Fatal(err)
	}
	var got []Event
	deadline := time.After(15 * time.Second)
	for done := false; !done; {
		select {
		case e := <-ch:
			if e.Kind == "partial" {
				var d struct{ Text string }
				json.Unmarshal(e.Data, &d)
				partial.WriteString(d.Text)
				continue
			}
			got = append(got, e)
			done = isResult(e)
		case <-deadline:
			t.Fatalf("no end of turn; events %v", got)
		}
	}
	return got, partial.String()
}

func kindsOf(got []Event) string {
	var k []string
	for _, e := range got {
		k = append(k, e.Kind+":"+claudeType(e))
	}
	return strings.Join(k, " ")
}

// An OpenCode turn reaches the apps in the same shape a Claude Code one does:
// the session's identity, the tool call and its output, the reply, and the end
// of the turn with its usage — so nothing in the apps knows it is OpenCode.
func TestAnOpenCodeSessionRoundTrips(t *testing.T) {
	_, s := newOpenCodeSession(t)
	got, partial := runTurn(t, s, "run make test")

	want := "message: claude:system/init claude:assistant claude:user claude:assistant claude:result/success"
	if kindsOf(got) != want {
		t.Errorf("events\n got %s\nwant %s", kindsOf(got), want)
	}

	var init struct {
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
		Harness   string `json:"harness"`
	}
	json.Unmarshal(got[1].Data, &init)
	if init.Harness != "opencode" || init.SessionID == "" {
		t.Errorf("the session's identity: %s", got[1].Data)
	}
	if init.Model != "opencode/deepseek-v4.1-flash-free" {
		t.Errorf("the model the session was told to use: %q", init.Model)
	}

	var tool struct {
		Message struct {
			Content []struct {
				Type, Name string
			}
		}
	}
	json.Unmarshal(got[2].Data, &tool)
	if c := tool.Message.Content; len(c) != 1 || c[0].Type != "tool_use" || c[0].Name != "read" {
		t.Errorf("the tool call: %s", got[2].Data)
	}
	if !strings.Contains(string(got[3].Data), `"tool_result"`) ||
		!strings.Contains(string(got[3].Data), "the file") {
		t.Errorf("the tool's output: %s", got[3].Data)
	}
	if assistantText(got[4]) != "done" || partial != "done" {
		t.Errorf("the reply %q, streamed as %q", assistantText(got[4]), partial)
	}
	if !strings.Contains(string(got[5].Data), `"output_tokens":20`) ||
		!strings.Contains(string(got[5].Data), `"input_tokens":100`) {
		t.Errorf("the turn's usage: %s", got[5].Data)
	}

	in := s.Info()
	if in.Harness != "opencode" || in.Caps.Approve || in.State != Idle {
		t.Errorf("info %+v", in)
	}
	if s.convID == "" || s.convID != init.SessionID {
		t.Errorf("the conversation id to resume by: %q, the session's: %q", s.convID, init.SessionID)
	}
}

// The reply must arrive in pieces, not as one lump: that is the whole reason
// this harness speaks ACP rather than `run --format json`, where a
// twenty-word answer was measured arriving as a single event.
func TestAnOpenCodeReplyStreamsInChunks(t *testing.T) {
	_, s := newOpenCodeSession(t)
	_, partial := runTurn(t, s, "hello")
	if partial != "echo: hello" {
		t.Errorf("the streamed reply: %q", partial)
	}
}

// A turn sent before the ACP session exists must not be dropped: the session
// id arrives a round trip after the process starts, and a device sends as
// soon as it is told the session is up.
func TestATurnSentBeforeTheSessionExistsIsKept(t *testing.T) {
	c := OpenCode{}.Codec(StartOptions{Note: "NOTE"}).(*openCodec)

	// Sent immediately: nothing to write yet, but it must not be lost.
	if out := c.Turn("are you there?"); len(out) != 0 {
		t.Errorf("a turn before the session exists wrote %v", out)
	}
	if len(c.pending) != 1 {
		t.Fatalf("the turn was not kept: %v", c.pending)
	}

	// initialize → session/new → the session exists, and the turn goes.
	_, _ = c.Decode(rawJSON(ack(c.Start()[0], map[string]any{"protocolVersion": 1})))
	opened := c.openID
	msgs, write := c.Decode(rawJSON(ackID(opened, map[string]any{
		"sessionId":     "ses_test",
		"configOptions": []any{map[string]any{"id": "model", "currentValue": "m"}}})))

	// The turn goes out as a WRITE, with the model — not as an event.
	var sawPrompt bool
	for _, w := range write {
		if strings.Contains(mustJSON(w), "session/prompt") &&
			strings.Contains(mustJSON(w), "are you there?") {
			sawPrompt = true
		}
	}
	if !sawPrompt {
		t.Fatalf("the queued turn never went; writes: %s", mustJSON(write))
	}
	if len(msgs) == 0 || !strings.Contains(string(msgs[0]), "system") {
		t.Errorf("the session's identity should come with it: %s", joinRaw(msgs))
	}
	if len(c.pending) != 0 {
		t.Errorf("the queue was not drained: %v", c.pending)
	}
}

// The session's own instructions go on the first prompt only, as a block of
// their own, so the person's text stays exactly what they typed — and so the
// note is not repeated on every turn of a long conversation.
func TestTheNoteGoesOnTheFirstPromptOnly(t *testing.T) {
	c := OpenCode{}.Codec(StartOptions{Note: "YOU ARE A SHROOMS AGENT"}).(*openCodec)
	c.sessionID, c.ready = "ses_test", true

	first := c.prompt("one")
	if !strings.Contains(mustJSON(first), "YOU ARE A SHROOMS AGENT") {
		t.Errorf("the note is missing from the first prompt: %s", mustJSON(first))
	}
	second := c.prompt("two")
	if strings.Contains(mustJSON(second), "YOU ARE A SHROOMS AGENT") {
		t.Errorf("the note was sent again: %s", mustJSON(second))
	}
	if !strings.Contains(mustJSON(second), "two") {
		t.Errorf("the second turn: %s", mustJSON(second))
	}
}

// The mesh's agents as tools, in the shape ACP asks for, and only when this
// agent has an MCP command to give.
func TestTheMeshToolsAreOfferedOverACP(t *testing.T) {
	with := OpenCode{}.Codec(StartOptions{MCP: "/usr/bin/shrooms-agent", MCPArgs: []string{"-x"}}).(*openCodec)
	got := mustJSON(with.open()[0])
	if !strings.Contains(got, `"name":"shrooms"`) ||
		!strings.Contains(got, `"/usr/bin/shrooms-agent"`) ||
		!strings.Contains(got, `"mcp","-x"`) {
		t.Errorf("the MCP server: %s", got)
	}
	// OpenCode validates this entry with a discriminated union and refuses it
	// outright without "type" — measured against 1.19.2, where session/new
	// answered -32602 and no session was ever opened. "env" it wants as a
	// list. Sending an entry the real one rejects is not "offering the tools".
	if !strings.Contains(got, `"type":"stdio"`) || !strings.Contains(got, `"env":[]`) {
		t.Errorf("the entry must carry the discriminator and env, or it is refused: %s", got)
	}
	without := OpenCode{}.Codec(StartOptions{}).(*openCodec)
	if strings.Contains(mustJSON(without.open()[0]), "shrooms") {
		t.Errorf("a server was offered with nothing to run: %s", mustJSON(without.open()[0]))
	}
}

// A conversation this machine no longer has must not leave the session unable
// to take a message: the load fails, and a new conversation is opened.
func TestAFailedResumeOpensANewConversation(t *testing.T) {
	c := OpenCode{}.Codec(StartOptions{Resume: "ses_gone"}).(*openCodec)
	_, _ = c.Decode(rawJSON(ack(c.Start()[0], map[string]any{})))
	if got := mustJSON(c.open()[0]); !strings.Contains(got, "session/load") ||
		!strings.Contains(got, "ses_gone") {
		t.Fatalf("a session with an id to resume should load it: %s", got)
	}
	loadID := c.openID
	_, write := c.Decode(rawJSON(map[string]any{"jsonrpc": "2.0", "id": loadID,
		"error": map[string]any{"code": -32001, "message": "no such session"}}))
	var opened string
	for _, w := range write {
		var r struct{ Method string }
		json.Unmarshal(rawJSON(w), &r)
		if r.Method == acpNewSession {
			opened = r.Method
		}
	}
	if opened != acpNewSession {
		t.Fatalf("a failed load must fall back to a new session, got: %s", mustJSON(write))
	}
}

// A session/new that is refused must fail the turn — and must NOT be asked
// again. The fallback a failed LOAD gets exists because a conversation can be
// gone from a machine; a session/new that fails fails the same way every time,
// so asking again is a spin, not a retry. This was a real bug, found by
// running it: a malformed mcpServers entry made opencode 1.19.2 answer -32602,
// and the session asked again forever at 127% CPU with nothing said to anyone.
func TestARefusedNewSessionFailsTheTurnAndIsNotRetried(t *testing.T) {
	count := filepath.Join(t.TempDir(), "new-requests")
	t.Setenv("FAKE_OPENCODE_NO_NEW", "1")
	t.Setenv("FAKE_OPENCODE_NEW_PARAMS", count)
	_, s := newOpenCodeSession(t)
	got, _ := runTurn(t, s, "hello")

	last := got[len(got)-1]
	if !strings.Contains(string(last.Data), "error_during_execution") {
		t.Errorf("a refused session/new ended as: %s", last.Data)
	}
	all := make([]json.RawMessage, 0, len(got))
	for _, e := range got {
		all = append(all, e.Data)
	}
	if !strings.Contains(joinRaw(all), "would not open a session") {
		t.Errorf("the reason was not shown: %s", joinRaw(all))
	}
	b, err := os.ReadFile(count)
	if err != nil {
		t.Fatalf("the fake never saw a request: %v", err)
	}
	if n := strings.Count(string(b), "session/new"); n != 1 {
		t.Errorf("session/new was asked %d time(s), want 1 — asking again is a spin", n)
	}
}

// A turn that fails is reported as a failed turn, not as a quiet success.
func TestAnOpenCodeFailureIsAFailedTurn(t *testing.T) {
	_, s := newOpenCodeSession(t)
	got, _ := runTurn(t, s, "fail")
	last := got[len(got)-1]
	if !strings.Contains(string(last.Data), "error_during_execution") {
		t.Errorf("a failed turn ended as: %s", last.Data)
	}
	if !strings.Contains(joinRaw([]json.RawMessage{got[2].Data}), "model is on fire") {
		t.Errorf("the reason was not shown: %s", got[2].Data)
	}
}

// Interrupting writes ACP's cancel for the session, and nothing before there
// is a session to cancel.
func TestInterruptCancelsTheSession(t *testing.T) {
	c := OpenCode{}.Codec(StartOptions{}).(*openCodec)
	if got := c.Interrupt(); got != nil {
		t.Errorf("interrupt before the session exists wrote %v", got)
	}
	c.sessionID = "ses_test"
	got := c.Interrupt()
	if len(got) != 1 || !strings.Contains(mustJSON(got[0]), "session/cancel") ||
		!strings.Contains(mustJSON(got[0]), "ses_test") {
		t.Errorf("interrupt wrote %v", got)
	}
}

// The two facts about OpenCode that the apps act on, pinned where they cannot
// drift: it is started bare (everything else goes over the protocol), and it
// raises no permission request, so auto-approve must not be offered.
//
// Measured, not assumed: a write went through with no prompt both through
// `run` without --auto and over ACP.
func TestOpenCodeIsStartedBareAndCannotApprove(t *testing.T) {
	if args := (OpenCode{}).Args(StartOptions{Resume: "x", AutoApprove: true, Note: "n"}); len(args) != 1 || args[0] != "acp" {
		t.Errorf("Args = %v, want just [acp]", args)
	}
	if (OpenCode{}).Caps().Approve {
		t.Error("Approve must be false: OpenCode never asks, so auto-approve means nothing")
	}
	if (OpenCode{}).Name() != "opencode" || (OpenCode{}).Bin() != "opencode" {
		t.Errorf("name/bin: %q %q", (OpenCode{}).Name(), (OpenCode{}).Bin())
	}
}

// --- helpers ----------------------------------------------------------------

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func joinRaw(ms []json.RawMessage) string {
	var b strings.Builder
	for _, m := range ms {
		b.Write(m)
		b.WriteString("\n")
	}
	return b.String()
}

// ack turns one of our own requests into the reply to it, keeping its id.
func ack(req any, result map[string]any) map[string]any {
	m, _ := json.Marshal(req)
	var r struct {
		ID int `json:"id"`
	}
	json.Unmarshal(m, &r)
	return ackID(r.ID, result)
}

func ackID(id int, result map[string]any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}
