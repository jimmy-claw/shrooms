package agent

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Transcriber turns a voice note into text on this machine, with whisper.cpp's
// tools: the audio never leaves the owner's devices, whatever speech engine the
// phone happens to ship (docs/agents.md).
//
// Two engines, told apart by the model: Parakeet v3 (parakeet-cli), the
// default since 2026-10-03 — five to eight times faster than Whisper turbo on
// this laptop, Czech included, and it detects the language itself — and
// Whisper (whisper-cli). docs/speech-to-text.md has the measurements.
type Transcriber struct {
	Bin     string // parakeet-cli or whisper-cli
	Model   string // a ggml model file
	FFmpeg  string
	FFprobe string
	Threads int
}

// IsParakeet reports whether a model file is a Parakeet one, which takes
// parakeet-cli and no language.
func IsParakeet(model string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(model)), "parakeet")
}

// EngineFor is the CLI that runs a model.
func EngineFor(model string) string {
	if IsParakeet(model) {
		return "parakeet-cli"
	}
	return "whisper-cli"
}

var validLang = regexp.MustCompile(`^(auto|[a-z]{2,3})$`)

// Transcribe returns the text of an audio file in any format ffmpeg reads.
//
// lang is a language code or "auto". Parakeet ignores it: it detects the
// language as it goes, at no cost. For Whisper, detecting costs a whole extra
// pass of the encoder, doubling the time (19 s against 6 s for an 11-second
// clip), and its encoder is given a window as long as the clip rather than the
// 30 seconds it always pads to (--audio-ctx): 13 s → 5.4 s, the same words.
func (t *Transcriber) Transcribe(ctx context.Context, audio, lang string) (string, error) {
	if lang == "" {
		lang = "auto"
	}
	if !validLang.MatchString(lang) {
		return "", fmt.Errorf("language %q", lang)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	wav := strings.TrimSuffix(audio, filepath.Ext(audio)) + ".16k.wav"
	defer os.Remove(wav)
	if out, err := exec.CommandContext(ctx, t.FFmpeg, "-loglevel", "error", "-y", "-i", audio,
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav).CombinedOutput(); err != nil {
		return "", fmt.Errorf("converting the audio: %v: %s", err, bytes.TrimSpace(out))
	}

	if IsParakeet(t.Model) {
		if secs := wavSeconds(wav); secs > pieceOver {
			return t.inPieces(ctx, wav, secs)
		}
		return t.run(ctx, wav, t.threads())
	}
	args := []string{"-l", lang, "-nt"}
	if secs, err := t.duration(ctx, wav); err == nil {
		args = append(args, "-ac", strconv.Itoa(audioCtx(secs)))
	}
	return t.run(ctx, wav, t.threads(), args...)
}

// run is one pass of the engine over a 16 kHz wav.
func (t *Transcriber) run(ctx context.Context, wav string, threads int, extra ...string) (string, error) {
	args := append([]string{"-m", t.Model, "-f", wav, "-np", "-t", strconv.Itoa(threads)}, extra...)
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, t.Bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("transcribing: %v: %s", err, lastLine(stderr.String()))
	}
	return strings.Join(strings.Fields(stdout.String()), " "), nil
}

// A long note in pieces (2026-10-13). Parakeet takes a whole clip at once,
// and its time grows faster than the clip: on this laptop 115 s took 15 s,
// 406 s took 99 s and 4.9 GB. Cut at pauses into pieces of about a minute,
// the same 406 s took 46 s one piece after another and 30-33 s four at a time
// — and the text was no worse: 97% the same words, and where they differed
// the pieces were mostly the right ones ("Piper", "Czech", "would be great"
// where the one long pass heard "hyper", "check", "we grade"). Whisper is left
// as it is: it works in 30-second windows of its own.
const (
	pieceOver   = 90.0 // seconds: notes longer than this are cut
	pieceTarget = 60.0 // seconds a piece aims at
	maxPieces   = 4    // at once: more stops paying, memory bandwidth is the limit
)

