package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A cage program is either an absolute path inside the cage, or a bare name on
// the cage's PATH — and the two go with different mounts. An absolute path is
// only there because CageMounts mounted its directory; a bare name is only
// there because the image installs it.
//
// This is the rule that a refactor has to keep, and it is a test because
// breaking it is invisible: nothing executes a caged pi in the suite, so
// Pi.CageProgram returning the HOST's absolute path — a path the cage does not
// have, since pi is installed in the image and nothing of the host's is
// mounted for it — passed every test while breaking every caged pi session.
// Found by review, not by the suite.
func TestACageProgramMatchesWhatTheCageHas(t *testing.T) {
	home := t.TempDir()
	f := CageFacts{
		Home:        home,
		Dir:         filepath.Join(home, "proj"),
		PrivateHome: filepath.Join(home, "private"),
		Bin:         "/opt/host-only/bin/the-program",
		Container:   "c1",
	}
	os.MkdirAll(f.PrivateHome, 0o700)

	for _, h := range []Harness{Claude{}, Pi{}, OpenCode{}} {
		cg, ok := h.(Cageable)
		if !ok {
			t.Errorf("%s does not implement Cageable, so it cannot be caged", h.Name())
			continue
		}
		prog, err := cg.CageProgram(f)
		if err != nil {
			// A harness whose host binary is not here may say so; it must not
			// then be started.
			continue
		}
		if prog == "" {
			t.Errorf("%s: an empty program", h.Name())
			continue
		}
		if !filepath.IsAbs(prog) {
			// A bare name: the image must provide it. Nothing of the host's
			// needs mounting for it, and the name must not be a path in
			// disguise.
			if strings.ContainsRune(prog, '/') {
				t.Errorf("%s: %q is neither a bare name nor an absolute path", h.Name(), prog)
			}
			continue
		}
		// Absolute: CageMounts must have put its directory in the cage.
		dir := filepath.Dir(prog)
		var mounted bool
		for _, m := range cg.CageMounts(f) {
			at := m.at
			if at == "" {
				at = m.path
			}
			if at == dir || strings.HasPrefix(dir+"/", at+"/") || strings.HasPrefix(at+"/", dir+"/") {
				mounted = true
			}
		}
		if !mounted {
			t.Errorf("%s: CageProgram is %q, an absolute path, but CageMounts mounts nothing that provides it — the cage will fail at exec",
				h.Name(), prog)
		}
	}
}

// pi specifically: the cage's own pi, by name. The image installs it, so the
// host's path must not be used — it is not in the cage.
func TestAPiCageUsesTheImagesOwnPi(t *testing.T) {
	prog, err := (Pi{}).CageProgram(CageFacts{Bin: "/home/owner/.local/bin/pi"})
	if err != nil {
		t.Fatal(err)
	}
	if prog != (Pi{}).Bin() {
		t.Errorf("pi in a cage is %q, want the image's own %q", prog, (Pi{}).Bin())
	}
	if filepath.IsAbs(prog) {
		t.Errorf("the host's path is not in the cage: %q", prog)
	}
}
