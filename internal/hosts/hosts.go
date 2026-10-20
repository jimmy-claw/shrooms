// Package hosts renders and applies /etc/hosts entries for mesh peers.
//
// This is the zero-dependency way to reach peers by name. It is deliberately
// not the long-term answer — it is static, needs root, and does not exist on
// Android — but it works everywhere else with nothing installed. See
// docs/adr/013-name-resolution.md.
package hosts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Markers delimiting the block this package owns. Everything outside them is
// left byte-for-byte alone.
const (
	Begin = "# BEGIN logos-vpn — managed, do not edit inside this block"
	End   = "# END logos-vpn"
)

// DefaultFile is the file updated by default.
const DefaultFile = "/etc/hosts"

// Entry is one name/address pair, and the mesh it came from.
type Entry struct {
	Name string
	Addr string

	// AddrV4 is the synthetic IPv4 alias for this device, if it has one
	// (ADR-021). Written as a second line under the same names, which is what
	// makes an IPv4-only program work by name with no resolver involved: the
	// overlay is IPv6, and a great deal of software still cannot say so.
	//
	// A field rather than a second Entry on purpose. The duplicate-name logic
	// below distinguishes devices, and two entries for one device would look
	// to it exactly like two devices claiming one name — which it resolves by
	// mangling both.
	AddrV4 string

	// Mesh is the local label for the mesh this peer belongs to. Required:
	// names are qualified, so an entry without one is not written.
	Mesh string

	// Self marks this device's own entry. It no longer changes what is
	// written — no name is bare now — but the reason it existed still stands
	// and is why that must not change: a bare name here would shadow the
	// machine's own hostname. A host
	// normally resolves its hostname to 127.0.1.1, and every daemon that looks
	// itself up — PAM session setup, mail, web servers — expects a local
	// address. Adding an overlay address under the same name gives them a ULA
	// instead, which RFC 6724 will often *prefer*, and which is unroutable
	// entirely until shrooms0 exists. That happens on every boot, before this
	// daemon has started.
	//
	// The qualified name is kept because it is useful and shadows nothing.
	Self bool

	// Contested marks a peer claiming a name another device holds (the one
	// that has claimed it longest, internal/mesh Holders): it is written with
	// the last group of its address, never the plain name (audit F-18).
	Contested bool
}

// Render builds the managed block.
//
// Every name is qualified: <device>.<mesh>.<suffix>, and nothing shorter
// (docs/one-kind-of-mesh.md, 2026-09-29). The short form used to be written for
// any name only one mesh claimed, which meant the file answered peer.mesh on
// one device and not on another depending on which meshes each had joined —
// the same name meaning different machines, or nothing. An entry with no mesh
// has no name that resolves anywhere, so it is left out rather than written in
// a form nothing else agrees with.
//
// Within a mesh, device names are self-asserted in the announce, so two devices
// can claim the same one. Duplicates get a short piece of their overlay address
// appended, and neither is resolved by last-writer-wins — that silently sends
// your ssh to the wrong machine. Across meshes there is nothing to resolve:
// vps.home and vps.work are different names.
func Render(entries []Entry, suffix string) string {
	suffix = strings.Trim(suffix, ".")

	sorted := append([]Entry(nil), entries...)
	// The address breaks the tie, so the order is total. Two devices in one
	// mesh both calling themselves "vps" compare equal on (Mesh, Name), and
	// sort.Slice is not stable — so which of them owned vps.home was arbitrary
	// AND free to change between runs, which is the version of
	// last-writer-wins this file exists to refuse. Names are self-asserted, so
	// a peer can contest one deliberately; it should at least not flip.
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Mesh != sorted[j].Mesh {
			return sorted[i].Mesh < sorted[j].Mesh
		}
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Addr < sorted[j].Addr
	})

	var b strings.Builder
	b.WriteString(Begin + "\n")
	b.WriteString("# Regenerate with: logos-vpn hosts --write\n")

	// Who keeps a plain name claimed more than once: this device, in its own
	// file; else the one claimant that holds it, online or not; if more than
	// one says it does, nobody, and each is written with the last group of its
	// address. A member that took a name by sorting first, or by being online
	// while its holder slept, could take its ssh with it (audit F-18).
	type claim struct{ self, holders int }
	claims := map[string]*claim{}
	for _, e := range sorted {
		key := sanitise(e.Mesh) + "\x00" + sanitise(e.Name)
		c := claims[key]
		if c == nil {
			c = &claim{}
			claims[key] = c
		}
		if e.Self {
			c.self++
		} else if !e.Contested {
			c.holders++
		}
	}
	seen := map[string]bool{} // mesh+name, its plain name given out
	for _, e := range sorted {
		name, mesh := sanitise(e.Name), sanitise(e.Mesh)
		if name == "" || mesh == "" {
			continue
		}
		key := mesh + "\x00" + name
		c := claims[key]
		plain := !seen[key]
		switch {
		case c.self > 0:
			plain = e.Self
		case c.holders > 1:
			plain = false
		default:
			plain = !e.Contested
		}
		if !plain {
			if short := shortAddr(e.Addr); short != "" {
				name = name + "-" + short
			}
		} else {
			seen[key] = true
		}

		full := name + "." + mesh
		if suffix != "" {
			full += "." + suffix
		}
		fmt.Fprintf(&b, "%s  %s\n", e.Addr, full)
		// The same name again on IPv4. The overlay address goes first because
		// it is the real one and the alias is a local convenience; glibc
		// returns both either way.
		if e.AddrV4 != "" {
			fmt.Fprintf(&b, "%s  %s\n", e.AddrV4, full)
		}
	}
	b.WriteString(End + "\n")
	return b.String()
}

// sanitise keeps a device name usable as a hostname.
func sanitise(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// shortAddr returns a short distinguishing piece of an overlay address.
func shortAddr(addr string) string {
	parts := strings.Split(addr, ":")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// Apply replaces the managed block, leaving everything else intact. It reports
// whether the file actually changed.
//
// Written atomically via a temporary file in the same directory: a torn write
// to /etc/hosts would break name resolution for the whole machine, including
// whatever you would use to fix it.
func Apply(path, block string) (changed bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	updated, err := ReplaceBlock(string(existing), block)
	if err != nil {
		return false, err
	}
	if updated == string(existing) {
		return false, nil
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".logos-vpn-hosts-")
	if err != nil {
		return false, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, fmt.Errorf("replace %s: %w", path, err)
	}
	return true, nil
}

// ReplaceBlock swaps the managed block for a new one, appending it if absent.
func ReplaceBlock(existing, block string) (string, error) {
	begin := strings.Index(existing, Begin)
	if begin == -1 {
		out := existing
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if out != "" {
			out += "\n"
		}
		return out + block, nil
	}

	endIdx := strings.Index(existing[begin:], End)
	if endIdx == -1 {
		// A truncated block would otherwise be silently duplicated, leaving two
		// conflicting sets of entries.
		return "", fmt.Errorf("found %q without a matching %q — fix the file by hand", Begin, End)
	}
	end := begin + endIdx + len(End)
	if end < len(existing) && existing[end] == '\n' {
		end++
	}
	return existing[:begin] + block + existing[end:], nil
}
