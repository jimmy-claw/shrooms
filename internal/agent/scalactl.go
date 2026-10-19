package agent

// Scala reached through logosctl, as Scala's author's recipe has it
// (vpavlin/scala docs/headless-scheduler-recipe.md, scala 0.12.5): one
// isolated logosctl session per machine, its Loam root unlocked after every
// start. Every call prints {"status","result"} and result is usually JSON in
// a string, so it is read twice. Numbers and bools go bare: an int: prefix
// fails silently.
//
// listCalendars carries the calendar's read key (encryptionKey): it is read
// here and dropped, never logged, never shown to a model.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ScalaCtl is a Calendar over logosctl.
type ScalaCtl struct {
	Bin string   // logosctl
	Env []string // HOME and LOGOSCTL_CONFIG_DIR of this machine's session
	// run: tests, instead of the program.
	run func(ctx context.Context, args ...string) ([]byte, error)
}

func (c *ScalaCtl) call(module, method string, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	argv := append([]string{"call", module, method}, args...)
	var out []byte
	var err error
	if c.run != nil {
		out, err = c.run(ctx, argv...)
	} else {
		cmd := exec.CommandContext(ctx, c.Bin, argv...)
		cmd.Env = c.Env
		out, err = cmd.Output()
	}
	var r struct {
		Status  string          `json:"status"`
		Result  json.RawMessage `json:"result"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
	}
	if jerr := json.Unmarshal(out, &r); jerr != nil {
		if err != nil {
			return nil, fmt.Errorf("logosctl %s %s: %w", module, method, err)
		}
		return nil, fmt.Errorf("logosctl %s %s: %.200s", module, method, out)
	}
	if r.Status != "ok" {
		return nil, fmt.Errorf("logosctl %s %s: %s %s", module, method, r.Code, r.Message)
	}
	// A JSON document in a string: read it again.
	var s string
	if json.Unmarshal(r.Result, &s) == nil {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
			return json.RawMessage(s), nil
		}
		b, _ := json.Marshal(s)
		return b, nil
	}
	return r.Result, nil
}

// errorIn is an {"error":…} or {"ok":false,"error":…} answer.
func errorIn(b json.RawMessage) error {
	var e struct {
		Error string `json:"error"`
		OK    *bool  `json:"ok"`
	}
	if json.Unmarshal(b, &e) == nil && (e.Error != "" || (e.OK != nil && !*e.OK)) {
		if e.Error == "" {
			e.Error = "refused"
		}
		return errors.New(e.Error)
	}
	return nil
}

// State is the calendar's folded state, its events and who last wrote each.
func (c *ScalaCtl) State(calID string) (CalState, error) {
	b, err := c.call("scala", "listCalendars")
	if err != nil {
		return CalState{}, err
	}
	var cals []struct {
		ID      string            `json:"id"`
		Name    string            `json:"name"`
		Owner   string            `json:"owner"`
		Open    bool              `json:"open"`
		Collab  bool              `json:"collab"`
		Roles   map[string]string `json:"roles"`
		Invites map[string]any    `json:"invites"`
		Schema  json.RawMessage   `json:"schema"`
	}
	if err := json.Unmarshal(b, &cals); err != nil {
		return CalState{}, fmt.Errorf("listCalendars: %w", err)
	}
	st := CalState{ID: calID}
	found := false
	for _, k := range cals {
		if k.ID != calID {
			continue
		}
		found = true
		st.Name, st.Owner, st.Open, st.Collab, st.Roles = k.Name, k.Owner, k.Open, k.Collab, k.Roles
		// Any invite not yet claimed stops it; what it is for does not matter.
		for id := range k.Invites {
			st.Invites = append(st.Invites, id)
		}
		sort.Strings(st.Invites)
		// Who changed the name, fields or flags is not in the folded state:
		// "" is nobody known, so a change to them is always told.
		st.Meta = fmt.Sprintf("%s|%v|%v|%s", k.Name, k.Open, k.Collab, k.Schema)
	}
	if !found {
		return CalState{}, fmt.Errorf("this machine's Scala has no calendar %s: not joined yet, or the link was refused", calID)
	}
	b, err = c.call("scala", "listEvents", calID)
	if err != nil {
		return st, err
	}
	var evs []map[string]any
	if err := json.Unmarshal(b, &evs); err != nil {
		return st, fmt.Errorf("listEvents: %w", err)
	}
	for _, raw := range evs {
		e := calEventFrom(raw)
		e.Author = c.lastAuthor(calID, e.ID, raw)
		st.Events = append(st.Events, e)
	}
	sort.Slice(st.Events, func(i, j int) bool { return st.Events[i].ID < st.Events[j].ID })
	return st, nil
}

// calEventFrom is a listEvents entry: custom fields are flat on the event.
func calEventFrom(raw map[string]any) CalEvent {
	e := CalEvent{Fields: map[string]any{}}
	e.ID, _ = raw["id"].(string)
	e.Title, _ = raw["title"].(string)
	if ms, ok := raw["startTime"].(float64); ok {
		e.Start = time.UnixMilli(int64(ms))
	}
	if r, ok := raw["recur"].(map[string]any); ok {
		rc := &Recur{}
		rc.Freq, _ = r["freq"].(string)
		if iv, ok := r["interval"].(float64); ok {
			rc.Interval = int(iv)
		}
		if u, ok := r["until"].(float64); ok && u > 0 {
			rc.Until = time.UnixMilli(int64(u))
		}
		if rc.Freq != "" {
			e.Recur = rc
		}
	}
	for _, k := range []string{"session", "prompt", "checks", "maxCost", "timeoutMin", "enabled"} {
		if v, ok := raw[k]; ok {
			e.Fields[k] = v
		}
	}
	return e
}

// lastAuthor is who last wrote the event: its history's last entry
// (creatorId is only who first did).
func (c *ScalaCtl) lastAuthor(calID, eventID string, raw map[string]any) string {
	if b, err := c.call("scala", "getEventHistory", calID, eventID); err == nil {
		var h []struct {
			Author string `json:"author"`
			At     int64  `json:"at"`
		}
		if json.Unmarshal(b, &h) == nil && len(h) > 0 {
			sort.SliceStable(h, func(i, j int) bool { return h[i].At < h[j].At })
			return h[len(h)-1].Author
		}
	}
	a, _ := raw["creatorId"].(string)
	return a
}

// Due is the occurrences starting in [from, to), in tz.
func (c *ScalaCtl) Due(calID string, from, to time.Time, tz string) ([]Occurrence, error) {
	b, err := c.call("scala", "dueOccurrences", calID, strconv.FormatInt(from.UnixMilli(), 10),
		strconv.FormatInt(to.UnixMilli(), 10), tz)
	if err != nil {
		return nil, err
	}
	if err := errorIn(b); err != nil {
		return nil, fmt.Errorf("dueOccurrences: %w", err)
	}
	var raw []struct {
		EventID string `json:"eventId"`
		Occ     int64  `json:"occ"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("dueOccurrences: %w", err)
	}
	out := make([]Occurrence, 0, len(raw))
	for _, o := range raw {
		out = append(out, Occurrence{EventID: o.EventID, At: time.UnixMilli(o.Occ)})
	}
	return out, nil
}

