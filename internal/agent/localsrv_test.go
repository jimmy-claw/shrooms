package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
)

// localServer serves h as the agent's socket does: its callers are this
// machine's user. A test's server listens on loopback, which the agent no
// longer takes for local (audit F-24), so it is marked as the socket marks.
func localServer(h http.Handler) *httptest.Server {
	ts := httptest.NewUnstartedServer(h)
	ts.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, overSocketKey{}, true)
	}
	ts.Start()
	return ts
}
