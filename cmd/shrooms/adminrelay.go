package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
)

// cmdAdminRelay names the blind relays every member of a mesh should use
// (docs/distributing-a-blind-relay.md).
//
//	shrooms admin relay set 203.0.113.10:31760[,198.51.100.7:32100] [--token T]
//	shrooms admin relay clear
//
// Signed by the admin and published through the local daemon, like a
// revocation. Members adopt it unless they configured blind relays of their
// own or wrote relay_blind = "none".
func cmdAdminRelay(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: shrooms admin relay {set ADDR[,ADDR]|clear} [flags]")
	}
	switch args[0] {
	case "set":
		return adminRelayPublish("admin relay set", args[1:], true)
	case "clear":
		return adminRelayPublish("admin relay clear", args[1:], false)
	default:
		return fmt.Errorf("unknown admin relay command %q; want set or clear", args[0])
	}
}

func adminRelayPublish(name string, args []string, set bool) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where the admin key is kept")
	label := fs.String("mesh", "", "which mesh to advise (ADR-015)")
	token := fs.String("token", "", "the token the relays' operator issued, if they want one")
	signWith := fs.String("sign-with", "", "a command that signs a digest, instead of the admin key file")
	external := fs.Bool("external-signer", false, "print the digest and read the signature back (ADR-022)")
	serial := fs.Uint64("serial", 0, "the statement's serial; must exceed the one the mesh holds (default: now)")
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	publish := fs.Bool("publish", true, "hand it to the local daemon to put on the mesh")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	var relays []netip.AddrPort
	if set {
		if fs.NArg() != 1 {
			return errors.New("name the relays: shrooms admin relay set ADDR[,ADDR]")
		}
		var err error
		if relays, err = parseAdvisedRelays(fs.Arg(0)); err != nil {
			return err
		}
	} else {
		if fs.NArg() != 0 {
			return errors.New("admin relay clear takes no addresses")
		}
		if *token != "" {
			return errors.New("--token means nothing when clearing")
		}
	}

	if *serial == 0 {
		*serial = nextAdviceSerial(*sock, *label, uint64(time.Now().Unix()))
	}

	admin, auth, err := signerFor(*dir, *label, *signWith, *external)
	if err != nil {
		return err
	}
	a, err := cred.AdviseRelaysWith(admin, auth, *serial, relays, *token, time.Now())
	if err != nil {
		return err
	}
	raw, err := a.MarshalBinary()
	if err != nil {
		return err
	}

	if set {
		fmt.Printf("Members should use %s (serial %d).\n", joinAddrs(relays), *serial)
	} else {
		fmt.Printf("Withdrew the blind relay advice (serial %d).\n", *serial)
	}
	fmt.Println("A device that configures blind relays of its own, or says")
	fmt.Println(`relay_blind = "none", keeps doing what it says.`)
	fmt.Println()

	if !*publish {
		fmt.Printf("  %s\n\n", base64.StdEncoding.EncodeToString(raw))
		fmt.Println("Hand that to any running node to put it on the mesh.")
		return nil
	}
	if err := postSigned(*sock, "/relay-advice", *label, raw); err != nil {
		fmt.Printf("Not published: %v\n\n", err)
		fmt.Printf("It is signed; publish it later with --serial %d, or re-run this\n", *serial)
		fmt.Println("where a daemon is running.")
		return err
	}
	fmt.Println("Published. Every node verifies the signature, uses the relays if it")
	fmt.Println("has none of its own, and repeats the statement each epoch — so a node")
	fmt.Println("that was offline learns it from whoever is up.")
	return nil
}

// parseAdvisedRelays accepts the comma- or space-separated list a person types.
func parseAdvisedRelays(list string) ([]netip.AddrPort, error) {
	var out []netip.AddrPort
	for _, one := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' }) {
		ap, err := netip.ParseAddrPort(one)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address and port, like 203.0.113.10:31760", one)
		}
		out = append(out, ap)
	}
	if len(out) == 0 {
		return nil, errors.New("no relays named; to withdraw the advice use `shrooms admin relay clear`")
	}
	if len(out) > cred.MaxAdviceRelays {
		return nil, fmt.Errorf("%d relays named, at most %d", len(out), cred.MaxAdviceRelays)
	}
	return out, nil
}

// nextAdviceSerial is now, unless the mesh already holds a statement at or past
// it — two changes inside one second, or a clock behind the machine that
// signed the last one — in which case it is one more than that. Best effort:
// without a daemon to ask, now is the answer, and a refusal from the daemon
// later says why.
func nextAdviceSerial(sock, label string, now uint64) uint64 {
	st, err := fetchStatus(sock)
	if err != nil {
		return now
	}
	for _, m := range st.Meshes {
		if label != "" && m.Label != label {
			continue
		}
		if m.RelayAdviceSerial >= now {
			return m.RelayAdviceSerial + 1
		}
		if label == "" {
			break
		}
	}
	return now
}

func joinAddrs(aps []netip.AddrPort) string {
	s := make([]string, 0, len(aps))
	for _, a := range aps {
		s = append(s, a.String())
	}
	return strings.Join(s, ", ")
}
