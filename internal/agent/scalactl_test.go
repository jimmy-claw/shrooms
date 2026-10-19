package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ScalaCtl reads logosctl's answers as the recipe shows them (scala 0.12.5,
// run on jimmy-crib 2026-10-19): result as JSON in a string, custom fields
// flat on events, the last editor from the history; and never keeps the read
// key.
func TestScalaCtlReadsWhatLogosctlPrints(t *testing.T) {
	wrap := func(result any) []byte {
		s, _ := json.Marshal(result)
		b, _ := json.Marshal(map[string]any{"method": "m", "module": "scala", "result": string(s), "status": "ok"})
		return b
	}
	var calls []string
	c := &ScalaCtl{run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[2] {
		case "listCalendars":
			return wrap([]map[string]any{{"id": "cal1", "name": "Agent: crib/vpavlin", "owner": "0xowner",
				"roles": map[string]string{"0xme": "editor"}, "invites": map[string]any{}, "open": false, "collab": false,
				"encryptionKey": "SECRET-READ-KEY", "schema": []any{map[string]any{"key": "prompt"}}}}), nil
		case "listEvents":
			return wrap([]map[string]any{{"id": "e1", "calendarId": "cal1", "title": "PR + issue check",
				"startTime": 1792476000000, "recur": map[string]any{"freq": "daily", "interval": 2},
				"session": "jimmy-crib/vpavlin", "prompt": "Check my PRs", "checks": "gh pr list", "maxCost": 0.5,
				"timeoutMin": 15, "enabled": true, "creatorId": "0xowner"}}), nil
		case "getEventHistory":
			return wrap([]map[string]any{{"action": "created", "at": 1, "author": "0xowner"},
				{"action": "edited", "at": 2, "author": "0xme"}}), nil
		case "dueOccurrences":
			return wrap([]map[string]any{{"eventId": "e1", "occ": 1792476000000}}), nil
		case "postExt":
			return wrap(map[string]any{"id": "run-e1-1792476000000", "ok": true}), nil
		case "hdState":
			return wrap(map[string]any{"pending": 0, "error": ""}), nil
		}
		return []byte(`{"code":"NO","message":"unknown","status":"error"}`), nil
	}}
	st, err := c.State("cal1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Owner != "0xowner" || st.Roles["0xme"] != "editor" || len(st.Invites) != 0 || len(st.Events) != 1 {
		t.Fatalf("state: %+v", st)
	}
	e := st.Events[0]
	if e.Author != "0xme" || e.Recur == nil || e.Recur.Interval != 2 || e.text("prompt") != "Check my PRs" ||
		e.number("timeoutMin") != 15 || !e.enabled() || e.Start.UnixMilli() != 1792476000000 {
		t.Fatalf("event: %+v", e)
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal("the read key is kept")
	}
	due, err := c.Due("cal1", time.UnixMilli(1792447200000), time.UnixMilli(1793052000000), "Europe/Prague")
	if err != nil || len(due) != 1 || due[0].At.UnixMilli() != 1792476000000 {
		t.Fatalf("due: %v %+v", err, due)
	}
	if err := c.PostRun("cal1", "e1", RunRecord{Occ: due[0].At, Ran: time.UnixMilli(1792476012000), Changed: true, Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	// Numbers bare, and the run's id one a restarted runner makes again.
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "dueOccurrences cal1 1792447200000 1793052000000 Europe/Prague") ||
		!strings.Contains(joined, `"id":"run-e1-1792476000000"`) {
		t.Fatalf("calls:\n%s", joined)
	}
	// A locked root: written, but not signed, and said so.
	c2 := *c
	c2.run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[2] == "hdState" {
			return wrap(map[string]any{"pending": 1, "error": "locked"}), nil
		}
		return c.run(ctx, args...)
	}
	if err := c2.PostRun("cal1", "e1", RunRecord{Occ: due[0].At}); err == nil || !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("a locked root: %v", err)
	}
}
