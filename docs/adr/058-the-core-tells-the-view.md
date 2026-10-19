# 058. The core tells the Agents view what changed

**Status:** accepted 2026-10-15, built the same day: shrooms_core 0.26.0,
Shrooms Agents 0.52.0.

## Context

The Agents view in Basecamp learned everything by polling its core module:
the open session's events every 300 ms, the jobs and the queued messages
with them. Each poll is a synchronous call on Basecamp's GUI thread, and
Basecamp logs four lines for each. On the Duet tablet (MT8183) an open
session held Basecamp's main thread at 72-85% of a core (duet/duet).

Three causes were found, in this order:

- **An infinite animation.** The working dot's `SequentialAnimation` kept
  the whole window repainting at vsync, about 53 frames a second. It was
  most of the cost: removing it took the main thread from 72% to 25%.
- **A stream rebuilt the conversation.** Every tick that brought a few
  words of a reply rebuilt the conversation from all its events.
- **Polling.** About 8 core calls a second with a session open.

Other Logos apps (KYM, Perun, Scala) have their core emit events and also
poll slowly, because events are not delivered by every host.

## Decision

**The core emits `agentChanged`** (a `logos_events:` declaration) when the
followed session's events arrive, when an upload or a voice note is done,
and when the outbox changes. Whole events are told at once; pieces of a
streaming reply at most every 150 ms. `agentWatch` answers `"events": true`
so the view knows at once.

**The view reads on the event and polls only as a safety net**: every 3 s
with a core that sends events, and as before with one that does not
(300 ms while something moves, 1.5 s otherwise).

**Pieces of a reply grow the streaming row only**; the conversation is
rebuilt when a whole event arrives.

**No infinite animations in the views.** A working session's dot is lit,
and does not blink. The Android app follows: the spores behind the agents
and the mesh are gone, and what still moves there is stepped a few times
a second rather than animated every frame. `basecamp-check` fails on
`Animation.Infinite`.

**Not gated on visibility.** Basecamp 0.3.1 hosts a view in a window that
reports itself hidden while it is on screen, and 0.51.1, which polled only
when visible, never listed an agent.

## Consequences

- With a session open: about 0.8 core calls a second, working or idle,
  against about 8 (measured in Basecamp 0.3.1 under Xvfb). The Android
  agents screen with nothing to show drew 12 frames in 10 s, against 454.
- The events are emitted from the core's follower thread. They arrive in
  Basecamp 0.3.1. A host that drops them still works, through the 3 s poll.
- A view on core 0.25 works as before, without events.
- Duet's re-measurement on the tablet is the check of record.
