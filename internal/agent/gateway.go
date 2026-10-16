package agent

// The model gateway (ADR-059): a cage reaches its model through its own
// agent, which holds the credentials and adds them on the way out. The cage
// holds no key and no token; a forwarder in it (shrooms-agent llm-forward)
// turns the harness's http:// base URL into requests on the cage's socket,
// which this serves at /llm/<upstream>/<path>.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GatewayListen is where the forwarder in a cage listens: on the cage's own
// loopback, which is all of the machine a sealed cage can reach anyway.
const GatewayListen = "127.0.0.1:7390"

// GatewayBase is the base URL a harness in a cage is given for an upstream.
func GatewayBase(upstream string) string { return "http://" + GatewayListen + "/" + upstream }

// GatewayKey is what a harness is given where it wants a key: it opens
// nothing, and says where the real one is.
const GatewayKey = "shrooms-gateway-holds-the-key"

// Gatewayed is a harness that can reach its model through the gateway. Told
// whether its cage is sealed, it says how its process is pointed there: the
// environment to set and, for a harness that reads a file instead, files to
// lay over the ones in the cage (path in the cage → content). Adding a
// harness to the gateway is implementing this; the cage code does not change.
type Gatewayed interface {
	GatewayEnv(sealed bool) map[string]string
	GatewayFiles(sealed bool) map[string]string
}

// upstream is one model API the gateway forwards to.
type upstream struct {
	base string
	// auth puts the real credential on a request; an error if the agent has
	// none for this cage.
	auth func(h http.Header) error
}

// gatewayPaths are the only endpoints forwarded: asking a model, counting
// its tokens, listing models. Nothing else of an upstream's API (no keys, no
// billing, no files) is reachable through it.
var gatewayPaths = []string{"messages", "messages/count_tokens", "chat/completions", "completions", "responses", "models"}

func gatewayPathAllowed(method, p string) bool {
	p = strings.Trim(path.Clean("/"+p), "/")
	for _, a := range gatewayPaths {
		if p == a || strings.HasSuffix(p, "/"+a) {
			return method == http.MethodPost || (a == "models" && method == http.MethodGet)
		}
	}
	return false
}

// bearer is an upstream that takes its key as a bearer token: from the
// agent's environment, or where the owner keeps it for pi (piProviderKey).
func bearer(base, env, provider string) upstream {
	return upstream{base: base, auth: func(h http.Header) error {
		k := os.Getenv(env)
		if k == "" {
			k = piProviderKey(provider)
		}
		if k == "" {
			return fmt.Errorf("this machine's agent has no %s, and pi's models.json no key for %s, to reach it with", env, provider)
		}
		h.Set("Authorization", "Bearer "+k)
		return nil
	}}
}

// OpenCodePublicToken is what opencode sends when it has no key of its own —
// measured against 1.19.2, which sends exactly this and is answered. Its free
// models need no credential.
const OpenCodePublicToken = "public"

// openCodeKey is the owner's own OpenCode key, from the environment or where pi
// keeps it, or "" — the free models need none.
func openCodeKey() string {
	if k := os.Getenv("OPENCODE_API_KEY"); k != "" {
		return k
	}
	return piProviderKey("opencode")
}

