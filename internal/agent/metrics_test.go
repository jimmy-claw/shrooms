package agent

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// /metrics gives a session's tokens, turns and provider from its usage, and
// leaves out a session a scraper's mesh does not see (ADR-049).
func TestTheAgentsMetrics(t *testing.T) {
	m, s := newPiSession(t)
	s.Send("hello", "")
	waitFor(t, s, 0, isResult)
	m.Meshes = func() []MeshInfo { return []MeshInfo{{"office", netip.MustParseAddr("fd00::1")}} }
	if _, err := m.CreateWith("home-only", t.TempDir(), "pi"); err != nil {
		t.Fatal(err)
	}
	ho, _ := m.Get("home-only")
	ho.SetMeshes([]string{"home"}, nil, "")
	ho.Send("hello", "")
	waitFor(t, ho, 0, isResult)

	get := func(remote string) string {
		h := &handler{log: slog.New(slog.DiscardHandler), m: m, who: func(a netip.Addr) string {
			if a == netip.MustParseAddr("fd00::7") {
				return "pi5.office"
			}
			return ""
		}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/metrics", nil)
		req.RemoteAddr = remote
		h.metrics(rec, req)
		return rec.Body.String()
	}
	local := get("127.0.0.1:1234")
	for _, want := range []string{
		`shrooms_agent_tokens_total{machine="`, `session="pi-proj",harness="pi",model="local/qwen3.5:0.8b",provider="local",kind="output"}`,
		`shrooms_agent_turns_total{`, `shrooms_agent_cost_usd_total{`, `shrooms_agent_sessions{machine=`, `state="idle"} 2`,
		`session="home-only"`,
	} {
		if !strings.Contains(local, want) {
			t.Errorf("no %s in:\n%s", want, local)
		}
	}
	office := get("[fd00::7]:1234")
	if strings.Contains(office, `session="home-only"`) || !strings.Contains(office, `session="pi-proj"`) {
		t.Errorf("an office scraper sees:\n%s", office)
	}
	if providerOf("claude-opus-5-5", "claude") != "anthropic" || providerOf("venice/deepseek", "pi") != "venice" || providerOf("k11/Qwen", "pi") != "k11" {
		t.Error("providers")
	}
	_ = http.StatusOK
}