// PostRun writes a run back on its event, under an id a restarted runner
// would make again.
func (c *ScalaCtl) PostRun(calID, eventID string, r RunRecord) error {
	item, _ := json.Marshal(map[string]any{"ns": "xyz.shrooms", "kind": "run", "target": eventID,
		"id": fmt.Sprintf("run-%s-%d", eventID, r.Occ.UnixMilli()),
		"data": map[string]any{"occ": r.Occ.UnixMilli(), "ranAt": r.Ran.UnixMilli(), "changed": r.Changed,
			"woke": r.Woke, "result": r.Result}})
	b, err := c.call("scala", "postExt", calID, string(item))
	if err != nil {
		return err
	}
	if err := errorIn(b); err != nil {
		return err
	}
	return c.signed()
}

// signed says whether writes went out: with the Loam root locked they are
// accepted and wait unsigned.
func (c *ScalaCtl) signed() error {
	b, err := c.call("scala", "hdState")
	if err != nil {
		return err
	}
	var st struct {
		Pending int    `json:"pending"`
		Error   string `json:"error"`
	}
	json.Unmarshal(b, &st)
	if st.Pending > 0 || st.Error != "" {
		return fmt.Errorf("written but not signed yet (%s, %d waiting): unlock this machine's Loam root", st.Error, st.Pending)
	}
	return nil
}

// Put writes an event as this machine's identity for the calendar.
func (c *ScalaCtl) Put(calID string, e CalEvent) (string, error) {
	id := e.ID
	if id == "" {
		var b [16]byte
		rand.Read(b[:])
		id = hex.EncodeToString(b[:])
	}
	ev := map[string]any{"title": e.Title, "startTime": e.Start.UnixMilli(), "endTime": e.Start.Add(15 * time.Minute).UnixMilli()}
	if e.Recur != nil {
		r := map[string]any{"freq": e.Recur.Freq, "interval": e.Recur.Interval}
		if !e.Recur.Until.IsZero() {
			r["until"] = e.Recur.Until.UnixMilli()
		}
		ev["recur"] = r
	}
	for k, v := range e.Fields {
		ev[k] = v
	}
	body, _ := json.Marshal(ev)
	b, err := c.call("scala", "putEvent", calID, id, string(body))
	if err != nil {
		return "", err
	}
	var got string
	if json.Unmarshal(b, &got) != nil || got == "" {
		return "", fmt.Errorf("putEvent refused it")
	}
	return got, c.signed()
}

// Delete removes an event.
func (c *ScalaCtl) Delete(calID, eventID string) error {
	b, err := c.call("scala", "deleteEvent", eventID)
	if err != nil {
		return err
	}
	if string(b) != "true" {
		return fmt.Errorf("deleteEvent refused it")
	}
	return c.signed()
}
