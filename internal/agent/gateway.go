package agent

// The model gateway (ADR-059): a cage reaches its model through its own
// agent, which holds the credentials and adds them on the way out. The cage
// holds no key and no token; a forwarder in it (shrooms-agent llm-forward)
// turns the harness's http:// base URL into requests on the cage's socket,
// which this serves at /llm/<upstream>/<path>.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
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

// bearer is an upstream that takes its key as a bearer token, read from the
// agent's environment.
func bearer(base, env string) upstream {
	return upstream{base: base, auth: func(h http.Header) error {
		k := os.Getenv(env)
		if k == "" {
			return fmt.Errorf("this machine's agent has no %s to reach it with", env)
		}
		h.Set("Authorization", "Bearer "+k)
		return nil
	}}
}

// upstreams are the model APIs a session's cage may reach. A sealed cage's
// Anthropic is the machine's sealed token (ADR-045), sent as Claude Code
// sends it; any other's, the agent's API key.
func (m *Manager) upstreams(sealed bool) map[string]upstream {
	ups := map[string]upstream{
		"openrouter": bearer("https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"),
		"venice":     bearer("https://api.venice.ai/api/v1", "VENICE_API_KEY"),
		"openai":     bearer("https://api.openai.com/v1", "OPENAI_API_KEY"),
	}
	ups["anthropic"] = upstream{base: "https://api.anthropic.com", auth: func(h http.Header) error {
		if sealed {
			token, err := m.sealedToken()
			if err != nil {
				return err
			}
			h.Set("Authorization", "Bearer "+token)
			// What marks a request as made with a Claude subscription's token.
			beta := h.Get("Anthropic-Beta")
			if !strings.Contains(beta, "oauth-2025-04-20") {
				h.Set("Anthropic-Beta", strings.TrimPrefix(beta+",oauth-2025-04-20", ","))
			}
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
		ups := m.upstreams(sealed)
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
