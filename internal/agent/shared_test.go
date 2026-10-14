package agent

// shared_test.go — the folder every cage sees the same copy of.
//
// Two things here are load-bearing and neither is visible from the call site:
// a named volume has no path to stat, so the filter that drops mounts which are
// not there must keep it; and the folder is read-write for a caged session
// (the owner's own agent) but read-only for a sealed cage (code nobody vouches
// for). Both are asserted, and each has a case that fails if the rule is lost
// rather than if it is merely written down.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheSharedFolderSurvivesTheMissingMountFilter(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "not-there")
	ms := keepMounts([]mount{
		{path: sharedVolume, at: CageSharedDir, volume: true},
		{path: gone, at: "/gone"},
	})
	// The negative control: a path that really is absent IS dropped, so this
	// fails if the filter stops filtering, and fails differently if it drops
	// the volume. Removing the x.volume case drops the volume and keeps the
	// count at 1 - the path check below is what catches that.
	if len(ms) != 1 {
		t.Fatalf("kept %d mounts, want 1: %+v", len(ms), ms)
	}
	if ms[0].path != sharedVolume || !ms[0].volume {
		t.Fatalf("the shared volume was dropped and %q kept instead", ms[0].path)
	}
}

func TestACagedSessionGetsTheSharedFolderReadWrite(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	s := &Session{dir: filepath.Join(state, "session"), cage: &Cage{}}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	found := findShared(m.cageMounts(s, "pi"))
	if found == nil {
		t.Fatal("a caged session does not get the shared folder at all")
	}
	if found.ro {
		t.Fatal("a caged session's shared folder is read-only: its own agent cannot work in it")
	}
	if found.at != CageSharedDir {
		t.Fatalf("mounted at %q, want %q", found.at, CageSharedDir)
	}
}

func TestASealedCageGetsTheSharedFolderReadOnly(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	if err := os.WriteFile(filepath.Join(state, SealedTokenFile), []byte("not-a-real-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "sharedtest"
	s := &Session{dir: filepath.Join(state, "session"),
		harness: missingHarness{name: "claude"},
		cage:    &Cage{Container: "sharedtest", Outbox: filepath.Join(state, "outbox")}}
	s.label.Store(&name)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ms, err := m.sealedMounts(s)
	if err != nil {
		t.Skipf("a sealed cage cannot be built here (%v); the caged case above still runs", err)
	}
	found := findShared(ms)
	if found == nil {
		t.Fatal("a sealed cage does not get the shared folder: it is told to read data it cannot see")
	}
	if !found.ro {
		t.Fatal("a sealed cage's shared folder is READ-WRITE: code nobody vouches for can poison it for every other cage")
	}
}

func findShared(ms []mount) *mount {
	for i := range ms {
		if ms[i].path == sharedVolume {
			return &ms[i]
		}
	}
	return nil
}
