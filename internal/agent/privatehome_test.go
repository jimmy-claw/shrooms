package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A caged session's harness directory is a private one, with the owner's
// settings read-only and only its own conversation folder from the real
// one; nothing else of the owner's harness directory.
func TestACageSeesOnlyItsOwnConversations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	state := t.TempDir()
	m := newTestManager(t, state)
	proj := filepath.Join(home, "devel", "github.com", "x_y")
	os.MkdirAll(proj, 0o700)
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(filepath.Join(claudeDir, "projects", "-home-other"), 0o700)
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(claudeDir, "history.jsonl"), []byte("secret prompt\n"), 0o600)

	name := "box"
	s := &Session{dir: proj, harness: Claude{}, cage: &Cage{Container: "c1"}}
	s.label.Store(&name)
	ms := m.cageMounts(s, "claude")
	at := func(x mount) string {
		if x.at != "" {
			return x.at
		}
		return x.path
	}
	var got []string
	for _, x := range ms {
		if strings.HasPrefix(at(x), claudeDir) {
			got = append(got, strings.TrimPrefix(at(x), home)+map[bool]string{true: ":ro", false: ""}[x.ro]+" <- "+strings.TrimPrefix(x.path, home))
		}
	}
	joined := strings.Join(got, "\n")
	own := filepath.Join(claudeDir, "projects", "-"+strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(proj, "/"), "/", "-"), ".", "-"))
	own = strings.ReplaceAll(own, "x_y", "x-y")
	for _, want := range []string{
		"/.claude <- " + strings.TrimPrefix(filepath.Join(state, "cages", "c1.home", "claude"), home),
		"/.claude/settings.json:ro <- /.claude/settings.json",
		strings.TrimPrefix(own, home) + " <- " + strings.TrimPrefix(own, home),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-home-other") || strings.Contains(joined, "history.jsonl") || strings.Contains(joined, "/.claude <- /.claude") {
		t.Errorf("more of the owner's .claude than its own:\n%s", joined)
	}
	if fi, err := os.Stat(own); err != nil || !fi.IsDir() {
		t.Error("its conversation folder was not made")
	}

	// pi: the same, with its sessions folder.
	p := &Session{dir: proj, harness: Pi{}, cage: &Cage{Container: "c2"}}
	p.label.Store(&name)
	var pis []string
	for _, x := range m.cageMounts(p, "pi") {
		pis = append(pis, at(x)+" <- "+x.path)
	}
	pj := strings.Join(pis, "\n")
	if !strings.Contains(pj, filepath.Join(home, ".pi", "agent")+" <- "+filepath.Join(state, "cages", "c2.home", "pi")) ||
		!strings.Contains(pj, "/sessions/--"+strings.ReplaceAll(strings.TrimPrefix(proj, "/"), "/", "-")+"--") {
		t.Errorf("pi's mounts:\n%s", pj)
	}
}

func TestTheConversationFolderNames(t *testing.T) {
	if got := claudeProjectName("/home/v/devel/github.com/vpavlin/logos-vpn"); got != "-home-v-devel-github-com-vpavlin-logos-vpn" {
		t.Errorf("claude: %s", got)
	}
	if got := piSessionsName("/home/vpavlin/devel/github.com/vpavlin/lmao"); got != "--home-vpavlin-devel-github.com-vpavlin-lmao--" {
		t.Errorf("pi: %s", got)
	}
}

// A caged session's private home gets the owner's decisions about its own
// project and no other: pi's trust in it, and Claude Code's entry for it.
func TestACageCarriesItsOwnProjectsTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	state := t.TempDir()
	m := newTestManager(t, state)
	proj, other := filepath.Join(home, "foray-office"), filepath.Join(home, "secret-project")
	os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700)
	os.WriteFile(filepath.Join(home, ".pi", "agent", "trust.json"), []byte(`{"`+proj+`":true,"`+other+`":true}`), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"email":"me@x"},"projects":{"`+proj+`":{"enabledMcpjsonServers":["foray"]},"`+other+`":{"allowedTools":["x"]}}}`), 0o600)
	name := "fo"
	pi := &Session{dir: proj, harness: Pi{}, cage: &Cage{Container: "c1"}}
	pi.label.Store(&name)
	m.carryProjectTrust(pi, "c1")
	b, _ := os.ReadFile(filepath.Join(state, "cages", "c1.home", "pi", "trust.json"))
	if !strings.Contains(string(b), proj) || strings.Contains(string(b), other) {
		t.Errorf("pi's trust in the cage: %s", b)
	}
	cl := &Session{dir: proj, harness: Claude{}, cage: &Cage{Container: "c2"}}
	cl.label.Store(&name)
	os.MkdirAll(filepath.Join(state, "cages", "c2.home", "claude"), 0o700)
	os.WriteFile(filepath.Join(state, "cages", "c2.home", "claude", ".claude.json"), []byte(`{"numStartups":3}`), 0o600)
	m.carryProjectTrust(cl, "c2")
	b, _ = os.ReadFile(filepath.Join(state, "cages", "c2.home", "claude", ".claude.json"))
	if !strings.Contains(string(b), `"foray"`) || strings.Contains(string(b), other) || strings.Contains(string(b), "me@x") || !strings.Contains(string(b), "numStartups") {
		t.Errorf("Claude Code's config in the cage: %s", b)
	}
}
