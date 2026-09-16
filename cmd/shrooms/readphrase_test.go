package main

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// A confirmation that is not a secret must be echoed.
//
// Typing a mesh's name to confirm removing it went through readSecret, the
// passphrase reader, so nothing appeared as it was typed. 2026-09-16: "error:
// stopped, and nothing was changed" — indistinguishable from changing your
// mind, with no way to tell that a keystroke went astray.

func TestReadPhraseTrimsAndReturnsTheLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"default\n", "default"},
		{"  default  \n", "default"},
		{"yes\n", "yes"},
		{"office\r\n", "office"}, // a terminal that sends CRLF
		{"\n", ""},
	} {
		stdinReader = bufio.NewReader(strings.NewReader(tc.in))
		got, err := readPhrase("confirm: ")
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("readPhrase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	stdinReader = nil
}

// A piped answer still works, which is what keeps this usable from a script.
func TestReadPhraseFromAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { w.WriteString("default\n"); w.Close() }()
	stdinReader = bufio.NewReader(r)
	got, err := readPhrase("confirm: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "default" {
		t.Errorf("got %q, want %q", got, "default")
	}
	stdinReader = nil
}

// Nothing to read at all is an error rather than a silent empty confirmation,
// since an empty string would never match a mesh name anyway but "yes" checks
// elsewhere deserve the same care.
func TestReadPhraseOnClosedInput(t *testing.T) {
	stdinReader = bufio.NewReader(strings.NewReader(""))
	if _, err := readPhrase("confirm: "); err == nil {
		t.Error("closed input produced no error")
	}
	stdinReader = nil
}

// Nothing to read must say what to do about it.
//
// The container wrappers run the CLI through `docker exec` with no terminal, so
// every interactive command on a containerised node hits this. On vps,
// 2026-09-16, `config flatten` printed its plan and then "error: EOF" — which
// reads as the flatten having failed rather than as nobody having been asked.
func TestNoInputSaysWhatToDo(t *testing.T) {
	stdinReader = bufio.NewReader(strings.NewReader(""))
	_, err := readPhrase("confirm: ")
	stdinReader = nil

	if err == nil {
		t.Fatal("closed input produced no error")
	}
	msg := err.Error()
	if msg == "EOF" {
		t.Fatal("still reports a bare EOF, which says nothing about what to do")
	}
	for _, want := range []string{"not a terminal", "--yes"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}
