package filescmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/files"
	"github.com/yand3r3d3v/nexr/internal/output"
	"github.com/yand3r3d3v/nexr/internal/workpool"
)

type downOptions struct {
	include, exclude []string
	skipExisting     bool
	noVerify         bool
	dryRun           bool
	concurrency      int
}

func newDownCmd(f *cmdutil.Factory) *cobra.Command {
	opts := &downOptions{}
	cmd := &cobra.Command{
		Use:     "down REPO/PATH [DEST]",
		Aliases: []string{"download", "get"},
		Short:   "Download files and directories",
		Long: `Download one file or a directory tree. Any format that stores files under
paths works.

A path without a trailing slash that names a file downloads that file, to
./NAME, into DEST when it is a directory or ends with a separator, to the file
DEST otherwise, or to stdout with DEST "-". Otherwise the path is a directory:
the files below it are downloaded into DEST (default: the current directory),
keeping their structure, so REPO/PATH/sub/x becomes DEST/sub/x.

Every file is checked against its SHA-256 or SHA-1 from Nexus, written to a
temporary file first and renamed into place only when it is complete, so an
interrupted download never leaves a truncated file. Remote names that would
leave DEST or are not valid on this system are rejected.

` + indexLagNote,
		Example: `  nexr down raw-releases/myapp/1.4.0/myapp.tar.gz
  nexr down raw-releases/myapp/1.4.0/ ./release
  nexr down raw-releases/config.json - | jq .
  nexr down raw-releases/myapp/ ./mirror --exclude '*.tmp' --skip-existing`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 || len(args) > 2 {
				return errs.Usage("nexr down needs REPO/PATH and optionally DEST").WithHint("usage: %s", cmd.UseLine())
			}
			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return completePath(f)(cmd, args, toComplete)
			}
			return nil, cobra.ShellCompDirectiveDefault
		},
		RunE: func(cmd *cobra.Command, args []string) error { return runDown(cmd, f, opts, args) },
	}
	fl := cmd.Flags()
	addPatternFlags(cmd, &opts.include, &opts.exclude)
	fl.BoolVar(&opts.skipExisting, "skip-existing", false, "skip files that exist locally")
	fl.BoolVar(&opts.noVerify, "no-verify", false, "do not check checksums")
	fl.BoolVar(&opts.dryRun, "dry-run", false, "show what would be downloaded, without downloading")
	addConcurrencyFlag(cmd, &opts.concurrency)
	return cmd
}

type downItemJSON struct {
	Repository  string     `json:"repository"`
	Path        string     `json:"path"`
	Destination string     `json:"destination"`
	Size        *int64     `json:"size"`
	Reason      string     `json:"reason,omitempty"`
	Error       *errorJSON `json:"error,omitempty"`
}

type downJSON struct {
	DryRun     bool            `json:"dry_run"`
	Downloaded []downItemJSON  `json:"downloaded"`
	Skipped    []downItemJSON  `json:"skipped"`
	Failed     []downItemJSON  `json:"failed"`
	Summary    transferSummary `json:"summary"`
}

type downJob struct {
	item files.DownloadItem
	n    int64
}

