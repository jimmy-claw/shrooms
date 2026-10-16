package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// OpenCode is OpenCode (opencode.ai), spoken to over ACP — the Agent Client
// Protocol — because that is the only transport of its that fits a session.
//
// `opencode run` is one-shot: the message is an argument and the process
// exits, so it cannot take a second turn and has no stdin to interrupt.
// `opencode acp` is a long-lived JSON-RPC server on stdin/stdout, like
// `claude -p --input-format stream-json` and `pi --mode rpc`, and it streams
// the reply in chunks — which `run --format json` does not (measured: a
// twenty-word answer arrived as one `text` event).
//
// Written against opencode 1.19.2.
type OpenCode struct {
	// Model is this MACHINE's default for the ACP model config option,
	// e.g. "opencode/deepseek-v4.1-flash-free". Empty — the default —
	// leaves OpenCode's own config to decide. A session's own choice
	// (StartOptions.Model, what the apps' model setting sets) wins over it.
	//
	// Deliberately not defaulted to a free model: OpenCode's free ones are
	// mostly stealth (unreleased and anonymous, free for a limited time), so
	// a default naming one would rot into a support call.
	Model string
}

func (OpenCode) Name() string  { return "opencode" }
func (OpenCode) Title() string { return "OpenCode" }
func (OpenCode) Bin() string   { return "opencode" }

// Caps: OpenCode runs its tools without asking, so there is nothing for
// auto-approve to mean.
//
// Measured, not assumed: asked to write a file it wrote it, with no prompt,
// both through `run` without `--auto` and over ACP. Its `permission` config
// decides per action — run, block, or ask in a TUI — and no request reaches a
// client, so the apps' approve button has nothing to hook. Approve stays
// false rather than being offered and doing nothing.
//
// Takeover is false for now: OpenCode keeps its conversations and ACP can
// list and load them (sessionCapabilities: list, resume, fork), but listing
// another terminal's conversations is not built here.
func (OpenCode) Caps() Caps { return Caps{} }

// Args starts the ACP server. Nothing else is passed on the command line:
// the ACP server takes no flag for the conversation to continue, the model,
// the mesh's tools or what the session is told about itself, so all four go
// over the protocol, in the codec.
func (OpenCode) Args(StartOptions) []string { return []string{"acp"} }

// Codec is a translator that knows how this process was started, because
// everything a session is given has to be sent as a message.
func (o OpenCode) Codec(s StartOptions) Codec {
	// The session's own choice wins over the machine's default, which wins
	// over OpenCode's config. Without this the apps' model setting did
	// nothing on an OpenCode session.
	model := s.Model
	if model == "" {
		model = o.Model
	}
	return &openCodec{
		model:   model,
		dir:     s.Dir,
		resume:  s.Resume,
		mcp:     s.MCP,
		mcpArgs: s.MCPArgs,
		note:    s.Note,
	}
}

// --- in a cage (ADR-062) ------------------------------------------------------

// openCodeConfigDir and openCodeDataDir are where opencode 1.19.2 keeps the
// owner's settings and its conversations. Measured, not assumed: it honours
// XDG_CONFIG_HOME and XDG_DATA_HOME, and with neither set they are these.
func openCodeConfigDir(home string) string { return filepath.Join(home, ".config", "opencode") }
func openCodeDataDir(home string) string   { return filepath.Join(home, ".local", "share", "opencode") }

// openCodeHostCode is what of the owner's config comes into the cage read-only.
// Not a credential: opencode's key is OPENCODE_API_KEY or its own auth store,
// and CageEnv names neither, so neither can get in.
var openCodeHostCode = []string{"opencode.jsonc", "opencode.json", "AGENTS.md", "package.json", "node_modules"}

