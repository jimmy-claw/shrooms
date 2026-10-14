package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cage is TOLD where the shared folder is. A folder nobody is told about
// is a folder nobody uses, which is the whole failure mode this guards.
func TestACageIsToldWhereTheSharedFolderIs(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	name := "envtest"
	s := &Session{dir: filepath.Join(state, "session"), harness: missingHarness{name: "pi"}, cage: &Cage{}}
	s.label.Store(&name)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := m.cageEnv(s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "SHROOMS_SHARED="+CageSharedDir) {
		t.Fatalf("a caged session is not told about %s:\n%s", CageSharedDir, b)
	}
}
