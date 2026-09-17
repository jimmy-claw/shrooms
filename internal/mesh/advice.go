package mesh

import (
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/relay"
	"github.com/vpavlin/shrooms/internal/topic"
)

// Blind relays named by the mesh's admin (docs/distributing-a-blind-relay.md).
//
// A blind relay cannot announce itself, so until this existed it had to be
// typed into every device. The admin now signs one statement naming them, any
// member carries it, and each node verifies it against the authority before
// using it — the same trust model as a revocation.
//
// Advice, not an instruction. It is adopted only when this device configured
// no blind relays of its own and did not say relay_blind = "none". A device
// that chose its relays keeps them; one that refused keeps refusing. Either way
// the statement is still held and repeated, because the next member may want
// it.

// allRelays is every relay this node may use: the configured ones in the
// operator's order, then the ones the admin advised.
//
// Configured first, so that where both exist — a pinned member relay and
// advised blind ones — the local choice keeps its precedence in every ordered
// walk. The adopted half is swapped whole when a new statement arrives, so a
// caller holds a consistent list without taking a lock.
func (m *Mesh) allRelays() []relayTarget {
	adopted := m.adopted.Load()
	if adopted == nil || len(*adopted) == 0 {
		return m.relays
	}
	out := make([]relayTarget, 0, len(m.relays)+len(*adopted))
	out = append(out, m.relays...)
	return append(out, *adopted...)
}

// AdoptedRelays are the blind relays in use because the admin named them.
func (m *Mesh) AdoptedRelays() []netip.AddrPort {
	adopted := m.adopted.Load()
	if adopted == nil {
		return nil
	}
	out := make([]netip.AddrPort, 0, len(*adopted))
	for _, t := range *adopted {
		out = append(out, t.addr)
	}
	return out
}

// RelayAdviceSerial is the serial of the statement this node holds, or zero.
func (m *Mesh) RelayAdviceSerial() uint64 {
	m.adviceMu.Lock()
	defer m.adviceMu.Unlock()
	if m.advice == nil {
		return 0
	}
	return m.advice.Serial
}

// adoptsAdvice says whether this device lets the admin choose its blind relays.
func (m *Mesh) adoptsAdvice() bool {
	return !m.cfg.RelayNone && len(m.cfg.RelayBlind) == 0
}

// loadRelayAdvice restores the statement this node held before it stopped,
// verifying it again: the file is the same shape as a message from a peer.
func (m *Mesh) loadRelayAdvice() {
	if m.authority == nil {
		return
	}
	raw := m.st.RelayAdvice(m.networkID)
	if raw == nil {
		return
	}
	if _, err := m.applyRelayAdvice(raw, false); err != nil {
		m.log.Warn("dropping stored relay advice", "err", err)
	}
}

// RelayAdvice takes a statement from the admin tooling and puts it on the bus,
// verified exactly as one from a peer would be.
func (m *Mesh) RelayAdvice(raw []byte) error {
	if _, err := m.applyRelayAdvice(raw, true); err != nil {
		return err
	}
	// Published even when already held: the admin asked for it to go out, and
	// saying it again is always safe.
	return m.publishRelayAdvice(raw, time.Now())
}

// handleRelayAdvice takes a statement off the bus.
func (m *Mesh) handleRelayAdvice(raw []byte, now time.Time) {
	fresh, err := m.applyRelayAdvice(raw, true)
	if err != nil {
		m.log.Warn("ignoring relay advice", "err", err)
		return
	}
	if fresh {
		// New to us may be new to a peer that was away when the admin
		// published it.
		if err := m.publishRelayAdvice(raw, now); err != nil {
			m.log.Debug("could not relay relay advice", "err", err)
		}
	}
}

// errStaleAdvice is a statement no newer than the one held. Not worth a
// warning: every node repeats what it holds, so most arrivals are this.
var errStaleAdvice = errors.New("not newer than the relay advice already held")

