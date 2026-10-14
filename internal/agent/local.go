package agent

// The agent's own socket, and the agent without a mesh (ADR-056). It serves
// on a unix socket only its user can open, always; with no shrooms daemon it
// serves on that alone, and this machine is the only one. A request over the
// socket is local, as one from this machine's own mesh address is: the
// socket's permissions decided who could make it.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"
)

// LocalAddr stands for this machine where an address is needed and there is
// no mesh to give one: the loopback address, which no request reaching this
// agent over TCP carries in local mode, since then it listens on no TCP.
var LocalAddr = netip.IPv6Loopback()

// SocketPath is where an agent serves, and where its own tools find it:
// $SHROOMS_AGENT_SOCKET, or shrooms-agent/agent.sock in the user's runtime
// directory, or under ~/.local/state when there is none.
func SocketPath() string {
	if p := os.Getenv("SHROOMS_AGENT_SOCKET"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "shrooms-agent", "agent.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "shrooms-agent", "agent.sock")
}

// ListenSocket listens on path, in a directory only this user can enter, as a
// socket only this user can open. A socket left by an agent that is gone is
// replaced; one an agent still answers on is not.
func ListenSocket(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s is there and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("another agent is serving on %s", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// SocketUp is whether an agent answers on path.
func SocketUp(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

type overSocketKey struct{}

// MarkLocal is an http.Server's ConnContext: it marks the connections that
// came over a unix socket, which from and the asker's address then know as
// this machine's.
func MarkLocal(ctx context.Context, c net.Conn) context.Context {
	if _, ok := c.(*net.UnixConn); ok {
		return context.WithValue(ctx, overSocketKey{}, true)
	}
	return ctx
}

func overSocket(r *http.Request) bool {
	v, _ := r.Context().Value(overSocketKey{}).(bool)
	return v
}

// remoteAddr is where a request came from: its TCP peer, or LocalAddr over
// the socket.
func remoteAddr(r *http.Request) (netip.Addr, error) {
	if overSocket(r) {
		return LocalAddr, nil
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, err
	}
	return a.Unmap(), nil
}

// selfName is this machine's name as its sessions sign their tasks: the
// mesh's name for it, or the hostname with no mesh (machines, first).
func (m *Manager) selfName() string {
	if ms, err := m.machines(); err == nil && len(ms) > 0 {
		return ms[0].Name
	}
	host, _ := os.Hostname()
	return host
}

// mcpArgs tell the MCP tools a session starts which daemon and which agent
// are theirs: this agent's own.
func (m *Manager) mcpArgs() []string {
	var a []string
	if m.DaemonSocket != "" {
		a = append(a, "--socket", m.DaemonSocket)
	}
	if m.Socket != "" {
		a = append(a, "--agent-socket", m.Socket)
	}
	return a
}

// MCPArgs is mcpArgs, for the pi config cmd/shrooms-agent writes.
func (m *Manager) MCPArgs() []string { return m.mcpArgs() }

// client is an HTTP client to the agent at addr: through this agent's own
// socket for LocalAddr, over the mesh for any other.
func (m *Manager) client(addr netip.Addr, timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if addr == LocalAddr && m.Socket != "" && m.agentAt == nil {
		sock := m.Socket
		c.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}
	}
	return c
}
