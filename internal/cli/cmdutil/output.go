package cmdutil

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

// ErrorJSON describes the failure of one item in JSON output.
type ErrorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ItemError describes err for JSON output.
func ItemError(err error) ErrorJSON {
	return ErrorJSON{Code: errs.Classify(err).String(), Message: err.Error()}
}

// NullTime returns an RFC 3339 UTC timestamp, or nil for the zero time.
func NullTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// NullString returns nil for the empty string.
func NullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Millis returns a duration in milliseconds, for JSON output.
func Millis(d time.Duration) int64 { return d.Milliseconds() }

// Elapsed formats a duration for summaries: "45ms", "3.1s", "2m5s".
func Elapsed(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}

// Count returns "1 file" or "3 files".
func Count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Interrupted reports a bulk operation that was stopped by a signal; what
// names the items, e.g. "files".
func Interrupted(ctx context.Context, notStarted int, what string) error {
	if ctx.Err() == nil {
		return nil
	}
	return errs.Wrap(errs.KindInterrupted, ctx.Err(), "interrupted; %d %s were not processed", notStarted, what)
}

// ParsePatterns compiles the values of a pattern flag.
func ParsePatterns(flag string, list []string) ([]remote.Pattern, error) {
	p, err := remote.ParsePatterns(list)
	if err != nil {
		return nil, errs.Wrap(errs.KindUsage, err, "invalid %s", flag)
	}
	return p, nil
}

// Concurrency returns the --concurrency flag, or the configured default.
func Concurrency(cmd *cobra.Command, f *Factory, flag int) (int, error) {
	if !cmd.Flags().Changed("concurrency") {
		cfg, err := f.Config()
		if err != nil {
			return 0, err
		}
		return cfg.Concurrency.Value, nil
	}
	if flag < 1 || flag > 32 {
		return 0, errs.Usage("invalid --concurrency %d: use a value from 1 to 32", flag)
	}
	return flag, nil
}

// AddConcurrencyFlag adds --concurrency; what names the parallel operations.
func AddConcurrencyFlag(cmd *cobra.Command, v *int, what string) {
	cmd.Flags().IntVar(v, "concurrency", 0, fmt.Sprintf("parallel %s (default 4, or \"concurrency\" from the config)", what))
}
