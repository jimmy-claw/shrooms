package agent

// The three parameters of GET /v1/tasks exist because a poller wants a
// different task than a reader does (ADR-063). The hub polls every session of
// every peer every 30 s and paid 589,776 B per cycle for it.
//
// Each test here is written so that it FAILS if its rule is removed — the
// defaults test and the 400s are the controls: they must not change behaviour
// for the apps that never pass a parameter.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type tasksListRig struct {
	t  *testing.T
	r  a2aRig
	at map[string]time.Time
}

func newTasksList(t *testing.T) tasksListRig {
	t.Helper()
	r := newA2A(t)
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	at := map[string]time.Time{}
	add := func(session, msg string, when time.Time, state string) {
		id := session + ":" + msg
		r.m.tasks.mu.Lock()
		r.m.tasks.tasks[id] = &Task{
			ID: id, Session: session, MessageID: msg, From: "pi5.default/jimmy",
			Request: "ask " + msg, Summary: "result " + msg,
			Title: "title " + msg, Created: when, Updated: when, State: state,
		}
		r.m.tasks.mu.Unlock()
		at[msg] = when
	}
	add("proj", "old", base, taskCompleted)
	add("proj", "mid", base.Add(time.Hour), taskCompleted)
	add("proj", "new", base.Add(2*time.Hour), taskWorking)
	// a task whose session no longer exists: never visible to anyone
	add("ghost", "hidden", base.Add(3*time.Hour), taskCompleted)
	return tasksListRig{t: t, r: r, at: at}
}

func (g tasksListRig) get(query string) (int, []a2aTask, string) {
	url := g.r.remote.URL + "/v1/tasks" + query
	res, err := http.Get(url)
	if err != nil {
		g.t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	var body struct {
		Tasks []a2aTask `json:"tasks"`
		Error string    `json:"error"`
	}
	json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body.Tasks, body.Error
}

func ids(ts []a2aTask) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

// The apps pass nothing, so nothing may change for them: request and result
// still travel with the task.
func TestTasksListDefaultsAreUnchanged(t *testing.T) {
	g := newTasksList(t)
	code, tasks, _ := g.get("?session=proj")
	if code != http.StatusOK || len(tasks) != 3 {
		t.Fatalf("default list: %d %v", code, ids(tasks))
	}
	for _, task := range tasks {
		if len(task.History) == 0 {
			t.Errorf("%s: default list lost its history", task.ID)
		}
	}
	if got := tasks[1].Artifacts; len(got) == 0 {
		t.Errorf("a completed task lost its artifact")
	}
}

func TestTasksListHistoryOffDropsTheConversation(t *testing.T) {
	g := newTasksList(t)
	_, full, _ := g.get("?session=proj")
	_, lean, _ := g.get("?session=proj&history=0")
	if len(lean) != len(full) {
		t.Fatalf("history=0 changed the number of tasks: %d vs %d", len(lean), len(full))
	}
	for _, task := range lean {
		if task.History != nil || task.Artifacts != nil {
			t.Errorf("%s: history=0 still carries history=%v artifacts=%v",
				task.ID, task.History != nil, task.Artifacts != nil)
		}
		// what a poller needs must survive
		if task.ID == "" || task.ContextID == "" || task.Status.State == "" || task.Status.Timestamp == "" {
			t.Errorf("%s: history=0 dropped identity or status", task.ID)
		}
		if task.Metadata["shrooms/session"] == nil {
			t.Errorf("%s: history=0 dropped metadata", task.ID)
		}
	}
	// the status message is what tells a reader what happened, so it stays
	if m := lean[1].Status.Message; m == nil || len(m.Parts) == 0 {
		t.Errorf("history=0 dropped the status message")
	}
}

func TestTasksListSinceIsStrictlyAfter(t *testing.T) {
	g := newTasksList(t)
	// exactly the middle task's timestamp: it must NOT be included
	boundary := g.at["mid"].Format(time.RFC3339)
	_, tasks, _ := g.get("?session=proj&since=" + boundary + "&history=0")
	if got := ids(tasks); len(got) != 1 || got[0] != "proj:new" {
		t.Fatalf("since=boundary gave %v, want only proj:new", got)
	}
	// one second earlier: both mid and new
	earlier := g.at["mid"].Add(-time.Second).Format(time.RFC3339)
	_, tasks, _ = g.get("?session=proj&since=" + earlier)
	if got := ids(tasks); len(got) != 2 {
		t.Fatalf("since=just before mid gave %v, want 2", got)
	}
}

// Unknown parameters have always been ignored here; a `since` that cannot be
// understood must not silently become "everything", which is how the cost of a
// full list went unnoticed for a week.
func TestTasksListRejectsWhatItCannotParse(t *testing.T) {
	g := newTasksList(t)
	for _, q := range []string{"?session=proj&since=yesterday", "?session=proj&since=2026-10-21",
		"?session=proj&limit=two", "?session=proj&limit=-1"} {
		code, _, msg := g.get(q)
		if code != http.StatusBadRequest {
			t.Errorf("%s: code %d, want 400", q, code)
		}
		if msg == "" {
			t.Errorf("%s: 400 without a reason", q)
		}
	}
}

func TestTasksListLimitIsNewestFirst(t *testing.T) {
	g := newTasksList(t)
	_, tasks, _ := g.get("?session=proj&limit=2&history=0")
	if got := ids(tasks); len(got) != 2 || got[0] != "proj:new" || got[1] != "proj:mid" {
		t.Fatalf("limit=2 gave %v, want [proj:new proj:mid]", got)
	}
}

// Filter by visibility BEFORE the limit: a task the caller may not see must not
// use up the allowance, or a small limit could return nothing at all.
func TestTasksListVisibilityBeforeLimit(t *testing.T) {
	g := newTasksList(t)
	// "ghost" is the newest task in the store and belongs to no live session
	code, tasks, _ := g.get("?limit=1")
	if code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if len(tasks) != 1 {
		t.Fatalf("limit=1 returned %d tasks", len(tasks))
	}
	if tasks[0].ID == "ghost:hidden" {
		t.Fatalf("a task from a dead session was returned")
	}
	if tasks[0].ID != "proj:new" {
		t.Fatalf("limit=1 returned %s, want the newest visible task", tasks[0].ID)
	}
}

// Unused import guard: httptest is used by the rig's parent, keep the compiler
// honest about it here too.
var _ = httptest.NewServer
