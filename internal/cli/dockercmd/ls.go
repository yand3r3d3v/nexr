package dockercmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/images"
	"github.com/yand3r3d3v/nexr/internal/output"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

type lsOptions struct {
	match []string
	long  bool
}

func newLsCmd(f *cmdutil.Factory, rf *repoFlags) *cobra.Command {
	opts := &lsOptions{}
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "images"},
		Short:   "List the images of a repository",
		Long: `List the image names of a repository, sorted, from the catalog of the Registry
API. If the registry endpoint fails, the names come from the components.

-l adds the number of tags and the last push of each image; it reads every
component of the repository once.`,
		Example: `  nexr docker ls -R docker-hosted
  nexr docker ls --match 'team/*' -l
  nexr docker ls --json`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLs(cmd, f, rf, opts) },
	}
	cmd.Flags().StringArrayVar(&opts.match, "match", nil, "only images matching this glob or re:REGEX (repeatable)")
	cmd.Flags().BoolVarP(&opts.long, "long", "l", false, "show the number of tags and the last push")
	return cmd
}

type imageJSON struct {
	Repository string  `json:"repository"`
	Name       string  `json:"name"`
	TagCount   *int    `json:"tag_count"`
	LastPushed *string `json:"last_pushed"`
}

func runLs(cmd *cobra.Command, f *cmdutil.Factory, rf *repoFlags, opts *lsOptions) error {
	match, err := cmdutil.ParsePatterns("--match", opts.match)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	repo, err := resolveRepo(ctx, f, rf, "")
	if err != nil {
		return err
	}
	svc, err := open(ctx, f, rf, repo)
	if err != nil {
		return err
	}
	names, err := svc.Images(ctx)
	if err != nil {
		return err
	}
	if len(match) > 0 {
		kept := names[:0]
		for _, n := range names {
			if remote.MatchAny(match, n) {
				kept = append(kept, n)
			}
		}
		names = kept
	}
	var stats map[string]images.Stats
	if opts.long {
		if stats, err = svc.Stats(ctx); err != nil {
			return err
		}
	}

	switch {
	case f.JSON():
		list := make([]imageJSON, 0, len(names))
		for _, n := range names {
			j := imageJSON{Repository: repo, Name: n}
			if opts.long {
				st := stats[n]
				j.TagCount, j.LastPushed = &st.Tags, cmdutil.NullTime(st.LastPushed)
			}
			list = append(list, j)
		}
		return output.WriteJSON(f.IO.Out, list, f.IO.IsStdoutTTY())
	case !opts.long || f.Flags.Quiet:
		for _, n := range names {
			fmt.Fprintln(f.IO.Out, n)
		}
		return nil
	}
	t := output.NewTable(f.IO.Out, "IMAGE", "TAGS", "LAST PUSHED")
	for _, n := range names {
		st := stats[n]
		t.AddRow(n, strconv.Itoa(st.Tags), output.HumanTime(st.LastPushed))
	}
	return t.Render()
}
