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
