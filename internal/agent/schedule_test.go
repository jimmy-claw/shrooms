package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeCal is Scala as the runner sees it: one calendar's folded state, what
// is due, and the runs written back.
type fakeCal struct {
	state CalState
	due   []Occurrence
	runs  []RunRecord
}

func (f *fakeCal) State(string) (CalState, error) { return f.state, nil }
func (f *fakeCal) Due(_ string, from, to time.Time, _ string) ([]Occurrence, error) {
	var out []Occurrence
	for _, o := range f.due {
		if o.At.After(from) && !o.At.After(to) {
			out = append(out, o)
		}
	}
	return out, nil
}
func (f *fakeCal) PostRun(_, _ string, r RunRecord) error { f.runs = append(f.runs, r); return nil }

const (
	owner   = "0xowner"
	session = "0xsession"
)

func goodState(events ...CalEvent) CalState {
	return CalState{ID: "cal1", Name: "proj", Owner: owner, MetaAuthor: owner,
		Roles: map[string]string{session: "editor"}, Events: events}
}

func newScheduled(t *testing.T, cal *fakeCal) (*Manager, *Session) {
	t.Helper()
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	m.Schedules = &Schedules{Cal: cal, Owner: owner, Sessions: map[string]SessionCalendar{"proj": {Calendar: "cal1", Self: session}}}
	return m, s
}

func scheduleNotes(s *Session) []string {
	var out []string
	for _, e := range sinceAll(s) {
		if e.Kind == "schedule" {
			out = append(out, string(e.Data))
		}
	}
	return out
}

func messages(s *Session) []string {
	var out []string
	for _, e := range sinceAll(s) {
		if e.Kind == "message" {
			var d struct{ Text string }
			json.Unmarshal(e.Data, &d)
			out = append(out, d.Text)
		}
	}
	return out
}

// A routine that is due wakes its session once, with its prompt, and the run
// is written back; the same occurrence seen again is not run again.
func TestARoutineThatIsDueWakesItsSessionOnce(t *testing.T) {
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
	cal := &fakeCal{state: goodState(CalEvent{ID: "e1", Title: "PRs", Author: owner, Fields: map[string]any{"prompt": "review new PRs"}}),
		due: []Occurrence{{EventID: "e1", At: at}}}
	m, s := newScheduled(t, cal)
	m.ScheduleTick(at.Add(-time.Minute))
	m.ScheduleTick(at.Add(time.Minute))
	m.ScheduleTick(at.Add(2 * time.Minute))
	if got := messages(s); len(got) != 1 || !strings.HasPrefix(got[0], "[scheduled: PRs]\nreview new PRs") {
		t.Fatalf("messages: %q", got)
	}
	if len(cal.runs) != 1 || !cal.runs[0].Woke || !cal.runs[0].Occ.Equal(at) {
		t.Fatalf("runs written back: %+v", cal.runs)
	}
}

// Checks run first; the model is woken only when what they print changed.
func TestChecksWakeTheModelOnlyOnAChange(t *testing.T) {
	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
	cal := &fakeCal{state: goodState(CalEvent{ID: "e1", Title: "PRs", Author: owner,
		Fields: map[string]any{"prompt": "look", "checks": "gh pr list"}})}
	m, s := newScheduled(t, cal)
	out := "#41 new"
	m.checkRunner = func(_ context.Context, _ *Session, checks string) (string, error) { return out, nil }
	tick := func(d time.Duration) {
		cal.due = []Occurrence{{EventID: "e1", At: base.Add(d)}}
		m.ScheduleTick(base.Add(d - time.Minute))
		m.ScheduleTick(base.Add(d + time.Minute))
	}
	tick(0)
	tick(time.Hour) // the same output
	out = "#41 new\n#42 new"
	tick(2 * time.Hour)
	got := messages(s)
	if len(got) != 2 || !strings.Contains(got[1], "#42 new") {
		t.Fatalf("woken %d times: %q", len(got), got)
	}
	if len(cal.runs) != 3 || cal.runs[1].Changed || cal.runs[1].Woke {
		t.Fatalf("runs: %+v", cal.runs)
	}
}

