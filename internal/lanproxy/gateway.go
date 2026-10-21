// Package lanproxy is the gateway side of ADR-068: it answers for the LAN hosts
// a node offers, in a userspace TCP stack, and proxies to the LAN. Its own
// package so that only a desktop daemon links gVisor; the phone, which is never
// a gateway, does not.
package lanproxy

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/lanhost"
)

// A Gateway wraps the data plane's TUN device. Packets WireGuard delivers for
// a configured host's overlay address are diverted into a userspace TCP stack
// (gVisor, through wireguard-go's netstack) instead of the kernel, and the
// stack's replies are merged back into what WireGuard reads. The host's
// overlay address therefore exists in no kernel namespace at all: nothing the
// gateway binds to its own addresses is reachable through it, which is what
// the review asked a separate namespace for.
//
// The stack listens on each host's configured ports. Every connection is
// checked, when it arrives, against an in-force grant naming this gateway,
// the host and the port; the LAN address is checked on-link and not the
// gateway's own; and only then is the LAN dialled. Only TCP enters the stack:
// UDP and ICMP for a host's address are dropped, not answered.

// Connection caps, per host and per member: an appliance on a LAN is not built
// for a crowd, and one member should not be able to fill a host's sockets.
const (
	MaxConnsPerHost   = 64
	MaxConnsPerMember = 16
	dialTimeout       = 5 * time.Second
	// packetOffset is the headroom wireguard-go reads and writes packets with
	// (device.MessageTransportHeaderSize); the kernel TUN needs it for its
	// virtio header.
	packetOffset = 16
)

// GrantFunc returns the grant in force for one of this gateway's hosts, or
// nil. It is the mesh's view, which changes as grants arrive and lapse.
type GrantFunc func(host string, now time.Time) *cred.LanGrant

type servedHost struct {
	spec    lanhost.Spec
	overlay netip.Addr
	conns   atomic.Int32
	mu      sync.Mutex
	perPeer map[netip.Addr]int
}

type packet struct {
	b   []byte
	err error
}

// Gateway is a tun.Device that answers for LAN hosts.
type Gateway struct {
	inner  tun.Device
	ns     tun.Device
	net    *netstack.Net
	self   netip.Addr
	hosts  map[netip.Addr]*servedHost
	grant  atomic.Pointer[GrantFunc]
	log    *slog.Logger
	out    chan packet
	closed chan struct{}
	once   sync.Once
	lns    []net.Listener
	// onLink is the LAN check, a field so tests can stand in for the
	// machine's interfaces.
	onLink func(netip.Addr) error
}

// NewGateway serves specs behind inner. prefix is the mesh's /48, gateway this
// device's key, self its own overlay address.
func NewGateway(inner tun.Device, specs []lanhost.Spec, prefix netip.Prefix, gateway ed25519.PublicKey,
	self netip.Addr, mtu int, log *slog.Logger) (*Gateway, error) {

	if len(specs) == 0 {
		return nil, errors.New("no LAN hosts to serve")
	}
	g := &Gateway{
		inner:  inner,
		self:   self,
		hosts:  map[netip.Addr]*servedHost{},
		log:    log,
		out:    make(chan packet, 1024),
		closed: make(chan struct{}),
		onLink: OnLink,
	}
	var addrs []netip.Addr
	for _, sp := range specs {
		o := lanhost.Overlay(prefix, gateway, sp.Name)
		g.hosts[o] = &servedHost{spec: sp, overlay: o, perPeer: map[netip.Addr]int{}}
		addrs = append(addrs, o)
	}
	ns, n, err := netstack.CreateNetTUN(addrs, nil, mtu)
	if err != nil {
		return nil, fmt.Errorf("LAN host stack: %w", err)
	}
	g.ns, g.net = ns, n
	for _, h := range g.hosts {
		for _, r := range h.spec.Ports {
			for p := int(r.From); p <= int(r.To); p++ {
				ln, err := n.ListenTCPAddrPort(netip.AddrPortFrom(h.overlay, uint16(p)))
				if err != nil {
					g.Close()
					return nil, fmt.Errorf("LAN host %s port %d: %w", h.spec.Name, p, err)
				}
				g.lns = append(g.lns, ln)
				go g.accept(h, uint16(p), ln)
			}
		}
	}
	go g.pumpInner()
	go g.pumpStack()
	return g, nil
}