// openCodeProgram is OpenCode itself, symlinks followed: a single file with its
// runtime inside it.
func openCodeProgram(bin string) (string, error) {
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// CageMounts: the owner's config read-only into a private copy, and a data
// directory of the CAGE's own.
//
// The data directory has to be the cage's own. OpenCode keeps every project's
// conversations in ONE SQLite database (~/.local/share/opencode/opencode.db,
// measured; sessions carry their directory in a column but the file is one),
// so mounting the owner's would put every other project's conversation in the
// cage — ADR-060's line, which is that a cage sees its own and nothing else of
// the owner's. With one database the only way to keep that line is to give the
// cage a database of its own. A filtered copy was considered and rejected by
// ADR-060 for the same shape ("two copies of a conversation ... can lose part
// of one"); here the consequence is stated rather than hidden — a caged
// OpenCode session's conversations are the cage's, they are not in the owner's
// database, and they go when the cage does. The session's own history in the
// apps is the agent's event log, which is unaffected.
func (OpenCode) CageMounts(f CageFacts) []mount {
	cfg, data := openCodeConfigDir(f.Home), openCodeDataDir(f.Home)
	// Made here, because a mount whose source is not there is dropped: the
	// private home itself is made by the agent, these two are the harness's.
	privCfg, privData := filepath.Join(f.PrivateHome, "config"), filepath.Join(f.PrivateHome, "data")
	os.MkdirAll(privCfg, 0o700)
	os.MkdirAll(privData, 0o700)
	ms := []mount{{path: privCfg, at: cfg}}
	ms = append(ms, readOnlyIn(cfg, openCodeHostCode)...)
	ms = append(ms, mount{path: privData, at: data})
	// Its program, one file.
	if bin, err := openCodeProgram(f.Bin); err == nil {
		ms = append(ms, mount{path: filepath.Dir(bin), ro: true})
	}
	return ms
}

func (OpenCode) CageProgram(f CageFacts) (string, error) { return openCodeProgram(f.Bin) }

// CageTrust: nothing to carry. OpenCode has no per-project trust — what a
// session may run is its own permission config, which is the owner's settings
// and comes in read-only.
func (OpenCode) CageTrust(CageFacts) error { return nil }

// CageEnv: nothing of the owner's environment. Its key is not named, so it
// cannot come in, and its config and data are found at their default paths,
// which in the cage are the private copies CageMounts put there.
func (OpenCode) CageEnv() []string { return nil }

// GatewayEnv points OpenCode at the gateway (ADR-059): its own provider's base
// URL and a key the gateway replaces with the real one.
//
// Measured, and it matters: OPENCODE_CONFIG_CONTENT is read and WINS over the
// owner's opencode.jsonc. A gateway FILE named opencode.json would be silently
// ignored, because opencode.jsonc takes precedence over opencode.json — which
// is what the owner has. So the override goes in the environment, and the
// owner's settings are not shadowed by it.
func (OpenCode) GatewayEnv(bool) map[string]string {
	cfg, err := json.Marshal(map[string]any{"provider": map[string]any{
		"opencode": map[string]any{"options": map[string]any{
			"baseURL": GatewayBase("opencode") + "/v1", "apiKey": GatewayKey}}}})
	if err != nil {
		return nil
	}
	return map[string]string{"OPENCODE_CONFIG_CONTENT": string(cfg)}
}

// GatewayFiles: none. The whole of it is the environment above.
func (OpenCode) GatewayFiles(bool) map[string]string { return nil }

// --- the codec ---------------------------------------------------------------

// ACP method names, as opencode 1.19.2 answers them.
const (
	acpInitialize = "initialize"
	acpNewSession = "session/new"
	acpLoad       = "session/load"
	acpSetConfig  = "session/set_config_option"
	acpPrompt     = "session/prompt"
	acpCancel     = "session/cancel"
	acpUpdate     = "session/update"
)

type openCodec struct {
	model   string
	dir     string
	resume  string
	mcp     string
	mcpArgs []string
	note    string

	mu sync.Mutex
	// nextID numbers the requests we send. Responses are matched by it, so
	// the codec knows which of its own steps it is being answered about.
	nextID   int
	initID   int
	openID   int
	opened   string // acpNewSession or acpLoad: only a failed LOAD may fall back
	setID    int
	promptID int

	sessionID string
	ready     bool
	// writes is what Decode queued for the process: see Answering.
	writes []any
	// pending holds turns that arrived before the session existed: a device
	// can send a message the moment a session starts, and the ACP session is
	// created a round trip later. Each carries whether it was cancelled while
	// it waited, so an interrupt touches the turns it was pressed for and not
	// one that arrives afterwards.
	pending []ocTurn
	// sent remembers the turns we wrote, so the same text coming back as a
	// user message is recognised as ours and not reported as outside.
	sent []string

	// noteSent: the note goes on the first prompt of the process, as its own
	// content block, because session/new ignores a system prompt (measured:
	// a systemPrompt param asking for BANANA was ignored and the model
	// answered Paris).
	noteSent bool

	// text accumulates the current assistant message, flushed when the
	// message id changes or the turn ends, so the record has whole messages
	// while the app has been fed chunks.
	text    strings.Builder
	curMsg  string
	usage   rawUsage
	failed  bool
	lastErr string
}

func (c *openCodec) request(method string, params any) map[string]any {
	c.nextID++
	return map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params}
}