// applyRelayAdvice verifies a statement and, when it is newer than the one
// held, keeps it and applies it. Reports whether it was new.
func (m *Mesh) applyRelayAdvice(raw []byte, persist bool) (bool, error) {
	if m.authority == nil {
		return false, errors.New("this mesh has no admin keys, so nobody can advise it")
	}
	a, err := cred.UnmarshalRelayAdvice(raw)
	if err != nil {
		return false, fmt.Errorf("unreadable relay advice: %w", err)
	}
	if err := cred.VerifyRelayAdviceBy(m.authority, a); err != nil {
		return false, fmt.Errorf("this mesh did not sign that relay advice: %w", err)
	}

	// Held throughout, so two newer statements arriving at once are applied
	// in serial order and not in whichever order their goroutines ran.
	m.adviceMu.Lock()
	defer m.adviceMu.Unlock()
	if m.advice != nil && a.Serial <= m.advice.Serial {
		if a.Serial == m.advice.Serial {
			return false, nil
		}
		return false, errStaleAdvice
	}
	m.advice = a
	m.adviceRaw = append([]byte(nil), raw...)

	if persist {
		if err := m.st.SetRelayAdvice(m.networkID, raw); err != nil {
			m.log.Error("could not persist relay advice; it will be relearned from peers after a restart", "err", err)
		}
	}

	if !m.adoptsAdvice() {
		m.log.Info("relay advice from the admin held but not used: this device configures its own blind relays",
			"serial", a.Serial, "relays", a.Relays, "refused", m.cfg.RelayNone)
		return true, nil
	}
	m.adoptRelays(a)
	return true, nil
}

// adoptRelays makes an advised list the one this node uses. The caller holds
// adviceMu.
func (m *Mesh) adoptRelays(a *cred.RelayAdvice) {
	key := relay.OpenKey()
	if a.Token != "" {
		key = relay.TokenKey(a.Token)
	}
	targets := make([]relayTarget, 0, len(a.Relays))
	for _, ap := range a.Relays {
		t := relayTarget{addr: ap, key: key, blind: true}
		targets = append(targets, t)
		// ParseEndpoint rebuilds relay endpoints from strings, and needs to
		// know how to speak to this one.
		if m.dev != nil {
			m.dev.Bind.SetRelayIdentityFor(ap, key, m.handleFor(t, m.st.Identity.WGPub))
		}
	}
	m.adopted.Store(&targets)
	if len(targets) == 0 {
		m.log.Info("the admin withdrew its blind relay advice", "serial", a.Serial)
	} else {
		m.log.Info("adopted blind relay from the admin", "relays", a.Relays,
			"serial", a.Serial, "token", a.Token != "")
	}
	// Routing reads the list; registration picks it up on the next probe
	// tick, which is when selectRelay first sees the new relays.
	m.requestResync()
}

// republishRelayAdvice repeats the held statement, on the same schedule and
// under the same switch as revocations: it is the other admin statement that
// has to reach a node that was not listening when it was first said.
//
// announce_revocations = "false" silences it too. That setting exists for a
// metered uplink, and the reason applies unchanged; a second switch would be a
// second thing to find.
func (m *Mesh) republishRelayAdvice(now time.Time) {
	if m.cfg.QuietRevocations {
		return
	}
	m.adviceMu.Lock()
	raw := m.adviceRaw
	m.adviceMu.Unlock()
	if raw == nil {
		return
	}
	if err := m.publishRelayAdvice(raw, now); err != nil {
		m.log.Debug("could not re-publish relay advice", "err", err)
	}
}

// adviceOnDiscovery repeats the held statement when a peer appears, so a node
// that joined after the admin spoke is not left without a relay until the next
// epoch. The cooldown makes a wave of arrivals cost one repetition, as it does
// for revocations.
func (m *Mesh) adviceOnDiscovery(now time.Time) {
	if m.shouldRepeatAdvice(now) {
		m.republishRelayAdvice(now)
	}
}

// shouldRepeatAdvice is the decision in adviceOnDiscovery, split out so it can
// be tested without a rendezvous node.
func (m *Mesh) shouldRepeatAdvice(now time.Time) bool {
	if m.cfg.QuietRevocations {
		return false
	}
	m.adviceMu.Lock()
	defer m.adviceMu.Unlock()
	if m.adviceRaw == nil {
		return false
	}
	if !m.lastAdviceOut.IsZero() && now.Sub(m.lastAdviceOut) < RevocationRepublishCooldown {
		return false
	}
	m.lastAdviceOut = now
	return true
}

// publishRelayAdvice puts a statement on the bus, sealed under the current
// generation: it may carry a relay token, and a device rotated out of the
// mesh should not learn a new one.
func (m *Mesh) publishRelayAdvice(raw []byte, now time.Time) error {
	if m.node == nil {
		return errors.New("no rendezvous connection")
	}
	msg := &control.Advice{
		Kind:      control.KindAdvice,
		DevicePub: m.st.Identity.DevicePub,
		Payload:   raw,
		Timestamp: now.Unix(),
	}
	sealed, err := m.keys().Seal(topic.Epoch(now), m.st.Identity.DevicePriv, msg)
	if err != nil {
		return err
	}
	_, err = m.node.Send(topic.Current(m.nk, now), sealed, true)
	return err
}