// A calendar that someone other than the owner and the session could write
// to is not run from, and the session says why; fixed, it runs again.
func TestACalendarOthersCanWriteToIsNotRun(t *testing.T) {
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
	ev := CalEvent{ID: "e1", Title: "PRs", Author: owner, Fields: map[string]any{"prompt": "look"}}
	for name, st := range map[string]CalState{
		"another owner": {Owner: "0xother", Roles: map[string]string{session: "editor"}, Events: []CalEvent{ev}},
		"open":          {Owner: owner, Open: true, Roles: map[string]string{session: "editor"}, Events: []CalEvent{ev}},
		"an invite":     {Owner: owner, Invites: []string{"editor"}, Roles: map[string]string{session: "editor"}, Events: []CalEvent{ev}},
		"a 2nd editor":  {Owner: owner, Roles: map[string]string{session: "editor", "0xx": "editor"}, Events: []CalEvent{ev}},
		"a viewer":      {Owner: owner, Roles: map[string]string{session: "editor", "0xv": "viewer"}, Events: []CalEvent{ev}},
	} {
		cal := &fakeCal{state: st, due: []Occurrence{{EventID: "e1", At: at}}}
		m, s := newScheduled(t, cal)
		m.ScheduleTick(at.Add(-time.Minute))
		m.ScheduleTick(at.Add(time.Minute))
		if len(messages(s)) != 0 || len(cal.runs) != 0 {
			t.Errorf("%s: ran", name)
		}
		if n := scheduleNotes(s); len(n) == 0 || !strings.Contains(n[0], "nothing in its schedule runs") {
			t.Errorf("%s: not said why: %q", name, n)
		}
	}
}

// Every change the owner did not make is told; his own are not.
func TestChangesTheOwnerDidNotMakeAreTold(t *testing.T) {
	cal := &fakeCal{state: goodState(CalEvent{ID: "e1", Title: "PRs", Author: owner})}
	m, s := newScheduled(t, cal)
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
	m.ScheduleTick(now)
	cal.state.Events = append(cal.state.Events, CalEvent{ID: "e2", Title: "Cars", Author: session})
	cal.state.Events[0] = CalEvent{ID: "e1", Title: "PRs", Author: owner, Fields: map[string]any{"prompt": "x"}}
	m.ScheduleTick(now.Add(time.Minute))
	cal.state.Roles = map[string]string{session: "editor", "0xx": "viewer"}
	cal.state.Events = cal.state.Events[:1]
	m.ScheduleTick(now.Add(2 * time.Minute))
	notes := strings.Join(scheduleNotes(s), "\n")
	for _, want := range []string{`added \"Cars\"`, `removed \"Cars\"`, "who may write to it changed"} {
		if !strings.Contains(notes, want) {
			t.Errorf("not told %s:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, `changed \"PRs\"`) {
		t.Errorf("told of the owner's own change:\n%s", notes)
	}
}

// Outside a cage, checks the session wrote do not run: they would be the
// owner's shell with no model in the loop. Its prompt still does.
func TestAnUncagedSessionsOwnChecksDoNotRun(t *testing.T) {
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
	cal := &fakeCal{state: goodState(CalEvent{ID: "e1", Title: "PRs", Author: session,
		Fields: map[string]any{"prompt": "look", "checks": "rm -rf ~"}}), due: []Occurrence{{EventID: "e1", At: at}}}
	m, s := newScheduled(t, cal)
	ran := false
	m.checkRunner = func(context.Context, *Session, string) (string, error) { ran = true; return "", nil }
	m.ScheduleTick(at.Add(-time.Minute))
	m.ScheduleTick(at.Add(time.Minute))
	if ran {
		t.Fatal("an uncaged session's own checks ran")
	}
	if got := messages(s); len(got) != 1 || strings.Contains(got[0], "rm -rf") {
		t.Fatalf("the prompt alone should wake it: %q", got)
	}
	if n := strings.Join(scheduleNotes(s), "\n"); !strings.Contains(n, "do not run") {
		t.Errorf("not said: %s", n)
	}
}

// A day's wakes are capped, and quiet hours run nothing.
func TestWakesAreCappedAndQuietHoursAreQuiet(t *testing.T) {
	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
	cal := &fakeCal{state: goodState(CalEvent{ID: "e1", Title: "ping", Author: owner, Fields: map[string]any{"prompt": "hi"}})}
	m, s := newScheduled(t, cal)
	m.Schedules.MaxWakesPerDay = 2
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		cal.due = []Occurrence{{EventID: "e1", At: at}}
		m.ScheduleTick(at.Add(-time.Minute))
		m.ScheduleTick(at.Add(time.Minute))
	}
	if n := len(messages(s)); n != 2 {
		t.Fatalf("woken %d times, the cap is 2", n)
	}
	if r := cal.runs[3].Result; !strings.Contains(r, "runs today already") {
		t.Errorf("the capped run says: %q", r)
	}
	m.Schedules.QuietFrom, m.Schedules.QuietTo = 22, 7
	if !m.Schedules.quiet(time.Date(2026, 10, 20, 23, 0, 0, 0, time.Local)) || m.Schedules.quiet(base) {
		t.Error("quiet hours across midnight")
	}
}

func sinceAll(s *Session) []Event {
	evs, ch := s.Since(0)
	s.Unsubscribe(ch)
	return evs
}