func (c *openCodec) notify(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

// mcpServers is the mesh's agents as tools, in ACP's stdio shape.
func (c *openCodec) mcpServers() []any {
	if c.mcp == "" {
		return []any{}
	}
	args := append([]string{"mcp"}, c.mcpArgs...)
	// OpenCode validates this with a discriminated union, so the entry is
	// rejected outright without "type" — and it wants "env" as a list.
	// Measured against opencode 1.19.2: without them, session/new answers
	// -32602 "Invalid params" and no session is ever opened.
	return []any{map[string]any{
		"name": "shrooms", "type": "stdio", "command": c.mcp, "args": args, "env": []any{}}}
}

// Start opens the protocol: initialize first, and the session itself once
// that is answered.
func (c *openCodec) Start() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initID = c.nextID + 1
	return []any{c.request(acpInitialize, map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{}})}
}

// open asks for a new conversation, or for the one this session already has.
func (c *openCodec) open() []any {
	// ACP wants an absolute path; the session's directory is one. "." is only
	// a fallback for a codec built without one (a test), which the protocol
	// does not promise to accept.
	cwd := c.dir
	if cwd == "" {
		cwd = "."
	}
	params := map[string]any{"cwd": cwd, "mcpServers": c.mcpServers()}
	method := acpNewSession
	if c.resume != "" {
		method = acpLoad
		params["sessionId"] = c.resume
	}
	c.openID = c.nextID + 1
	c.opened = method
	return []any{c.request(method, params)}
}

// ocTurn is a turn waiting for the session to exist.
type ocTurn struct {
	text      string
	cancelled bool
}

// Turn is a user turn. Before the session exists it is queued: the ACP
// session id arrives a round trip after the process starts, and a device
// that sends at once must not have its message dropped.
func (c *openCodec) Turn(text string) []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, text)
	if !c.ready {
		c.pending = append(c.pending, ocTurn{text: text})
		return nil
	}
	return []any{c.prompt(text)}
}

// prompt writes a turn to the open session. Called with the lock held.
func (c *openCodec) prompt(text string) map[string]any {
	blocks := []any{}
	if c.note != "" && !c.noteSent {
		// The session's own instructions, once, as a block of their own so
		// the person's text stays exactly what they typed.
		blocks = append(blocks, map[string]any{"type": "text", "text": c.note})
		c.noteSent = true
	}
	blocks = append(blocks, map[string]any{"type": "text", "text": text})
	c.promptID = c.nextID + 1
	return c.request(acpPrompt, map[string]any{"sessionId": c.sessionID, "prompt": blocks})
}

