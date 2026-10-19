package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The forwarder passes /<upstream>/<path> to /llm/<upstream>/<path> on the
// cage's socket, with the query, and returns the reply.
func TestTheForwarderGoesToTheSocket(t *testing.T) {
	d, _ := os.MkdirTemp("", "lf")
	defer os.RemoveAll(d)
	sock := filepath.Join(d, "proxy.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Method + " " + r.URL.RequestURI()
		io.WriteString(w, "reply")
	}))
	srv := httptest.NewServer(llmForwarder(sock))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/anthropic/v1/messages?beta=true", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if seen != "POST /llm/anthropic/v1/messages?beta=true" || string(b) != "reply" {
		t.Fatalf("the socket saw %q, the harness got %q", seen, b)
	}
}
