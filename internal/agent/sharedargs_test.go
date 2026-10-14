package agent

// sharedargs_test.go — what the volume mount actually looks like on the podman
// command line. The mount LIST is not enough: :nocopy and the mode are decided
// in createArgs, and a missing :nocopy silently seeds the shared folder from
// whichever image mounts the empty volume first.

import (
	"strings"
	"testing"
)

func TestAVolumeMountCarriesItsModeAndNocopy(t *testing.T) {
	c := &Cages{Podman: "podman"}
	args := c.createArgs("n", "s", "img", []mount{
		{path: sharedVolume, at: CageSharedDir, volume: true},
		{path: sharedVolume, at: CageSharedDir, volume: true, ro: true},
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, sharedVolume+":"+CageSharedDir+",nocopy") {
		t.Fatalf("the read-write volume mount is not %s:%s,nocopy:\n%s", sharedVolume, CageSharedDir, joined)
	}
	if !strings.Contains(joined, sharedVolume+":"+CageSharedDir+":ro,nocopy") {
		t.Fatalf("the read-only volume mount is not %s:%s:ro,nocopy:\n%s", sharedVolume, CageSharedDir, joined)
	}
	// And a plain path mount must NOT get nocopy: podman rejects it there.
	plain := strings.Join(c.createArgs("n", "s", "img", []mount{{path: "/tmp/x", at: "/x", ro: true}}), " ")
	if strings.Contains(plain, "nocopy") {
		t.Fatalf("a path mount was given nocopy, which podman does not accept:\n%s", plain)
	}
}
