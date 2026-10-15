package agent

import (
	"slices"
	"strings"
	"testing"
)

// A session's own model replaces the machine's --model and --provider for pi,
// keeps the rest of the machine's arguments, and goes to Claude Code as
// --model; without one, the machine's arguments are used as they are.
func TestASessionsModelIsGivenToItsHarness(t *testing.T) {
	p := Pi{Extra: []string{"--provider", "venice", "--model", "deepseek-v4-1-flash", "--thinking", "low", "--model=x"}}
	got := strings.Join(p.Args(StartOptions{Model: "venice/claude-haiku-5-5"}), " ")
	if got != "--mode rpc --thinking low --model venice/claude-haiku-5-5" {
		t.Errorf("pi with a model of its own: %s", got)
	}
	if got := strings.Join(p.Args(StartOptions{}), " "); got != "--mode rpc "+strings.Join(p.Extra, " ") {
		t.Errorf("pi without: %s", got)
	}
	c := Claude{}.Args(StartOptions{Model: "haiku"})
	if i := slices.Index(c, "--model"); i < 0 || c[i+1] != "haiku" {
		t.Errorf("claude: %v", c)
	}
	if slices.Contains(Claude{}.Args(StartOptions{}), "--model") {
		t.Error("claude is given a model nobody chose")
	}
}

// The model chosen is kept with the session, listed, given at its next start
// after the running process is stopped, and refused for nonsense.
func TestASessionsModelIsKept(t *testing.T) {
	_, s := newPiSession(t)
	s.Send("hello", "")
	waitFor(t, s, 0, isResult)
	if err := s.SetModel("venice/claude-haiku-5-5", "phone"); err != nil {
		t.Fatal(err)
	}
	if in := s.Info(); in.ModelSetting != "venice/claude-haiku-5-5" || in.Running {
		t.Fatalf("after choosing a model: setting %q, running %v", in.ModelSetting, in.Running)
	}
	m2 := newTestManager(t, s.m.dir)
	if s2, _ := m2.Get("pi-proj"); s2 == nil || s2.Info().ModelSetting != "venice/claude-haiku-5-5" {
		t.Fatal("the model chosen is not kept when the agent starts again")
	}
	for _, bad := range []string{"--help", "a b"} {
		if err := s.SetModel(bad, "phone"); err == nil {
			t.Errorf("%q taken as a model", bad)
		}
	}
	if err := s.SetModel("", "phone"); err != nil || s.Info().ModelSetting != "" {
		t.Errorf("back to the default: %v %q", err, s.Info().ModelSetting)
	}
}
