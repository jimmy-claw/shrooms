# 069. The task list has a window, and its parameters are checked

**Status:** proposed 2026-10-22. Proposed by pi5/jimmy, whose hub does the
polling; the measurement and the required shape are `laptop/shrooms`'.

## Context

The board's bridge polls every session of every peer every 30 seconds, with
`GET /v1/tasks?session=NAME`. That endpoint returned **every task with its
request and its result text**, and nothing could ask for less.

Measured on 21-22/10 from the pi5 hub, one peer, one cycle:

| session | bytes |
|---|---|
| laptop/reviewer | 383,644 |
| laptop/shrooms | 154,067 |
| laptop/basecamp-voice | 31,939 |
| laptop/SPEL | 20,126 |
| **total** | **589,776 B per cycle ≈ 1.15 MB/min ≈ 67 MB/h** |

The same request, from the same machine, inside one hour, took **0.8 s, 1.7 s,
2.5 s and 9.1 s** — the peer's load decides, not the payload size. The hub's
abort was 6 s, so a loaded peer was reported as an outage: *"laptop answer 200
but failed 15 of the last ~20 cycles (10 min)"*, while the agent answered
`/v1/sessions` in 0.56 s throughout. The abort is now 20 s, which turns an
hour-long outage shape into isolated moments — a bandage, not a cure.

Worse, and the reason this is an ADR rather than a patch: **`?since=` was
silently ignored.** `since=2026-10-21` and `since=2026-10-21T18:00:00` both
returned the identical 383,644 B. Unknown query parameters have always been
ignored here, which is a reasonable default and exactly how a cost like this
stays unseen: the caller believes it asked for a window and is handed the
world.

## Decision

`GET /v1/tasks` takes three parameters. **Defaults are unchanged** — no
parameter means what it always meant, so the apps and every existing client
keep their behaviour.

- **`history=0`** — drop `history` and `artifacts` (the conversation). Keep
  `id`, `contextId`, `status` (state, timestamp, message) and `metadata`, which
  is what a poller needs to know a task exists, what it is and where it stands.
- **`since=<RFC3339>`** — only tasks whose `status.timestamp` is **strictly**
  after it. A value that cannot be parsed is **400**, never a full list.
- **`limit=N`** — the newest N. Applied **after** visibility, so a task the
  caller may not see never consumes the allowance; otherwise a small limit
  could return nothing while visible tasks wait behind hidden ones.

Ordering is newest first whenever `limit` or `since` is in play, so that `limit`
means "the newest N" rather than "whichever N the store happened to hold".

## What it is worth, measured on the real reviewer session

| | |
|---|---|
| full list | 296,217 B (28 tasks) |
| `history=0` | **132,463 B — 55.3% less** |
| of which `history` | ~69,475 B |
| of which `artifacts` | ~96,943 B |
| `since=2026-10-22T04:00Z` after a quiet hour | **0 of 28 tasks** |

So a poll that asks for the window gets nothing at all when nothing happened,
and roughly half the bytes when something did.

## The run, on the built binary

Not only unit tests: the new agent was built (`go build ./cmd/shrooms-agent`) and
served on a unix socket over a copy of pi5's own store (27 tasks), so these are
real HTTP answers from the real handler:

| request | bytes | time |
|---|---|---|
| `?session=jimmy` (today's behaviour) | 124,250 | 2.55 ms |
| `&history=0` | 55,863 (**−55.0%**) | 0.99 ms |
| `&history=0&limit=5` | 8,589 | 0.69 ms |
| `&history=0&since=2026-10-22T00:00:00Z` | **13** (`{"tasks":[]}`) | 0.46 ms |

Malformed windows against the same binary: `since=yesterday` and `since=2026-10-21`
→ 400 *"since must be RFC3339, e.g. …"*; `limit=two` and `limit=-1` → 400
*"limit must be a whole number, 0 or more"*; a valid boundary → 200.

The laptop's own reviewer session could not be run end-to-end without deploying
the binary there, which is not mine to do; instead its real payload was measured
directly (296,217 B → 132,463 B, table above) and the endpoint was exercised on
pi5's store. The peer-side numbers are for whoever deploys it.

## Consequences

- **A `since` list cannot show deletions.** A task that was pruned or removed
  is absent, and absence is indistinguishable from "not updated". A poller
  must therefore still take a **full `history=0` list every few minutes** to
  drop tasks that are gone. The hub does this; it is a rule of the protocol,
  not a habit of one client.
- `history=0` is still large because `metadata.shrooms/last_worker_line`
  carries up to 600 characters per task. Trimming that is a separate decision.
- The 400s are the part most likely to break a caller, and deliberately so:
  a caller that passes a malformed window is told, instead of quietly paying
  full price.

## Evidence that the rules hold

`internal/agent/taskslist_test.go`: defaults unchanged (history and artifacts
still travel), `history=0` keeps identity/status/metadata and the status
message, `since` is strictly after and excludes the boundary, unparseable
`since` and `limit` are 400 with a reason, `limit` is newest-first, and
visibility is applied before the limit.

Each rule was removed on purpose and the suite caught all five: ignoring
`history=0`, making `since` non-strict, silently accepting a bad `since`,
counting the limit before visibility, and ordering oldest-first.

## What would change our mind

If a real client needs the conversation on every poll, or if a `since` poll
turns out to miss changes that matter (for instance a task whose state changes
without its timestamp moving), the window is in the wrong place and this
should be revisited. The first thing to check would be whether every state
change writes `Updated` — the whole scheme rests on that.
