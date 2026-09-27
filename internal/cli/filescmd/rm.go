package filescmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/files"
	"github.com/yand3r3d3v/nexr/internal/output"
	"github.com/yand3r3d3v/nexr/internal/remote"
	"github.com/yand3r3d3v/nexr/internal/workpool"
)

type rmOptions struct {
	include, exclude []string
	recursive        bool
	ignoreMissing    bool
	serverSide       bool
	waitTimeout      time.Duration
	dryRun           bool
	yes              bool
	concurrency      int
}

func newRmCmd(f *cmdutil.Factory) *cobra.Command {
	opts := &rmOptions{}
	cmd := &cobra.Command{
		Use:     "rm REPO/PATH...",
		Aliases: []string{"delete"},
		Short:   "Delete files and directories",
		Long: `Delete files, or with -r directories and everything below them.

A path ending in "/", or one that exists only as a directory, is a directory
and needs -r. A path that is a file and a directory at the same time loses only
the file without -r, and both with -r.

Deleting more than one file asks for confirmation on a terminal; without a
terminal, --yes is required. Deleting the entire content of a repository
(nexr rm -r REPO) asks you to type the repository name. --dry-run shows what
would be deleted.

Files of raw repositories are deleted one by one, which needs only the delete
privilege. --server-side lets Nexus delete a whole directory instead (faster
for very large directories, but it needs more privileges and Nexus deletes in
the background; nexr waits until the directory is gone).

Empty directories disappear by themselves on the H2 database. On PostgreSQL
they can remain visible until the "Repair - Repository trim browse tree" task
runs.

` + indexLagNote,
		Example: `  nexr rm raw-releases/myapp/1.4.0/myapp.tar.gz
  nexr rm -r raw-releases/myapp/1.3.0/ --dry-run
  nexr rm -r raw-releases/tmp/ --exclude '*.keep' --yes
  nexr ls -r -q raw-releases/tmp/ | xargs nexr rm --yes`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errs.Usage("missing argument REPO/PATH").WithHint("usage: %s", cmd.UseLine())
			}
			return nil
		},
		ValidArgsFunction: completePath(f),
		RunE:              func(cmd *cobra.Command, args []string) error { return runRm(cmd, f, opts, args) },
	}
	fl := cmd.Flags()
	fl.BoolVarP(&opts.recursive, "recursive", "r", false, "delete directories and everything below them")
	addPatternFlags(cmd, &opts.include, &opts.exclude)
	fl.BoolVar(&opts.ignoreMissing, "ignore-missing", false, "succeed when a target does not exist")
	fl.BoolVar(&opts.serverSide, "server-side", false, "let Nexus delete whole directories (Browse API, more privileges)")
	fl.DurationVar(&opts.waitTimeout, "wait-timeout", 10*time.Minute, "how long to wait for a server-side deletion")
	fl.BoolVar(&opts.dryRun, "dry-run", false, "show what would be deleted, without deleting")
	fl.BoolVarP(&opts.yes, "yes", "y", false, "do not ask for confirmation")
	cmdutil.AddConcurrencyFlag(cmd, &opts.concurrency, "deletions")
	return cmd
}

type rmItemJSON struct {
	Repository string             `json:"repository"`
	Path       string             `json:"path"`
	Type       string             `json:"type"`
	Error      *cmdutil.ErrorJSON `json:"error,omitempty"`
}

type rmJSON struct {
	DryRun  bool         `json:"dry_run"`
	Deleted []rmItemJSON `json:"deleted"`
	Missing []rmItemJSON `json:"missing"`
	Failed  []rmItemJSON `json:"failed"`
	Summary struct {
		Deleted    int   `json:"deleted"`
		Missing    int   `json:"missing"`
		Failed     int   `json:"failed"`
		DurationMS int64 `json:"duration_ms"`
	} `json:"summary"`
}