// SetGrants sets where grants are looked up. Until it is called, every
// connection is refused.
func (g *Gateway) SetGrants(f GrantFunc) { g.grant.Store(&f) }

// Overlays is the hosts' overlay addresses, for status.
func (g *Gateway) Overlays() map[string]netip.Addr {
	out := make(map[string]netip.Addr, len(g.hosts))
	for o, h := range g.hosts {
		out[h.spec.Name] = o
	}
	return out
}

// dst6 is an IPv6 packet's destination, and whether it is TCP with no
// extension header in front: anything else is not for the stack.
func dst6(p []byte) (netip.Addr, bool, bool) {
	if len(p) < 40 || p[0]>>4 != 6 {
		return netip.Addr{}, false, false
	}
	var a [16]byte
	copy(a[:], p[24:40])
	return netip.AddrFrom16(a), p[6] == 6, true
}

// Write is WireGuard handing packets to the machine: those for a host go to
// the stack, the rest to the kernel.
func (g *Gateway) Write(bufs [][]byte, offset int) (int, error) {
	var kernel, stack [][]byte
	for _, b := range bufs {
		if dst, tcp, ok := dst6(b[offset:]); ok {
			if _, ours := g.hosts[dst]; ours {
				if tcp {
					stack = append(stack, b)
				}
				continue // UDP and ICMP for a host are dropped (ADR-068: TCP only)
			}
		}
		kernel = append(kernel, b)
	}
	if len(stack) > 0 {
		if _, err := g.ns.Write(stack, offset); err != nil {
			g.log.Debug("LAN host stack refused packets", "err", err)
		}
	}
	if len(kernel) == 0 {
		return len(bufs), nil
	}
	if _, err := g.inner.Write(kernel, offset); err != nil {
		return 0, err
	}
	return len(bufs), nil
}

// pumpInner reads what the machine sends into the tunnel. A packet for a host
// (the gateway's own processes reaching it) goes to the stack; the rest is
// queued for WireGuard.
func (g *Gateway) pumpInner() {
	batch := g.inner.BatchSize()
	if batch < 1 {
		batch = 1
	}
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, packetOffset+65535)
	}
	sizes := make([]int, batch)
	for {
		n, err := g.inner.Read(bufs, sizes, packetOffset)
		for i := 0; i < n; i++ {
			p := bufs[i][packetOffset : packetOffset+sizes[i]]
			if dst, tcp, ok := dst6(p); ok {
				if _, ours := g.hosts[dst]; ours {
					if tcp {
						g.ns.Write([][]byte{append(make([]byte, packetOffset), p...)}, packetOffset)
					}
					continue
				}
			}
			if !g.queue(append([]byte(nil), p...)) {
				return
			}
		}
		if err != nil {
			if errors.Is(err, tun.ErrTooManySegments) {
				continue
			}
			select {
			case g.out <- packet{err: err}:
			case <-g.closed:
			}
			return
		}
	}
}

// pumpStack reads the stack's replies. One to this gateway's own address goes
// back to the kernel; the rest is queued for WireGuard.
func (g *Gateway) pumpStack() {
	bufs := [][]byte{make([]byte, packetOffset+65535)}
	sizes := []int{0}
	for {
		n, err := g.ns.Read(bufs, sizes, packetOffset)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		p := bufs[0][packetOffset : packetOffset+sizes[0]]
		if len(p) >= 40 && p[0]>>4 == 6 {
			var a [16]byte
			copy(a[:], p[24:40])
			if netip.AddrFrom16(a) == g.self {
				g.inner.Write([][]byte{append(make([]byte, packetOffset), p...)}, packetOffset)
				continue
			}
		}
		if !g.queue(append([]byte(nil), p...)) {
			return
		}
	}
}

func (g *Gateway) queue(p []byte) bool {
	select {
	case g.out <- packet{b: p}:
		return true
	case <-g.closed:
		return false
	}
}

// Read is WireGuard taking packets to send: the machine's and the stack's,
// merged.
func (g *Gateway) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	var first packet
	select {
	case first = <-g.out:
	case <-g.closed:
		return 0, os.ErrClosed
	}
	if first.err != nil {
		return 0, first.err
	}
	n := 0
	put := func(p []byte) {
		sizes[n] = copy(bufs[n][offset:], p)
		n++
	}
	put(first.b)
	for n < len(bufs) {
		select {
		case p := <-g.out:
			if p.err != nil {
				// Delivered on the next call, so these packets are not lost.
				go func() { g.out <- p }()
				return n, nil
			}
			put(p.b)
		default:
			return n, nil
		}
	}
	return n, nil
}

