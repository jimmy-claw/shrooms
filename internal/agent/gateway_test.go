package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A sealed cage's request reaches the model with the machine's token and not
// with what the cage sent; only a model's endpoints are forwarded; the reply
// streams back as it came.
func TestTheGatewayHoldsTheCredential(t *testing.T) {
	var got http.Header
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotPath = r.Header.Clone(), r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: hello\n\n")
	}))
	defer up.Close()
	state := t.TempDir()
	m := newTestManager(t, state)
	os.WriteFile(filepath.Join(state, SealedTokenFile), []byte("sk-ant-oat-real\n"), 0o600)
	m.upstreamBase = func(string) string { return up.URL }
	s := &Session{cage: &Cage{Sealed: true}}
	mux := http.NewServeMux()
	mux.HandleFunc("/llm/{upstream}/{path...}", m.gateway(s))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/llm/anthropic/v1/messages?beta=true", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("X-Api-Key", GatewayKey)
	req.Header.Set("Anthropic-Beta", "prompt-caching")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "data: hello\n\n" || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("reply: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if got.Get("Authorization") != "Bearer sk-ant-oat-real" || got.Get("X-Api-Key") != "" {
		t.Fatalf("the upstream got auth %q and key %q", got.Get("Authorization"), got.Get("X-Api-Key"))
	}
	if b := got.Get("Anthropic-Beta"); !strings.Contains(b, "prompt-caching") || !strings.Contains(b, "oauth-2025-04-20") {
		t.Errorf("beta header %q", b)
	}
	if gotPath != "/v1/messages?beta=true" {
		t.Errorf("forwarded to %s", gotPath)
	}

	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/llm/anthropic/v1/organizations/api_keys", 403},
		{"POST", "/llm/anthropic/v1/files", 403},
		{"DELETE", "/llm/anthropic/v1/messages", 403},
		{"POST", "/llm/nowhere/v1/messages", 404},
		{"GET", "/llm/openrouter/models", 0},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if c.want != 0 && resp.StatusCode != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}

	// Not sealed, and no key on the machine: said, not sent bare.
	t.Setenv("ANTHROPIC_API_KEY", "")
	s.cage.Sealed = false
	req, _ = http.NewRequest("POST", srv.URL+"/llm/anthropic/v1/messages", strings.NewReader("{}"))
	resp, _ = http.DefaultClient.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || !strings.Contains(string(b), "ANTHROPIC_API_KEY") {
		t.Errorf("no key: %d %s", resp.StatusCode, b)
	}
}

// A sealed cage whose harness can be pointed at the gateway is given its
// address and no token.
func TestASealedCageHoldsNoToken(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Self = "/usr/local/bin/shrooms-agent"
	os.WriteFile(filepath.Join(state, SealedTokenFile), []byte("sk-ant-oat-real\n"), 0o600)
	name := "rev"
	s := &Session{dir: t.TempDir(), harness: Claude{}, cage: &Cage{Sealed: true, Container: "c"}}
	s.label.Store(&name)
	env, err := m.sealedEnv(s)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range env {
		if strings.Contains(v, "sk-ant-oat-real") {
			t.Fatalf("the token is in the cage, as %s", k)
		}
	}
	if env["ANTHROPIC_BASE_URL"] != GatewayBase("anthropic") || env["ANTHROPIC_API_KEY"] != GatewayKey {
		t.Fatalf("not pointed at the gateway: %v", env)
	}
	if !m.gatewayed(s) {
		t.Error("the forwarder would not be started")
	}
}