func runRm(cmd *cobra.Command, f *cmdutil.Factory, opts *rmOptions, args []string) error {
	var targets []remote.Path
	for _, a := range args {
		t, err := parseTarget(a)
		if err != nil {
			return err
		}
		targets = append(targets, t)
	}
	include, err := cmdutil.ParsePatterns("--include", opts.include)
	if err != nil {
		return err
	}
	exclude, err := cmdutil.ParsePatterns("--exclude", opts.exclude)
	if err != nil {
		return err
	}
	conc, err := cmdutil.Concurrency(cmd, f, opts.concurrency)
	if err != nil {
		return err
	}
	if opts.serverSide && !opts.recursive {
		return errs.Usage("--server-side deletes directories and needs -r")
	}
	svc, err := service(f)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	ropts := files.RemoveOptions{
		Include: include, Exclude: exclude, Recursive: opts.recursive,
		ServerSide: opts.serverSide, WaitTimeout: opts.waitTimeout,
	}
	plan, err := svc.PlanRemove(ctx, targets, ropts)
	if err != nil {
		return err
	}
	result := rmJSON{DryRun: opts.dryRun, Deleted: []rmItemJSON{}, Missing: []rmItemJSON{}, Failed: []rmItemJSON{}}
	itemJSON := func(it files.RemoveItem) rmItemJSON {
		typ := "file"
		if it.Folder {
			typ = "directory"
		}
		return rmItemJSON{Repository: it.Repo.Name, Path: it.Path, Type: typ}
	}
	human := !f.JSON() && !f.Flags.Quiet

	var todo []files.RemoveItem
	var failures []error
	for _, it := range plan.Items {
		if !it.Missing {
			todo = append(todo, it)
			continue
		}
		if opts.ignoreMissing {
			result.Missing = append(result.Missing, itemJSON(it))
			if human {
				f.IO.Warnf("%s not found; ignored", it.Ref())
			}
			continue
		}
		err := errs.NotFound("%s not found", it.Ref())
		failures = append(failures, err)
		j := itemJSON(it)
		e := cmdutil.ItemError(err)
		j.Error = &e
		result.Failed = append(result.Failed, j)
		if !f.JSON() && (len(plan.Items) > 1 || opts.dryRun) {
			fmt.Fprintf(f.IO.ErrOut, "failed    %s: not found\n", it.Ref())
		}
	}

	if opts.dryRun {
		for _, it := range todo {
			result.Deleted = append(result.Deleted, itemJSON(it))
			switch {
			case f.JSON():
			case f.Flags.Quiet:
				fmt.Fprintln(f.IO.Out, it.Ref())
			default:
				fmt.Fprintf(f.IO.Out, "would delete  %s\n", it.Ref())
			}
		}
		result.Summary.Deleted, result.Summary.Missing, result.Summary.Failed = len(result.Deleted), len(result.Missing), len(result.Failed)
		if f.JSON() {
			return output.WriteJSON(f.IO.Out, result, f.IO.IsStdoutTTY())
		}
		if human {
			fmt.Fprintf(f.IO.Out, "%s would be deleted (dry run)\n", describe(todo))
		}
		return nil
	}

	if err := confirmRemoval(f, plan, todo, opts.yes); err != nil {
		return err
	}
	start := time.Now()
	total := len(plan.Items)
	notStarted := workpool.Run(ctx, conc, todo, func(ctx context.Context, it files.RemoveItem) error {
		return svc.Remove(ctx, it, ropts)
	}, func(it files.RemoveItem, err error) {
		switch {
		case err == nil:
			result.Deleted = append(result.Deleted, itemJSON(it))
			if human {
				fmt.Fprintf(f.IO.Out, "deleted  %s\n", it.Ref())
			}
		case files.IsMissing(err) && opts.ignoreMissing:
			result.Missing = append(result.Missing, itemJSON(it))
		default:
			if files.IsMissing(err) {
				err = errs.NotFound("%s not found", it.Ref())
			}
			failures = append(failures, err)
			j := itemJSON(it)
			e := cmdutil.ItemError(err)
			j.Error = &e
			result.Failed = append(result.Failed, j)
			if total > 1 && !f.JSON() {
				fmt.Fprintf(f.IO.ErrOut, "failed    %s: %v\n", it.Ref(), err)
			}
		}
	})
	result.Summary.Deleted, result.Summary.Missing, result.Summary.Failed = len(result.Deleted), len(result.Missing), len(result.Failed)
	result.Summary.DurationMS = cmdutil.Millis(time.Since(start))
	if f.JSON() {
		if err := output.WriteJSON(f.IO.Out, result, f.IO.IsStdoutTTY()); err != nil {
			return err
		}
	} else if human && total > 1 {
		fmt.Fprintf(f.IO.Out, "%s deleted in %s%s\n", describe(deletedItems(result.Deleted)), cmdutil.Elapsed(time.Since(start)),
			extraCounts(0, len(result.Failed))+missingText(len(result.Missing)))
	}
	if err := cmdutil.Interrupted(ctx, notStarted, "files"); err != nil {
		return err
	}
	return errs.Bulk("deletions", total, failures)
}

func missingText(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", %d not found", n)
}

// describe summarises what a plan deletes: "3 files" or "2 files and 1 directory".
func describe(items []files.RemoveItem) string {
	nFiles, nDirs := 0, 0
	for _, it := range items {
		if it.Folder {
			nDirs++
		} else {
			nFiles++
		}
	}
	s := plural(nFiles, "file")
	if nDirs > 0 {
		s += " and " + plural(nDirs, "directory")
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if strings.HasSuffix(word, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(word, "y"))
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// confirmRemoval asks before bulk deletions (FR-SAFE-3, FR-SAFE-4). Deleting
// one explicitly named file never asks (FR-SAFE-5).
func confirmRemoval(f *cmdutil.Factory, plan *files.RemovePlan, todo []files.RemoveItem, yes bool) error {
	folders := 0
	for _, it := range todo {
		if it.Folder {
			folders++
		}
	}
	if yes || (len(plan.WholeRepos) == 0 && folders == 0 && len(todo) <= 1) {
		return nil
	}
	what := describe(todo)
	if !f.IO.CanPrompt() {
		return errs.Usage("refusing to delete %s without confirmation", what).
			WithHint("pass --yes to delete without asking, or --dry-run to see what would be deleted")
	}
	fmt.Fprintf(f.IO.ErrOut, "This deletes %s:\n", what)
	for i, it := range todo {
		if i == 5 {
			fmt.Fprintf(f.IO.ErrOut, "  … and %d more\n", len(todo)-5)
			break
		}
		fmt.Fprintf(f.IO.ErrOut, "  %s\n", it.Ref())
	}
	var ok bool
	var err error
	if len(plan.WholeRepos) > 0 {
		name := plan.WholeRepos[0]
		ok, err = f.IO.ConfirmName(fmt.Sprintf("This deletes the entire content of repository %s.", name), name)
	} else {
		ok, err = f.IO.Confirm("Delete?")
	}
	if err != nil {
		return err
	}
	if !ok {
		return errs.Usage("nothing deleted: not confirmed")
	}
	return nil
}

func deletedItems(list []rmItemJSON) []files.RemoveItem {
	out := make([]files.RemoveItem, len(list))
	for i, j := range list {
		out[i].Folder = j.Type == "directory"
	}
	return out
}
