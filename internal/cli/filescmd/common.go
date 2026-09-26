// Package filescmd implements the file commands: ls, up, down and rm.
package filescmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/files"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

// New returns the file commands.
func New(f *cmdutil.Factory) []*cobra.Command {
	return []*cobra.Command{newLsCmd(f), newUpCmd(f), newDownCmd(f), newRmCmd(f)}
}

// indexLagNote is added to the help of the commands that list files (FR-LS-7).
const indexLagNote = `Nexus updates its search and browse indexes a few seconds after an upload,
so files uploaded in the last seconds may be missing from listings.`

func service(f *cmdutil.Factory) (*files.Service, error) {
	nx, err := f.Nexus()
	if err != nil {
		return nil, err
	}
	return files.New(nx), nil
}

func parseTarget(arg string) (remote.Path, error) {
	p, err := remote.ParsePath(arg)
	if err != nil {
		return p, errs.Wrap(errs.KindUsage, err, "")
	}
	return p, nil
}

func parsePatterns(flag string, list []string) ([]remote.Pattern, error) {
	p, err := remote.ParsePatterns(list)
	if err != nil {
		return nil, errs.Wrap(errs.KindUsage, err, "invalid %s", flag)
	}
	return p, nil
}

// concurrency returns the --concurrency flag, or the configured default.
func concurrency(cmd *cobra.Command, f *cmdutil.Factory, flag int) (int, error) {
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

func addConcurrencyFlag(cmd *cobra.Command, v *int) {
	cmd.Flags().IntVar(v, "concurrency", 0, "parallel transfers (default 4, or \"concurrency\" from the config)")
}

func addPatternFlags(cmd *cobra.Command, include, exclude *[]string) {
	cmd.Flags().StringArrayVar(include, "include", nil, "only paths matching this glob or re:REGEX (repeatable)")
	cmd.Flags().StringArrayVar(exclude, "exclude", nil, "skip paths matching this glob or re:REGEX (repeatable)")
}

// errorJSON describes the failure of one item in JSON output.
type errorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func itemError(err error) errorJSON {
	return errorJSON{Code: errs.Classify(err).String(), Message: err.Error()}
}

// interrupted reports a bulk operation that was stopped by a signal.
func interrupted(ctx context.Context, notStarted int) error {
	if ctx.Err() == nil {
		return nil
	}
	return errs.Wrap(errs.KindInterrupted, ctx.Err(), "interrupted; %d files were not processed", notStarted)
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// nullTime returns an RFC 3339 UTC timestamp, or nil for the zero time.
func nullTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// elapsed formats a duration for summaries: "45ms", "3.1s", "2m5s".
func elapsed(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}

// count returns "1 file" or "3 files".
func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