// inPieces transcribes a long wav as pieces cut at pauses, several at a time,
// and joins their text in order.
func (t *Transcriber) inPieces(ctx context.Context, wav string, secs float64) (string, error) {
	cuts := cutPoints(secs, t.pauses(ctx, wav))
	n := len(cuts) - 1
	par := min(n, maxPieces, max(1, t.threads()/4))
	per := max(2, t.threads()/par)
	texts := make([]string, n)
	errs := make([]error, n)
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		piece := fmt.Sprintf("%s.%02d.wav", strings.TrimSuffix(wav, ".wav"), i)
		defer os.Remove(piece)
		wg.Add(1)
		go func(i int, piece string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if out, err := exec.CommandContext(ctx, t.FFmpeg, "-loglevel", "error", "-y", "-i", wav,
				"-ss", fmt.Sprintf("%.3f", cuts[i]), "-to", fmt.Sprintf("%.3f", cuts[i+1]), "-c", "copy", piece).CombinedOutput(); err != nil {
				errs[i] = fmt.Errorf("cutting the audio: %v: %s", err, bytes.TrimSpace(out))
				return
			}
			texts[i], errs[i] = t.run(ctx, piece, per)
		}(i, piece)
	}
	wg.Wait()
	var parts []string
	for i := range texts {
		if errs[i] != nil {
			return "", errs[i]
		}
		if texts[i] != "" {
			parts = append(parts, texts[i])
		}
	}
	return strings.Join(parts, " "), nil
}

// wavSeconds is how long the converted wav is, from its size: 16 kHz, mono,
// 16-bit, so 32000 bytes a second after the 44-byte header. No ffprobe pass.
func wavSeconds(wav string) float64 {
	st, err := os.Stat(wav)
	if err != nil || st.Size() <= 44 {
		return 0
	}
	return float64(st.Size()-44) / 32000
}

var silenceAt = regexp.MustCompile(`silence_(start|end): ([0-9.]+)`)

// pauses are the middles of the clip's silences, as ffmpeg finds them; none
// when it cannot say, and the pieces are then cut where they fall.
func (t *Transcriber) pauses(ctx context.Context, wav string) []float64 {
	out, _ := exec.CommandContext(ctx, t.FFmpeg, "-hide_banner", "-nostats", "-i", wav,
		"-af", "silencedetect=noise=-30dB:d=0.25", "-f", "null", "-").CombinedOutput()
	var mids []float64
	start := -1.0
	for _, m := range silenceAt.FindAllStringSubmatch(string(out), -1) {
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		if m[1] == "start" {
			start = v
		} else if start >= 0 {
			mids = append(mids, (start+v)/2)
			start = -1
		}
	}
	return mids
}

// cutPoints are where to cut a clip of dur seconds: about every pieceTarget,
// at the pause nearest to it within 40% of a piece, else where it falls; the
// last piece is never a sliver.
func cutPoints(dur float64, pauses []float64) []float64 {
	cuts := []float64{0}
	for next := pieceTarget; next < dur-pieceTarget/3; {
		cut, best := next, pieceTarget*0.4
		for _, p := range pauses {
			if d := math.Abs(p - next); d < best && p > cuts[len(cuts)-1]+5 {
				cut, best = p, d
			}
		}
		cuts = append(cuts, cut)
		next = cut + pieceTarget
	}
	return append(cuts, dur)
}

func (t *Transcriber) threads() int {
	if t.Threads > 0 {
		return t.Threads
	}
	return 8
}

func (t *Transcriber) duration(ctx context.Context, wav string) (float64, error) {
	out, err := exec.CommandContext(ctx, t.FFprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "csv=p=0", wav).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// audioCtx is the encoder window for a clip: 1500 frames is whisper's full 30
// seconds, so the clip's share of that, plus a margin — a window cut exactly
// at the last word loses it.
func audioCtx(secs float64) int {
	n := int(math.Ceil(secs/30*1500)) + 128
	return min(max(n, 256), 1500)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
