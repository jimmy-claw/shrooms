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
//
// What this does NOT see: Args(o), where a harness could embed a host path as
// an ARGUMENT while CageProgram correctly returns a bare name — Claude Code's
// does exactly that, putting the agent's own path (o.MCP) inside
// --mcp-config's JSON. That one is safe, but for a different reason: it is
// mounted by cageMounts' universal section, outside any harness's CageMounts.
// So a future harness embedding an UNMOUNTED host path in Args would slip
// past this test. Checking it here would mean teaching the test every shape a
// harness's arguments can take, which is the same mistake as trusting a fake.
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
		if !mountProvides(cg.CageMounts(f), filepath.Dir(prog)) {
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

// mountProvides says whether these mounts put dir — and so a program in it —
// inside the cage. The mount is dir itself, or dir is nested under it.
//
// NOT the reverse, and that is the whole point of having this in one place:
// a mount of a SUBDIRECTORY of dir does not put dir's own files in the cage.
// Review found the test's first version accepting /opt/app/plugins as
// "providing" /opt/app/the-program, which would have made the next harness's
// real breakage pass.
func mountProvides(ms []mount, dir string) bool {
	for _, m := range ms {
		at := m.at
		if at == "" {
			at = m.path
		}
		if at == dir || strings.HasPrefix(dir+"/", at+"/") {
			return true
		}
	}
	return false
}

// The matcher, both ways round: a directory mount provides a program in it; a
// mount of a subdirectory does not.
func TestAMountProvidesOnlyWhatIsInIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		ms   []mount
		dir  string
		want bool
	}{
		{"the directory itself", []mount{{path: "/host/app", at: "/opt/app"}}, "/opt/app", true},
		{"the directory, no at", []mount{{path: "/opt/app"}}, "/opt/app", true},
		{"a parent of it", []mount{{path: "/host/bin", at: "/opt"}}, "/opt/app", true},
		{"a subdirectory of it", []mount{{path: "/host/plugins", at: "/opt/app/plugins"}}, "/opt/app", false},
		{"a sibling", []mount{{path: "/host/other", at: "/opt/other"}}, "/opt/app", false},
		{"nothing", nil, "/opt/app", false},
		// A prefix that is not a path boundary must not count.
		{"a name that merely starts the same", []mount{{path: "/host/x", at: "/opt/app-extra"}}, "/opt/app", false},
	} {
		if got := mountProvides(tc.ms, tc.dir); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}
