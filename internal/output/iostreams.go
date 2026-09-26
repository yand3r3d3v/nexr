// Package output handles terminal detection, tables, JSON and messages.
package output

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

// IOStreams bundles the standard streams and what is known about them.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer

	stdinTTY  bool
	stdoutTTY bool
	stderrTTY bool
	noColor   bool
}

// System returns streams bound to the process's stdin, stdout and stderr.
func System() *IOStreams {
	return &IOStreams{
		In:        os.Stdin,
		Out:       os.Stdout,
		ErrOut:    os.Stderr,
		stdinTTY:  isTerminal(os.Stdin),
		stdoutTTY: isTerminal(os.Stdout),
		stderrTTY: isTerminal(os.Stderr),
		noColor:   os.Getenv("NO_COLOR") != "",
	}
}

// Test returns streams backed by buffers. No stream is a terminal.
func Test() (ios *IOStreams, in, out, errOut *bytes.Buffer) {
	in, out, errOut = &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}
	return &IOStreams{In: in, Out: out, ErrOut: errOut}, in, out, errOut
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// IsStdinTTY reports whether stdin is a terminal.
func (s *IOStreams) IsStdinTTY() bool { return s.stdinTTY }

// IsStdoutTTY reports whether stdout is a terminal.
func (s *IOStreams) IsStdoutTTY() bool { return s.stdoutTTY }

// IsStderrTTY reports whether stderr is a terminal.
func (s *IOStreams) IsStderrTTY() bool { return s.stderrTTY }

// SetTTY overrides terminal detection (used in tests).
func (s *IOStreams) SetTTY(stdin, stdout, stderr bool) {
	s.stdinTTY, s.stdoutTTY, s.stderrTTY = stdin, stdout, stderr
}

// SetNoColor disables colour output.
func (s *IOStreams) SetNoColor(v bool) { s.noColor = v }

// ColorEnabled reports whether colour may be used on stdout.
func (s *IOStreams) ColorEnabled() bool { return s.stdoutTTY && !s.noColor }

// CanPrompt reports whether the user can answer interactive questions.
func (s *IOStreams) CanPrompt() bool { return s.stdinTTY && s.stderrTTY }

// Warnf prints a warning to stderr.
func (s *IOStreams) Warnf(format string, args ...any) {
	fmt.Fprintf(s.ErrOut, "warning: "+format+"\n", args...)
}
