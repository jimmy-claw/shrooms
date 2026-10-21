package lanproxy

import (
	"os"
	"path/filepath"
	"strings"
)

// physicalUnder is physicalLink under a sysfs root, so a test can build one.
func physicalUnder(root, name string, depth int) bool {
	if depth > 4 || name == "" || strings.ContainsAny(name, "/") {
		return false
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(filepath.Join(dir, "device")); err == nil {
		return true
	}
	// A bridge's ports, and a bond's or VLAN's lower devices.
	ports, _ := os.ReadDir(filepath.Join(dir, "brif"))
	for _, p := range ports {
		if physicalUnder(root, p.Name(), depth+1) {
			return true
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if lower, ok := strings.CutPrefix(e.Name(), "lower_"); ok && physicalUnder(root, lower, depth+1) {
			return true
		}
	}
	return false
}
