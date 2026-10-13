# Faster speech-to-text for voice notes

**Status:** measured 2026-10-03, Czech included; switched to Parakeet the same day.

Voice notes to agents are transcribed on the agent's machine
([docs/agents.md](agents.md)). The first engine, whisper.cpp with
large-v3-turbo, managed about 1.7× real time on the laptop at best, and 1.0×
under the balanced power profile: a minute of speech took a minute. Too slow
for something you wait on.

## Measured

Laptop: i7-13700H, 12 threads, balanced power profile (≈1.4 GHz average
during the runs), CPU only. Best of two runs. "Voice" is a real 75-second
voice note recorded on a phone, in English, with filler words and a false
start; JFK is whisper.cpp's 11-second sample; "long" is it five times.

| engine / model | size | JFK 11 s | voice 75 s | long 55 s | × real time |
|---|---|---|---|---|---|
| whisper large-v3-turbo q5_0 (`-l en`, clip-sized `-ac`) | 574 MB | 11.0 s | 73.5 s | 47.3 s | **1.0–1.2×** |
| whisper small q5_1 | 190 MB | 3.2 s | 23.3 s | 15.3 s | 3.2–3.6× |
| whisper base q5_1 | 60 MB | 1.2 s | 9.1 s | 5.6 s | 8–10× |
| **parakeet-tdt-0.6b-v3 q4_k** | 416 MB | 1.8 s | 10.5 s | 7.0 s | **6–8×** |
| parakeet-tdt-0.6b-v3 q8_0 | 669 MB | 2.0 s | 12.1 s | 8.4 s | 5.6–6.5× |

Quality on the real voice note:

- **turbo** — the cleanest: punctuation, "Logos Basecamp" spelled right,
  filler words dropped. Slowest by far.
- **parakeet v3** — the same words, "Logos Basecamp" right, but verbatim: it
  keeps "uh", "um" and the false start ("it would di uh deploy"). For text
  sent to a model, harmless; for text a person reads back, slightly noisy.
- **small** — good, but "logos base camp", fewer sentence breaks.
- **base** — fastest, and wrong in places ("a configured idea local AI model",
  "run on an hardware").

### Czech (measured 2026-10-03)

A real 9-second Czech voice note from the phone, same laptop:

| engine | time | text |
|---|---|---|
| whisper turbo, `-l cs`, as the agent runs it | 8.7 s | …když **Parkýt** bude **funbovat**. |
| whisper turbo, `-l auto` | 29 s | "a my." — the language was not recognised |
| **parakeet v3 q4_k**, no language given | **1.5–1.9 s** | …když **parky** bude **fungovat**. |
| parakeet v3 q8_0 | 1.6–2.0 s | the same as q4_k |

Parakeet found the language itself, got every ordinary word right where
Whisper mangled one, and took a fifth of the time. Neither knew "Parakeet".
A shorter `-ac` (384) made Whisper repeat the sentence five times — the clip
window rule in stt.go matters for it, and does not exist for Parakeet.

## What Parakeet changes besides speed

- **No language choice.** Parakeet v3 detects the language itself, with no
  extra pass — Whisper's auto-detection doubled its time, which is why the
  apps grew a CS/EN/AUTO switch. That switch is gone.
- **Czech works** (v3 covers 25 European languages): measured above.
- It runs in whisper.cpp's own build (`parakeet-cli`, upstream since
  2026-10), with ggml models from `ggml-org/parakeet-GGUF` on Hugging Face —
  no new dependency.

## Long notes: in pieces (2026-10-13)

Parakeet takes a whole clip in one pass, and its time and memory grow faster
than the clip. On this laptop (i7-13700H, 12 threads), with q4_k:

| Note | One pass | Pieces of about a minute, cut at pauses |
|---|---|---|
| 115 s | 15 s | 11 s |
| 406 s | 99 s, 4.9 GB | 40 s (in pieces, run at once), under 1.5 GB each |

The text did not suffer: 96-97% the same words as the one pass, and where
they differed the pieces were mostly right ("Piper", "Czech", "would be
great" where the one pass heard "hyper", "check", "we grade"). Joining
pieces at pauses lost or doubled nothing at the seams checked.

So a Parakeet note over 90 s is cut at the pause nearest each minute
(`silencedetect`, within 40% of a piece; where it falls when there is none,
and the last piece is never a sliver) and the pieces run at once: one for every four
transcription threads, up to four, the threads shared between them. The
agent uses at most 12 threads unless `--stt-threads` says more, so three
run at once by default. More than four did not pay (30 s with seven). Whisper is unchanged: it
works in 30-second windows of its own.

## Faster still, not tried

- **The performance power profile** — everything above ran at about 1.4 GHz.
- **Vulkan on the Iris Xe** — whisper.cpp (and llama.cpp) build with it; the
  headers are installed, `glslc` is not (`sudo apt install glslc`).

## Decision

**Parakeet v3 q4_k is shrooms-agent's default** (decided 2026-10-03, after
the Czech note). The engine follows the model: a file named `*parakeet*` runs
with `parakeet-cli` and no language, anything else with `whisper-cli`, its
language and the clip-sized window — so `--stt-model …turbo…` brings Whisper
back. The apps no longer offer a language; they send `lang=auto`. The cost is
verbatim filler words ("uh", false starts) in the text.
