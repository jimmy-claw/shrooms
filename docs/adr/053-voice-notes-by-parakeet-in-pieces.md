# 053. Voice notes by Parakeet, a long one in pieces at its pauses

**Status:** accepted 2026-10-03 (Parakeet), built the same day; long notes in
pieces built 2026-10-13 — measurements in
[docs/speech-to-text.md](../speech-to-text.md). Replaces whisper.cpp's Whisper
as named in [ADR-036](036-agents-grow-out-of-the-mesh.md).

## Context

A voice note sent to an agent is transcribed on the agent's machine, so the
audio never leaves the owner's devices (ADR-036). The first engine was
whisper.cpp with large-v3-turbo. On the laptop (i7-13700H, balanced power
profile, CPU only) it ran at 1.0-1.2× real time: a minute of speech took a
minute, for something the sender waits on. Detecting the language doubled
that, which is why the apps grew a CS/EN/AUTO switch.

Measured the same day (docs/speech-to-text.md): Parakeet TDT 0.6B v3, q4_k,
ran at 6-8× real time on the same notes. On a real 9-second Czech note it took
1.5-1.9 s against Whisper's 8.7 s, found the language itself, and got every
ordinary word right where Whisper mangled one. It runs in whisper.cpp's own
build (`parakeet-cli`), so nothing new to depend on.

Parakeet takes a clip in one pass, and its time and memory grow faster than
the clip. On the laptop a 115 s note took 15 s, and a 406 s one 99 s and
4.9 GB.

## Decision

- **Parakeet v3 q4_k is the default model.** The engine follows the model
  file: a name with `parakeet` in it runs `parakeet-cli` with no language,
  anything else `whisper-cli` with its language and a clip-sized window. So
  `--stt-model` brings Whisper back. The apps send `lang=auto` and offer no
  language.
- **A Parakeet note over 90 s is cut into pieces of about 60 s, at pauses.**
  Each cut goes to the silence (ffmpeg `silencedetect`) nearest the minute,
  within 40% of a piece; where there is none, where it falls. The last piece
  is never a sliver.
- **The pieces run at once**, one for every four threads, up to four, and the
  threads are split between them. With the agent's default of at most 12
  threads that is three. More than four did not pay: seven at once
  took 30 s, no better.
- Whisper is left as it was: it works in 30-second windows of its own.

Cut at pauses, not at fixed times: a cut in the middle of a word loses or
doubles it at the seam. Speed is the goal; accuracy was the condition.

## Consequences

- The 406 s note: 40 s end to end in pieces, against 99 s in one pass, under
  1.5 GB a piece. The 115 s one: 11 s against 15 s.
- The text did not get worse: 96-97% the same words as the one pass, and
  where they differed the pieces were mostly right ("Piper", "Czech", "would
  be great" where the one pass heard "hyper", "check", "we grade"). No word
  was lost or doubled at the seams checked.
- Parakeet is verbatim: "uh", "um" and false starts stay in the text. For text
  sent to a model, harmless.
- The agent's default thread count is the machine's cores, up to 12. That
  allows three pieces at a time; four needs `--stt-threads 16` or more.
- `install-agent.sh --voice` builds `parakeet-cli` at a pinned commit and
  fetches the model by its sha256.

## What would change our mind

A Parakeet build that keeps its time linear in the clip, which would make the
pieces unnecessary; or a GPU path (Vulkan) on the machines that transcribe,
which was not tried.
