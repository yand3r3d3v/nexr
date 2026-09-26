package output

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

func TestTable(t *testing.T) {
	var b bytes.Buffer
	tb := NewTable(&b, "name", "format")
	tb.AddRow("raw-hosted", "raw")
	tb.AddRow("docker-hosted-long", "docker")
	if err := tb.Render(); err != nil {
		t.Fatal(err)
	}
	want := "NAME                FORMAT\nraw-hosted          raw\ndocker-hosted-long  docker\n"
	if b.String() != want {
		t.Fatalf("table:\n%q\nwant\n%q", b.String(), want)
	}
}

func TestWriteJSON(t *testing.T) {
	var compact, pretty bytes.Buffer
	v := map[string]any{"a": "<b>", "n": 1}
	if err := WriteJSON(&compact, v, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&pretty, v, true); err != nil {
		t.Fatal(err)
	}
	if compact.String() != "{\"a\":\"<b>\",\"n\":1}\n" {
		t.Fatalf("compact = %q", compact.String())
	}
	if !strings.Contains(pretty.String(), "\n  \"a\": \"<b>\"") {
		t.Fatalf("pretty = %q", pretty.String())
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 312: "312 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 50541231: "48.2 MiB", 1 << 40: "1.0 TiB"} {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

type statusErr struct{}

func (statusErr) Error() string   { return "not found" }
func (statusErr) HTTPStatus() int { return 404 }
func (statusErr) Kind() errs.Kind { return errs.KindNotFound }
func (statusErr) Hints() []string { return []string{"check the name"} }

func TestPrintError(t *testing.T) {
	var text, js bytes.Buffer
	err := errs.Wrap(errs.KindGeneric, statusErr{}, "repository \"x\"")
	PrintError(&text, err, false)
	if text.String() != "nexr: repository \"x\": not found\nhint: check the name\n" {
		t.Fatalf("text = %q", text.String())
	}
	PrintError(&js, err, true)
	want := `{"error":{"code":"not_found","message":"repository \"x\": not found","exit_code":5,"http_status":404,"hints":["check the name"]}}` + "\n"
	if js.String() != want {
		t.Fatalf("json = %q\nwant  %q", js.String(), want)
	}
	js.Reset()
	PrintError(&js, errors.New("boom"), true)
	if !strings.Contains(js.String(), `"http_status":null`) || !strings.Contains(js.String(), `"hints":[]`) {
		t.Fatalf("json = %q", js.String())
	}
}

func TestIOStreams(t *testing.T) {
	ios, _, _, errOut := Test()
	if ios.IsStdinTTY() || ios.IsStdoutTTY() || ios.IsStderrTTY() || ios.ColorEnabled() || ios.CanPrompt() {
		t.Fatal("test streams must not be terminals")
	}
	ios.SetTTY(true, true, true)
	if !ios.ColorEnabled() || !ios.CanPrompt() {
		t.Fatal("terminals allow colour and prompts")
	}
	ios.SetNoColor(true)
	if ios.ColorEnabled() {
		t.Fatal("--no-color must disable colour")
	}
	ios.Warnf("disk %s", "full")
	if errOut.String() != "warning: disk full\n" {
		t.Fatalf("Warnf wrote %q", errOut.String())
	}
	if sys := System(); sys.Out == nil || sys.ErrOut == nil || sys.In == nil {
		t.Fatal("System() must bind the process streams")
	}
}

func TestHumanTimeAndTableLen(t *testing.T) {
	if HumanTime(time.Time{}) != "-" {
		t.Fatal("the zero time must be shown as -")
	}
	ts := time.Date(2026, 9, 26, 17, 4, 0, 0, time.Local)
	if got := HumanTime(ts); got != "2026-09-26 17:04" {
		t.Fatalf("HumanTime() = %q", got)
	}
	tbl := NewTable(&bytes.Buffer{}, "a")
	tbl.AddRow("1")
	tbl.AddRow("2")
	if tbl.Len() != 2 {
		t.Fatalf("Len() = %d", tbl.Len())
	}
}
