package mesh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/state"
)

// Holding an invite open, on the node that is already a member.
//
// The alternative was for `shrooms invite` to start a Logos Delivery node of
// its own, which worked and was unpleasant: three seconds of dialling, a page
// of library logging that cannot be turned off, and a second Core node joining
// the fleet for the sake of two messages. The daemon is already connected.
//
// The split is the same one the admin key exists for. The daemon holds the
// network key and the connection and never sees the admin key; the CLI holds
// the admin key and signs, and never sees the network key. Neither half can
// admit a device alone.

// invites are the invite topics this node is currently listening on, by content
// topic. Normally empty, and at most one entry outside a test.
type invites struct {
	mu sync.Mutex
	by map[string]*heldInvite

	// admitting is which device each open exchange is admitting, kept after
	// HoldInvite has returned so a credential offered later can be checked
	// against it. See rememberAdmitting.
	admitting map[string]admitting

	// answered is the admission answer for each consumed invite, kept until
	// the invite's own deadline so a device that missed it can be given the
	// same bytes again. See handleInvite.
	answered map[string]*answeredInvite

	// bus is where invite traffic goes. The node, except in tests: the
	// exchange's whole point is what arrives and what is lost on the way, and
	// the real node cannot be told to lose a message.
	bus inviteBus
}

// inviteBus is the part of the rendezvous node an invite needs to hold a topic
// and answer on it. *waku.Node has exactly these.
type inviteBus interface {
	Subscribe(contentTopic string) error
	Unsubscribe(contentTopic string) error
	Send(contentTopic string, payload []byte, ephemeral bool) (string, error)
}

func (m *Mesh) invBus() inviteBus {
	if m.inv.bus != nil {
		return m.inv.bus
	}
	return m.node
}

// answeredInvite is one admission answer, and who may have it again.
//
// Keyed by BOTH keys the consuming request carried — the ephemeral key the
// answer is sealed to, and the device key the credential names. A request that
// matches both is the device that was admitted, asking again because it did not
// hear the answer; it gets the identical sealed bytes, which only its ephemeral
// private key opens. Anything else presenting the token gets nothing, which is
// what "an invite admits one device" has always meant.
type answeredInvite struct {
	secret    invite.Secret
	name      string
	ephPub    []byte
	devicePub []byte
	until     time.Time

	// blob is set by ReplyInvite. Until then the exchange is between the
	// request and the admin's signature, and repeats are dropped.
	blob     []byte
	lastSent time.Time
	resent   int
}

// inviteResendEvery and inviteResendCap bound what a replayed request can make
// this node publish: at most one copy per interval and a handful in all, for
// the invite's remaining life. Variables so tests can run at millisecond scale.
var (
	inviteResendEvery = 2 * time.Second
	inviteResendCap   = 30
)

// admitting is the device keys one exchange handed out, and when to forget them.
type admitting struct {
	devicePub []byte
	wgPub     []byte
	until     time.Time
}

type heldInvite struct {
	secret invite.Secret
	reqs   chan *invite.Request
}

// Hold subscribes to an invite's topic and returns the first request that opens
// under the token.
//
// One request, then done: an invite admits one device, and the decision is
// local to this node because nobody else is listening on that topic. Returns
// ctx.Err() if the invite expires first.
func (m *Mesh) HoldInvite(ctx context.Context, s invite.Secret) (*invite.Request, error) {
	name := s.Topic()

	held := &heldInvite{secret: s, reqs: make(chan *invite.Request, 1)}
	m.inv.mu.Lock()
	if m.inv.by == nil {
		m.inv.by = make(map[string]*heldInvite)
	}
	if _, busy := m.inv.by[name]; busy {
		m.inv.mu.Unlock()
		return nil, errors.New("that invite is already open")
	}
	m.inv.by[name] = held
	m.inv.mu.Unlock()

	// How long this invite lives: its holder's deadline, or the default.
	until := time.Now().Add(invite.DefaultTTL)
	if d, ok := ctx.Deadline(); ok {
		until = d
	}

	// Stay subscribed after a request is taken, unless nothing was.
	//
	// This used to unsubscribe the moment HoldInvite returned — before the
	// admin had even signed — so a device that missed the one answer was
	// asking again into a topic nobody listened to. Three devices in a row on
	// 2026-09-29: the inviter logged "admitted a device" every time and none of
	// them heard it. Now the topic stays open until the invite's deadline,
	// which is what lets handleInvite give the admitted device its answer again.
	kept := false
	defer func() {
		m.inv.mu.Lock()
		delete(m.inv.by, name)
		m.inv.mu.Unlock()
		if kept {
			return
		}
		// Unsubscribing is best-effort: a stale subscription costs a little
		// traffic on one topic, where failing here would abort a completed
		// enrolment.
		if err := m.invBus().Unsubscribe(name); err != nil {
			m.log.Debug("could not unsubscribe from an invite topic", "err", err)
		}
	}()

	if err := m.invBus().Subscribe(name); err != nil {
		return nil, fmt.Errorf("subscribe to the invite topic: %w", err)
	}
	// The fleet is logged on both ends deliberately: if they differ, both sides
	// work perfectly and never meet, and this is the only place it shows.
	m.log.Info("holding an invite open", "preset", m.cfg.Preset, "cluster", m.cfg.ClusterID)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case req := <-held.reqs:
		m.rememberAdmitting(name, req, until)
		m.keepAnswering(name, s, req, until)
		kept = true
		return req, nil
	}
}

