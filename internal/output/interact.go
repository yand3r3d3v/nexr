package output

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Confirm asks a yes/no question on stderr and reads the answer from stdin.
// Only "y" and "yes" confirm. The caller checks CanPrompt first.
func (s *IOStreams) Confirm(question string) (bool, error) {
	fmt.Fprintf(s.ErrOut, "%s [y/N] ", question)
	answer, err := s.readLine()
	if err != nil {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes", nil
}

// ConfirmName asks the user to type name to confirm, for operations that
// cannot be undone at a large scale.
func (s *IOStreams) ConfirmName(question, name string) (bool, error) {
	fmt.Fprintf(s.ErrOut, "%s\nType %q to confirm: ", question, name)
	answer, err := s.readLine()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(answer) == name, nil
}

func (s *IOStreams) readLine() (string, error) {
	if s.inReader == nil {
		s.inReader = bufio.NewReader(s.In)
	}
	line, err := s.inReader.ReadString('\n')
	if err == io.EOF && line != "" {
		err = nil
	}
	return line, err
}

// JSONArray streams a JSON array, one element at a time, so that long
// listings do not have to be held in memory.
type JSONArray struct {
	w      io.Writer
	pretty bool
	n      int
}

// NewJSONArray starts an array on w. It is indented when pretty is true.
func NewJSONArray(w io.Writer, pretty bool) *JSONArray {
	return &JSONArray{w: w, pretty: pretty}
}

// Add writes one element.
func (a *JSONArray) Add(v any) error {
	var (
		b   []byte
		err error
	)
	if a.pretty {
		b, err = json.MarshalIndent(v, "  ", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		return err
	}
	sep := ","
	if a.n == 0 {
		sep = "["
	}
	if a.pretty {
		sep += "\n  "
	}
	a.n++
	_, err = fmt.Fprintf(a.w, "%s%s", sep, b)
	return err
}

// Close ends the array.
func (a *JSONArray) Close() error {
	var err error
	switch {
	case a.n == 0:
		_, err = io.WriteString(a.w, "[]\n")
	case a.pretty:
		_, err = io.WriteString(a.w, "\n]\n")
	default:
		_, err = io.WriteString(a.w, "]\n")
	}
	return err
}

// Progress shows the progress of a transfer on one line of stderr, redrawn at
// most ten times per second (FR-OUT-10). A nil *Progress does nothing.
type Progress struct {
	w          io.Writer
	verb       string
	totalFiles int
	totalBytes int64
	start      time.Time
	files      atomic.Int64
	bytes      atomic.Int64
	mu         sync.Mutex
	shown      bool
	stop       chan struct{}
	done       chan struct{}
}

// NewProgress starts a progress display when stderr is a terminal and nil
// otherwise. totalBytes is negative when unknown.
func (s *IOStreams) NewProgress(verb string, totalFiles int, totalBytes int64) *Progress {
	if !s.IsStderrTTY() {
		return nil
	}
	p := &Progress{
		w: s.ErrOut, verb: verb, totalFiles: totalFiles, totalBytes: totalBytes, start: time.Now(),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go p.loop()
	return p
}

func (p *Progress) loop() {
	defer close(p.done)
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.draw()
		}
	}
}

func (p *Progress) draw() {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.bytes.Load()
	line := fmt.Sprintf("%s %d/%d files, %s", p.verb, p.files.Load(), p.totalFiles, HumanBytes(b))
	if p.totalBytes >= 0 {
		line += " of " + HumanBytes(p.totalBytes)
	}
	if secs := time.Since(p.start).Seconds(); secs >= 1 {
		line += fmt.Sprintf(", %s/s", HumanBytes(int64(float64(b)/secs)))
	}
	fmt.Fprintf(p.w, "\r\033[K%s", line)
	p.shown = true
}

// Add counts transferred bytes.
func (p *Progress) Add(n int64) {
	if p != nil {
		p.bytes.Add(n)
	}
}

// FileDone counts a finished file.
func (p *Progress) FileDone() {
	if p != nil {
		p.files.Add(1)
	}
}

// Clear removes the progress line so that other output can be printed; the
// next redraw shows it again.
func (p *Progress) Clear() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shown {
		fmt.Fprint(p.w, "\r\033[K")
		p.shown = false
	}
}

// Stop ends the display and removes the line.
func (p *Progress) Stop() {
	if p == nil {
		return
	}
	close(p.stop)
	<-p.done
	p.Clear()
}
