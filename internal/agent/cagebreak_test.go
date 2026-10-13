package agent

// cagebreak_test.go — an AUTHORISED attempt to break OUT of a sealed cage
// (ADR-045), written for Václav on 2026-10-13.
//
// It does not re-implement the boundary. It calls the SAME code the agent
// calls — sealedSeccomp() for the profile, createArgs() for the container,
// closeAgentPort() for the nftables ruleset — and then probes from inside the
// container it made. A re-implementation would test my reading of the code;
// this tests the code.
//
//	SHROOMS_REAL_CAGE=1 SHROOMS_CAGE_TEST_IMAGE=<a local image> \
//	  go test ./internal/agent/ -run TestSealedCageBoundary -v
//
// The image is passed in because this machine has no claude cage image, and
// building one here is not worth it: the boundary is made of the seccomp
// profile and the nft ruleset, neither of which depends on the image.
//
// This is READ-ONLY in effect. It creates one container, and removes it.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealedCageBoundary(t *testing.T) {
	if os.Getenv("SHROOMS_REAL_CAGE") != "1" {
		t.Skip("SHROOMS_REAL_CAGE=1 runs it")
	}
	image := os.Getenv("SHROOMS_CAGE_TEST_IMAGE")
	if image == "" {
		t.Skip("SHROOMS_CAGE_TEST_IMAGE=<a local image> runs it")
	}
	probe := os.Getenv("SHROOMS_CAGE_PROBE")
	if probe == "" {
		probe = filepath.Join("testdata", "sealed-cage-probe.sh") // the one in the repo
	}
	if _, err := os.Stat(probe); err != nil {
		t.Skipf("no probe script at %s", probe)
	}

	state := t.TempDir()
	m := newTestManager(t, state)
	if m.Cages = NewCages(""); m.Cages == nil {
		t.Fatal("no podman on this machine")
	}
	c := m.Cages
	// Resource limits need the cgroup v2 memory controller delegated to this user session, which
	// pi5's is not: crun fails with "opening file `memory.max` for writing: No such file or directory".
	// Memory, CPUs and Pids are limits, not part of the boundary under test, so drop them here.
	c.Memory, c.CPUs, c.Pids = "", "", 0
	ctx := context.Background()

	// The REAL profile: derived from podman's default by the agent's own code.
	prof, err := m.sealedSeccomp()
	if err != nil {
		t.Fatalf("the agent's own sealed seccomp profile: %v", err)
	}
	t.Logf("sealed seccomp profile written by sealedSeccomp(): %s", prof)

	name := "shrooms-cagebreak"
	_ = exec.Command(c.Podman, "rm", "-f", name).Run()
	t.Cleanup(func() { _ = exec.Command(c.Podman, "rm", "-f", name).Run() })

	uploads := filepath.Join(state, "uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	// The shape of a sealed cage's mounts: the project read-write, what was sent
	// to the session read-only. (The rest — its own ~/.claude, the outbox, the
	// socket — belong to the agent's plumbing, not to the boundary.)
	ms := []mount{{path: proj}, {path: uploads, ro: true}}

	// The REAL container, by the agent's own argument builder, plus the two
	// args the agent adds for a sealed cage.
	// The sealed args as the agent builds them: the profile, the label, and the resolvers a
	// sealed cage is given so that DNS needs no local exception (sealedDNSArgs).
	extra := append([]string{"--security-opt", "seccomp=" + prof, "--label", sealedLabel + "=" + sealedGeneration},
		sealedDNSArgs()...)
	args := c.createArgs(name, "cagebreak", image, ms, extra...)
	if out, err := exec.Command(c.Podman, args...).CombinedOutput(); err != nil {
		t.Fatalf("creating the cage: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(c.Podman, "start", name).CombinedOutput(); err != nil {
		t.Fatalf("starting the cage: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// The REAL ruleset, by the agent's own code, through the agent's own path
	// (nsenter into the container's netns from outside it).
	if err := c.closeAgentPort(ctx, name, true); err != nil {
		t.Fatalf("the agent's own closeAgentPort(): %v", err)
	}

	// Read the ruleset back out of the cage's own netns: "applied" is a claim.
	rules, _ := exec.Command(c.Podman, "unshare", "nsenter", "-t", pidOf(t, c, name), "-n",
		"nft", "list", "table", "inet", "shrooms_cage").CombinedOutput()
	t.Logf("the ruleset as the cage's own netns holds it:\n%s", strings.TrimSpace(string(rules)))

	// The probe goes in and runs. podman cp rather than a mount, so the probe
	// itself is not part of what is being measured.
	if out, err := exec.Command(c.Podman, "cp", probe, name+":/tmp/probe.sh").CombinedOutput(); err != nil {
		t.Fatalf("copying the probe in: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// PROBE_RO_MOUNT: the probe checks that a mount meant to be READ-ONLY really refuses writes.
	// That is a boundary; the cage's own /etc is not (its root is an overlay).
	out, _ := exec.Command(c.Podman, "exec", "-e", "PROBE_RO_MOUNT="+uploads,
		name, "bash", "/tmp/probe.sh").CombinedOutput()
	got := string(out)
	if dst := os.Getenv("SHROOMS_CAGE_PROBE_OUT"); dst != "" {
		_ = os.WriteFile(dst, out, 0o600)
	}
	t.Logf("probe output:\n%s", got)

	// The verdicts, as assertions, so that a regression FAILS this test rather
	// than merely being visible in a log somebody has to read.
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "PROBE|") {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 4 {
			continue
		}
		switch f[2] {
		case "ESCAPE":
			t.Errorf("ESCAPE: %s — %s", f[1], f[3])
		case "SKIP":
			t.Logf("could not tell: %s — %s", f[1], f[3])
		}
	}
	if !strings.Contains(got, "PROBE|internet|OK") {
		t.Errorf("the control failed: a sealed cage did not reach the internet, so the OK verdicts mean nothing")
	}
}

func pidOf(t *testing.T, c *Cages, name string) string {
	t.Helper()
	out, err := exec.Command(c.Podman, "inspect", "--format", "{{.State.Pid}}", name).Output()
	if err != nil {
		t.Fatalf("the cage's pid: %v", err)
	}
	return strings.TrimSpace(string(out))
}