// rememberAdmitting records which device an exchange is admitting.
//
// HoldInvite deletes the held entry as it returns, so without this the daemon
// forgets the request the moment it hands it to whoever will sign for it — and
// has no way to tell, when a credential comes back, whether it names the device
// that actually asked to join.
//
// That check is what lets /invite/reply be reachable from the socket group
// (ADR-033). Anybody may mint a token and walk a device of their own through
// the exchange, so the question is not who is calling but whether the admin
// signed for the device in front of us.
//
// Kept for the token's remaining life, keyed by topic because the token is what
// names the exchange and both ends already derive it.
func (m *Mesh) rememberAdmitting(topic string, req *invite.Request, until time.Time) {
	if req == nil {
		return
	}
	m.inv.mu.Lock()
	defer m.inv.mu.Unlock()
	if m.inv.admitting == nil {
		m.inv.admitting = map[string]admitting{}
	}
	now := time.Now()
	// Swept here rather than on a timer: this runs once per enrolment, which is
	// rare, and an entry nobody asks about costs two keys until the next one.
	for t, a := range m.inv.admitting {
		if now.After(a.until) {
			delete(m.inv.admitting, t)
		}
	}
	m.inv.admitting[topic] = admitting{
		devicePub: append([]byte(nil), req.DevicePub...),
		wgPub:     append([]byte(nil), req.WGPub...),
		until:     until,
	}
}

// Admitting reports the device keys this node handed out for a token's
// exchange, so a credential offered for it can be checked against them.
func (m *Mesh) Admitting(topic string) (devicePub, wgPub []byte, ok bool) {
	m.inv.mu.Lock()
	defer m.inv.mu.Unlock()
	a, ok := m.inv.admitting[topic]
	if !ok || time.Now().After(a.until) {
		return nil, nil, false
	}
	return a.devicePub, a.wgPub, true
}

// answerDeferred replies to a first-round request with enough to identify the
// mesh and nothing else, so the joiner can derive the identity it will use here
// and ask again for a credential naming it (ADR-017).
//
// It sends the mesh *id* rather than the network key. The id is a one-way hash
// of the key (state.NetworkID) and is the only thing the joiner needs from this
// round: it derives a per-mesh identity from it and comes back. Sending the key
// here handed the mesh's secret to anyone holding the token, on a round that by
// design does not consume the invite and answers as often as it is asked - so
// the "one device, once" the token promises was never enforced on this path.
//
// Answered by the daemon rather than surfaced to the CLI, because nothing in it
// needs the admin key. The invite stays open for the second request, which is
// the one that carries a secret and the one worth a person looking at.
func (m *Mesh) answerDeferred(s invite.Secret, req *invite.Request) {
	if err := m.replyMeshOnly(s, req); err != nil {
		m.log.Warn("could not answer the first round of an invite", "err", err)
		return
	}
	m.log.Info("told a joining device which mesh this is; waiting for its per-mesh keys",
		"name", req.Name)
}