// drainQueue sends what is queued and ends what was cancelled, in the order
// the turns arrived. Called with the lock held.
//
// The cancelled ones are ENDED, each with its own result: a turn the codec
// drops without one leaves the session reading as working for ever. The order
// matters too — a result that arrives before an earlier turn's prompt would
// report the turns out of order.
func (c *openCodec) drainQueue() (msgs []json.RawMessage, write []any) {
	for _, t := range c.pending {
		if t.cancelled {
			msgs = append(msgs, out(map[string]any{"type": "result",
				"subtype": "error_during_execution", "session_id": c.sessionID,
				"error": "the turn was cancelled before the session was open"}))
			continue
		}
		write = append(write, c.prompt(t.text))
	}
	c.pending = nil
	return msgs, write
}

func (c *openCodec) Interrupt() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionID == "" {
		// There is nothing at ACP to cancel yet — but a turn may be waiting
		// for the session, and leaving it queued would make cancel a button
		// that does nothing. It is dropped here, and the turn is ENDED when
		// the session opens: a dropped turn with no result would leave the
		// session working for ever.
		// Each turn waiting here is marked, NOT the session: a turn that
		// arrives after the cancel must still go. Marking the session instead
		// swallowed it too, and told the person their message was cancelled
		// when nobody had cancelled it (found by review).
		for i := range c.pending {
			c.pending[i].cancelled = true
		}
		return nil
	}
	return []any{c.notify(acpCancel, map[string]any{"sessionId": c.sessionID})}
}

// Respond: nothing to answer. OpenCode raises no permission request (see
// Caps), so a control_request never comes from it.
func (c *openCodec) Respond(string, map[string]any) []any { return nil }

// assistant is one whole assistant message, in the shape the apps already
// read from Claude Code and pi.
func ocText(text, model string) json.RawMessage {
	return out(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant", "model": model,
		"content": []any{map[string]any{"type": "text", "text": text}}}})
}

func textDelta(text string) json.RawMessage {
	return out(map[string]any{"type": "stream_event", "event": map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "text_delta", "text": text}}})
}

func thoughtDelta(text string) json.RawMessage {
	return out(map[string]any{"type": "stream_event", "event": map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "thinking_delta", "thinking": text}}})
}

// flushText emits the accumulated message, if there is one. Called with the
// lock held.
func (c *openCodec) flushText() []json.RawMessage {
	if strings.TrimSpace(c.text.String()) == "" {
		c.text.Reset()
		return nil
	}
	m := ocText(c.text.String(), c.model)
	c.text.Reset()
	return []json.RawMessage{m}
}

// Decode turns one line of the ACP server into messages in the agent's own
// shape, queueing anything that has to be written back to the server.
//
// It is the one codec here that writes while decoding, and it must: the ACP
// session does not exist until initialize is answered with session/new, and
// every turn needs the id that comes back. A device may send a message the
// moment a session is up (Session.SendID writes it at once), so that turn is
// queued and goes out on this step, with the session/new request. See
// Answering, which is how the read loop knows to write it.
func (c *openCodec) Decode(line json.RawMessage) []json.RawMessage {
	msgs, write := c.decode(line)
	if len(write) > 0 {
		c.mu.Lock()
		c.writes = append(c.writes, write...)
		c.mu.Unlock()
	}
	return msgs
}

// Decoding returns what this codec queued to write, and empties the queue.
func (c *openCodec) Decoding() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.writes
	c.writes = nil
	return w
}

