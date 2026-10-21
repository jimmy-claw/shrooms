package mesh

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/lanhost"
	"github.com/vpavlin/shrooms/internal/topic"
)

// LAN host grants (ADR-068): the admin's say-so for a gateway member to offer
// one LAN device. Held by every node, verified on arrival and again on load,
// repeated each epoch and when a peer appears, as relay advice is. A node acts
// on a grant only while its gateway is a member and the grant is in force.

// maxLanGrantsHeld caps what a node keeps, so a stream of validly signed
// grants (the admin's own, from a script gone wrong) cannot grow the file or
// the tunnel config without bound.
const maxLanGrantsHeld = 256

type heldLanGrant struct {
	g   *cred.LanGrant
	raw []byte
}

func lanGrantKey(gateway []byte, host string) string {
	return hex.EncodeToString(gateway) + "/" + host
}

// LanHost is one host a grant offers, as a node knows it.
type LanHost struct {
	Name        string
	Gateway     ed25519.PublicKey
	GatewayName string     // the gateway's name on this mesh; this device's own when Self
	Overlay     netip.Addr // the host's address on the mesh (lanhost.Overlay)
	Ports       []cred.PortRange
	NotAfter    time.Time
	Self        bool // this device is the gateway
	Online      bool // the gateway is announcing (always true when Self)
}

// loadLanGrants restores the grants this node held before it stopped.
func (m *Mesh) loadLanGrants() {
	if m.authority == nil || m.st == nil {
		return
	}
	for _, raw := range m.st.LanGrants(m.networkID) {
		if _, err := m.applyLanGrant(raw, false); err != nil && !errors.As(err, new(heldSerial)) {
			m.log.Debug("dropping a stored LAN host grant", "err", err)
		}
	}
}

// LanGrant takes a grant from the admin tooling and puts it on the bus,
// verified exactly as one from a peer would be.
//
// A grant that is not newer than the one held is the admin's mistake, and is
// refused rather than published: this used to report success, so a withdrawal
// in the same second as its grant (the default serial was the time in
// seconds) said "Withdrew" and left the host granted (F-28). Only the very
// statement already held is accepted again, which makes a retry harmless.
func (m *Mesh) LanGrant(raw []byte) error {
	if _, err := m.applyLanGrant(raw, true); err != nil {
		var held heldSerial
		if errors.As(err, &held) && held.same {
			return m.publishLanGrant(raw, time.Now())
		}
		return err
	}
	// Said twice: in the lab the first grant, published moments after the
	// gateway restarted, reached nobody, and the next chance was the epoch's
	// repetition (2026-10-21). A second copy a little later costs one small
	// message; a receiver that has it already ignores the repeat. Only if it
	// is still the latest: repeating a grant the admin has since withdrawn
	// would offer it again to a node that missed the withdrawal (F-38).
	time.AfterFunc(lanGrantRepeat, func() {
		if !m.holdsLanGrant(raw) {
			return
		}
		if err := m.publishLanGrant(raw, time.Now()); err != nil {
			m.log.Debug("could not repeat a LAN host grant", "err", err)
		}
	})
	return m.publishLanGrant(raw, time.Now())
}

// holdsLanGrant reports whether raw is the statement held for its gateway and
// host.
func (m *Mesh) holdsLanGrant(raw []byte) bool {
	g, err := cred.UnmarshalLanGrant(raw)
	if err != nil {
		return false
	}
	m.lanMu.Lock()
	defer m.lanMu.Unlock()
	h, ok := m.lanGrants[lanGrantKey(g.Gateway, g.Host)]
	return ok && bytes.Equal(h.raw, raw)
}

// OnLanGrant registers f to run whenever the grants held change: the gateway
// closes, at once, the connections a withdrawal or narrowing no longer
// covers (F-27).
func (m *Mesh) OnLanGrant(f func()) {
	m.lanMu.Lock()
	m.lanChanged = f
	m.lanMu.Unlock()
}

// lanGrantRepeat is how long after the admin's grant it is said again.
const lanGrantRepeat = 20 * time.Second

// handleLanGrant takes a grant off the bus.
func (m *Mesh) handleLanGrant(raw []byte, now time.Time) {
	fresh, err := m.applyLanGrant(raw, true)
	if err != nil {
		if !errors.As(err, new(heldSerial)) {
			m.log.Warn("ignoring a LAN host grant", "err", err)
		}
		return
	}
	if fresh {
		// New to us may be new to a peer that was away when the admin spoke.
		if err := m.publishLanGrant(raw, now); err != nil {
			m.log.Debug("could not relay a LAN host grant", "err", err)
		}
	}
}

