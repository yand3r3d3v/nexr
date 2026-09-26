// Package versioncmd implements "nexr version".
package versioncmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/buildinfo"
	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/output"
)

// New returns the version command.
func New(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  "Print the version, commit, build date, Go version and platform of nexr.",
		Example: `  nexr version
  nexr version --json`,
		Args: cmdutil.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			info := buildinfo.Get()
			if f.Flags.JSON {
				return output.WriteJSON(f.IO.Out, info, f.IO.IsStdoutTTY())
			}
			fmt.Fprintf(f.IO.Out, "nexr %s\n", info.Version)
			fmt.Fprintf(f.IO.Out, "commit:   %s\n", output.OrDash(info.Commit))
			fmt.Fprintf(f.IO.Out, "built:    %s\n", output.OrDash(info.Date))
			fmt.Fprintf(f.IO.Out, "go:       %s\n", info.GoVersion)
			fmt.Fprintf(f.IO.Out, "platform: %s\n", info.Platform)
			return nil
		},
	}
}
