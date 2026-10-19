package hosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample() []Entry {
	return []Entry{
		{Name: "laptop", Addr: "fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69", Mesh: "home"},
		{Name: "vps", Addr: "fd3b:ffe9:f81:6f18:41e:c574:c529:5bbf", Mesh: "home"},
	}
}

func TestRenderIncludesSelfAndPeers(t *testing.T) {
	out := Render(sample(), "mesh")

	for _, want := range []string{
		"fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69  laptop.home.mesh",
		"fd3b:ffe9:f81:6f18:41e:c574:c529:5bbf  vps.home.mesh",
		Begin, End,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Names are self-asserted in the announce, so two devices can claim the same
// one. Silently letting the last win would point ssh at the wrong machine.
func TestDuplicateNamesDisambiguated(t *testing.T) {
	st := append(sample(), Entry{Name: "vps", Addr: "fd3b:ffe9:f81:aaaa:1:2:3:dead", Mesh: "home"})

	out := Render(st, "mesh")
	if strings.Count(out, " vps.home.mesh\n") > 1 {
		t.Fatalf("two entries claim vps.home.mesh:\n%s", out)
	}
	if !strings.Contains(out, "vps-dead.home.mesh") {
		t.Errorf("duplicate was not disambiguated by address:\n%s", out)
	}
}

// Both halves of the name are sanitised on their own: the device and the mesh.
func TestNamesSanitised(t *testing.T) {
	st := []Entry{{Name: "My Laptop_2!", Addr: "fd00::1", Mesh: "Home Net"}}

	out := Render(st, "mesh")
	if !strings.Contains(out, "my-laptop-2.home-net.mesh") {
		t.Errorf("name not sanitised into a usable hostname:\n%s", out)
	}
	if strings.Contains(out, "!") || strings.Contains(out, "My") {
		t.Errorf("unsafe characters survived:\n%s", out)
	}
}

// With no suffix the name is still qualified by its mesh; only the suffix is
// left off.
func TestSuffixOptional(t *testing.T) {
	out := Render(sample(), "")
	if strings.Contains(out, "laptop.home.") {
		t.Errorf("suffix applied when none was asked for:\n%s", out)
	}
	if !strings.Contains(out, "  laptop.home\n") {
		t.Errorf("qualified name missing:\n%s", out)
	}
}

// The block must be replaced in place. Mangling the rest of /etc/hosts would
// break name resolution for the whole machine.
func TestReplaceBlockPreservesSurroundings(t *testing.T) {
	existing := "127.0.0.1 localhost\n::1 localhost\n\n" +
		Begin + "\nfd00::9  stale stale.mesh\n" + End + "\n" +
		"10.0.0.5 something-else\n"

	out, err := ReplaceBlock(existing, Begin+"\nfd00::1  fresh fresh.mesh\n"+End+"\n")
	if err != nil {
		t.Fatalf("replaceBlock: %v", err)
	}
	for _, want := range []string{"127.0.0.1 localhost", "10.0.0.5 something-else", "fresh"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stale") {
		t.Errorf("old entry survived:\n%s", out)
	}
	if strings.Count(out, Begin) != 1 {
		t.Errorf("block duplicated:\n%s", out)
	}
}

func TestReplaceBlockAppendsWhenAbsent(t *testing.T) {
	out, err := ReplaceBlock("127.0.0.1 localhost\n", Begin+"\nfd00::1  a\n"+End+"\n")
	if err != nil {
		t.Fatalf("replaceBlock: %v", err)
	}
	if !strings.HasPrefix(out, "127.0.0.1 localhost\n") {
		t.Errorf("existing content not preserved at the top:\n%s", out)
	}
	if !strings.Contains(out, Begin) {
		t.Errorf("block not appended:\n%s", out)
	}
}

// A half-written block must be reported, not silently duplicated — that would
// leave two conflicting sets of entries.
func TestReplaceBlockRejectsTruncated(t *testing.T) {
	if _, err := ReplaceBlock("x\n"+Begin+"\nfd00::1 a\n", "new"); err == nil {
		t.Fatal("accepted a block with no end marker")
	}
}

func TestReplaceBlockIsIdempotent(t *testing.T) {
	block := Begin + "\nfd00::1  a\n" + End + "\n"
	once, _ := ReplaceBlock("127.0.0.1 localhost\n", block)
	twice, _ := ReplaceBlock(once, block)
	if once != twice {
		t.Errorf("running twice changed the file:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// applyForTest adapts Apply's two return values for the existing assertions.
func applyForTest(path, block string) error {
	_, err := Apply(path, block)
	return err
}

func TestUpdateHostsFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	block := Render(sample(), "mesh")
	if err := applyForTest(path, block); err != nil {
		t.Fatalf("updateHostsFile: %v", err)
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "vps.home.mesh") || !strings.Contains(string(got), "127.0.0.1 localhost") {
		t.Errorf("unexpected result:\n%s", got)
	}

	// No temporary files left behind.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("expected only the hosts file, found %d entries", len(ents))
	}
}

// No name is written without its mesh, on a node with one mesh or several
// (docs/one-kind-of-mesh.md, 2026-09-29). The short form used to be written
// wherever only one mesh claimed a name, so whether peer.mesh existed depended
// on which meshes a device had joined — and it meant different machines, or
// nothing, on different devices.
func TestOnlyQualifiedNamesAreWritten(t *testing.T) {
	got := Render([]Entry{
		{Name: "vps", Addr: "fd00::1", Mesh: "home"},
		{Name: "laptop", Addr: "fd00::2", Mesh: "home"},
	}, "mesh")

	for _, want := range []string{"vps.home.mesh", "laptop.home.mesh"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, name := range fields[1:] { // fields[0] is the address
			if name == "vps" || name == "vps.mesh" || name == "laptop" || name == "laptop.mesh" {
				t.Errorf("short name %q was written: %s", name, line)
			}
		}
	}
}

// Two meshes, each with a "vps": two different names, both written, and the
// bare name for neither.
func TestTheSameNameOnTwoMeshesIsTwoNames(t *testing.T) {
	got := Render([]Entry{
		{Name: "vps", Addr: "fd00::1", Mesh: "home"},
		{Name: "vps", Addr: "fd11::1", Mesh: "shared"},
	}, "mesh")

	for _, want := range []string{"fd00::1  vps.home.mesh", "fd11::1  vps.shared.mesh"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "vps-") {
		t.Errorf("a name on another mesh was treated as a duplicate:\n%s", got)
	}
}

// Duplicate device names inside one mesh keep the existing disambiguation, and
// it must survive into the qualified form too.
func TestWithinMeshDuplicatesStillDisambiguated(t *testing.T) {
	got := Render([]Entry{
		{Name: "box", Addr: "fd00::dead:beef", Mesh: "home"},
		{Name: "box", Addr: "fd00::cafe:f00d", Mesh: "home"},
	}, "mesh")

	// Two online claimants: the plain name is nobody's (audit F-18).
	if strings.Count(got, " box.home.mesh") != 0 || strings.Count(got, " box-") != 2 {
		t.Errorf("box.home.mesh given to one of two online claimants:\n%s", got)
	}
	if !strings.Contains(got, "box-") {
		t.Errorf("second device was not disambiguated:\n%s", got)
	}
}

// An entry with no mesh has no name that resolves anywhere, so it is left out.
// This reverses the old rule, which wrote it bare: the resolver answers only
// qualified names now, and a file answering names nothing else answers is the
// "works in one place, not in another" this decision exists to end.
func TestUnlabelledEntriesAreNotWritten(t *testing.T) {
	got := Render([]Entry{{Name: "vps", Addr: "fd00::1"}}, "mesh")
	if strings.Contains(got, "fd00::1") {
		t.Errorf("an entry with no mesh was written:\n%s", got)
	}
}

// A device must never write its own bare name into its own /etc/hosts.
//
// The machine's hostname normally resolves to 127.0.1.1, and daemons that look
// themselves up — PAM session setup, web servers, mail — expect a local
// address. An overlay address under the same name gives them a ULA that RFC
// 6724 will often prefer, and that is unroutable entirely until shrooms0 exists,
// which is the state every boot starts in. No name is bare any more, but this
// is the case where that matters most, so it keeps its own test.
func TestSelfDoesNotShadowTheHostname(t *testing.T) {
	block := Render([]Entry{
		{Name: "jimmy-crib", Addr: "fd3b:ffe9:f81:891a::1", Mesh: "office", Self: true},
		{Name: "vps", Addr: "fd3b:ffe9:f81:6f18::1", Mesh: "office"},
	}, "mesh")

	for _, line := range strings.Split(block, "\n") {
		if !strings.Contains(line, "891a") {
			continue
		}
		for _, field := range strings.Fields(line)[1:] {
			if field == "jimmy-crib" {
				t.Errorf("own bare hostname written to /etc/hosts:\n%s", block)
			}
		}
		if !strings.Contains(line, "jimmy-crib.office.mesh") {
			t.Errorf("own qualified name is missing:\n%s", block)
		}
	}
}

// The overlay is IPv6 and a great deal of software still cannot say so, so a
// device's names have to resolve on both families (ADR-021). This is what makes
// an IPv4-only program work by name with no resolver in the picture at all.
func TestBothFamiliesUnderTheSameNames(t *testing.T) {
	block := Render([]Entry{
		{Name: "nas", Addr: "fd3b::1", AddrV4: "198.19.224.254", Mesh: "home"},
	}, "mesh")

	for _, want := range []string{
		"fd3b::1  nas.home.mesh",
		"198.19.224.254  nas.home.mesh",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("missing %q in:\n%s", want, block)
		}
	}
}

// A device with no alias renders one line, since a peer on an older build
// announces nothing to make one from.
func TestNoAliasRendersOneLine(t *testing.T) {
	block := Render([]Entry{{Name: "nas", Addr: "fd3b::1", Mesh: "home"}}, "mesh")
	if n := strings.Count(block, "nas.home.mesh"); n != 1 {
		t.Errorf("a peer with no alias produced %d lines:\n%s", n, block)
	}
}

// The dangerous case: two devices claiming one name are disambiguated by a
// piece of their overlay address, and each still has to get both of its own
// lines. Getting this wrong sends an IPv4 connection to the other machine —
// silently, which is the whole reason the duplicate logic exists.
func TestDuplicateNamesKeepTheirOwnAliases(t *testing.T) {
	block := Render([]Entry{
		{Name: "nas", Addr: "fd3b::aaaa", AddrV4: "198.19.0.1", Mesh: "home"},
		{Name: "nas", Addr: "fd3b::bbbb", AddrV4: "198.19.0.2", Mesh: "home"},
	}, "mesh")

	// Both online: neither keeps the plain name (audit F-18).
	for _, line := range []string{
		"fd3b::aaaa  nas-aaaa.home.mesh",
		"198.19.0.1  nas-aaaa.home.mesh",
		"fd3b::bbbb  nas-bbbb.home.mesh",
		"198.19.0.2  nas-bbbb.home.mesh",
	} {
		if !strings.Contains(block, line) {
			t.Errorf("missing %q in:\n%s", line, block)
		}
	}
	for _, wrong := range []string{"nas.home.mesh", "198.19.0.1  nas-bbbb"} {
		if strings.Contains(block, wrong) {
			t.Errorf("an alias landed on the wrong device: %q in:\n%s", wrong, block)
		}
	}
}

// Who keeps a name claimed twice (audit F-18): the one online claimant over
// one gone quiet; this device over anyone, in its own file.
func TestANameClaimedTwiceGoesToNoImpostor(t *testing.T) {
	got := Render([]Entry{
		{Name: "box", Addr: "fd00::aaaa", Mesh: "home", Offline: true},
		{Name: "box", Addr: "fd00::bbbb", Mesh: "home"},
		{Name: "me", Addr: "fd00::cccc", Mesh: "home", Self: true},
		{Name: "me", Addr: "fd00::1111", Mesh: "home"},
	}, "mesh")
	for _, line := range []string{"fd00::bbbb  box.home.mesh", "fd00::aaaa  box-aaaa.home.mesh",
		"fd00::cccc  me.home.mesh", "fd00::1111  me-1111.home.mesh"} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in:\n%s", line, got)
		}
	}
}
