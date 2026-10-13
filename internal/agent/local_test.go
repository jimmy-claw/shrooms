package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A short directory: a unix socket's path is limited to about 100 bytes, and
// t.TempDir's can be longer.
func sockDir(t *testing.T) string {
	d, err := os.MkdirTemp("", "sa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// The socket is its user's alone, a live one is not taken over, and one left
// by an agent that is gone is.
func TestTheSocketIsItsUsersAlone(t *testing.T) {
	path := filepath.Join(sockDir(t), "run", "agent.sock")
	ln, err := ListenSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: %o, want %o", p, got, want)
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := ListenSocket(path); err == nil || !strings.Contains(err.Error(), "another agent") {
		t.Errorf("a second agent on a live socket: %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	ln2, err := ListenSocket(path)
	if err != nil {
		t.Fatalf("a socket left behind is not replaced: %v", err)
	}
	ln2.Close()

	plain := filepath.Join(sockDir(t), "agent.sock")
	os.WriteFile(plain, []byte("x"), 0o600)
	if _, err := ListenSocket(plain); err == nil {
		t.Error("a file that is not a socket was replaced")
	}
}

// A request over the socket is this machine's: local for the sessions it may
// see, and LocalAddr as the address an asker is answered at.
func TestOverTheSocketIsHere(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Meshes = func() []MeshInfo { return []MeshInfo{{"office", netip.MustParseAddr("fd00::1")}} }
	h := &handler{m: m, who: func(netip.Addr) string { return "" }}
	path := filepath.Join(sockDir(t), "agent.sock")
	ln, err := ListenSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	type seen struct {
		local bool
		addr  netip.Addr
	}
	got := make(chan seen, 1)
	srv := &http.Server{ConnContext: MarkLocal, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, local := h.from(r)
		a, _ := remoteAddr(r)
		got <- seen{local, a}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	m.Socket = path
	resp, err := m.client(LocalAddr, 0).Get("http://agent/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if s := <-got; !s.local || s.addr != LocalAddr {
		t.Errorf("over the socket: local %v, address %v; want local, %v", s.local, s.addr, LocalAddr)
	}
}

// LocalAddr is reached through the agent's socket, and only LocalAddr: any
// other address goes over the network as before.
func TestOnlyLocalAddrGoesThroughTheSocket(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	path := filepath.Join(sockDir(t), "agent.sock")
	ln, err := ListenSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "socket") })}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	m.Socket = path

	resp, err := m.client(LocalAddr, 0).Get(m.agentURL(LocalAddr) + "/x")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "socket" {
		t.Errorf("LocalAddr answered %q", b)
	}
	if c := m.client(netip.MustParseAddr("fd00::7"), 0); c.Transport != nil {
		t.Error("a mesh address would go through the socket")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://[fd00::7]:7387/x", nil)
	if _, err := m.client(netip.MustParseAddr("fd00::7"), 0).Do(req); err == nil {
		t.Error("a cancelled request to a mesh address succeeded")
	}
}