// ReplyInvite seals a response under the token and publishes it.
//
// The mesh's own values — network key, admin keys, name suffix — are filled in
// here rather than by the caller, so the CLI never handles the network key and
// nothing but a well-formed invite response can be published on this topic.
func (m *Mesh) ReplyInvite(s invite.Secret, req *invite.Request, credential []byte) error {
	if req == nil {
		return errors.New("no request to reply to")
	}
	resp := m.inviteResponse()
	resp.NetworkKey = m.nk[:]
	resp.Credential = credential
	blob, err := invite.SealResponse(s, req.EphPub, resp)
	if err != nil {
		return err
	}
	if _, err := m.invBus().Send(s.Topic(), blob, true); err != nil {
		return fmt.Errorf("publish the invite response: %w", err)
	}
	m.storeAnswer(s.Topic(), req.EphPub, blob, time.Now())
	m.log.Info("admitted a device", "name", req.Name, "credential", len(credential) > 0)
	return nil
}

// keepAnswering records that an invite has been consumed by this request, and
// arranges to stop listening when the invite's life is over.
func (m *Mesh) keepAnswering(topic string, s invite.Secret, req *invite.Request, until time.Time) {
	m.inv.mu.Lock()
	if m.inv.answered == nil {
		m.inv.answered = map[string]*answeredInvite{}
	}
	m.inv.answered[topic] = &answeredInvite{
		secret:    s,
		name:      req.Name,
		ephPub:    append([]byte(nil), req.EphPub...),
		devicePub: append([]byte(nil), req.DevicePub...),
		until:     until,
	}
	m.inv.mu.Unlock()
	time.AfterFunc(time.Until(until), func() { m.forgetAnswered(topic) })
}

// storeAnswer keeps the exact sealed answer, but only for the request that
// consumed the invite. The ephemeral key the reply was sealed to must be the
// one that request carried, or the stored bytes would be an answer to somebody
// else — and would never open for the device asking again anyway.
func (m *Mesh) storeAnswer(topic string, ephPub, blob []byte, now time.Time) {
	m.inv.mu.Lock()
	defer m.inv.mu.Unlock()
	a := m.inv.answered[topic]
	if a == nil || !bytes.Equal(a.ephPub, ephPub) {
		return
	}
	a.blob = append([]byte(nil), blob...)
	a.lastSent = now
}

// forgetAnswered drops a consumed invite once its life is over and stops
// listening on its topic, unless somebody is holding it again.
func (m *Mesh) forgetAnswered(topic string) {
	m.inv.mu.Lock()
	delete(m.inv.answered, topic)
	_, held := m.inv.by[topic]
	m.inv.mu.Unlock()
	if held {
		return
	}
	if err := m.invBus().Unsubscribe(topic); err != nil {
		m.log.Debug("could not unsubscribe from an invite topic", "err", err)
	}
}

// inviteResponse is everything an invite answer carries that is not a secret:
// which mesh this is, who may sign for it, and the DNS suffix.
func (m *Mesh) inviteResponse() *invite.Response {
	resp := &invite.Response{
		MeshID:    state.NetworkID(m.nk),
		Label:     suggestedLabel(m.cfg.MeshLabel),
		Timestamp: time.Now().Unix(),
	}
	if m.authority != nil {
		for _, k := range m.authority.Keys {
			resp.AdminKeys = append(resp.AdminKeys, append([]byte(nil), k...))
		}
	}
	return resp
}

// suggestedLabel is the name offered to a joiner: this device's own name for
// the mesh, unless that is "default". That is not a name — it is what the old
// single-mesh config shape was called — and passing it on would give the joiner
// the very label that answers to nothing anybody else uses.
func suggestedLabel(label string) string {
	if label == state.DefaultLabel {
		return ""
	}
	return label
}

// replyMeshOnly answers the first round. No network key and no credential: this
// round does not consume the invite, so whatever it sends can be had as many
// times as the token is presented.
func (m *Mesh) replyMeshOnly(s invite.Secret, req *invite.Request) error {
	if req == nil {
		return errors.New("no request to reply to")
	}
	blob, err := invite.SealResponse(s, req.EphPub, m.inviteResponse())
	if err != nil {
		return err
	}
	if _, err := m.invBus().Send(s.Topic(), blob, true); err != nil {
		return fmt.Errorf("publish the invite response: %w", err)
	}
	return nil
}

