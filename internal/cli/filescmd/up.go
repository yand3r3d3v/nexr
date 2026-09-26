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

type upOptions struct {
	include, exclude []string
	followSymlinks   bool
	skipExisting     bool
	verify           bool
	dryRun           bool
	method           string
	contentType      string
	concurrency      int
}

func newUpCmd(f *cmdutil.Factory) *cobra.Command {
	opts := &upOptions{}
	cmd := &cobra.Command{
		Use:     "up SRC... REPO[/PATH]",
		Aliases: []string{"upload"},
		Short:   "Upload files and directories",
		Long: `Upload files and directory trees to a hosted raw repository.

  file a.txt      → REPO            uploads REPO/a.txt
  file a.txt      → REPO/dir/       uploads REPO/dir/a.txt
  file a.txt      → REPO/dir/b.txt  uploads REPO/dir/b.txt
  directory build → REPO/app/1.0    uploads the contents of build below
                                    REPO/app/1.0/, e.g. build/bin/x as
                                    REPO/app/1.0/bin/x
  -               → REPO/dir/f.bin  uploads stdin (not retried)

With several sources the target must be a directory (ending in "/"). Hidden
files are included; symbolic links are skipped unless --follow-symlinks is
given. --include and --exclude match the path relative to a source directory,
with the rules of .gitignore: '*.log' matches at any depth, 'build/*.map' only
there.

Existing files are overwritten if the repository allows redeployment and
rejected otherwise; --skip-existing skips them. Files are uploaded in parallel
and retried after network errors and 5xx responses.`,
		Example: `  nexr up ./dist raw-releases/myapp/1.4.0/
  nexr up app.tar.gz checksums.txt raw-releases/myapp/1.4.0/
  nexr up ./site raw-docs/ --exclude '*.map' --skip-existing
  tar -cz . | nexr up - raw-backups/2026-09-26.tar.gz
  nexr up ./dist raw-releases/myapp/1.4.0/ --dry-run`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return errs.Usage("nexr up needs at least one source and a target").WithHint("usage: %s", cmd.UseLine())
			}
			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveDefault // local files
			}
			out, d := completePath(f)(cmd, args, toComplete)
			return out, d | cobra.ShellCompDirectiveDefault
		},
		RunE: func(cmd *cobra.Command, args []string) error { return runUp(cmd, f, opts, args) },
	}
	fl := cmd.Flags()
	addPatternFlags(cmd, &opts.include, &opts.exclude)
	fl.BoolVar(&opts.followSymlinks, "follow-symlinks", false, "follow symbolic links in directories")
	fl.BoolVar(&opts.skipExisting, "skip-existing", false, "skip files that exist in the repository")
	fl.BoolVar(&opts.verify, "verify", false, "compare the SHA-1 of every uploaded file with the stored one")
	fl.BoolVar(&opts.dryRun, "dry-run", false, "show what would be uploaded, without uploading")
	fl.StringVar(&opts.method, "method", "", "upload with \"put\" (default) or the \"components\" API")
	fl.StringVar(&opts.contentType, "content-type", "", "content type of a single file (default: from the extension)")
	addConcurrencyFlag(cmd, &opts.concurrency)
	return cmd
}

type upItemJSON struct {
	Source     string     `json:"source"`
	Repository string     `json:"repository"`
	Path       string     `json:"path"`
	Size       *int64     `json:"size"`
	Reason     string     `json:"reason,omitempty"`
	Error      *errorJSON `json:"error,omitempty"`
}

type transferSummary struct {
	Files      int   `json:"files"`
	Bytes      int64 `json:"bytes"`
	Skipped    int   `json:"skipped"`
	Failed     int   `json:"failed"`
	DurationMS int64 `json:"duration_ms"`
}

type upJSON struct {
	DryRun   bool            `json:"dry_run"`
	Uploaded []upItemJSON    `json:"uploaded"`
	Skipped  []upItemJSON    `json:"skipped"`
	Failed   []upItemJSON    `json:"failed"`
	Summary  transferSummary `json:"summary"`
}

type upJob struct {
	item files.UploadItem
	n    int64
}

