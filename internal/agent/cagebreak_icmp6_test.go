package agent

// cagebreak_icmp6_test.go — does a sealed cage really refuse ICMPv6 to local addresses?
//
// The ADR says private, mesh, link-local, multicast and loopback-range addresses are refused,
// and that a hostile review "cannot touch the LAN". cageRules puts the ICMPv6 accept ABOVE the
// reject, so ICMPv6 goes to any address. This measures which of the two is true.
//
//	SHROOMS_REAL_CAGE=1 SHROOMS_CAGE_TEST_IMAGE=<a local image> \
//	  go test ./internal/agent/ -run TestSealedCageICMPv6 -v

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealedCageICMPv6(t *testing.T) {
	if os.Getenv("SHROOMS_REAL_CAGE") != "1" {
		t.Skip("SHROOMS_REAL_CAGE=1 runs it")
	}
	image := os.Getenv("SHROOMS_CAGE_TEST_IMAGE")
	if image == "" {
		t.Skip("SHROOMS_CAGE_TEST_IMAGE=<a local image> runs it")
	}
	dst := os.Getenv("SHROOMS_ICMP6_DST")
	if dst == "" {
		t.Skip("SHROOMS_ICMP6_DST=<a local IPv6, e.g. this machine's mesh address> runs it")
	}

	state := t.TempDir()
	m := newTestManager(t, state)
	if m.Cages = NewCages(""); m.Cages == nil {
		t.Fatal("no podman")
	}
	c := m.Cages
	c.Memory, c.CPUs, c.Pids = "", "", 0
	prof, err := m.sealedSeccomp()
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	ms := []mount{{path: proj}}

	name := "shrooms-icmp6"
	_ = exec.Command(c.Podman, "rm", "-f", name).Run()
	t.Cleanup(func() { _ = exec.Command(c.Podman, "rm", "-f", name).Run() })
	extra := append([]string{"--security-opt", "seccomp=" + prof, "--label", sealedLabel + "=" + sealedGeneration},
		sealedDNSArgs()...)
	args := c.createArgs(name, "icmp6", image, ms, extra...)
	if out, err := exec.Command(c.Podman, args...).CombinedOutput(); err != nil {
		t.Fatalf("creating the cage: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(c.Podman, "start", name).CombinedOutput(); err != nil {
		t.Fatalf("starting the cage: %v: %s", err, strings.TrimSpace(string(out)))
	}

	run := func(withRules bool) {
		if withRules {
			if err := c.closeAgentPort(t.Context(), name, true); err != nil {
				t.Fatalf("closeAgentPort(): %v", err)
			}
		}
		if out, err := exec.Command(c.Podman, "cp", filepath.Join("testdata", "icmp6probe.py"),
			name+":/tmp/icmp6probe.py").CombinedOutput(); err != nil {
			t.Fatalf("copying the probe in: %v: %s", err, strings.TrimSpace(string(out)))
		}
		out, _ := exec.Command(c.Podman, "exec", name, "python3", "/tmp/icmp6probe.py", dst).CombinedOutput()
		t.Logf("--- %s\n%s", map[bool]string{true: "with the sealed rules", false: "MUTANT: without the rules"}[withRules],
			strings.TrimSpace(string(out)))
	}

	// The control first: with no rules, ICMPv6 must work and the UDP control must not be refused.
	run(false)
	// Then the real thing.
	run(true)
}
