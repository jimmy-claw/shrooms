# 064. An agent's schedule is its own Scala calendar, co-owned with the owner

**Status:** proposed 2026-10-19, for review by Scala's author
(jimmy-crib/vpavlin). Nothing built. Decides what `docs/agents-calendar.md`
(2026-10-05) left open.

## Context

The owner wants agents to do things on a schedule. Examples:
- SPEL: every other morning, check its PRs and issues.
- Jimmy: every morning and evening, the car and phone scans.
- laptop/shrooms: review issues and PRs at night, and give a "what needs a
  decision" digest at 9:00.

The lesson from running Jimmy under OpenClaw was that its schedule lived
where nobody could see it. Václav had to ask for it, and the answer did not
match what ran. A schedule has to be something he can see and change at any
time.

He also does not want to be its only writer. A routine is usually agreed in
conversation ("yes, do that every morning"). Having to open a calendar and
type it in afterwards is the friction that makes schedules live in an agent's
head.

Heartbeats were switched off once already, for burning tokens on nothing
(ADR-042). A schedule that wakes a model to find nothing changed repeats that.

Scala (its author's review, 2026-10-19, scala main 7fcf52f) gives us:
- roles per calendar: owner, editors, viewers;
- closed calendars, where only the owner and editors write events;
- custom fields defined by a schema, shown and edited by its desktop and
  phone views;
- `ext` items, which any member may post on an event.

## Decision

**Each agent session that has a schedule has its own Scala calendar. The owner
and that session are its two editors, and nobody else writes to it.**

- **One calendar per session.** It is co-owned:
  - Václav is its owner.
  - The session is an editor: it adds, changes and removes its own routines
    once they are agreed with Václav, without him typing them.
  - No other session, and no other machine, can write to it.
- **Václav sees all of them together.** Scala shows several calendars at once,
  coloured per calendar, on the desktop and the phone.
- **The Shrooms Agents views show the same, per session:** the schedule tab,
  and each run's result.
- **A session writes through its agent, never directly.**
  - The session gets an MCP tool, `schedule`, to list, add, change and remove
    events in its own calendar.
  - The agent holds the session's Scala identity and signs on its behalf, so
    the session can only ever name its own calendar.
  - A caged session never holds the identity's key. The tool goes through the
    agent, as the model gateway does (ADR-059).
- **An event is a routine, in its custom fields:**
  - `prompt`: what the session is asked.
  - `checks`: commands, one per line. They run first, without the model, in
    the session's folder or cage. The model is woken only if their output
    differs from the last run's.
  - `maxCost` and `timeoutMin`.
  - `enabled`.
  - Recurrence is Scala's own rule.
- **Each run is written back on the event, as an `ext` item:** when it ran,
  whether its checks saw a change, a one-line result and its cost. The full
  reply goes to the session's conversation, and so to the phone, as any turn
  does.
- **Václav is told when a session changes its own schedule.** Adding,
  changing or removing a routine is a note in the session's conversation and a
  notification. Visible at the time, not only when he looks.
- **The runner is the session's own agent, and it refuses anything else.** It
  runs only:
  - events from the folded state, never the raw log;
  - from a closed calendar;
  - whose owner is Václav's address, pinned in the agent's config;
  - whose editors are exactly Václav and that session;
  - for that session, and nothing taken from an `ext` item.
- **Limits:**
  - A per-session daily cap on runs and on cost. Past it, the agent refuses
    and says so in the session.
  - Quiet hours.
  - A session's responsibilities (ADR-063) still bound what a routine may do.

## Consequences

- **Scala work** (sizes from its author):
  - `dueOccurrences(calId, from, to)` exposed from the core: small. The agent
    reuses Scala's own recurrence expansion instead of a second copy.
  - A time zone for that expansion, so 08:00 stays 08:00 across daylight
    saving: small.
  - **Open:** one Scala identity per session, held by one machine's agent.
    Today a core instance has one identity. Several identities in one
    instance, or one instance per scheduled session, is for Scala's author to
    say.
  - Later: "weekdays only" (BYDAY) and skipping one occurrence. Both medium.
    Until then, the `enabled` field, or two events.
- **Shrooms work:**
  - the runner in each agent;
  - the `schedule` tool;
  - the notification on a change;
  - the schedule tab with run history in both apps;
  - creating a session's calendar and its viewer and editor setup when a
    schedule is first wanted.
- **A session can now schedule itself.** A prompt-injected session could add
  a routine. That is visible at once (the notification) and in Scala. It runs
  in the session's cage, within its caps and responsibilities, and Václav can
  delete it like any event. This is the price of not making him type every
  routine, and it is paid in the open.
- **Holding a calendar's key means reading it.** A machine whose key leaks
  exposes its sessions' prompts and check commands. Scala has no key rotation
  yet: rotating means a new calendar.
- **Agent proposals are no longer needed** as a separate mechanism: a session
  asks in conversation and then adds the routine itself. `ext` stays for run
  results.
