package cmdutil

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

// NoArgs rejects positional arguments with a usage error.
func NoArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return errs.Usage("unexpected argument %q for %q", args[0], cmd.CommandPath()).
			WithHint("run \"%s --help\" for usage", cmd.CommandPath())
	}
	return nil
}

// ExactArgs requires exactly n positional arguments, named for the message.
func ExactArgs(names ...string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == len(names) {
			return nil
		}
		if len(args) < len(names) {
			return errs.Usage("missing argument %s", strings.ToUpper(names[len(args)])).
				WithHint("usage: %s", cmd.UseLine())
		}
		return errs.Usage("unexpected argument %q for %q", args[len(names)], cmd.CommandPath()).
			WithHint("usage: %s", cmd.UseLine())
	}
}

// GroupArgs rejects the arguments of a command group: they can only be
// unknown subcommands.
func GroupArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	e := errs.Usage("unknown command %q for %q", args[0], cmd.CommandPath())
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2 // cobra's default, applied only in its own error path
	}
	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		e.WithHint("did you mean \"%s %s\"?", cmd.CommandPath(), s[0])
	}
	return e.WithHint("run \"%s --help\" for the available commands", cmd.CommandPath())
}

// GroupRunE shows the help of a command group run without a subcommand.
func GroupRunE(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}

// FlagError converts a flag parsing error into a usage error.
func FlagError(cmd *cobra.Command, err error) error {
	return errs.Wrap(errs.KindUsage, err, "").WithHint("run \"%s --help\" for usage", cmd.CommandPath())
}

// CompletionContext returns a short-lived context for shell completion, which
// must never hang the shell.
func CompletionContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, 3*time.Second)
}
