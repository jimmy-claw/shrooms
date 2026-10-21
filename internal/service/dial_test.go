package service

import (
	"context"
	"net"
	"testing"
)

// "localhost" is dialled as the loopback addresses, never looked up, so a
// hosts file that points it elsewhere cannot send a service off the machine
// (audit F-34). Both families, for a program that listens on only one.
func TestLocalhostIsDialledAsLoopback(t *testing.T) {
	for _, network := range []string{"tcp4", "tcp6"} {
		addr := "127.0.0.1:0"
		if network == "tcp6" {
			addr = "[::1]:0"
		}
		ln, err := net.Listen(network, addr)
		if err != nil {
			t.Logf("%s: %v", network, err)
			continue
		}
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		c, err := dialLocal(context.Background(), net.JoinHostPort("localhost", port))
		if err != nil {
			t.Errorf("%s: %v", network, err)
		} else {
			if got := c.RemoteAddr().String(); got != ln.Addr().String() {
				t.Errorf("%s: dialled %s, want %s", network, got, ln.Addr())
			}
			c.Close()
		}
		ln.Close()
	}
}