// heldSerial is a grant not newer than the one held for its gateway and host.
// same is when it is that very grant, which is nothing to complain about.
type heldSerial struct {
	serial uint64
	same   bool
}

// IsStaleLanGrant reports whether err is a grant refused for not being newer
// than the one held.
func IsStaleLanGrant(err error) bool { return errors.As(err, new(heldSerial)) }

func (e heldSerial) Error() string {
	return fmt.Sprintf("this mesh already holds serial %d for that gateway and host; a new statement needs a higher one (--serial %d)",
		e.serial, e.serial+1)
}

// applyLanGrant verifies a grant and keeps it when it is newer than the one
// held for its gateway and host. Reports whether it was new.
func (m *Mesh) applyLanGrant(raw []byte, persist bool) (bool, error) {
	if m.authority == nil {
		return false, errors.New("this mesh has no admin keys, so it has no LAN hosts (ADR-068)")
	}
	g, err := cred.UnmarshalLanGrant(raw)
	if err != nil {
		return false, fmt.Errorf("unreadable LAN host grant: %w", err)
	}
	if err := cred.VerifyLanGrantBy(m.authority, g); err != nil {
		return false, fmt.Errorf("this mesh did not sign that LAN host grant: %w", err)
	}
	key := lanGrantKey(g.Gateway, g.Host)

	m.lanMu.Lock()
	if m.lanGrants == nil {
		m.lanGrants = map[string]heldLanGrant{}
	}
	if old, ok := m.lanGrants[key]; ok && g.Serial <= old.g.Serial {
		m.lanMu.Unlock()
		return false, heldSerial{serial: old.g.Serial, same: bytes.Equal(old.raw, raw)}
	}
	if _, ok := m.lanGrants[key]; !ok {
		if len(m.lanGrants) >= maxLanGrantsHeld {
			m.lanMu.Unlock()
			return false, fmt.Errorf("already holding %d LAN host grants", maxLanGrantsHeld)
		}
		if !g.Withdrawn && m.activeForGatewayLocked(g.Gateway, time.Now()) >= lanhost.MaxPerGateway {
			m.lanMu.Unlock()
			return false, fmt.Errorf("gateway already offers %d LAN hosts, the most one may (ADR-068)", lanhost.MaxPerGateway)
		}
	}
	m.lanGrants[key] = heldLanGrant{g: g, raw: append([]byte(nil), raw...)}
	all := m.lanRawsLocked()
	changed := m.lanChanged
	m.lanMu.Unlock()
	if changed != nil {
		changed()
	}

	if persist && m.st != nil {
		if err := m.st.SetLanGrants(m.networkID, all); err != nil {
			m.log.Error("could not persist LAN host grants; they will be relearned from peers after a restart", "err", err)
		}
	}
	if g.Withdrawn {
		m.log.Info("the admin withdrew a LAN host", "host", g.Host, "gateway", hex.EncodeToString(g.Gateway)[:16])
	} else {
		m.log.Info("LAN host granted", "host", g.Host, "gateway", hex.EncodeToString(g.Gateway)[:16],
			"ports", cred.FormatLanPorts(g.Ports), "until", time.Unix(g.NotAfter, 0).Format(time.DateOnly))
	}
	// Members route the host's address to its gateway; a gateway starts or
	// stops serving it.
	m.requestResync()
	return true, nil
}

func (m *Mesh) activeForGatewayLocked(gateway []byte, now time.Time) int {
	n := 0
	for _, h := range m.lanGrants {
		if bytes.Equal(h.g.Gateway, gateway) && h.g.Active(now) {
			n++
		}
	}
	return n
}

func (m *Mesh) lanRawsLocked() [][]byte {
	keys := make([]string, 0, len(m.lanGrants))
	for k := range m.lanGrants {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]byte, 0, len(keys))
	for _, k := range keys {
		out = append(out, m.lanGrants[k].raw)
	}
	return out
}

