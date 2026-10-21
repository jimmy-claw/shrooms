package main

import (
	"log/slog"
	"net/netip"

	"github.com/vpavlin/shrooms/internal/wg"
)

// routeLanAliases routes exactly the active LAN aliases at this mesh's
// interface (ADR-068, lanhost.Alias). A route only while the alias is active:
// the address is the LAN's again as soon as its host is not on the mesh, so a
// laptop back at the gateway's own site still reaches the device directly
// when the grant or the gateway is gone. The /32 is more specific than any
// LAN's own route, which is the point: on another LAN with the same range,
// the packet goes to the mesh and not to whatever local device has that
// address (audit F-29).
func (in *instance) routeLanAliases(log *slog.Logger, active map[netip.Addr]netip.Addr) {
	in.aliasMu.Lock()
	defer in.aliasMu.Unlock()
	if in.aliasRoutes == nil {
		in.aliasRoutes = map[netip.Addr]bool{}
	}
	src := in.aliases.Self()
	for a := range active {
		if in.aliasRoutes[a] {
			continue
		}
		if err := wg.RouteHost(in.tunName, a, src, wg.DefaultMTU); err != nil {
			log.Warn("could not route a LAN alias", "mesh", in.label, "lan", a, "err", err)
			continue
		}
		in.aliasRoutes[a] = true
		log.Info("LAN alias routed to the mesh", "mesh", in.label, "lan", a, "host", active[a])
	}
	for a := range in.aliasRoutes {
		if _, ok := active[a]; ok {
			continue
		}
		if err := wg.UnrouteHost(in.tunName, a); err != nil {
			log.Debug("could not take a LAN alias route away", "mesh", in.label, "lan", a, "err", err)
		}
		delete(in.aliasRoutes, a)
		log.Info("LAN alias back to the LAN", "mesh", in.label, "lan", a)
	}
}
