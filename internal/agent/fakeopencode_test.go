package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// fakeOpenCode is `opencode acp` as observed from opencode 1.19.2
// (2026-10-16): initialize answers capabilities; session/new answers a
// session id and a model config option; session/set_config_option answers the
// option it was given; session/prompt streams the reply as
// agent_message_chunk and then answers with stopReason and usage.
//
// Turns:
//
//	"run <x>"  → a tool call and its result, then "done";
//	"think"    → a thought chunk before the reply;
//	"fail"     → a JSON-RPC error instead of an answer;
//	anything   → "echo: <it>", streamed in two chunks on purpose, because the
//	             whole point of ACP over `run --format json` is that text
//	             arrives in pieces.
//
// FAKE_OPENCODE_NO_RESUME makes session/load fail, the way a machine that no
// longer has the conversation does.
func fakeOpenCode() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 8<<20)
	out := json.NewEncoder(os.Stdout)
	emit := func(v any) { out.Encode(v) }

	id := fmt.Sprintf("ses_fake%d", os.Getpid())
	model := "opencode/big-pickle"

	chunk := func(mid, text string) {
		emit(map[string]any{"jsonrpc": "2.0", "method": "session/update",
			"params": map[string]any{"sessionId": id, "update": map[string]any{
				"sessionUpdate": "agent_message_chunk", "messageId": mid,
				"content": map[string]any{"type": "text", "text": text}}}})
	}
	thought := func(mid, text string) {
		emit(map[string]any{"jsonrpc": "2.0", "method": "session/update",
			"params": map[string]any{"sessionId": id, "update": map[string]any{
				"sessionUpdate": "agent_thought_chunk", "messageId": mid,
				"content": map[string]any{"type": "text", "text": text}}}})
	}

scan:
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			emit(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{"loadSession": true},
				"agentInfo":         map[string]any{"name": "OpenCode", "version": "1.19.2-fake"}}})

		case "session/new", "session/load":
			var p struct {
				SessionID  string `json:"sessionId"`
				McpServers []struct {
					Name    string   `json:"name"`
					Type    string   `json:"type"`
					Command string   `json:"command"`
					Args    []string `json:"args"`
					Env     *[]any   `json:"env"`
				} `json:"mcpServers"`
			}
			json.Unmarshal(req.Params, &p)
			// What we were asked, for a test to read.
			if f := os.Getenv("FAKE_OPENCODE_NEW_PARAMS"); f != "" {
				if h, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					h.WriteString(line + "\n")
					h.Close()
				}
			}
			if os.Getenv("FAKE_OPENCODE_NO_NEW") == "1" && req.Method == "session/new" {
				// Exactly what opencode 1.19.2 answers when the mcpServers
				// entry has no discriminator — measured, not imagined.
				emit(map[string]any{"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32602, "message": "Invalid params"}})
				continue
			}
			// The real one validates this with a discriminated union and
			// refuses the entry without "type", so the fake does too: a fake
			// that accepts what the real one rejects is worse than none.
			//
			// `continue scan`, not a bare continue: a bare one continues the
			// INNER range, so the refusal was followed by the success reply
			// for the same id — refusing and then accepting. Found by review,
			// which piped a type-less request in and got two replies.
			for _, s := range p.McpServers {
				if s.Type == "" {
					emit(map[string]any{"jsonrpc": "2.0", "id": req.ID,
						"error": map[string]any{"code": -32602, "message": "Invalid params"}})
					continue scan
				}
			}
			if req.Method == "session/load" {
				if os.Getenv("FAKE_OPENCODE_NO_RESUME") == "1" {
					emit(map[string]any{"jsonrpc": "2.0", "id": req.ID,
						"error": map[string]any{"code": -32001, "message": "no such session"}})
					continue
				}
				if p.SessionID != "" {
					id = p.SessionID
				}
			}
			opt := map[string]any{
				"id": "model", "name": "Model", "category": "model", "type": "select",
				"currentValue": model,
				"options": []any{
					map[string]any{"value": "opencode/big-pickle", "name": "OpenCode/Big Pickle"},
					map[string]any{"value": "opencode/deepseek-v4.1-flash-free", "name": "DeepSeek"},
				},
			}
			// Measured against 1.19.2: session/new answers a sessionId,
			// session/load answers only configOptions. The fake said
			// otherwise, which is why a resume that lost the id passed the
			// tests and failed on the real thing.
			res := map[string]any{"configOptions": []any{opt}}
			if req.Method == "session/new" {
				res["sessionId"] = id
			}
			emit(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": res})

		case "session/set_config_option":
			var p struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			}
			json.Unmarshal(req.Params, &p)
			if p.ConfigID == "model" {
				model = p.Value
			}
			emit(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"configOptions": []any{map[string]any{"id": "model", "type": "select",
					"currentValue": model}}}})

		case "session/cancel":
			// a notification: nothing is answered

		case "session/prompt":
			var p struct {
				Prompt []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"prompt"`
			}
			json.Unmarshal(req.Params, &p)
			text := ""
			for _, b := range p.Prompt {
				// The first block of the first prompt is the session's own
				// note; the turn is the last block.
				text = b.Text
			}
			switch {
			case strings.HasPrefix(text, "fail"):
				emit(map[string]any{"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32002, "message": "model is on fire"}})
			case strings.HasPrefix(text, "run "):
				thought("prt_fake", "let me look")
				emit(map[string]any{"jsonrpc": "2.0", "method": "session/update",
					"params": map[string]any{"sessionId": id, "update": map[string]any{
						"sessionUpdate": "tool_call", "toolCallId": "call_fake1",
						"title": "read", "kind": "read", "status": "pending"}}})
				emit(map[string]any{"jsonrpc": "2.0", "method": "session/update",
					"params": map[string]any{"sessionId": id, "update": map[string]any{
						"sessionUpdate": "tool_call_update", "toolCallId": "call_fake1",
						"status": "completed", "kind": "read", "title": "read",
						"content": []any{map[string]any{"type": "content",
							"content": map[string]any{"type": "text", "text": "the file"}}}}}})
				chunk("msg_fake1", "done")
			case text == "think":
				thought("prt_fake", "hmm")
				chunk("msg_fake1", "thought about it")
			default:
				// Two chunks, so a test can tell streaming from one lump.
				chunk("msg_fake1", "echo: ")
				chunk("msg_fake1", text)
			}
			emit(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"stopReason": "end_turn",
				"usage": map[string]any{"inputTokens": 100, "outputTokens": 20,
					"totalTokens": 120, "cachedReadTokens": 5}}})
		}
	}
}
