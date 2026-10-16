package agent

// The agent's own Prometheus metrics, at /metrics on its addresses: what its
// models did, so that spikes show over time, by provider, machine and
// session, beside the daemons' network and load (docs/metrics.md). The
// totals are the usage the session logs already hold (usage.go), whose scan
// reads only what each log gained since the last one.

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// providerOf is who serves a model: the part before the slash as pi names
// models ("venice/…", "k11/…"), Anthropic for Claude Code's.
func providerOf(model, harness string) string {
	if p, _, ok := strings.Cut(model, "/"); ok && p != "" {
		return p
	}
	if strings.HasPrefix(model, "claude") || harness == "claude" {
		return "anthropic"
	}
	if model == "" {
		return "unknown"
	}
	return model
}

// metrics serves /metrics. A session limited to some meshes (ADR-049) is
// counted only for a scraper on one of them, as everything else about it.
func (h *handler) metrics(w http.ResponseWriter, r *http.Request) {
	machine := h.m.selfName()
	mesh, local := h.from(r)
	seen := func(session string) bool {
		s, ok := h.m.Get(session)
		if !ok {
			return local // a session since deleted: this machine's own business
		}
		return s.servesFrom(mesh, local)
	}

	type key struct{ session, harness, model string }
	type sum struct {
		turns                              int
		input, cacheRead, cacheWrite, outp uint64
		cost                               float64
		busyMs                             int64
	}
	sums := map[key]*sum{}
	for _, u := range h.m.Usage("") {
		if !seen(u.Session) {
			continue
		}
		k := key{u.Session, u.Harness, u.Model}
		s := sums[k]
		if s == nil {
			s = &sum{}
			sums[k] = s
		}
		s.turns += u.Turns
		s.input += u.Input
		s.cacheRead += u.CacheRead
		s.cacheWrite += u.CacheWrite
		s.outp += u.Output
		s.cost += u.CostUSD
		s.busyMs += u.BusyMs
	}
	keys := make([]key, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.session != b.session {
			return a.session < b.session
		}
		return a.model < b.model
	})

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	e := &promWriter{w: w}
	labels := func(k key, extra ...string) []string {
		return append([]string{"machine", machine, "session", k.session, "harness", k.harness, "model", k.model,
			"provider", providerOf(k.model, k.harness)}, extra...)
	}
	e.family("shrooms_agent_tokens_total", "counter", "Tokens a session's model took and wrote, by kind: input, cache_read, cache_write, output.")
	for _, k := range keys {
		s := sums[k]
		for _, kv := range []struct {
			kind string
			v    uint64
		}{{"input", s.input}, {"cache_read", s.cacheRead}, {"cache_write", s.cacheWrite}, {"output", s.outp}} {
			e.sample("shrooms_agent_tokens_total", float64(kv.v), labels(k, "kind", kv.kind)...)
		}
	}
	e.family("shrooms_agent_turns_total", "counter", "Turns a session's model took.")
	for _, k := range keys {
		e.sample("shrooms_agent_turns_total", float64(sums[k].turns), labels(k)...)
	}
	e.family("shrooms_agent_cost_usd_total", "counter", "What a session's turns cost, as its harness prices them (0 for a local model).")
	for _, k := range keys {
		e.sample("shrooms_agent_cost_usd_total", sums[k].cost, labels(k)...)
	}
	e.family("shrooms_agent_busy_seconds_total", "counter", "Time from asking to the answer, over a session's turns.")
	for _, k := range keys {
		e.sample("shrooms_agent_busy_seconds_total", float64(sums[k].busyMs)/1000, labels(k)...)
	}

	// Now: each session's state, and how fast its model last wrote.
	states := map[State]int{}
	e.family("shrooms_agent_tokens_per_second", "gauge", "How fast a session's model wrote in its last turn (rate.go).")
	for _, in := range h.m.List() {
		if !seen(in.Name) {
			continue
		}
		states[in.State]++
		if in.TokensPerSecond > 0 {
			e.sample("shrooms_agent_tokens_per_second", in.TokensPerSecond, "machine", machine, "session", in.Name,
				"harness", in.Harness, "model", in.Model, "provider", providerOf(in.Model, in.Harness))
		}
	}
	e.family("shrooms_agent_sessions", "gauge", "Sessions on this machine, by state.")
	for _, st := range []State{Idle, Working, Waiting} {
		e.sample("shrooms_agent_sessions", float64(states[st]), "machine", machine, "state", string(st))
	}
}

// promWriter writes Prometheus' text format, as the daemon does: by hand,
// for a few dozen lines.
type promWriter struct{ w io.Writer }

func (e *promWriter) family(name, typ, help string) {
	fmt.Fprintf(e.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (e *promWriter) sample(name string, v float64, labels ...string) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%s=\"%s\"", labels[i], strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(strings.ToValidUTF8(labels[i+1], "?")))
		}
		b.WriteByte('}')
	}
	fmt.Fprintf(e.w, "%s %s\n", b.String(), strconv.FormatFloat(v, 'f', -1, 64))
}
