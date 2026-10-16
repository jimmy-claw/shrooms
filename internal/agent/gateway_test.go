package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	genv, files, ok := m.gatewayFor(s)
	if !ok || len(files) != 0 || genv["ANTHROPIC_BASE_URL"] == "" {
		t.Fatalf("a sealed Claude cage is not pointed at the gateway by its environment: %v %v %v", genv, files, ok)
	}
	s.cageGateway = true // as prepareCage sets it
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
}

// A caged pi session made with the gateway gets a models.json pointing the
// cloud providers at it, keeps the owner's own providers, and no model keys;
// a caged Claude session keeps its own login and is not gatewayed.
func TestACagedPiReachesItsModelsThroughTheGateway(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(`{"providers":{"k11":{"baseUrl":"http://ai.k11.home.mesh/v1","api":"openai-completions","apiKey":"lemonade","models":[{"id":"q"}]},"openrouter":{"headers":{"X-Title":"mine"}}}}`), 0o600)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-real")
	t.Setenv("VENICE_INFERENCE_KEY", "vn-real-inference")
	t.Setenv("GH_TOKEN", "gh-real")
	m := newTestManager(t, t.TempDir())
	m.Self = "/usr/local/bin/shrooms-agent"
	name := "pi-box"
	s := &Session{dir: t.TempDir(), harness: Pi{}, cage: &Cage{Container: "c", GitHub: true}}
	s.label.Store(&name)
	_, files, ok := m.gatewayFor(s)
	mj := files[filepath.Join(agentDir, "models.json")]
	if !ok || !strings.Contains(mj, GatewayBase("openrouter")) || !strings.Contains(mj, "ai.k11.home.mesh") || !strings.Contains(mj, `"X-Title"`) || strings.Contains(mj, "sk-or-real") {
		t.Fatalf("pi's models.json through the gateway:\n%s", mj)
	}
	s.cageGateway = true
	path, err := m.cageEnv(s)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "sk-or-real") || strings.Contains(string(b), "vn-real-inference") || strings.Contains(string(b), "gh-real") {
		t.Fatalf("the cage's environment:\n%s", b)
	}
	// A caged Claude session: through the gateway, with an empty credentials
	// file over the owner's login in the cage.
	c := &Session{dir: t.TempDir(), harness: Claude{}, cage: &Cage{Container: "d"}}
	c.label.Store(&name)
	cenv, cfiles, ok := m.gatewayFor(c)
	home, _ := os.UserHomeDir()
	if !ok || cenv["ANTHROPIC_BASE_URL"] != GatewayBase("anthropic") || cfiles[filepath.Join(home, ".claude", ".credentials.json")] != "{}\n" {
		t.Errorf("a caged Claude session is not gatewayed with its login hidden: %v %v %v", cenv, cfiles, ok)
	}
}

// The gateway uses the owner's Claude login for Claude Code, read fresh from
// the file each request, and says so when there is none.
func TestTheGatewayUsesTheOwnersLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := newTestManager(t, t.TempDir())
	if _, err := m.claudeLogin(); err == nil {
		t.Fatal("no login, and no error")
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	write := func(tok string) {
		os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"),
			[]byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"r","expiresAt":%d}}`, tok, time.Now().Add(time.Hour).UnixMilli())), 0o600)
	}
	write("sk-ant-oat-one")
	h := http.Header{}
	if err := m.upstreams(false, "claude")["anthropic"].auth(h); err != nil || h.Get("Authorization") != "Bearer sk-ant-oat-one" {
		t.Fatalf("%v %q", err, h.Get("Authorization"))
	}
	write("sk-ant-oat-two") // renewed by the owner's own Claude session
	h = http.Header{}
	m.upstreams(false, "claude")["anthropic"].auth(h)
	if h.Get("Authorization") != "Bearer sk-ant-oat-two" || !strings.Contains(h.Get("Anthropic-Beta"), "oauth-2025-04-20") {
		t.Fatalf("not read fresh: %q %q", h.Get("Authorization"), h.Get("Anthropic-Beta"))
	}
	// pi's anthropic provider uses the agent's API key, not the login.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api")
	h = http.Header{}
	m.upstreams(false, "pi")["anthropic"].auth(h)
	if h.Get("X-Api-Key") != "sk-ant-api" || h.Get("Authorization") != "" {
		t.Fatalf("pi: %v", h)
	}
}

// A caged pi session's cage is made with the gateway's models.json in place
// of the owner's read-only one (one mount there, not two), labelled, and the
// forwarder started before pi; a cage made before the gateway keeps its keys.
func TestACagedPiCageIsMadeWithTheGateway(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(`{"providers":{}}`), 0o600)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-real")
	podman, log := fakePodman(t, false)
	m := newTestManager(t, t.TempDir())
	m.Register(Pi{}, fakePiBin(t))
	m.Self = fakePiBin(t)
	m.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Nft: "nft"}
	if _, err := m.CreateCaged("pibox", t.TempDir(), "pi", &Cage{}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("pibox")
	if err := s.Send("hello", "phone"); err != nil {
		t.Fatal(err)
	}
	// Until pi is run in the cage (whether the fake pi answers there is not
	// what this is about).
	var cs []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cs = calls(t, log); indexOf(cs, `^exec -i `) >= 0 {
			break
		}
	}
	create := indexOf(cs, `^create `)
	at := filepath.Join(agentDir, "models.json")
	if create < 0 || strings.Count(cs[create], ":"+at) != 1 || !strings.Contains(cs[create], ".gw/") || !strings.Contains(cs[create], "--label "+gatewayLabel+"=1") {
		t.Fatalf("not made with the gateway's models.json alone at %s, labelled: %s", at, cs[create])
	}
	fw := indexOf(cs, `^exec -d \S+ \S+ llm-forward --listen `+GatewayListen)
	run := indexOf(cs, `^exec -i `)
	if fw < 0 || run < 0 || fw > run {
		t.Fatalf("the forwarder is not started before pi: %q", cs)
	}
	env, _ := os.ReadFile(filepath.Join(m.dir, "cages", s.cage.Container+".env"))
	if strings.Contains(string(env), "sk-or-real") {
		t.Fatalf("the key is in the cage:\n%s", env)
	}
}

// A provider's key kept in pi's models.json (as on proteus) is the gateway's
// to use when the agent's environment has none: a key, or a variable's name.
func TestTheGatewayFindsAKeyInPisModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("VENICE_API_KEY", "")
	t.Setenv("MY_OR_KEY", "sk-or-from-var")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("MY_OAI", "sk-oai-from-dollar")
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"venice":{"apiKey":"vn-literal"},"openrouter":{"apiKey":"MY_OR_KEY"},"openai":{"apiKey":"$MY_OAI"}}}`), 0o600)
	m := newTestManager(t, t.TempDir())
	for name, want := range map[string]string{"venice": "Bearer vn-literal", "openrouter": "Bearer sk-or-from-var", "openai": "Bearer sk-oai-from-dollar"} {
		h := http.Header{}
		if err := m.upstreams(false, "pi")[name].auth(h); err != nil || h.Get("Authorization") != want {
			t.Errorf("%s: %v %q", name, err, h.Get("Authorization"))
		}
	}
	t.Setenv("MY_OAI", "")
	if err := m.upstreams(false, "pi")["openai"].auth(http.Header{}); err == nil {
		t.Error("a variable nobody set was sent as the key")
	}
}
