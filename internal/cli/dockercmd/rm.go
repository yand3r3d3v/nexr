package dockercmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/config"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/images"
	"github.com/yand3r3d3v/nexr/internal/output"
	"github.com/yand3r3d3v/nexr/internal/remote"
	"github.com/yand3r3d3v/nexr/internal/retention"
	"github.com/yand3r3d3v/nexr/internal/workpool"
)

type rmOptions struct {
	keep          int
	olderThan     string
	all           bool
	match         []string
	exclude       []string
	sort          string
	ignoreMissing bool
	dryRun        bool
	yes           bool
	concurrency   int
}

func newRmCmd(f *cmdutil.Factory, rf *repoFlags) *cobra.Command {
	opts := &rmOptions{}
	cmd := &cobra.Command{
		Use:     "rm IMAGE:TAG... | rm IMAGE... (--keep N | --older-than AGE | --all)",
		Aliases: []string{"delete"},
		Short:   "Delete image tags",
		Long: `Delete tags of images, one by one or with a retention policy.

With IMAGE:TAG arguments, exactly these tags are deleted. Deleting more than one
tag asks for confirmation on a terminal; without a terminal, --yes is required.

With IMAGE and a retention policy, nexr decides for every tag of the image:
  1. the candidates are the tags that match --match (all tags without it);
  2. candidates that match --exclude or the "docker.exclude" setting (default:
     latest) are protected: never deleted, and not counted by --keep;
  3. the other candidates are ordered by --sort: pushed (the default, newest
     push first), semver (highest version first; tags that are not versions
     are never deleted) or name (last by name first, e.g. for date stamps);
  4. --keep N keeps the first N of them, --older-than AGE keeps those pushed
     less than AGE ago (e.g. 30d, 2w, 36h), and --all selects all of them;
  5. every other candidate is deleted.
The plan is shown before the confirmation, which is always asked for (or
--yes); --dry-run shows the plan without deleting. IMAGE may be a glob or
re:REGEX to apply the policy to every matching image.

Deleting a tag removes only this tag: other tags of the same manifest and the
manifests of multi-platform images stay. The storage is reclaimed when the
Nexus tasks "Docker - Delete unused manifests and images" and "Admin - Compact
blob store" run.

` + indexLagNote,
		Example: `  nexr docker rm team/app:1.0 team/app:1.1
  nexr docker rm team/app --keep 5 --dry-run
  nexr docker rm team/app --older-than 30d --match 'feature-*' --yes
  nexr docker rm team/app --keep 10 --sort semver
  nexr docker rm 'team/*' --keep 3 --yes`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errs.Usage("missing argument IMAGE:TAG or IMAGE").WithHint("usage: nexr docker %s", cmd.Use)
			}
			return nil
		},
		ValidArgsFunction: completeImage(f, rf),
		RunE:              func(cmd *cobra.Command, args []string) error { return runRm(cmd, f, rf, opts, args) },
	}
	fl := cmd.Flags()
	fl.IntVar(&opts.keep, "keep", 0, "keep the N newest tags (retention)")
	fl.StringVar(&opts.olderThan, "older-than", "", "keep tags pushed less than AGE ago, e.g. 30d (retention)")
	fl.BoolVar(&opts.all, "all", false, "delete every candidate tag (retention)")
	fl.StringArrayVar(&opts.match, "match", nil, "only consider tags matching this glob or re:REGEX (retention, repeatable)")
	fl.StringArrayVar(&opts.exclude, "exclude", nil, "protect tags matching this glob or re:REGEX, besides docker.exclude (retention, repeatable)")
	fl.StringVar(&opts.sort, "sort", "pushed", "which tags count as newest: pushed, semver or name (retention)")
	fl.BoolVar(&opts.ignoreMissing, "ignore-missing", false, "succeed when a tag does not exist")
	fl.BoolVar(&opts.dryRun, "dry-run", false, "show what would be deleted, without deleting")
	fl.BoolVarP(&opts.yes, "yes", "y", false, "do not ask for confirmation")
	cmdutil.AddConcurrencyFlag(cmd, &opts.concurrency, "deletions")
	_ = cmd.RegisterFlagCompletionFunc("sort", cobra.FixedCompletions([]string{"pushed", "semver", "name"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// target is a tag to delete.
type target struct {
	svc *images.Service
	tag images.Tag
}

func (t target) ref() string { return t.tag.Repository + "/" + t.tag.Ref() }

type tagRefJSON struct {
	Repository string             `json:"repository"`
	Image      string             `json:"image"`
	Tag        string             `json:"tag"`
	Digest     *string            `json:"digest"`
	Reason     string             `json:"reason,omitempty"`
	Error      *cmdutil.ErrorJSON `json:"error,omitempty"`
}

func refJSON(t images.Tag) tagRefJSON {
	return tagRefJSON{Repository: t.Repository, Image: t.Image, Tag: t.Name, Digest: cmdutil.NullString(t.Digest)}
}

type decisionJSON struct {
	Tag    string  `json:"tag"`
	Digest *string `json:"digest"`
	Pushed *string `json:"pushed"`
	Action string  `json:"action"`
	Reason string  `json:"reason"`
}

type planJSON struct {
	Repository string         `json:"repository"`
	Image      string         `json:"image"`
	Decisions  []decisionJSON `json:"decisions"`
}

type rmJSON struct {
	DryRun  bool         `json:"dry_run"`
	Images  []planJSON   `json:"images"`
	Deleted []tagRefJSON `json:"deleted"`
	Missing []tagRefJSON `json:"missing"`
	Skipped []tagRefJSON `json:"skipped"`
	Failed  []tagRefJSON `json:"failed"`
	Summary struct {
		Deleted    int   `json:"deleted"`
		Kept       int   `json:"kept"`
		Skipped    int   `json:"skipped"`
		Missing    int   `json:"missing"`
		Failed     int   `json:"failed"`
		DurationMS int64 `json:"duration_ms"`
	} `json:"summary"`
}

func newResult(dryRun bool) *rmJSON {
	return &rmJSON{DryRun: dryRun, Images: []planJSON{}, Deleted: []tagRefJSON{}, Missing: []tagRefJSON{},
		Skipped: []tagRefJSON{}, Failed: []tagRefJSON{}}
}

func runRm(cmd *cobra.Command, f *cmdutil.Factory, rf *repoFlags, opts *rmOptions, args []string) error {
	conc, err := cmdutil.Concurrency(cmd, f, opts.concurrency)
	if err != nil {
		return err
	}
	fl := cmd.Flags()
	if fl.Changed("keep") || fl.Changed("older-than") || opts.all {
		return runRetention(cmd, f, rf, opts, args, conc)
	}
	for _, name := range []string{"match", "exclude", "sort"} {
		if fl.Changed(name) {
			return errs.Usage("--%s needs a retention policy: --keep, --older-than or --all", name)
		}
	}
	return runExplicit(cmd.Context(), f, rf, opts, args, conc)
}

// services opens each repository once.
type services struct {
	f    *cmdutil.Factory
	rf   *repoFlags
	byID map[string]*images.Service
}

func (s *services) get(ctx context.Context, repo string) (*images.Service, error) {
	if svc, ok := s.byID[repo]; ok {
		return svc, nil
	}
	svc, err := open(ctx, s.f, s.rf, repo)
	if err != nil {
		return nil, err
	}
	s.byID[repo] = svc
	return svc, nil
}

// runExplicit deletes the tags named on the command line (FR-DRM-1).
func runExplicit(ctx context.Context, f *cmdutil.Factory, rf *repoFlags, opts *rmOptions, args []string, conc int) error {
	svcs := &services{f: f, rf: rf, byID: map[string]*images.Service{}}
	result := newResult(opts.dryRun)
	human := !f.JSON() && !f.Flags.Quiet
	var todo []target
	var failures []error
	seen := map[string]bool{}
	for _, arg := range args {
		ref, err := parseRef(arg)
		if err != nil {
			return err
		}
		if ref.Tag == "" {
			return errs.Usage("%s has no tag", arg).
				WithHint("name the tags to delete (IMAGE:TAG), or give a retention policy: --keep, --older-than or --all")
		}
		repo, err := resolveRepo(ctx, f, rf, ref.Host)
		if err != nil {
			return err
		}
		key := repo + "/" + ref.Name + ":" + ref.Tag
		if seen[key] {
			continue
		}
		seen[key] = true
		svc, err := svcs.get(ctx, repo)
		if err != nil {
			return err
		}
		tag, ok, err := svc.FindTag(ctx, ref.Name, ref.Tag)
		if err != nil {
			return err
		}
		if ok {
			todo = append(todo, target{svc: svc, tag: tag})
			continue
		}
		missingTag := images.Tag{Repository: repo, Image: ref.Name, Name: ref.Tag}
		if opts.ignoreMissing {
			result.Missing = append(result.Missing, refJSON(missingTag))
			if human {
				f.IO.Warnf("%s/%s not found; ignored", repo, missingTag.Ref())
			}
			continue
		}
		err = errs.NotFound("%s/%s not found", repo, missingTag.Ref())
		failures = append(failures, err)
		j := refJSON(missingTag)
		e := cmdutil.ItemError(err)
		j.Error = &e
		result.Failed = append(result.Failed, j)
		if !f.JSON() && (len(args) > 1 || opts.dryRun) {
			fmt.Fprintf(f.IO.ErrOut, "failed    %s/%s: not found\n", repo, missingTag.Ref())
		}
	}
	total := len(todo) + len(failures)

	if opts.dryRun {
		for _, t := range todo {
			result.Deleted = append(result.Deleted, refJSON(t.tag))
			switch {
			case f.JSON():
			case f.Flags.Quiet:
				fmt.Fprintln(f.IO.Out, t.ref())
			default:
				fmt.Fprintf(f.IO.Out, "would delete  %s\n", t.ref())
			}
		}
		if human {
			fmt.Fprintf(f.IO.Out, "%s would be deleted (dry run)\n", cmdutil.Count(len(todo), "tag"))
		}
		// A dry run changes nothing and succeeds, but says what is missing (FR-SAFE-1).
		return finish(f, result, time.Time{}, nil)
	}
	if len(todo) > 1 && !opts.yes {
		if err := confirm(f, todo, fmt.Sprintf("This deletes %s:", cmdutil.Count(len(todo), "tag"))); err != nil {
			return err
		}
	}
	start := time.Now()
	notStarted := deleteAll(ctx, f, todo, conc, false, opts.ignoreMissing, total > 1, result, &failures)
	if human && total > 1 {
		fmt.Fprintf(f.IO.Out, "%s deleted in %s%s\n", cmdutil.Count(len(result.Deleted), "tag"),
			cmdutil.Elapsed(time.Since(start)), countsText(len(result.Failed), len(result.Missing), len(result.Skipped)))
	}
	gcHint(f, result)
	if err := cmdutil.Interrupted(ctx, notStarted, "tags"); err != nil {
		_ = finish(f, result, start, nil)
		return err
	}
	return finish(f, result, start, errs.Bulk("deletions", total, failures))
}

// imagePlan is the retention plan of one image.
type imagePlan struct {
	svc       *images.Service
	image     string
	tags      map[string]images.Tag
	decisions []retention.Decision
}

// runRetention applies a retention policy to images (FR-DRM-2 to FR-DRM-7).
func runRetention(cmd *cobra.Command, f *cmdutil.Factory, rf *repoFlags, opts *rmOptions, args []string, conc int) error {
	ctx := cmd.Context()
	policy, err := buildPolicy(cmd, f, opts)
	if err != nil {
		return err
	}
	svcs := &services{f: f, rf: rf, byID: map[string]*images.Service{}}
	var plans []imagePlan
	planned := map[string]bool{}
	for _, arg := range args {
		ps, err := planArg(ctx, f, rf, svcs, arg, policy)
		if err != nil {
			return err
		}
		for _, p := range ps {
			if key := p.svc.Repository().Name + "/" + p.image; !planned[key] {
				planned[key] = true
				plans = append(plans, p)
			}
		}
	}

	result := newResult(opts.dryRun)
	human := !f.JSON() && !f.Flags.Quiet
	var todo []target
	for i, p := range plans {
		pj := planJSON{Repository: p.svc.Repository().Name, Image: p.image, Decisions: []decisionJSON{}}
		var tbl *output.Table
		if human {
			tbl = output.NewTable(f.IO.Out, "TAG", "PUSHED", "ACTION", "REASON")
		}
		deletions := 0
		for _, d := range p.decisions {
			t := p.tags[d.Tag.Name]
			pj.Decisions = append(pj.Decisions, decisionJSON{Tag: t.Name, Digest: cmdutil.NullString(t.Digest),
				Pushed: cmdutil.NullTime(t.Pushed), Action: string(d.Action), Reason: d.Reason})
			switch d.Action {
			case retention.Delete:
				deletions++
				todo = append(todo, target{svc: p.svc, tag: t})
			case retention.Keep:
				result.Summary.Kept++
			case retention.Skip:
				result.Summary.Skipped++
			}
			if tbl != nil {
				tbl.AddRow(t.Name, output.HumanTime(t.Pushed), string(d.Action), d.Reason)
			}
		}
		result.Images = append(result.Images, pj)
		if tbl != nil {
			if i > 0 {
				fmt.Fprintln(f.IO.Out)
			}
			if err := tbl.Render(); err != nil {
				return err
			}
			where := p.svc.Repository().Name + "/" + p.image
			if opts.dryRun {
				fmt.Fprintf(f.IO.Out, "dry run: %d of %s would be deleted from %s\n", deletions, cmdutil.Count(len(p.decisions), "tag"), where)
			} else {
				fmt.Fprintf(f.IO.Out, "%d of %s to delete from %s\n", deletions, cmdutil.Count(len(p.decisions), "tag"), where)
			}
		}
	}

	if opts.dryRun || len(todo) == 0 {
		for _, t := range todo {
			result.Deleted = append(result.Deleted, refJSON(t.tag))
			if f.Flags.Quiet && !f.JSON() {
				fmt.Fprintln(f.IO.Out, t.ref())
			}
		}
		if human && len(todo) == 0 {
			fmt.Fprintln(f.IO.Out, "nothing to delete")
		}
		return finish(f, result, time.Time{}, nil)
	}
	if !opts.yes {
		if err := confirm(f, todo, ""); err != nil {
			return err
		}
	}
	start := time.Now()
	var failures []error
	notStarted := deleteAll(ctx, f, todo, conc, true, true, true, result, &failures)
	if human {
		fmt.Fprintf(f.IO.Out, "%s deleted in %s%s\n", cmdutil.Count(len(result.Deleted), "tag"),
			cmdutil.Elapsed(time.Since(start)), countsText(len(result.Failed), len(result.Missing), len(result.Skipped)))
	}
	gcHint(f, result)
	if err := cmdutil.Interrupted(ctx, notStarted, "tags"); err != nil {
		_ = finish(f, result, start, nil)
		return err
	}
	return finish(f, result, start, errs.Bulk("deletions", len(todo), failures))
}

// buildPolicy reads the retention flags and the docker.exclude setting.
func buildPolicy(cmd *cobra.Command, f *cmdutil.Factory, opts *rmOptions) (retention.Policy, error) {
	p := retention.Policy{Keep: opts.keep, All: opts.all, Now: time.Now()}
	switch {
	case cmd.Flags().Changed("keep") && opts.keep < 1:
		return p, errs.Usage("invalid --keep %d: keep at least 1 tag", opts.keep).WithHint("use --all to delete every tag")
	case opts.keep > 0 && opts.all:
		return p, errs.Usage("--keep and --all cannot be used together")
	}
	if opts.olderThan != "" {
		d, err := config.ParseDuration(opts.olderThan)
		if err != nil || d <= 0 {
			return p, errs.Usage("invalid --older-than %q: use a duration such as 30d, 2w or 36h", opts.olderThan)
		}
		p.OlderThan = d
	}
	var err error
	if p.Sort, err = retention.ParseSort(opts.sort); err != nil {
		return p, errs.Wrap(errs.KindUsage, err, "")
	}
	if p.Match, err = cmdutil.ParsePatterns("--match", opts.match); err != nil {
		return p, err
	}
	if p.Exclude, err = cmdutil.ParsePatterns("--exclude", opts.exclude); err != nil {
		return p, err
	}
	cfg, err := f.Config()
	if err != nil {
		return p, err
	}
	defaults, err := remote.ParsePatterns(cfg.DockerExclude.Value)
	if err != nil {
		return p, errs.Wrap(errs.KindConfig, err, "invalid docker.exclude (from %s)", cfg.DockerExclude.Origin)
	}
	p.Exclude = append(p.Exclude, defaults...)
	return p, nil
}

// planArg plans the images of one argument: an image, or a pattern of images.
func planArg(ctx context.Context, f *cmdutil.Factory, rf *repoFlags, svcs *services, arg string, policy retention.Policy) ([]imagePlan, error) {
	var repo string
	var names []string
	pattern := remote.IsPattern(arg)
	if pattern {
		p, err := cmdutil.ParsePatterns("IMAGE", []string{arg})
		if err != nil {
			return nil, err
		}
		if repo, err = resolveRepo(ctx, f, rf, ""); err != nil {
			return nil, err
		}
		svc, err := svcs.get(ctx, repo)
		if err != nil {
			return nil, err
		}
		all, err := svc.Images(ctx)
		if err != nil {
			return nil, err
		}
		for _, n := range all {
			if p[0].Match(n) {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			f.IO.Warnf("no image in %s matches %s", repo, arg)
		}
	} else {
		ref, err := parseRef(arg)
		if err != nil {
			return nil, err
		}
		if ref.Tag != "" {
			return nil, errs.Usage("%s names a tag, but a retention policy applies to whole images", arg).
				WithHint("give the image without a tag, or leave out --keep, --older-than and --all to delete this tag")
		}
		if repo, err = resolveRepo(ctx, f, rf, ref.Host); err != nil {
			return nil, err
		}
		names = []string{ref.Name}
	}
	svc, err := svcs.get(ctx, repo)
	if err != nil {
		return nil, err
	}
	var plans []imagePlan
	for _, name := range names {
		tags, err := svc.Tags(ctx, name)
		if err != nil {
			// An image of the catalog can have no tags left until the
			// Docker cleanup task runs.
			if pattern && errs.Classify(err) == errs.KindNotFound {
				continue
			}
			return nil, err
		}
		p := imagePlan{svc: svc, image: name, tags: map[string]images.Tag{}}
		rts := make([]retention.Tag, len(tags))
		for i, t := range tags {
			p.tags[t.Name] = t
			rts[i] = retentionTag(t)
		}
		if p.decisions, err = retention.Plan(rts, policy); err != nil {
			return nil, errs.Wrap(errs.KindUsage, err, "")
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// confirm asks before deleting tags (FR-SAFE-3); without a terminal it
// requires --yes.
func confirm(f *cmdutil.Factory, todo []target, heading string) error {
	what := cmdutil.Count(len(todo), "tag")
	if !f.IO.CanPrompt() {
		return errs.Usage("refusing to delete %s without confirmation", what).
			WithHint("pass --yes to delete without asking, or --dry-run to see what would be deleted")
	}
	if heading != "" {
		fmt.Fprintln(f.IO.ErrOut, heading)
		for i, t := range todo {
			if i == 5 {
				fmt.Fprintf(f.IO.ErrOut, "  … and %d more\n", len(todo)-5)
				break
			}
			fmt.Fprintf(f.IO.ErrOut, "  %s\n", t.ref())
		}
	}
	ok, err := f.IO.Confirm(fmt.Sprintf("Delete %s?", what))
	if err != nil {
		return err
	}
	if !ok {
		return errs.Usage("nothing deleted: not confirmed")
	}
	return nil
}

// deleteAll deletes the targets in parallel and records the outcomes. A tag
// that is already gone counts as missing when ignoreMissing is set, and a tag
// that was pushed again since the listing (verify) is skipped.
func deleteAll(ctx context.Context, f *cmdutil.Factory, todo []target, conc int, verify, ignoreMissing, bulk bool,
	result *rmJSON, failures *[]error) (notStarted int) {
	human := !f.JSON() && !f.Flags.Quiet
	var mu sync.Mutex
	return workpool.Run(ctx, conc, todo, func(ctx context.Context, t target) error {
		return t.svc.DeleteTag(ctx, t.tag, verify)
	}, func(t target, err error) {
		mu.Lock()
		defer mu.Unlock()
		j := refJSON(t.tag)
		switch {
		case err == nil:
			result.Deleted = append(result.Deleted, j)
			switch {
			case human:
				fmt.Fprintf(f.IO.Out, "deleted  %s\n", t.ref())
			case f.Flags.Quiet && !f.JSON():
				fmt.Fprintln(f.IO.Out, t.ref())
			}
		case images.IsMissing(err) && ignoreMissing:
			result.Missing = append(result.Missing, j)
			if human {
				f.IO.Warnf("%s was already deleted", t.ref())
			}
		case errors.Is(err, images.ErrChanged):
			j.Reason = err.Error()
			result.Skipped = append(result.Skipped, j)
			if !f.JSON() {
				f.IO.Warnf("skipped %s: %v", t.ref(), err)
			}
		default:
			*failures = append(*failures, err)
			e := cmdutil.ItemError(err)
			j.Error = &e
			result.Failed = append(result.Failed, j)
			if bulk && !f.JSON() {
				fmt.Fprintf(f.IO.ErrOut, "failed    %s: %v\n", t.ref(), err)
			}
		}
	})
}

func countsText(failed, missing, skipped int) string {
	var parts []string
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d already gone", missing))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

// gcHint reminds that deleting tags does not free storage by itself
// (FR-DRM-5).
func gcHint(f *cmdutil.Factory, result *rmJSON) {
	if len(result.Deleted) > 0 && !f.JSON() && !f.Flags.Quiet {
		fmt.Fprintln(f.IO.ErrOut, `hint: storage is reclaimed when the Nexus tasks "Docker - Delete unused manifests and images" and "Admin - Compact blob store" run`)
	}
}

// finish writes the JSON result and returns err.
func finish(f *cmdutil.Factory, result *rmJSON, start time.Time, err error) error {
	s := &result.Summary
	s.Deleted, s.Missing, s.Failed = len(result.Deleted), len(result.Missing), len(result.Failed)
	s.Skipped += len(result.Skipped)
	if !start.IsZero() {
		s.DurationMS = cmdutil.Millis(time.Since(start))
	}
	if f.JSON() {
		if werr := output.WriteJSON(f.IO.Out, result, f.IO.IsStdoutTTY()); werr != nil {
			return werr
		}
	}
	return err
}