// piProviderKey is a provider's key as the owner keeps it in pi's
// models.json (providers.<name>.apiKey): a key, or the name of a variable
// that holds one, as pi reads it. On proteus the Venice key lives there and
// nowhere else, and a cage made with the gateway no longer sees the file.
func piProviderKey(provider string) string {
	b, err := os.ReadFile(filepath.Join(piAgentDir(), "models.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Providers map[string]struct {
			APIKey string `json:"apiKey"`
		} `json:"providers"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return ""
	}
	k := strings.TrimSpace(cfg.Providers[provider].APIKey)
	if k == "" || k == GatewayKey {
		return ""
	}
	// A variable, as pi reads it: NAME, $NAME or ${NAME} (proteus has
	// "$VENICE_INFERENCE_KEY", set from ~/.pi/agent/.env).
	name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(k, "$"), "{"), "}")
	if v := os.Getenv(name); v != "" {
		return v
	}
	if strings.HasPrefix(k, "$") {
		return "" // a variable this agent does not have: not a key to send
	}
	return k
}

// upstreams are the model APIs a session's cage may reach. Anthropic, for
// Claude Code: a sealed cage's is the machine's sealed token where there is
// one (ADR-045), and otherwise the owner's own Claude login, read fresh from
// ~/.claude on each request; neither is ever in the cage. For another
// harness (pi's anthropic provider), the agent's API key.
func (m *Manager) upstreams(sealed bool, harness string) map[string]upstream {
	ups := map[string]upstream{
		"openrouter": bearer("https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", "openrouter"),
		"venice":     bearer("https://api.venice.ai/api/v1", "VENICE_API_KEY", "venice"),
		"openai":     bearer("https://api.openai.com/v1", "OPENAI_API_KEY", "openai"),
		// OpenCode's free models go through its own inference endpoint, whose
		// key is OPENCODE_API_KEY (measured from its model catalog: the
		// provider "opencode" is https://opencode.ai/inference/openai/v1).
		// Without /v1 here: the client appends it, and a cage's request
		// arrives as /llm/opencode/v1/chat/completions.
		//
		// NOT bearer(): its free models need no credential at all. Measured
		// with no key set, opencode sends the literal "Bearer public" — which
		// is how atlas runs them with no key and no auth.json. A bearer()
		// here fails when the machine has no OPENCODE_API_KEY, so every caged
		// or sealed free-model turn would answer 502 — the one case this
		// upstream exists for. The owner's own key is used when there is one
		// (paid models); otherwise the public token passes through.
		"opencode": upstream{base: "https://opencode.ai/inference/openai", auth: func(h http.Header) error {
			if k := openCodeKey(); k != "" {
				h.Set("Authorization", "Bearer "+k)
				return nil
			}
			if h.Get("Authorization") == "" {
				h.Set("Authorization", "Bearer "+OpenCodePublicToken)
			}
			return nil
		}},
	}
	ups["anthropic"] = upstream{base: "https://api.anthropic.com", auth: func(h http.Header) error {
		bearer := func(token string) {
			h.Set("Authorization", "Bearer "+token)
			// What marks a request as made with a Claude subscription's token.
			beta := h.Get("Anthropic-Beta")
			if !strings.Contains(beta, "oauth-2025-04-20") {
				h.Set("Anthropic-Beta", strings.TrimPrefix(beta+",oauth-2025-04-20", ","))
			}
		}
		if sealed {
			if token, err := m.sealedToken(); err == nil {
				bearer(token)
				return nil
			}
		}
		if harness == "claude" {
			token, err := m.claudeLogin()
			if err != nil {
				return err
			}
			bearer(token)
			return nil
		}
		k := os.Getenv("ANTHROPIC_API_KEY")
		if k == "" {
			return errors.New("this machine's agent has no ANTHROPIC_API_KEY to reach it with")
		}
		h.Set("X-Api-Key", k)
		return nil
	}}
	if m.upstreamBase != nil {
		for name, u := range ups {
			if b := m.upstreamBase(name); b != "" {
				u.base = b
				ups[name] = u
			}
		}
	}
	return ups
}

// dropOnTheWay are the request headers not passed on: the caller's own
// credentials (a dummy, or something it should not have), and what belongs
// to one connection.
var dropOnTheWay = map[string]bool{
	"Authorization": true, "X-Api-Key": true, "Cookie": true, "Proxy-Authorization": true,
	"Connection": true, "Keep-Alive": true, "Te": true, "Trailer": true, "Transfer-Encoding": true,
	"Upgrade": true, "Host": true, "Content-Length": true, "Accept-Encoding": true,
}

// gateway serves /llm/{upstream}/{path...} on a session's cage socket.
func (m *Manager) gateway(s *Session) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		sealed := s.cage != nil && s.cage.Sealed
		s.mu.Unlock()
		harness := ""
		if s.harness != nil {
			harness = s.harness.Name()
		}
		ups := m.upstreams(sealed, harness)
		name, rest := r.PathValue("upstream"), r.PathValue("path")
		u, ok := ups[name]
		if !ok {
			var names []string
			for n := range ups {
				names = append(names, n)
			}
			sort.Strings(names)
			fail(w, http.StatusNotFound, fmt.Errorf("no model upstream %q here; there are %s", name, strings.Join(names, ", ")))
			return
		}
		if !gatewayPathAllowed(r.Method, rest) {
			fail(w, http.StatusForbidden, fmt.Errorf("the gateway forwards only a model's endpoints, not %s /%s", r.Method, rest))
			return
		}
		target := strings.TrimRight(u.base, "/") + "/" + strings.TrimLeft(rest, "/")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		out, err := http.NewRequestWithContext(r.Context(), r.Method, target, io.LimitReader(r.Body, 64<<20))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		for k, vs := range r.Header {
			if dropOnTheWay[http.CanonicalHeaderKey(k)] {
				continue
			}
			for _, v := range vs {
				out.Header.Add(k, v)
			}
		}
		if r.ContentLength > 0 {
			out.ContentLength = r.ContentLength
		}
		if err := u.auth(out.Header); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		resp, err := m.gatewayClient().Do(out)
		if err != nil {
			fail(w, http.StatusBadGateway, fmt.Errorf("%s: %w", name, err))
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			if k == "Content-Length" || k == "Transfer-Encoding" || k == "Connection" {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		// Streamed: a model's reply comes as server-sent events, and the
		// harness shows it as it comes.
		fl, _ := w.(http.Flusher)
		buf := make([]byte, 32<<10)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}
}

func (m *Manager) gatewayClient() *http.Client {
	if m.gatewayHTTP != nil {
		return m.gatewayHTTP
	}
	return http.DefaultClient
}

// claudeLogin is the owner's Claude login's access token, as Claude Code
// keeps it (~/.claude/.credentials.json), read fresh each time. The gateway
// never renews it: renewing replaces the refresh token, and racing the
// owner's own Claude sessions at it could log them out. When it is about to
// run out, Claude Code is asked to renew it (claude auth status), with its
// own locking, and the file is read again.
func (m *Manager) claudeLogin() (string, error) {
	read := func() (string, time.Time, error) {
		home, _ := os.UserHomeDir()
		b, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
		if err != nil {
			return "", time.Time{}, errors.New("this machine has no Claude login for the gateway to use (claude /login)")
		}
		var c struct {
			OAuth struct {
				AccessToken string `json:"accessToken"`
				ExpiresAt   int64  `json:"expiresAt"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(b, &c) != nil || c.OAuth.AccessToken == "" {
			return "", time.Time{}, errors.New("this machine's Claude login has no token the gateway can use")
		}
		return c.OAuth.AccessToken, time.UnixMilli(c.OAuth.ExpiresAt), nil
	}
	token, exp, err := read()
	if err != nil || time.Until(exp) > 2*time.Minute {
		return token, err
	}
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	if token, exp, err = read(); err != nil || time.Until(exp) > 2*time.Minute {
		return token, err
	}
	if bin := m.binOf("claude"); bin != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		exec.CommandContext(ctx, bin, "auth", "status").Run()
		cancel()
	}
	if token, exp, err = read(); err == nil && time.Until(exp) <= 0 {
		return "", errors.New("this machine's Claude login has run out and was not renewed: run claude on it once, or claude /login")
	}
	return token, err
}
