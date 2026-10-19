# 063. What each agent is for: skills and responsibilities, set by the owner

**Status:** accepted 2026-10-19. Step 1 (skills and responsibilities on the
cards) built the same day; step 2 (replies, acks, evidence) not yet.

## Context

The sessions on the mesh have settled into roles: a publisher that ships to
the LAN repos, a reviewer, SPEL for SPEL and LEZ, jimmy-crib/vpavlin for
Basecamp and Android apps, laptop/shrooms for Shrooms, pi5/jimmy for whatever
comes. None of it is written anywhere an agent can read. Each session's A2A
card (ADR-041) has a `skills` list, and ours held one placeholder: the
session's name, harness and folder. Agents chose whom to ask by name and
memory, and the owner routed what they guessed wrong.

What a seat may do without asking was not written either. Reviewed pull
requests waited days on the owner because nobody could say they were routine
(2026-10-19).

The task flow itself cost more than it carried, measured over one afternoon:
297 tasks, 39 of them one session asking the publisher to publish. Every
reply to a reply was a new task, every result had to be acknowledged by hand,
and twice a merge was asked for before the change had ever run where it
would run.

## Decision

**A session's card says what it is for, and the owner says it.**

- **Skills** are what a session is good at: A2A's own `AgentSkill` (name,
  description, tags), so any A2A client reads them. Set per session, as the
  model is (`POST /v1/sessions/{name}/settings`, `skills`); shown by
  `list_agents` and in the apps. Without any, the card keeps the placeholder.
- **Responsibilities** are what a session owns and may do without asking the
  owner ("publishes to the LAN repos", "merges reviewed PRs that ran"). A2A
  has no field for this, so they are this project's card extension, beside
  the task one (`responsibilities`), and `list_agents` shows them too.
- **Only the owner sets them**: through the agent's API from the owner's
  devices or the machine itself. A caged session's socket does not reach its
  settings, so it cannot give itself a skill or a responsibility.
- **Skills suggest, they do not route.** An agent looking for help reads the
  cards and asks whom it chooses; nothing sends work to a skill on its own.
- A responsibility is a statement, not an enforcement. Where it matters, the
  doing side still checks: a public release still waits for the code from the
  owner's phone (ADR-055).

**Step 2: the task flow** (ADR-042's tasks), not built yet:

- A reply continues the open task it answers, by A2A's own task and context
  ids, rather than opening a new one; not once that task is completed.
- A completed task closes itself once the asker has read its result. An
  explicit acknowledgement stays only where the result asks for a decision.
- A task finished as "it ran" carries where it ran, and "merges reviewed PRs
  that ran" means that evidence was there.

## Consequences

- The apps gain a skills-and-responsibilities setting per session, both of
  them, as for the model (ADR-050's parity).
- The owner writes the first set by hand; they are only as good as that.
- Who may decide what is now visible on the cards, so a request that needs the
  owner can say so, and one that does not need not wait.
