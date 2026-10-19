package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// What a session is for (ADR-063): set through its settings, kept when the
// agent starts again, on its card as A2A skills and the responsibilities
// extension, and out of a cage's reach.
func TestASessionsSkillsAndResponsibilitiesAreOnItsCard(t *testing.T) {
	r := newA2A(t)
	post := func(body string) int {
		resp, err := http.Post(r.local.URL+"/v1/sessions/proj/settings", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	card := func() map[string]any {
		resp, err := http.Get(r.remote.URL + "/a2a/proj/.well-known/agent-card.json")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var c map[string]any
		json.NewDecoder(resp.Body).Decode(&c)
		return c
	}
	// None set: the session itself, as before.
	if sk := card()["skills"].([]any); len(sk) != 1 || sk[0].(map[string]any)["name"] != "proj" {
		t.Fatalf("placeholder skill: %v", sk)
	}

	if code := post(`{"skills":[{"name":"Basecamp apps","description":"QML views and core modules","tags":["Basecamp","qml"]},` +
		`{"name":"basecamp apps"},{"name":"Android"}],"responsibilities":["publishes to the LAN repos"," "]}`); code != http.StatusOK {
		t.Fatalf("set: %d", code)
	}
	c := card()
	sk := c["skills"].([]any)
	first := sk[0].(map[string]any)
	if len(sk) != 2 || first["name"] != "Basecamp apps" || first["description"] != "QML views and core modules" ||
		!strings.Contains(strings.Join(toStrings(first["tags"]), ","), "basecamp,qml") {
		t.Fatalf("skills on the card (the duplicate dropped, tags lower-cased): %v", sk)
	}
	var duties []string
	for _, e := range c["capabilities"].(map[string]any)["extensions"].([]any) {
		e := e.(map[string]any)
		if e["uri"] == responsibilitiesExtension {
			duties = toStrings(e["params"].(map[string]any)["responsibilities"])
		}
	}
	if len(duties) != 1 || duties[0] != "publishes to the LAN repos" {
		t.Fatalf("responsibilities on the card: %v", duties)
	}

	// Kept, and listed.
	m2 := newTestManager(t, r.m.dir)
	if s2, _ := m2.Get("proj"); s2 == nil || len(s2.Info().Skills) != 2 || len(s2.Info().Responsibilities) != 1 {
		t.Fatal("what it is for is not kept when the agent starts again")
	}

	// Refused: a nameless skill, a multi-line one, too many.
	for _, bad := range []string{`{"skills":[{"name":""}]}`, `{"skills":[{"name":"a\nb"}]}`,
		`{"responsibilities":["` + strings.Repeat("x", maxDutyText+1) + `"]}`} {
		if code := post(bad); code != http.StatusBadRequest {
			t.Errorf("%s taken: %d", bad, code)
		}
	}
	// Only one part changed, the other kept; then cleared.
	post(`{"responsibilities":[]}`)
	if in := r.m.sessions["proj"].Info(); len(in.Skills) != 2 || len(in.Responsibilities) != 0 {
		t.Fatalf("one part changed: %+v %+v", in.Skills, in.Responsibilities)
	}

	// A cage cannot set its own.
	if proxyAllowed(http.MethodPost, "/v1/sessions/proj/settings", "") {
		t.Error("a cage reaches its session's settings")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
