package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Table renders aligned columns with an upper-case header row.
type Table struct {
	w       io.Writer
	headers []string
	rows    [][]string
}

// NewTable returns a table that writes to w.
func NewTable(w io.Writer, headers ...string) *Table {
	return &Table{w: w, headers: headers}
}

// AddRow appends a row. Missing cells are rendered empty.
func (t *Table) AddRow(cells ...string) {
	t.rows = append(t.rows, cells)
}

// Len returns the number of rows.
func (t *Table) Len() int { return len(t.rows) }

// Render writes the table. Lines do not end in padding.
func (t *Table) Render() error {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	if len(t.headers) > 0 {
		up := make([]string, len(t.headers))
		for i, h := range t.headers {
			up[i] = strings.ToUpper(h)
		}
		fmt.Fprintln(tw, strings.Join(up, "\t"))
	}
	for _, r := range t.rows {
		cells := make([]string, len(t.headers))
		copy(cells, r)
		if len(r) > len(cells) {
			cells = r
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	var out strings.Builder
	for line := range strings.Lines(buf.String()) {
		out.WriteString(strings.TrimRight(line, " \n"))
		out.WriteByte('\n')
	}
	_, err := io.WriteString(t.w, out.String())
	return err
}

// WriteJSON encodes v as one JSON document. It is indented when pretty is true.
func WriteJSON(w io.Writer, v any, pretty bool) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

// HumanBytes formats a size with IEC units and one decimal ("48.2 MiB").
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanTime formats a timestamp in local time for tables. The zero time is "-".
func HumanTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// OrDash returns s, or "-" when s is empty.
func OrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
