package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// Search covers the whole conversation: what was said in a terminal before
// the agent had it (no event to jump to), and the agent's own turns (with the
// event to jump to), newest first, ignoring case and Czech diacritics.
func TestSearchFindsTheWholeConversation(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", base)
	writeTranscript(t, base, "conv-s", t.TempDir(), "Ahoj, tady Vašek: the relay is down", "Looking at the RELAY now", time.Now().Add(-time.Hour))

	m := newTestManager(t, t.TempDir())
	if _, err := m.Adopt("taken", "", "conv-s"); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("taken")
	s.Send("restart the relay please", "nothing")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
	// Claude Code writes the agent's turns into the same transcript; they
	// must be found once, from the events.
	path, _ := transcriptPath("conv-s")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fmt.Fprintf(f, `{"type":"user","timestamp":%q,"message":{"role":"user","content":"restart the relay please"}}`+"\n", now)
	fmt.Fprintf(f, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"text","text":"echo: restart the relay please"}]}}`+"\n", now)
	f.Close()

	srv := localServer(Handler(slog.New(slog.DiscardHandler), m, nil))
	t.Cleanup(srv.Close)
	search := func(q string) []Found {
		t.Helper()
		resp, err := http.Get(srv.URL + "/v1/sessions/taken/search?q=" + url.QueryEscape(q))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct{ Found []Found }
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
			t.Fatalf("search %q: %s", q, resp.Status)
		}
		return body.Found
	}

	got := search("relay")
	var roles []string
	for _, f := range got {
		roles = append(roles, f.Role)
	}
	// The echo, what was sent, then the two terminal turns.
	if len(got) != 4 {
		t.Fatalf("relay: %d found, %+v", len(got), got)
	}
	if got[0].Role != "assistant" || got[0].Seq == 0 || got[0].Snippet != "echo: restart the relay please" || got[0].Text != "" {
		t.Errorf("newest first, with its event: %+v", got[0])
	}
	if got[1].Role != "user" || got[1].Seq == 0 || got[1].Seq >= got[0].Seq {
		t.Errorf("the turn sent from the phone: %+v", got[1])
	}
	if got[2].Seq != 0 || got[3].Seq != 0 || got[3].Text != "Ahoj, tady Vašek: the relay is down" {
		t.Errorf("the terminal's turns have no event and come with their text: %+v %+v", got[2], got[3])
	}

	if f := search("VASEK"); len(f) != 1 || f[0].Role != "user" {
		t.Errorf("case and diacritics: %+v", f)
	}
	if f := search("not said by anyone"); len(f) != 0 {
		t.Errorf("found what was never said: %+v", f)
	}
	if f := search("  "); len(f) != 0 {
		t.Errorf("an empty search found %d", len(f))
	}
}

func TestASnippetIsCutAroundTheMatch(t *testing.T) {
	long := ""
	for i := 0; i < 50; i++ {
		long += "padding words here "
	}
	f, ok := match(Said{Role: "assistant", Text: long + "the Žluťoučký kůň" + long}, []rune(fold("zlutoucky")))
	if !ok {
		t.Fatal("not found")
	}
	if len([]rune(f.Snippet)) > 2*snippetContext+len("zlutoucky")+2 || f.Snippet[:3] != "…" ||
		!containsStr(f.Snippet, "Žluťoučký") {
		t.Errorf("snippet %q", f.Snippet)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && indexRunes([]rune(s), []rune(sub)) >= 0
}
