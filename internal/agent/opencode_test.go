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
	_ = c.Decode(rawJSON(ack(c.Start()[0], map[string]any{"protocolVersion": 1})))
	opened := c.openID
	msgs, write := decode(c, rawJSON(ackID(opened, map[string]any{
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
	with := OpenCode{}.Codec(StartOptions{MCP: "/usr/bin/shrooms-agent", MCPArgs: []string{"-x"},
		Dir: "/tmp/a-project"}).(*openCodec)
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
	// ACP specifies an absolute path, so the session's directory goes in by
	// name rather than ".".
	if !strings.Contains(got, `"cwd":"/tmp/a-project"`) {
		t.Errorf("the session's directory, absolute, is the cwd: %s", got)
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
	_ = c.Decode(rawJSON(ack(c.Start()[0], map[string]any{})))
	if got := mustJSON(c.open()[0]); !strings.Contains(got, "session/load") ||
		!strings.Contains(got, "ses_gone") {
		t.Fatalf("a session with an id to resume should load it: %s", got)
	}
	loadID := c.openID
	_, write := decode(c, rawJSON(map[string]any{"jsonrpc": "2.0", "id": loadID,
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

// An interrupt while a turn is still waiting for the session must not be a
// button that does nothing. There is nothing at ACP to cancel yet, so the
// queued turn is dropped — and the turn is ENDED, because a turn the codec
// drops without a result leaves the session reading as working for ever.
func TestAnInterruptBeforeTheSessionExistsEndsTheWaitingTurn(t *testing.T) {
	c := OpenCode{}.Codec(StartOptions{}).(*openCodec)
	if got := c.Turn("are you there?"); len(got) != 0 {
		t.Fatalf("a turn before the session exists must wait: %s", mustJSON(got))
	}
	if got := c.Interrupt(); len(got) != 0 {
		t.Errorf("nothing to cancel at ACP yet, so nothing to write: %s", mustJSON(got))
	}
	_ = c.Decode(rawJSON(ack(c.Start()[0], map[string]any{})))
	msgs, write := decode(c, rawJSON(ackID(c.openID, map[string]any{
		"sessionId": "ses_x", "configOptions": []any{}})))

	for _, w := range write {
		if strings.Contains(mustJSON(w), "session/prompt") {
			t.Errorf("a cancelled turn was sent anyway: %s", mustJSON(w))
		}
	}
	if !strings.Contains(joinRaw(msgs), "error_during_execution") ||
		!strings.Contains(joinRaw(msgs), "cancelled before the session was open") {
		t.Errorf("the cancelled turn was not ended, or not said: %s", joinRaw(msgs))
	}
}

// A resumed conversation keeps the id we asked to resume. session/load
// answers configOptions and NO sessionId (measured against 1.19.2), so reading
// the id from the response alone left it empty and every prompt after a
// restart went out as "session not found" — which is what a model change does,
// since it restarts the process.
func TestAResumeKeepsTheConversationIdItAskedFor(t *testing.T) {
	c := OpenCode{}.Codec(StartOptions{Resume: "ses_old"}).(*openCodec)
	_ = c.Decode(rawJSON(ack(c.Start()[0], map[string]any{})))

	// The load's answer, exactly as opencode gives it: no sessionId.
	_, _ = decode(c, rawJSON(ackID(c.openID, map[string]any{
		"configOptions": []any{map[string]any{"id": "model", "currentValue": "m"}}})))

	got := mustJSON(c.Turn("hello"))
	if !strings.Contains(got, "ses_old") {
		t.Errorf("a turn after a resume must name the conversation resumed: %s", got)
	}
}

// OpenCode cannot be caged yet: the image has no opencode and there is no
// private home or gateway for it, so a cage would be made and then fail at
// exec. Refused where it is asked for, and refused before the podman check, so
// the answer does not depend on this machine having podman.
func TestAnOpenCodeSessionIsRefusedACage(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Register(OpenCode{}, fakeOpenCodeBin(t))

	_, err := m.CreateCaged("boxed", t.TempDir(), "opencode", &Cage{})
	if err == nil || !strings.Contains(err.Error(), "OpenCode is not caged yet (ADR-061)") {
		t.Errorf("caging an OpenCode session at creation: %v", err)
	}
	if _, ok := m.Get("boxed"); ok {
		t.Error("a refused cage must leave no session behind")
	}

	if _, err := m.CreateWith("plain", t.TempDir(), "opencode"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetCage("plain", &Cage{}, "nothing"); err == nil ||
		!strings.Contains(err.Error(), "OpenCode is not caged yet (ADR-061)") {
		t.Errorf("caging an OpenCode session that exists: %v", err)
	}
}

// The session's own model wins over the machine's default. Without this the
// apps' model setting did nothing on an OpenCode session — it silently kept
// the machine's, which is what review found.
func TestTheSessionsModelWinsOverTheMachines(t *testing.T) {
	cases := []struct {
		name    string
		machine string
		session string
		want    string
	}{
		{"the session names one", "machine-model", "session-model", "session-model"},
		{"only the machine names one", "machine-model", "", "machine-model"},
		{"neither: OpenCode's own config decides", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := OpenCode{Model: tc.machine}.Codec(StartOptions{Model: tc.session}).(*openCodec)
			_ = c.Decode(rawJSON(ack(c.Start()[0], map[string]any{})))
			_, write := decode(c, rawJSON(ackID(c.openID, map[string]any{
				"sessionId": "ses_x", "configOptions": []any{}})))
			got := mustJSON(write)
			if tc.want == "" {
				if strings.Contains(got, acpSetConfig) {
					t.Errorf("nothing should be set, OpenCode decides: %s", got)
				}
				return
			}
			if !strings.Contains(got, acpSetConfig) || !strings.Contains(got, tc.want) {
				t.Errorf("the model set should be %q: %s", tc.want, got)
			}
		})
	}
}

// An interrupt cancels the turns that were waiting for it — not the session.
// A turn that arrives AFTER the cancel must still go: marking the session
// swallowed it too, and told the person their message was cancelled when
// nobody had cancelled it. Found by review, which mutated one step past my own
// two assertions and noticed that `c.pending = nil` was doing nothing.
func TestATurnAfterAnInterruptIsStillSent(t *testing.T) {
	c := OpenCode{}.Codec(StartOptions{}).(*openCodec)
	c.Turn("first")  // waits for the session
	c.Interrupt()    // ... and is the one that is cancelled
	c.Turn("second") // typed after the cancel: it must go
	_ = c.Decode(rawJSON(ack(c.Start()[0], map[string]any{})))
	msgs, write := decode(c, rawJSON(ackID(c.openID, map[string]any{
		"sessionId": "ses_x", "configOptions": []any{}})))

	if got := strings.Count(joinRaw(msgs), "cancelled before the session was open"); got != 1 {
		t.Errorf("one cancelled turn should end with exactly one result, got %d: %s", got, joinRaw(msgs))
	}
	if strings.Contains(joinRaw(msgs), "first") {
		t.Errorf("the cancelled turn was sent anyway: %s", joinRaw(msgs))
	}
	sent := mustJSON(write)
	if !strings.Contains(sent, "session/prompt") || !strings.Contains(sent, "second") {
		t.Errorf("the turn that arrived after the cancel must still go: %s", sent)
	}
	if strings.Contains(sent, "first") {
		t.Errorf("only the turn that was waiting is cancelled: %s", sent)
	}
}

// Usage is THIS TURN's, not a running total. Every other harness reports the
// turn's own figures and the mesh reads them that way — rate.go divides
// output_tokens by the turn's own generating time, so a cumulative figure
// inflates the rate from the second turn on.
//
// This test first asserted the SUM, which is what the code did: found by the
// reviewer asking whether nothing covered two turns, and then by measuring
// opencode 1.19.2, where turn 1 answers in 231/out 3 and turn 2 in 7680/out 4
// — the context being re-read, not 231+7680. A test that agrees with the bug
// is worse than no test, and this one was.
func TestUsageIsTheTurnsOwnAndNotARunningTotal(t *testing.T) {
	_, s := newOpenCodeSession(t)

	first, _ := runTurn(t, s, "one")
	if got := usageOf(t, first); got != [3]uint64{100, 20, 5} {
		t.Errorf("the first turn's usage: %v, want [100 20 5]", got)
	}
	second, _ := runTurn(t, s, "two")
	// The fake answers every prompt with the same figures, so a running total
	// reads 200/40/10 here and the turn's own reads 100/20/5.
	if got := usageOf(t, second); got != [3]uint64{100, 20, 5} {
		t.Errorf("the second turn's usage: %v, want [100 20 5] — the turn's own, not the session's total", got)
	}
}

// usageOf reads {input, output, cache_read} from a turn's result event.
func usageOf(t *testing.T, evs []Event) [3]uint64 {
	t.Helper()
	var res struct {
		Usage struct {
			Input  uint64 `json:"input_tokens"`
			Output uint64 `json:"output_tokens"`
			Cache  uint64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	last := evs[len(evs)-1]
	if err := json.Unmarshal(last.Data, &res); err != nil {
		t.Fatalf("the turn's result: %s", last.Data)
	}
	return [3]uint64{res.Usage.Input, res.Usage.Output, res.Usage.Cache}
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

// decode feeds one line to the codec and returns its messages plus what it
// queued to write (the opt-in Answering side), which is the pair the read loop
// acts on.
func decode(c *openCodec, line json.RawMessage) ([]json.RawMessage, []any) {
	msgs := c.Decode(line)
	return msgs, c.Decoding()
}

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
