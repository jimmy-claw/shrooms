package agent

import (
	"encoding/json"
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
	// Model is the value of the ACP model config option, e.g.
	// "opencode/deepseek-v4.1-flash-free". Empty leaves OpenCode's own
	// default, which is a stealth model (opencode/big-pickle) — free, and
	// able to disappear without notice, so a session meant to last should
	// name one.
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
	return &openCodec{
		model:   o.Model,
		resume:  s.Resume,
		mcp:     s.MCP,
		mcpArgs: s.MCPArgs,
		note:    s.Note,
	}
}

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
	params := map[string]any{"cwd": ".", "mcpServers": c.mcpServers()}
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
// shape, and into the requests this codec has to answer the server with.
//
// It is the one harness here that writes while decoding, and it must: the ACP
// session does not exist until initialize is answered with session/new, and
// every turn needs the id that comes back. A device may send a message the
// moment a session is up (Session.SendID writes it at once), so that turn is
// queued and goes out on this step, with the session/new request.
func (c *openCodec) Decode(line json.RawMessage) ([]json.RawMessage, []any) {
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
				Usage      struct {
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
