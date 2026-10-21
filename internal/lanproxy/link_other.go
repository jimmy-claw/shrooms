//go:build !linux

package lanproxy

// physicalLink is Linux-only: a gateway runs the Linux daemon. Elsewhere
// nothing counts as a LAN, so nothing is offered.
func physicalLink(string) bool { return false }
