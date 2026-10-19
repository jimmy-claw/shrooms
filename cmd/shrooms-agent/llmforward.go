package main

// shrooms-agent llm-forward: run in a cage, it gives the harness an http://
// base URL for its model and passes each request to the cage's socket to its
// agent, which forwards it with the real credentials (ADR-059). It holds no
// secret: what it passes on carries none, and what it gets back is the reply.

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

func llmForwardMain(args []string) int {
	fs := flag.NewFlagSet("llm-forward", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:7390", "where the harness reaches it")
	sock := fs.String("socket", "/run/shrooms-agent/proxy.sock", "the cage's socket to its agent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	h := llmForwarder(*sock)
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "llm-forward:", err)
		return 1
	}
	return 0
}

// llmForwarder passes /<upstream>/<path> to /llm/<upstream>/<path> on the
// socket, streaming the reply as it comes.
func llmForwarder(sock string) http.Handler {
	target, _ := url.Parse("http://cage")
	rp := httputil.NewSingleHostReverseProxy(target)
	base := rp.Director
	rp.Director = func(r *http.Request) {
		base(r)
		r.URL.Path = "/llm" + r.URL.Path
		r.URL.RawPath = ""
		r.Host = "cage"
	}
	rp.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	rp.FlushInterval = -1
	return rp
}
