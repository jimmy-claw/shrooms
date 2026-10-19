package agent

// A session's schedule (ADR-064): its own Scala calendar, which the owner and
// that session co-own. The agent reads it, runs what is due for the session,
// writes each run back on its event, and tells the owner of every change he
// did not make. It keeps no schedule of its own: what runs is what the
// calendar says, and nothing else.
//
// Scala is reached through Calendar; scalactl.go speaks to a scala core over
// logosctl, and the tests use a fake with the same shapes.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CalEvent is one routine as the calendar's folded state has it.
type CalEvent struct {
	ID     string         `json:"id"`
	Title  string         `json:"title"`
	Author string         `json:"author"` // who last wrote it, its signed address
	Fields map[string]any `json:"fields"` // the schema's fields: prompt, checks, …
}

func (e CalEvent) text(k string) string {
	v, _ := e.Fields[k].(string)
	return strings.TrimSpace(v)
}

func (e CalEvent) number(k string) float64 {
	switch v := e.Fields[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

// enabled: on unless the field says off.
func (e CalEvent) enabled() bool {
	v, ok := e.Fields["enabled"].(bool)
	return !ok || v
}

// CalState is a calendar's folded state: what Scala's fold leaves on every
// device, never the raw log.
type CalState struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Owner      string            `json:"owner"`
	Open       bool              `json:"open"`
	Collab     bool              `json:"collab"`
	MetaAuthor string            `json:"meta_author"` // who last changed its name, fields or flags
	Roles      map[string]string `json:"roles"`       // address → editor or viewer; the owner is not here
	Invites    []string          `json:"invites"`     // the roles of invites not yet claimed
	Events     []CalEvent        `json:"events"`
}

// Occurrence is one time an event is due.
type Occurrence struct {
	EventID string    `json:"eventId"`
	At      time.Time `json:"occ"`
}

// RunRecord is a run written back on its event (an ext item, ns xyz.shrooms).
type RunRecord struct {
	Occ     time.Time `json:"occ"`
	Ran     time.Time `json:"ran"`
	Changed bool      `json:"changed"` // its checks saw something new; true when it has none
	Woke    bool      `json:"woke"`    // the model was asked
	Result  string    `json:"result"`  // one line
}

// Calendar is Scala, as the runner needs it.
type Calendar interface {
	State(calID string) (CalState, error)
	Due(calID string, from, to time.Time, tz string) ([]Occurrence, error)
	PostRun(calID, eventID string, r RunRecord) error
}

// Schedules is this machine's: Scala, the owner it trusts, and which calendar
// is whose.
type Schedules struct {
	Cal Calendar
	// Owner is the owner's main Scala address (loam:main): every schedule's
	// owner must be it.
	Owner string
	// TZ the routines' times are in ("" for this machine's).
	TZ string
	// Sessions: session name → its calendar and the address Scala derived for
	// it there.
	Sessions map[string]SessionCalendar
	// Per session and day: at most this many runs that wake the model, and
	// no run once its usage that day has cost this much (0: no limit).
	MaxWakesPerDay int
	MaxCostPerDay  float64
	// QuietFrom and QuietTo are hours (0–23) when nothing runs; equal for none.
	QuietFrom, QuietTo int
}

// SessionCalendar is a session's calendar and its own address in it.
type SessionCalendar struct {
	Calendar string `json:"calendar"`
	Self     string `json:"self"`
}

// runnable says why a calendar cannot be run from, or nil. Each rule closes
// a way for someone other than the owner and the session to schedule work.
func (sc *Schedules) runnable(st CalState, self string) error {
	switch {
	case sc.Owner == "" || st.Owner != sc.Owner:
		return fmt.Errorf("its owner is not the one this machine trusts")
	case st.Open || st.Collab:
		return errors.New("it is open to others: only a closed calendar is run")
	case len(st.Invites) > 0:
		return errors.New("it has an invite nobody has claimed yet, which whoever has the link could")
	}
	if len(st.Roles) != 1 || st.Roles[self] != "editor" {
		return errors.New("someone besides the owner and this session can write to it")
	}
	return nil
}

// changesBy is what changed between two states that the owner did not do,
// in words, for him (ADR-064: from the state, not from the tool, so a change
// made by anything holding the key is told too).
func changesBy(owner string, before, after CalState) []string {
	var out []string
	if after.MetaAuthor != owner && (before.Name != after.Name || before.Open != after.Open || before.Collab != after.Collab ||
		before.MetaAuthor != after.MetaAuthor) {
		out = append(out, "the calendar's name, fields or who may join it changed")
	}
	if !sameRoles(before, after) {
		out = append(out, "who may write to it changed")
	}
	old := map[string]CalEvent{}
	for _, e := range before.Events {
		old[e.ID] = e
	}
	seen := map[string]bool{}
	for _, e := range after.Events {
		seen[e.ID] = true
		o, had := old[e.ID]
		switch {
		case e.Author == owner:
		case !had:
			out = append(out, fmt.Sprintf("added %q", e.Title))
		case !sameEvent(o, e):
			out = append(out, fmt.Sprintf("changed %q", e.Title))
		}
	}
	for _, e := range before.Events {
		if !seen[e.ID] {
			out = append(out, fmt.Sprintf("removed %q", e.Title))
		}
	}
	return out
}

func sameRoles(a, b CalState) bool {
	if len(a.Roles) != len(b.Roles) || len(a.Invites) != len(b.Invites) {
		return false
	}
	for k, v := range a.Roles {
		if b.Roles[k] != v {
			return false
		}
	}
	return true
}

func sameEvent(a, b CalEvent) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// scheduleState is what the runner keeps per session between ticks.
type scheduleState struct {
	Last    time.Time            `json:"last"`    // the end of the last window looked at
	Seen    *CalState            `json:"seen"`    // the state last compared
	Done    map[string]time.Time `json:"done"`    // eventID@occ → when it ran
	Outputs map[string]string    `json:"outputs"` // eventID → its checks' last output, hashed
	Paused  string               `json:"paused"`  // why it is not run, as last said
	Day     string               `json:"day"`
	Wakes   int                  `json:"wakes"`
}

func (m *Manager) schedulePath(session string) string {
	return filepath.Join(m.dir, "schedules", session+".json")
}

func (m *Manager) loadSchedule(session string) *scheduleState {
	st := &scheduleState{Done: map[string]time.Time{}, Outputs: map[string]string{}}
	if b, err := os.ReadFile(m.schedulePath(session)); err == nil {
		json.Unmarshal(b, st)
	}
	if st.Done == nil {
		st.Done = map[string]time.Time{}
	}
	if st.Outputs == nil {
		st.Outputs = map[string]string{}
	}
	return st
}

func (m *Manager) saveSchedule(session string, st *scheduleState) {
	os.MkdirAll(filepath.Dir(m.schedulePath(session)), 0o700)
	b, _ := json.Marshal(st)
	tmp := m.schedulePath(session) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, m.schedulePath(session))
	}
}

