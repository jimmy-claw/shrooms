package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// The command line threads onto a task (reply), takes a task with or
// without its machine, and says what a refusal means.
func TestTheCommandLineRepliesAndTakesEitherTaskForm(t *testing.T) {
	var sent map[string]any
	var gets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/tasks/") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Method string
			Params map[string]any
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "SendMessage" {
			sent = req.Params["message"].(map[string]any)
		}
		if req.Method == "GetTask" {
			gets = append(gets, req.Params["id"].(string))
			if req.Params["id"] != "proteus:m1" {
				io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"no task"}}`)
				return
			}
		}
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"task":{"id":"proteus:m1","status":{"state":"TASK_STATE_WORKING"}}}}`)
	}))
	defer srv.Close()
	c := a2aClient{
		base: func(netip.Addr) string { return srv.URL },
		peers: func() ([]machine, error) {
			return []machine{{"laptop", netip.MustParseAddr("fd00::1")}, {"proteus", netip.MustParseAddr("fd00::2")}}, nil
		},
		sock: "/nonexistent",
	}

	// reply: onto the task, not a new one; and with the bare form, the
	// machine is found by asking.
	if _, err := c.answer("proteus:m1", "use master", false); err != nil {
		t.Fatal(err)
	}
	if sent["taskId"] != "proteus:m1" {
		t.Fatalf("the reply was not on the task: %v", sent)
	}
	if len(gets) == 0 {
		t.Fatal("the bare form asked nobody for the task")
	}
	if _, _, err := c.taskRef("proteus/proteus:m1"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"m1", "proteus/m1", "/proteus:m1"} {
		if _, _, err := c.taskRef(bad); err == nil || !strings.Contains(err.Error(), "MACHINE/SESSION:MESSAGE-ID") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, _, err := c.taskRef("ghost:m9"); err == nil || !strings.Contains(err.Error(), "no machine on the mesh knows") {
		t.Errorf("an unknown bare task: %v", err)
	}

	// update is the worker's: refused with what to use instead.
	if _, err := c.update("laptop/reviewer:cli-1", "done", "x"); err == nil || !strings.Contains(err.Error(), "a2a reply") {
		t.Errorf("update of a task this session did not get: %v", err)
	}
}

// An id without its SESSION: is refused for its shape before anything is
// asked, as get and ack refuse it, not reported as a task given to someone
// else (pi5/jimmy, 2026-10-15).
func TestUpdateSaysAShortIdIsShort(t *testing.T) {
	asked := false
	c := a2aClient{
		peers: func() ([]machine, error) { asked = true; return nil, nil },
		sock:  "/nonexistent",
	}
	for _, id := range []string{"cli-20261015T092619-d09000f60350fe2b", "laptop/cli-20261015T092619-d09000f60350fe2b"} {
		_, err := c.update(id, "done", "x")
		if err == nil || !strings.Contains(err.Error(), "a task is MACHINE/SESSION:MESSAGE-ID") || strings.Contains(err.Error(), "given to this session") {
			t.Errorf("update %s: %v", id, err)
		}
	}
	if asked {
		t.Error("a short id was sent to the agent before its shape was checked")
	}
}