func runUp(cmd *cobra.Command, f *cmdutil.Factory, opts *upOptions, args []string) error {
	dest, err := parseTarget(args[len(args)-1])
	if err != nil {
		return err
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
	method := opts.method
	if method == "" {
		cfg, err := f.Config()
		if err != nil {
			return err
		}
		method = cfg.UploadMethod.Value
	}
	if method != "put" && method != "components" {
		return errs.Usage("invalid --method %q: use put or components", method)
	}
	svc, err := service(f)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	uopts := files.UploadOptions{
		Include: include, Exclude: exclude, FollowSymlinks: opts.followSymlinks,
		SkipExisting: opts.skipExisting, Verify: opts.verify, Method: method, ContentType: opts.contentType,
	}
	plan, err := svc.PlanUpload(ctx, args[:len(args)-1], dest, uopts)
	if err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		f.IO.Warnf("%s", w)
	}
	repo := plan.Repo.Name
	var total int64
	for _, it := range plan.Items {
		total += max(it.Size, 0)
	}
	result := upJSON{DryRun: opts.dryRun, Uploaded: []upItemJSON{}, Skipped: []upItemJSON{}, Failed: []upItemJSON{}}
	itemJSON := func(it files.UploadItem, n int64) upItemJSON {
		j := upItemJSON{Source: it.Source, Repository: repo, Path: it.Path}
		if n >= 0 {
			j.Size = &n
		}
		return j
	}
	human := !f.JSON() && !f.Flags.Quiet
	if len(plan.Items) == 0 {
		f.IO.Warnf("no files to upload")
	}

	if opts.dryRun {
		for _, it := range plan.Items {
			result.Uploaded = append(result.Uploaded, itemJSON(it, it.Size))
			switch {
			case f.JSON():
			case f.Flags.Quiet:
				fmt.Fprintln(f.IO.Out, repo+"/"+it.Path)
			default:
				fmt.Fprintf(f.IO.Out, "would upload  %s → %s/%s  (%s)\n", it.Source, repo, it.Path, sizeText(it.Size))
			}
		}
		result.Summary = transferSummary{Files: len(plan.Items), Bytes: total}
		if f.JSON() {
			return output.WriteJSON(f.IO.Out, result, f.IO.IsStdoutTTY())
		}
		if human {
			fmt.Fprintf(f.IO.Out, "%s, %s would be uploaded (dry run)\n", count(len(plan.Items), "file"), output.HumanBytes(total))
		}
		return nil
	}

	var progress *output.Progress
	if human {
		progress = f.IO.NewProgress("uploading", len(plan.Items), total)
		uopts.OnBytes = progress.Add
	}
	jobs := make([]*upJob, len(plan.Items))
	for i, it := range plan.Items {
		jobs[i] = &upJob{item: it}
	}
	start := time.Now()
	var failures []error
	var sent int64
	notStarted := workpool.Run(ctx, conc, jobs, func(ctx context.Context, j *upJob) error {
		n, err := svc.Upload(ctx, plan, j.item, uopts, f.IO.In)
		j.n = n
		return err
	}, func(j *upJob, err error) {
		progress.FileDone()
		progress.Clear()
		ref := repo + "/" + j.item.Path
		switch {
		case err == nil:
			sent += j.n
			result.Uploaded = append(result.Uploaded, itemJSON(j.item, j.n))
			if human {
				fmt.Fprintf(f.IO.Out, "uploaded  %s  (%s)\n", ref, sizeText(j.n))
			}
		case files.IsSkip(err):
			var skip *files.SkipError
			errors.As(err, &skip)
			sj := itemJSON(j.item, j.item.Size)
			sj.Reason = skip.Reason
			result.Skipped = append(result.Skipped, sj)
			if human {
				fmt.Fprintf(f.IO.Out, "skipped   %s  (%s)\n", ref, skip.Reason)
			}
		default:
			failures = append(failures, err)
			fj := itemJSON(j.item, j.item.Size)
			e := itemError(err)
			fj.Error = &e
			result.Failed = append(result.Failed, fj)
			if len(plan.Items) > 1 && !f.JSON() {
				fmt.Fprintf(f.IO.ErrOut, "failed    %s: %v\n", ref, err)
			}
		}
	})
	progress.Stop()
	took := time.Since(start)
	result.Summary = transferSummary{
		Files: len(result.Uploaded), Bytes: sent, Skipped: len(result.Skipped), Failed: len(result.Failed), DurationMS: ms(took),
	}
	if f.JSON() {
		if err := output.WriteJSON(f.IO.Out, result, f.IO.IsStdoutTTY()); err != nil {
			return err
		}
	} else if human && len(plan.Items) > 1 {
		fmt.Fprintf(f.IO.Out, "%s, %s uploaded in %s%s\n", count(len(result.Uploaded), "file"), output.HumanBytes(sent),
			elapsed(took), extraCounts(len(result.Skipped), len(result.Failed)))
	}
	if err := interrupted(ctx, notStarted); err != nil {
		return err
	}
	return files.BulkError("uploads", len(plan.Items), failures)
}

func sizeText(n int64) string {
	if n < 0 {
		return "size unknown"
	}
	return output.HumanBytes(n)
}

func extraCounts(skipped, failed int) string {
	s := ""
	if skipped > 0 {
		s += fmt.Sprintf(", %d skipped", skipped)
	}
	if failed > 0 {
		s += fmt.Sprintf(", %d failed", failed)
	}
	return s
}
