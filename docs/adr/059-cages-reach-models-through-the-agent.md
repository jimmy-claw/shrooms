# 059. Cages reach their models through the agent, and hold no keys

**Status:** accepted 2026-10-16, built the same day:
- **Phase 1:** sealed cages running Claude Code reach Anthropic through the
  gateway, and hold no token.
- **Phase 2:** caged pi sessions reach their cloud providers through it, and
  hold no model keys.

- **Phase 3:** Claude Code in any cage, caged or sealed, reaches Anthropic
  on the owner's own login, read fresh by the gateway, and the login is never
  in the cage. A sealed cage no longer needs a token of its own: the gateway
  uses the sealed token if there is one, and the owner's login if not.

The difference between caged and sealed is the network: a sealed cage gets
the internet and nothing local. Neither may leak the owner's credentials,
and since phase 3 neither holds them.

## Context

A caged session needs a model, and today it gets the credential for one:
every `*_API_KEY` and `*_TOKEN` variable of the agent's is passed into a
caged session, and a sealed cage runs on the machine's sealed Claude token
(ADR-045). The code in a sealed cage is code nobody vouches for, and a
caged session can be talked into anything. Either can read the key and
send it anywhere it can reach, which for a sealed cage is the internet.

The Lambda Prize harness (lprize-env) split this in two: a LiteLLM proxy
held the real key, and the sandbox talked to the proxy with a throwaway
token. That worked, at the price of another container per sandbox.

The harnesses come and go (Claude Code, pi; opencode and codex are
candidates), so whatever this is has to be pluggable, not written per
harness in the cage code.

## Decision

**The agent is the gateway.** Every cage already has a socket to its own
agent (ADR-044). The agent serves model upstreams on it at
`/llm/<upstream>/…` and forwards each request to the upstream's real base
URL, dropping the caller's credentials and adding the real ones. Only the
model endpoints are forwarded, and nothing else of the upstream's API.

**Upstreams are named, with their credentials kept by the agent:**
`anthropic` (an API key, or the machine's Claude token for sealed cages),
`openrouter`, `venice`, `openai`, and local ones such as `k11`, which need
none. Adding one is a name, a base URL and where its key is read from.

**Inside the cage, a forwarder with no secret.** Harnesses want an
`http://` base URL, not a socket. `shrooms-agent llm-forward` is the agent's
own binary, already mounted read-only in the cage. It listens on the cage's
loopback and passes each request to the socket. It holds nothing worth
stealing.

**Harnesses plug in by saying how they are pointed elsewhere.** A harness
that can use the gateway implements one more method. Given the gateway's
base URL for each upstream, it returns the environment and the files its
process needs:
- **Claude Code:** `ANTHROPIC_BASE_URL` and a dummy `ANTHROPIC_API_KEY`.
- **pi:** a `models.json` laid over the real one in the cage, giving each
  provider the gateway's `baseUrl` and a dummy key. The real file stays as
  it is, and a provider re-pointed this way keeps its built-in models.
- **opencode or codex:** whatever each one reads. The cage code doesn't
  change.

**Then no keys go into the cage.** A cage whose harness plugs in gets no
`*_API_KEY` or `*_TOKEN` from the agent. Sealed cages come first, then
caged sessions by default.

## Consequences

- Sealed cages no longer hold the machine's Claude token. A 2026-10-16
  spike showed Claude Code working with only a dummy key, through a proxy
  that added the token: `POST /v1/messages` → 200. The same day a real
  sealed cage answered through the gateway, and the token was in none of
  its environment, processes or files; a request for Anthropic's API keys
  through it was refused (403).
- One place to add what the keys never had: a per-session spending cap, a
  log of usage, an allowlist of models.
- A harness that does not plug in keeps working as before, with the keys
  passed in, until it does.
- **A cage made before the gateway keeps its keys** until it is made again,
  rather than losing what was installed in it to an upgrade. Cages made with
  the gateway carry the label `xyz.vpavlin.shrooms.gateway=1`.
- **pi's file replaces a mount, not adds one.** pi's `models.json` is one of
  its settings mounted read-only, and podman refuses two mounts at one place.
  The first live run found this.
- **The owner's login, without racing it.** The gateway reads the access
  token from `~/.claude/.credentials.json` on each request. It never renews
  the token itself: renewing replaces the refresh token, and racing the
  owner's own Claude sessions at it could log them out. When the token is
  within 2 minutes of running out, it runs `claude auth status`, so that
  Claude Code renews it with its own locking, then reads the file again.
- **A caged Claude cage still mounts `~/.claude`** for its conversations,
  with an empty `{}` laid over `.credentials.json` in it. The rest of
  `~/.claude` (every session's conversations, memory, settings) is still
  there to read. That is the next thing to narrow.
- **Phase 3 tested live, 2026-10-16:** a caged and a sealed Claude session
  answered on the owner's login, the sealed one with no token configured.
  Neither cage held the access or the refresh token in its environment or
  processes. The caged one saw `{}` for credentials, and the owner's file
  was unchanged.
- **Tested live, 2026-10-16, on OpenRouter:** a caged pi session answered
  through the gateway. The key was in none of its environment or processes,
  its `models.json` pointed OpenRouter at the gateway and kept the owner's
  `k11` provider, and the real file on the machine was untouched.
- The gateway is in the path of every model call a cage makes. If the
  agent restarts, those calls fail and are retried, as on any network
  error.