// ScheduleTick looks at every session's calendar once: what changed, and what
// is due between the last tick and now.
func (m *Manager) ScheduleTick(now time.Time) {
	sc := m.Schedules
	if sc == nil || sc.Cal == nil {
		return
	}
	names := make([]string, 0, len(sc.Sessions))
	for n := range sc.Sessions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		s, ok := m.Get(name)
		if !ok {
			continue
		}
		m.scheduleSession(s, sc.Sessions[name], now)
	}
}

func (m *Manager) scheduleSession(s *Session, cal SessionCalendar, now time.Time) {
	sc := m.Schedules
	st := m.loadSchedule(s.Name())
	defer m.saveSchedule(s.Name(), st)
	state, err := sc.Cal.State(cal.Calendar)
	if err != nil {
		m.log.Warn("schedule: the calendar could not be read", "session", s.Name(), "err", err)
		return
	}
	if st.Seen != nil {
		if ch := changesBy(sc.Owner, *st.Seen, state); len(ch) > 0 {
			s.mu.Lock()
			s.record("schedule", "schedule", map[string]any{"changed": ch})
			s.mu.Unlock()
		}
	}
	st.Seen = &state
	if why := sc.runnable(state, cal.Self); why != nil {
		if st.Paused != why.Error() {
			st.Paused = why.Error()
			s.mu.Lock()
			s.record("schedule", "schedule", map[string]any{"paused": "nothing in its schedule runs: " + st.Paused})
			s.mu.Unlock()
		}
		st.Last = now
		return
	}
	if st.Paused != "" {
		st.Paused = ""
		s.mu.Lock()
		s.record("schedule", "schedule", map[string]any{"resumed": true})
		s.mu.Unlock()
	}
	from := st.Last
	if from.IsZero() || now.Sub(from) > 24*time.Hour {
		// First look, or the agent was off: not a day of missed runs at once.
		from = now.Add(-10 * time.Minute)
	}
	st.Last = now
	occs, err := sc.Cal.Due(cal.Calendar, from, now, sc.TZ)
	if err != nil {
		m.log.Warn("schedule: what is due could not be read", "session", s.Name(), "err", err)
		return
	}
	byID := map[string]CalEvent{}
	for _, e := range state.Events {
		byID[e.ID] = e
	}
	for _, o := range occs {
		e, ok := byID[o.EventID]
		key := o.EventID + "@" + o.At.UTC().Format(time.RFC3339)
		if !ok || !st.Done[key].IsZero() || !e.enabled() {
			continue
		}
		st.Done[key] = now
		rec := m.runRoutine(s, sc, st, e, now)
		rec.Occ = o.At
		if err := sc.Cal.PostRun(cal.Calendar, e.ID, rec); err != nil {
			m.log.Warn("schedule: a run could not be written back", "session", s.Name(), "err", err)
		}
	}
	// Keep a month of what ran.
	for k, t := range st.Done {
		if now.Sub(t) > 31*24*time.Hour {
			delete(st.Done, k)
		}
	}
}