// LanHosts is every host in force on this mesh whose gateway is a member (or
// this device), sorted by gateway name and host name. A host whose gateway
// has left, been revoked or expired is not listed: its grant is held, and
// comes back with the gateway.
func (m *Mesh) LanHosts(now time.Time) []LanHost {
	m.lanMu.Lock()
	grants := make([]*cred.LanGrant, 0, len(m.lanGrants))
	for _, h := range m.lanGrants {
		if h.g.Active(now) {
			grants = append(grants, h.g)
		}
	}
	m.lanMu.Unlock()
	if len(grants) == 0 {
		return nil
	}
	peers := map[string]PeerInfo{}
	if m.roster != nil {
		for _, p := range m.roster.Peers() {
			peers[p.ID()] = p
		}
	}
	prefix := m.nk.Prefix()
	var out []LanHost
	for _, g := range grants {
		h := LanHost{
			Name:     g.Host,
			Gateway:  ed25519.PublicKey(append([]byte(nil), g.Gateway...)),
			Overlay:  lanhost.Overlay(prefix, g.Gateway, g.Host),
			Ports:    g.Ports,
			NotAfter: time.Unix(g.NotAfter, 0),
		}
		if m.st != nil && bytes.Equal(g.Gateway, m.st.Identity.DevicePub) {
			h.Self, h.Online, h.GatewayName = true, true, sanitiseName(m.cfg.Name)
		} else if p, ok := peers[hex.EncodeToString(g.Gateway)]; ok && !m.expired(p.ID(), now) {
			// An expired gateway is in the roster until ForgetAfter, but it
			// has no tunnel, so its hosts would resolve to an address with no
			// route (F-31). A revoked one has already left the roster.
			h.GatewayName, h.Online = sanitiseName(p.Name), p.Online(now)
		} else {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GatewayName != out[j].GatewayName {
			return out[i].GatewayName < out[j].GatewayName
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// lanHostsVia is the hosts a given gateway offers, for its peer entry.
func lanHostsVia(hosts []LanHost, gateway []byte) []netip.Addr {
	var out []netip.Addr
	for _, h := range hosts {
		if !h.Self && bytes.Equal(h.Gateway, gateway) {
			out = append(out, h.Overlay)
		}
	}
	return out
}

// republishLanGrants repeats every grant held, under the same switch as
// revocations and relay advice: a node that was away needs them too.
func (m *Mesh) republishLanGrants(now time.Time) {
	if m.cfg.QuietRevocations {
		return
	}
	m.lanMu.Lock()
	raws := m.lanRawsLocked()
	m.lanMu.Unlock()
	for _, raw := range raws {
		if err := m.publishLanGrant(raw, now); err != nil {
			m.log.Debug("could not re-publish a LAN host grant", "err", err)
			return
		}
	}
}

// lanGrantsOnDiscovery repeats the grants when a peer appears, with the same
// cooldown as revocations, so a wave of arrivals costs one repetition.
func (m *Mesh) lanGrantsOnDiscovery(now time.Time) {
	if m.cfg.QuietRevocations {
		return
	}
	m.lanMu.Lock()
	if len(m.lanGrants) == 0 || (!m.lastLanOut.IsZero() && now.Sub(m.lastLanOut) < RevocationRepublishCooldown) {
		m.lanMu.Unlock()
		return
	}
	m.lastLanOut = now
	m.lanMu.Unlock()
	m.republishLanGrants(now)
}

// publishLanGrant puts a grant on the bus, sealed under the current
// generation.
func (m *Mesh) publishLanGrant(raw []byte, now time.Time) error {
	if m.node == nil {
		return errors.New("no rendezvous connection")
	}
	msg := &control.LanGrant{
		Kind:      control.KindLanGrant,
		DevicePub: m.st.Identity.DevicePub,
		Payload:   raw,
		Timestamp: now.Unix(),
	}
	sealed, err := m.keys().Seal(topic.Epoch(now), m.st.Identity.DevicePriv, msg)
	if err != nil {
		return err
	}
	_, err = m.send(SendLanGrant, sealed, now)
	return err
}

// OwnLanGrant is the grant in force for one of this device's own LAN hosts,
// or nil: what the gateway checks each connection against.
func (m *Mesh) OwnLanGrant(host string, now time.Time) *cred.LanGrant {
	if m.st == nil {
		return nil
	}
	m.lanMu.Lock()
	defer m.lanMu.Unlock()
	h, ok := m.lanGrants[lanGrantKey(m.st.Identity.DevicePub, host)]
	if !ok || !h.g.Active(now) {
		return nil
	}
	return h.g
}
