// Package filescmd implements the file commands: ls, up, down and rm.
package filescmd

import (
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

func addPatternFlags(cmd *cobra.Command, include, exclude *[]string) {
	cmd.Flags().StringArrayVar(include, "include", nil, "only paths matching this glob or re:REGEX (repeatable)")
	cmd.Flags().StringArrayVar(exclude, "exclude", nil, "skip paths matching this glob or re:REGEX (repeatable)")
}