func runDown(cmd *cobra.Command, f *cmdutil.Factory, opts *downOptions, args []string) error {
	src, err := parseTarget(args[0])
	if err != nil {
		return err
	}
	dest := ""
	if len(args) == 2 {
		dest = args[1]
	}
	if dest == "-" && f.JSON() {
		return errs.Usage("--json cannot be combined with downloading to stdout")
	}
	include, err := parsePatterns("--include", opts.include)
	if err != nil {
		return err
	}
	exclude, err := parsePatterns("--exclude", opts.exclude)
	if err != nil {
		return err
	}
	conc, err := concurrency(cmd, f, opts.concurrency)
	if err != nil {
		return err
	}
	svc, err := service(f)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	dopts := files.DownloadOptions{Include: include, Exclude: exclude, SkipExisting: opts.skipExisting, NoVerify: opts.noVerify}
	plan, err := svc.PlanDownload(ctx, src, dest, dopts)
	if err != nil {
		return err
	}
	repo := plan.Repo.Name
	if plan.ToStdout && !opts.dryRun {
		_, err := svc.Download(ctx, plan, plan.Items[0], dopts, f.IO.Out)
		return err
	}
	var total int64
	for _, it := range plan.Items {
		total += max(it.Size, 0)
	}
	result := downJSON{DryRun: opts.dryRun, Downloaded: []downItemJSON{}, Skipped: []downItemJSON{}, Failed: []downItemJSON{}}
	itemJSON := func(it files.DownloadItem, n int64) downItemJSON {
		j := downItemJSON{Repository: repo, Path: it.Path, Destination: it.Local}
		if plan.ToStdout {
			j.Destination = "-"
		}
		if n >= 0 {
			j.Size = &n
		}
		return j
	}
	human := !f.JSON() && !f.Flags.Quiet
	if len(plan.Items) == 0 {
		f.IO.Warnf("no files to download")
	}

	if opts.dryRun {
		for _, it := range plan.Items {
			result.Downloaded = append(result.Downloaded, itemJSON(it, it.Size))
			switch {
			case f.JSON():
			case f.Flags.Quiet:
				fmt.Fprintln(f.IO.Out, itemJSON(it, 0).Destination)
			case it.Err != nil:
				fmt.Fprintf(f.IO.Out, "would skip      %s/%s: %v\n", repo, it.Path, it.Err)
			default:
				fmt.Fprintf(f.IO.Out, "would download  %s/%s → %s  (%s)\n", repo, it.Path, itemJSON(it, 0).Destination, sizeText(it.Size))
			}
		}
		result.Summary = transferSummary{Files: len(plan.Items), Bytes: total}
		if f.JSON() {
			return output.WriteJSON(f.IO.Out, result, f.IO.IsStdoutTTY())
		}
		if human {
			fmt.Fprintf(f.IO.Out, "%s, %s would be downloaded (dry run)\n", count(len(plan.Items), "file"), output.HumanBytes(total))
		}
		return nil
	}

	var progress *output.Progress
	if human {
		progress = f.IO.NewProgress("downloading", len(plan.Items), total)
		dopts.OnBytes = progress.Add
	}
	jobs := make([]*downJob, len(plan.Items))
	for i, it := range plan.Items {
		jobs[i] = &downJob{item: it}
	}
	start := time.Now()
	var failures []error
	var received int64
	notStarted := workpool.Run(ctx, conc, jobs, func(ctx context.Context, j *downJob) error {
		n, err := svc.Download(ctx, plan, j.item, dopts, nil)
		j.n = n
		return err
	}, func(j *downJob, err error) {
		progress.FileDone()
		progress.Clear()
		switch {
		case err == nil:
			received += j.n
			result.Downloaded = append(result.Downloaded, itemJSON(j.item, j.n))
			if human {
				fmt.Fprintf(f.IO.Out, "downloaded  %s  (%s)\n", j.item.Local, sizeText(j.n))
			}
		case files.IsSkip(err):
			var skip *files.SkipError
			errors.As(err, &skip)
			sj := itemJSON(j.item, j.item.Size)
			sj.Reason = skip.Reason
			result.Skipped = append(result.Skipped, sj)
			if human {
				fmt.Fprintf(f.IO.Out, "skipped     %s  (%s)\n", j.item.Local, skip.Reason)
			}
		default:
			failures = append(failures, err)
			fj := itemJSON(j.item, j.item.Size)
			e := itemError(err)
			fj.Error = &e
			result.Failed = append(result.Failed, fj)
			if len(plan.Items) > 1 && !f.JSON() {
				fmt.Fprintf(f.IO.ErrOut, "failed      %s/%s: %v\n", repo, j.item.Path, err)
			}
		}
	})
	progress.Stop()
	took := time.Since(start)
	result.Summary = transferSummary{
		Files: len(result.Downloaded), Bytes: received, Skipped: len(result.Skipped), Failed: len(result.Failed), DurationMS: ms(took),
	}
	if f.JSON() {
		if err := output.WriteJSON(f.IO.Out, result, f.IO.IsStdoutTTY()); err != nil {
			return err
		}
	} else if human && len(plan.Items) > 1 {
		fmt.Fprintf(f.IO.Out, "%s, %s downloaded in %s%s\n", count(len(result.Downloaded), "file"), output.HumanBytes(received),
			elapsed(took), extraCounts(len(result.Skipped), len(result.Failed)))
	}
	if err := interrupted(ctx, notStarted); err != nil {
		return err
	}
	return files.BulkError("downloads", len(plan.Items), failures)
}