// handleInvite offers a message to any invite being held. Reports whether it
// belonged to one, so the ordinary control-plane path can skip it.
func (m *Mesh) handleInvite(contentTopic string, payload []byte, now time.Time) bool {
	m.inv.mu.Lock()
	held, ok := m.inv.by[contentTopic]
	m.inv.mu.Unlock()
	if !ok {
		return m.answerAgain(contentTopic, payload, now)
	}

	// The token is what opens it. Our own response comes back to us on the same
	// topic and does not open as a request, which is the only filtering needed.
	req, err := invite.OpenRequest(held.secret, payload, now)
	if err != nil {
		return true // ours by topic, not readable as a request: drop it quietly
	}
	// A first-round request is answered here and does not consume the invite:
	// it asks which mesh this is, and the answer holds no secret the token does
	// not already imply. The device comes back with keys derived for this mesh,
	// and that request is the one that admits it.
	//
	// A device that asks twice gets told twice, which costs two sealed messages
	// on a topic only it and this node are listening to.
	if req.Deferred {
		go m.answerDeferred(held.secret, req)
		return true
	}

	select {
	case held.reqs <- req:
	default: // already answered; an invite admits one device
	}
	return true
}

// answerAgain gives an admitted device its answer again, and nobody else
// anything.
//
// The answer to a consuming request is published once and ephemerally: the
// fleet does not store it, so a joiner whose filter subscription was not yet
// live when it went out never sees it, and every retry used to be dropped
// because the invite was spent. That is not rare. A cold phone publishes
// through lightpush within a second or two but receives through a filter
// subscription that takes longer, and on 2026-09-29 three devices in a row were
// "admitted" and heard nothing.
//
// What makes this safe to repeat, and not a way to reuse a QR code:
//
//   - only a request carrying the SAME ephemeral key and the SAME device key as
//     the one that consumed the invite matches. Within one attempt the joiner
//     re-sends the identical sealed request, so this is the device that asked.
//     A second holder of the token has neither key, and gets nothing;
//   - what is sent is the stored sealed blob, byte for byte — never re-sealed,
//     never a fresh credential. It opens only under the ephemeral private key,
//     which never left the joiner, so a replayed request buys an eavesdropper
//     a ciphertext it already had;
//   - it is rate-limited and capped, and forgotten at the invite's deadline, so
//     replaying a captured request cannot make this node chatter.
//
// Reports whether the message belonged to an invite this node knows.
func (m *Mesh) answerAgain(topic string, payload []byte, now time.Time) bool {
	m.inv.mu.Lock()
	a, ok := m.inv.answered[topic]
	if !ok {
		m.inv.mu.Unlock()
		return false
	}
	if now.After(a.until) {
		delete(m.inv.answered, topic)
		m.inv.mu.Unlock()
		return true
	}
	secret := a.secret
	m.inv.mu.Unlock()

	req, err := invite.OpenRequest(secret, payload, now)
	if err != nil {
		return true // our topic, not a request: our own answer coming back
	}

	m.inv.mu.Lock()
	if !a.mayResend(req, now) {
		m.inv.mu.Unlock()
		return true
	}
	a.resent++
	a.lastSent = now
	blob, name := a.blob, a.name
	m.inv.mu.Unlock()

	// Off the reader: a publish can block, and this runs on the node's event
	// path, which every mesh in the process shares.
	go func() {
		_, err := m.invBus().Send(topic, blob, true)
		recordSend(m.cfg.MeshLabel, SendInvite, len(blob), err, now)
		if err != nil {
			m.log.Warn("could not re-send an invite answer", "name", name, "err", err)
			return
		}
		m.log.Info("re-sent the invite answer to the device that asked", "name", name)
	}()
	return true
}

// mayResend decides whether a request gets the stored answer again. Called
// under m.inv.mu.
func (a *answeredInvite) mayResend(req *invite.Request, now time.Time) bool {
	switch {
	case a.blob == nil:
		return false // the admin has not signed yet; the joiner will ask again
	case req.Deferred:
		return false // a first-round request for a spent invite: nothing to add
	case !bytes.Equal(req.EphPub, a.ephPub), !bytes.Equal(req.DevicePub, a.devicePub):
		return false // somebody else holding the token: they get nothing
	case a.resent >= inviteResendCap:
		return false
	case now.Sub(a.lastSent) < inviteResendEvery:
		return false
	}
	return true
}
