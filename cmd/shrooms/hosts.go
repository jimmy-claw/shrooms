package main

import (
	"flag"
	"fmt"
	dnssrv "github.com/vpavlin/shrooms/internal/dns"
	"os"

	"github.com/vpavlin/shrooms/internal/hosts"
)

func cmdHosts(args []string) error {
	fs := flag.NewFlagSet("hosts", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	suffix := fs.String("suffix", "", "domain suffix (default: the one the daemon answers for)")
	write := fs.Bool("write", false, "update "+hosts.DefaultFile+" instead of printing")
	file := fs.String("file", hosts.DefaultFile, "hosts file to update")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := fetchStatus(*sock)
	if err != nil {
		return err
	}

	if *suffix == "" {
		*suffix = hostsSuffix(st)
	}
	entries := hostsEntriesFrom(st)
	block := hosts.Render(entries, *suffix)
	if !*write {
		fmt.Print(block)
		if len(st.Peers) > 0 {
			fmt.Fprintf(os.Stderr, "\n# to apply: sudo shrooms hosts --write\n")
		}
		return nil
	}

	changed, err := hosts.Apply(*file, block)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Printf("%s already up to date\n", *file)
		return nil
	}
	fmt.Printf("updated %s with %d entries\n", *file, len(entries))
	fmt.Printf("\nTo keep it current automatically, set in your config:\n")
	fmt.Printf("  manage_hosts = \"true\"\n")
	return nil
}

// hostsSuffix is the domain the daemon answers for, so that the names the file
// holds are the names the resolver gives. Defaulting to dnssrv.DefaultSuffix
// instead wrote .internal beside a daemon answering .mesh, and `shrooms
// status` — which checks against the daemon's — then called the block it had
// just recommended writing stale.
func hostsSuffix(st statusPayload) string {
	if st.DNS.Suffix != "" {
		return st.DNS.Suffix
	}
	return dnssrv.DefaultSuffix
}

// hostsEntriesFrom is what the managed block should hold, from the daemon's
// status: this device once per mesh it is running, and every peer under the
// mesh it was heard on.
//
// Once per mesh because this device has a different address on each, and a
// name is qualified by the mesh it is on — laptop.home and laptop.office are
// two names for two addresses. It used to be one entry from the first mesh,
// written unqualified, which answered for whichever mesh happened to be first.
func hostsEntriesFrom(st statusPayload) []hosts.Entry {
	var entries []hosts.Entry
	for _, m := range st.Meshes {
		if m.NotRunning || m.Overlay == "" {
			continue
		}
		entries = append(entries, hosts.Entry{
			Name: st.Name, Addr: m.Overlay, AddrV4: m.OverlayV4, Mesh: m.Label, Self: true,
		})
	}
	for _, p := range st.Peers {
		entries = append(entries, hosts.Entry{
			Name: p.Name, Addr: p.Overlay, AddrV4: p.OverlayV4, Mesh: p.Mesh, Contested: p.Contested,
		})
	}
	for _, h := range st.LanHosts {
		entries = append(entries, hosts.Entry{
			Name: h.Name, Addr: h.Overlay, AddrV4: h.OverlayV4, Mesh: h.Mesh, Gateway: h.Gateway,
		})
	}
	return entries
}