func (g *Gateway) File() *os.File           { return g.inner.File() }
func (g *Gateway) MTU() (int, error)        { return g.inner.MTU() }
func (g *Gateway) Name() (string, error)    { return g.inner.Name() }
func (g *Gateway) Events() <-chan tun.Event { return g.inner.Events() }
func (g *Gateway) BatchSize() int           { return g.inner.BatchSize() }

// Close stops the stack and the device beneath.
func (g *Gateway) Close() error {
	var err error
	g.once.Do(func() {
		close(g.closed)
		for _, ln := range g.lns {
			ln.Close()
		}
		if g.ns != nil {
			g.ns.Close()
		}
		err = g.inner.Close()
	})
	return err
}

// accept serves one host port.
func (g *Gateway) accept(h *servedHost, port uint16, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go g.serve(h, port, c)
	}
}

func (g *Gateway) serve(h *servedHost, port uint16, c net.Conn) {
	defer c.Close()
	from := remoteAddr(c)
	now := time.Now()
	if gf := g.grant.Load(); gf == nil {
		g.log.Info("refused a LAN host connection: no grants known yet", "host", h.spec.Name, "port", port, "from", from)
		return
	} else if gr := (*gf)(h.spec.Name, now); gr == nil || !gr.Active(now) || !gr.Contains(port) {
		g.log.Info("refused a LAN host connection: no grant in force for that host and port",
			"host", h.spec.Name, "port", port, "from", from)
		return
	}
	if !h.take(from) {
		g.log.Info("refused a LAN host connection: too many at once", "host", h.spec.Name, "from", from)
		return
	}
	defer h.give(from)
	if err := g.onLink(h.spec.Addr); err != nil {
		g.log.Warn("refused a LAN host connection: its LAN address is not usable here", "host", h.spec.Name, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	lan, err := (&net.Dialer{}).DialContext(ctx, "tcp", netip.AddrPortFrom(h.spec.Addr, port).String())
	cancel()
	if err != nil {
		g.log.Info("LAN host did not answer", "host", h.spec.Name, "port", port, "err", err)
		return
	}
	defer lan.Close()
	g.log.Debug("LAN host connection", "host", h.spec.Name, "port", port, "from", from)
	splice(c, lan)
}

func (h *servedHost) take(from netip.Addr) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns.Load() >= MaxConnsPerHost || h.perPeer[from] >= MaxConnsPerMember {
		return false
	}
	h.conns.Add(1)
	h.perPeer[from]++
	return true
}

func (h *servedHost) give(from netip.Addr) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns.Add(-1)
	if h.perPeer[from]--; h.perPeer[from] <= 0 {
		delete(h.perPeer, from)
	}
}

func remoteAddr(c net.Conn) netip.Addr {
	if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
		return ap.Addr()
	}
	return netip.Addr{}
}

// splice copies both ways, passing on a half-close, until both sides end.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// OnLink checks a LAN address against this machine's interfaces now: it must
// be inside a subnet of an up, non-loopback interface that is not a mesh
// tunnel, and must not be one of this machine's own addresses.
func OnLink(a netip.Addr) error {
	ifs, err := net.Interfaces()
	if err != nil {
		return err
	}
	on := false
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || meshInterface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			ipn, ok := ad.(*net.IPNet)
			if !ok {
				continue
			}
			own, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			if own.Unmap() == a {
				return fmt.Errorf("%s is this gateway's own address", a)
			}
			ones, _ := ipn.Mask.Size()
			if netip.PrefixFrom(own.Unmap(), ones).Masked().Contains(a) {
				on = true
			}
		}
	}
	if !on {
		return fmt.Errorf("%s is not on a LAN this gateway is on", a)
	}
	return nil
}

// meshInterface is a tunnel of ours: a host behind one would be reached over
// the mesh, which is a member's job, not a LAN host's.
func meshInterface(name string) bool {
	return strings.HasPrefix(name, "logos") || strings.HasPrefix(name, "shrooms")
}