func (c *openCodec) decode(line json.RawMessage) ([]json.RawMessage, []any) {
	var m struct {
		ID     *int            `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// A response to one of ours: this is where the session comes into being.
	if m.ID != nil {
		switch *m.ID {
		case c.initID:
			if m.Error != nil {
				c.failed = true
				return []json.RawMessage{out(map[string]any{"type": "result",
					"subtype": "error_during_execution", "session_id": "",
					"error": "opencode would not initialize: " + m.Error.Message})}, nil
			}
			// The session does not exist yet: ask for it.
			return nil, c.open()

		case c.openID:
			if m.Error != nil {
				if c.opened == acpLoad {
					// A conversation this machine no longer has: start a new
					// one rather than leaving the session unable to take a
					// message. Only a LOAD may fall back like this.
					c.resume = ""
					return nil, c.open()
				}
				// The session could not be opened at all. Falling back here
				// would ask again for the same thing that was just refused,
				// which is a spin, not a retry: a session/new that fails
				// fails again. Say why and fail the turn.
				c.failed = true
				c.ready = false
				c.pending = nil
				return []json.RawMessage{out(map[string]any{
					"type": "result", "subtype": "error_during_execution", "session_id": "",
					"error": "opencode would not open a session: " + m.Error.Message})}, nil
			}
			var res struct {
				SessionID     string `json:"sessionId"`
				ConfigOptions []struct {
					ID           string `json:"id"`
					CurrentValue string `json:"currentValue"`
				} `json:"configOptions"`
			}
			json.Unmarshal(m.Result, &res)
			c.sessionID = res.SessionID
			if c.sessionID == "" && c.opened == acpLoad {
				// session/load answers configOptions and NO sessionId — the id
				// is the one we asked to load. Measured against 1.19.2. Read
				// from the response alone it was empty, so the model setting
				// and every prompt went out with an empty id and opencode
				// answered "session not found", after any restart — which is
				// what a model change does.
				c.sessionID = c.resume
			}
			for _, o := range res.ConfigOptions {
				if o.ID == "model" && c.model == "" {
					c.model = o.CurrentValue
				}
			}
			msgs := []json.RawMessage{out(map[string]any{"type": "system", "subtype": "init",
				"session_id": c.sessionID, "model": c.model, "harness": "opencode"})}
			var write []any
			if c.model != "" {
				c.setID = c.nextID + 1
				write = append(write, c.request(acpSetConfig, map[string]any{
					"sessionId": c.sessionID, "configId": "model", "value": c.model}))
			}
			// The session is open: anything a device sent meanwhile goes now —
			// or is ended, if it was cancelled while it waited — and the init
			// event above tells the apps the session is up.
			qmsgs, qwrite := c.drainQueue()
			msgs = append(msgs, qmsgs...)
			write = append(write, qwrite...)
			c.ready = true
			return msgs, write

		case c.setID:
			// The model is a preference, not a precondition: if the value is
			// not one this machine offers, the turn still goes, on the
			// default, rather than the session dying.
			return nil, nil

		case c.promptID:
			msgs := c.flushText()
			var res struct {
				StopReason string `json:"stopReason"`
				// ACP's own end-turn usage RFD says "omission and null are
				// equivalent. Both mean the agent is not reporting that
				// category" — NOT zero. Measured: 1.19.2 reported
				// cachedReadTokens on one turn and omitted it entirely on the
				// next. An omitted field is read as 0 here, and 0 is a real
				// value: the running cache-read totals (usage.go, metrics.go)
				// ADD this figure, so an unreported turn undercounts them
				// rather than being marked unknown. Deliberate — carrying a
				// previous turn's figure forward would fabricate a number,
				// which is worse for an accounting total than a documented
				// undercount. Worth knowing when a cache_read total looks low.
				//
				// The RFD is still Draft (and gated behind an unstable
				// feature flag), so this is the protocol's stated intent
				// rather than a promise about what opencode does.
				Usage struct {
					InputTokens      uint64 `json:"inputTokens"`
					OutputTokens     uint64 `json:"outputTokens"`
					CachedReadTokens uint64 `json:"cachedReadTokens"`
				} `json:"usage"`
			}
			json.Unmarshal(m.Result, &res)
			// THIS TURN's figures, NOT a running total. Every other harness
			// reports the turn's own, and the mesh reads them that way:
			// rate.go divides output_tokens by the turn's OWN generating
			// time, so a cumulative figure inflates the rate from the second
			// turn on. Measured against opencode 1.19.2: turn 1 in 231 /
			// out 3, turn 2 in 7680 / out 4 — the second is the context
			// being re-read, not the sum of the two.
			c.usage = rawUsage{
				Input:     res.Usage.InputTokens,
				Output:    res.Usage.OutputTokens,
				CacheRead: res.Usage.CachedReadTokens,
			}
			sub := "success"
			if m.Error != nil {
				sub = "error_during_execution"
				c.failed = true
				c.lastErr = m.Error.Message
				msgs = append(msgs, ocText("opencode: "+m.Error.Message, c.model))
			} else if res.StopReason != "" && res.StopReason != "end_turn" &&
				res.StopReason != "max_tokens" && res.StopReason != "cancelled" {
				sub = "error_during_execution"
				c.failed = true
				msgs = append(msgs, ocText("opencode stopped: "+res.StopReason, c.model))
			}
			msgs = append(msgs, out(map[string]any{"type": "result", "subtype": sub,
				"session_id": c.sessionID,
				"usage": map[string]uint64{"input_tokens": c.usage.Input,
					"output_tokens":           c.usage.Output,
					"cache_read_input_tokens": c.usage.CacheRead}}))
			return msgs, nil
		}
		return nil, nil
	}

	if m.Method != acpUpdate {
		return nil, nil
	}
	// A session/update: the streaming half of the protocol.
	var up struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			MessageID     string `json:"messageId"`
			// "content" is an object for a message chunk and an array for a
			// tool result, so it is kept raw and read per case: two struct
			// fields cannot carry the same tag (go vet catches it).
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			Status     string          `json:"status"`
			RawInput   json.RawMessage `json:"rawInput"`
			RawOutput  json.RawMessage `json:"rawOutput"`
		} `json:"update"`
	}
	if json.Unmarshal(m.Params, &up) != nil {
		return nil, nil
	}
	u := up.Update

	switch u.SessionUpdate {
	case "agent_message_chunk":
		var ch struct {
			Text string `json:"text"`
		}
		json.Unmarshal(u.Content, &ch)
		if ch.Text == "" {
			return nil, nil
		}
		// A new message id means the previous message is finished.
		var msgs []json.RawMessage
		if u.MessageID != "" && c.curMsg != "" && u.MessageID != c.curMsg {
			msgs = c.flushText()
		}
		if u.MessageID != "" {
			c.curMsg = u.MessageID
		}
		c.text.WriteString(ch.Text)
		return append(msgs, textDelta(ch.Text)), nil

	case "agent_thought_chunk":
		var ch struct {
			Text string `json:"text"`
		}
		json.Unmarshal(u.Content, &ch)
		if ch.Text == "" {
			return nil, nil
		}
		return []json.RawMessage{thoughtDelta(ch.Text)}, nil

	case "tool_call":
		// The call itself. The result follows as a tool_call_update, and the
		// app shows the pair the way it shows Claude Code's.
		msgs := c.flushText()
		input := u.RawInput
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		name := u.Title
		if name == "" {
			name = u.Kind
		}
		return append(msgs, out(map[string]any{"type": "assistant", "message": map[string]any{
			"role": "assistant", "model": c.model,
			"content": []any{map[string]any{"type": "tool_use",
				"id": u.ToolCallID, "name": name, "input": input}}}})), nil

	case "tool_call_update":
		if u.Status != "completed" && u.Status != "failed" {
			return nil, nil
		}
		text := ""
		if len(u.Content) > 0 {
			var blocks []struct {
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(u.Content, &blocks) == nil {
				for _, b := range blocks {
					text += b.Content.Text
				}
			}
		}
		content := any(text)
		if u.Status == "failed" {
			content = any("failed: " + text)
		}
		return []json.RawMessage{out(map[string]any{"type": "user", "message": map[string]any{
			"role": "user",
			"content": []any{map[string]any{"type": "tool_result",
				"tool_use_id": u.ToolCallID, "content": content}}}})}, nil

	case "usage_update":
		// The running figure. The authoritative one is in the prompt
		// response, which is where the mesh's accounting reads it.
		return nil, nil
	}
	return nil, nil
}

// OpenCode is a Harness, checked at build time rather than by a test that
// would only fail if the compiler had already failed.
var _ Harness = OpenCode{}
