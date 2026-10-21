package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
)

// cmdAdminLan grants a gateway member the right to offer one LAN host to the
// mesh, or takes it away (ADR-068).
//
//	shrooms admin lan grant --name pi5 --host printer --ports 8883,990,6000,50000-50100 [--life 8760h]
//	shrooms admin lan withdraw --name pi5 --host printer
//
// The gateway serves the host only if it also lists it in its own config
// (mesh.<label>.lan_hosts), with the LAN address, which the grant does not
// carry. Signed by the admin and published through the local daemon, like a
// revocation.
func cmdAdminLan(args []string) error {
	if len(args) < 1 {
		return errors.New(adminLanUsage)
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Println(adminLanUsage)
		return nil
	case "grant":
		return adminLanPublish("admin lan grant", args[1:], false)
	case "withdraw":
		return adminLanPublish("admin lan withdraw", args[1:], true)
	default:
		return fmt.Errorf("unknown admin lan command %q; want grant or withdraw", args[0])
	}
}

const adminLanUsage = `usage: shrooms admin lan {grant|withdraw} --mesh MESH (--device KEY | --name GATEWAY) --host NAME [--ports P] [flags]

  grant     let a gateway member offer one LAN host on the listed TCP ports
  withdraw  take it away; open connections to it are closed

--name matches only a name the device's credential carries; otherwise pass
--device with its key. 'shrooms admin lan grant --help' lists every flag.
See ADR-068.`

func adminLanPublish(name string, args []string, withdraw bool) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where the admin key is kept")
	label := fs.String("mesh", "", "which mesh the host is offered on (ADR-015)")
	devName := fs.String("name", "", "the gateway member, by the name its credential carries (otherwise use --device)")
	devHex := fs.String("device", "", "the gateway member, by its device key (hex)")
	host := fs.String("host", "", "the host's name: printer in printer.lan.<gateway>.<mesh>.mesh")
	ports := fs.String("ports", "", "TCP ports and ranges, e.g. 8883,990,50000-50100 (at most 256 ports)")
	life := fs.Duration("life", 365*24*time.Hour, "how long the grant is in force")
	signWith := fs.String("sign-with", "", "a command that signs a digest, instead of the admin key file")
	external := fs.Bool("external-signer", false, "print the digest and read the signature back (ADR-022)")
	serial := fs.Uint64("serial", 0, "the grant's serial; must exceed the one the mesh holds for this host (default: now, in nanoseconds)")
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	publish := fs.Bool("publish", true, "hand it to the local daemon to put on the mesh")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	if err := cred.CheckLanHostName(*host); err != nil {
		return fmt.Errorf("--host: %w", err)
	}
	var rs []cred.PortRange
	if !withdraw {
		var err error
		if rs, err = cred.ParseLanPorts(*ports); err != nil {
			return fmt.Errorf("--ports: %w", err)
		}
	} else if *ports != "" {
		return errors.New("--ports means nothing when withdrawing")
	}

	if *devHex == "" && *devName != "" {
		hex, err := deviceByName(*sock, *label, *devName)
		if err != nil {
			return err
		}
		*devHex = hex
		fmt.Printf("%q is %s\n", *devName, hex)
	}
	if *devHex == "" {
		return errors.New("--name or --device is required: the gateway member that will offer the host")
	}
	var gw []byte
	if _, err := fmt.Sscanf(*devHex, "%x", &gw); err != nil || len(gw) != ed25519.PublicKeySize {
		return fmt.Errorf("--device must be a %d-byte hex key", ed25519.PublicKeySize)
	}
	// Nanoseconds, not seconds: with seconds, a withdrawal in the same second
	// as its grant carried the same serial, and was ignored (F-28). Either
	// unit exceeds every serial the other ever produced, so grants made
	// before the change are still superseded.
	if *serial == 0 {
		*serial = uint64(time.Now().UnixNano())
	}

	admin, auth, err := signerFor(*dir, *label, *signWith, *external)
	if err != nil {
		return err
	}
	g, err := cred.GrantLanHostWith(admin, auth, gw, *host, rs, *serial, time.Now(), *life, withdraw)
	if err != nil {
		return err
	}
	raw, err := g.MarshalBinary()
	if err != nil {
		return err
	}

	// What it says, only once the daemon has taken it: this printed
	// "Withdrew" before asking, and the daemon then ignored the withdrawal
	// (F-28).
	said := func() {
		if withdraw {
			fmt.Printf("Withdrew LAN host %q from gateway %x (serial %d).\n", *host, gw[:8], *serial)
		} else {
			fmt.Printf("Granted gateway %x to offer LAN host %q on TCP %s, until %s (serial %d).\n",
				gw[:8], *host, cred.FormatLanPorts(rs), time.Unix(g.NotAfter, 0).Format(time.DateOnly), *serial)
			fmt.Println("The gateway serves it once its own config lists the host and its LAN")
			fmt.Printf("address: mesh.<label>.lan_hosts = [\"%s=<private LAN address>/tcp:%s\"]\n", *host, cred.FormatLanPorts(rs))
		}
		fmt.Println()
	}

	if !*publish {
		fmt.Printf("Signed, not yet in force (serial %d):\n\n", *serial)
		fmt.Printf("  %s\n\n", base64.StdEncoding.EncodeToString(raw))
		fmt.Println("Hand that to any running node to put it on the mesh.")
		return nil
	}
	if err := postSigned(*sock, "/lan-grant", *label, raw); err != nil {
		fmt.Printf("Not published, so nothing changed: %v\n\n", err)
		fmt.Println("Re-run this where a daemon is running, with a --serial higher than the")
		fmt.Println("one the mesh holds if that is what it refused.")
		return err
	}
	said()
	fmt.Println("Published. Every node verifies the signature and repeats the grant each")
	fmt.Println("epoch, so a node that was offline learns it from whoever is up.")
	return nil
}