// runRoutine runs one occurrence: its checks, and the model if they saw
// something new or it has none.
func (m *Manager) runRoutine(s *Session, sc *Schedules, st *scheduleState, e CalEvent, now time.Time) RunRecord {
	rec := RunRecord{Ran: now, Changed: true}
	day := now.Format("2006-01-02")
	if st.Day != day {
		st.Day, st.Wakes = day, 0
	}
	if sc.quiet(now) {
		rec.Result = "quiet hours: not run"
		return rec
	}
	checks := e.text("checks")
	caged := s.Info().Cage != nil
	diff := ""
	if checks != "" {
		// Checks run with no model in the loop. In a cage, whoever wrote
		// them; outside one, they would be the owner's shell, so only his.
		if !caged && e.Author != sc.Owner {
			checks = ""
			s.mu.Lock()
			s.record("schedule", "schedule", map[string]any{"routine": e.Title,
				"note": "its checks were written by the session, which is not caged: they do not run; the prompt alone does"})
			s.mu.Unlock()
		}
	}
	if checks != "" {
		timeout := time.Duration(e.number("timeoutMin")) * time.Minute
		if timeout <= 0 || timeout > 30*time.Minute {
			timeout = 5 * time.Minute
		}
		out, err := m.runChecks(s, checks, timeout)
		if err != nil {
			out += "\n(the checks failed: " + err.Error() + ")"
		}
		h := sha256.Sum256([]byte(out))
		sum := hex.EncodeToString(h[:])
		if st.Outputs[e.ID] == sum {
			rec.Changed = false
			rec.Result = "checks ran: nothing changed"
			return rec
		}
		st.Outputs[e.ID] = sum
		diff = out
	}
	prompt := e.text("prompt")
	if prompt == "" && diff == "" {
		rec.Result = "nothing to ask: no prompt"
		return rec
	}
	if sc.MaxWakesPerDay > 0 && st.Wakes >= sc.MaxWakesPerDay {
		rec.Result = fmt.Sprintf("not woken: %d runs today already", st.Wakes)
		return rec
	}
	if limit := sc.MaxCostPerDay; limit > 0 {
		if spent := m.costToday(s.Name(), day); spent >= limit {
			rec.Result = fmt.Sprintf("not woken: $%.2f spent today, the limit is $%.2f", spent, limit)
			return rec
		}
	}
	if c := e.number("maxCost"); c > 0 {
		if spent := m.costToday(s.Name(), day); spent >= c {
			rec.Result = fmt.Sprintf("not woken: $%.2f spent today, this routine's limit is $%.2f", spent, c)
			return rec
		}
	}
	text := "[scheduled: " + e.Title + "]"
	if prompt != "" {
		text += "\n" + prompt
	}
	if diff != "" {
		if len(diff) > 6000 {
			diff = diff[:6000] + "\n…(cut)"
		}
		text += "\n\nIts checks found this, which differs from last time:\n```\n" + diff + "\n```"
	}
	if _, err := s.SendID(text, "schedule", "sched-"+e.ID+"-"+now.UTC().Format("20060102T1504")); err != nil {
		rec.Result = "could not wake it: " + err.Error()
		return rec
	}
	st.Wakes++
	rec.Woke = true
	rec.Result = "woken"
	return rec
}

func (sc *Schedules) quiet(now time.Time) bool {
	if sc.QuietFrom == sc.QuietTo {
		return false
	}
	h := now.Hour()
	if sc.QuietFrom < sc.QuietTo {
		return h >= sc.QuietFrom && h < sc.QuietTo
	}
	return h >= sc.QuietFrom || h < sc.QuietTo
}

func (m *Manager) costToday(session, day string) float64 {
	var c float64
	for _, u := range m.Usage(day) {
		if u.Session == session && u.Day == day {
			c += u.CostUSD
		}
	}
	return c
}

// runChecks runs a routine's commands in the session's cage, or its folder
// for one that is not caged, and returns what they wrote.
func (m *Manager) runChecks(s *Session, checks string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()
	if m.checkRunner != nil {
		return m.checkRunner(ctx, s, checks)
	}
	s.mu.Lock()
	cage := s.cage
	s.mu.Unlock()
	if cage != nil {
		r, err := m.prepareCage(s, m.binOf(s.harness.Name()))
		if err != nil {
			return "", err
		}
		return r.c.run(ctx, "exec", "-w", s.dir, cage.Container, "sh", "-c", checks)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", checks)
	cmd.Dir = s.dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
