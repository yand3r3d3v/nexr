package filescmd

import (
	"context"
	"fmt"

	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/files"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/output"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

type lsOptions struct {
	recursive bool
	long      bool
	match     string
	sortBy    string
	reverse   bool
}

func newLsCmd(f *cmdutil.Factory) *cobra.Command {
	opts := &lsOptions{}
	cmd := &cobra.Command{
		Use:     "ls REPO[/PATH]",
		Aliases: []string{"list"},
		Short:   "List files and directories",
		Long: `List the files and directories of a repository.

REPO or REPO/ lists the root of the repository and REPO/DIR/ the directory DIR.
Without a trailing slash, REPO/PATH lists the directory PATH if there is one,
shows the file PATH if there is one, and otherwise lists the entries of the
parent directory whose names start with the last segment, so
"nexr ls raw/app/1." shows 1.0/ and 1.1/.

With -r every file below the directory is listed, with its path relative to the
listed directory. -l adds the size and the time of the last change; --json
always includes all metadata. -q prints REPO/PATH references for other nexr
commands.

` + indexLagNote,
		Example: `  nexr ls raw-releases
  nexr ls -l raw-releases/myapp/
  nexr ls -r raw-releases/myapp/1.4.0/ --match '*.tar.gz'
  nexr ls -r -q raw-releases/tmp/ | xargs nexr rm`,
		Args:              cmdutil.ExactArgs("path"),
		ValidArgsFunction: completePath(f),
		RunE:              func(cmd *cobra.Command, args []string) error { return runLs(cmd, f, opts, args[0]) },
	}
	fl := cmd.Flags()
	fl.BoolVarP(&opts.recursive, "recursive", "r", false, "list every file below the directory")
	fl.BoolVarP(&opts.long, "long", "l", false, "show sizes and times")
	fl.StringVar(&opts.match, "match", "", "only entries matching this glob or re:REGEX")
	fl.StringVar(&opts.sortBy, "sort", "", "sort by name, size or time (buffers the whole listing)")
	fl.BoolVar(&opts.reverse, "reverse", false, "reverse the order")
	return cmd
}

func runLs(cmd *cobra.Command, f *cmdutil.Factory, opts *lsOptions, arg string) error {
	target, err := parseTarget(arg)
	if err != nil {
		return err
	}
	switch opts.sortBy {
	case "", "name", "size", "time":
	default:
		return errs.Usage("invalid --sort %q: use name, size or time", opts.sortBy)
	}
	var match *remote.Pattern
	if opts.match != "" {
		p, err := remote.ParsePattern(opts.match)
		if err != nil {
			return errs.Wrap(errs.KindUsage, err, "invalid --match")
		}
		match = &p
	}
	svc, err := service(f)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	repo, err := svc.Repository(ctx, target.Repo)
	if err != nil {
		return err
	}
	p := &lsPrinter{f: f, opts: opts, match: match, buffer: opts.sortBy != "" || !opts.recursive}
	p.meta = opts.long || f.JSON() || opts.sortBy == "size" || opts.sortBy == "time"

	isDir := target.Dir
	if !isDir {
		if isDir, err = svc.DirExists(ctx, repo, target.Path); err != nil {
			return err
		}
	}
	if isDir {
		if opts.recursive {
			n, err := p.walk(ctx, svc, repo, target.Path, target.Path)
			if err != nil {
				return err
			}
			if n == 0 && target.Path != "" {
				if exists, err := svc.DirExists(ctx, repo, target.Path); err != nil || !exists {
					return notFound(target, err)
				}
			}
			return p.close()
		}
		entries, exists, err := svc.ListDir(ctx, repo, target.Path, p.meta)
		if err != nil {
			return err
		}
		if !exists {
			return notFound(target, nil)
		}
		return p.level(entries, target.Path)
	}
	if file, ok, err := svc.StatFile(ctx, repo, target.Path); err != nil {
		return err
	} else if ok {
		return p.level([]files.Entry{file}, target.Parent())
	}
	// A name prefix: the entries of the parent that start with the last segment.
	parent, prefix := target.Parent(), target.Base()
	entries, _, err := svc.ListDir(ctx, repo, parent, p.meta)
	if err != nil {
		return err
	}
	var matched []files.Entry
	for _, e := range entries {
		if strings.HasPrefix(e.Name, prefix) {
			matched = append(matched, e)
		}
	}
	if len(matched) == 0 {
		return notFound(target, nil)
	}
	if !opts.recursive {
		return p.level(matched, parent)
	}
	for _, e := range matched {
		if !e.Dir {
			if err := p.add(e, parent); err != nil {
				return err
			}
			continue
		}
		if _, err := p.walk(ctx, svc, repo, e.Path, parent); err != nil {
			return err
		}
	}
	return p.close()
}

func notFound(target remote.Path, err error) error {
	if err != nil {
		return err
	}
	return errs.NotFound("nothing found at %s", target).
		WithHint("the search and browse indexes of Nexus can lag a few seconds behind uploads")
}

// lsPrinter writes entries as they come, or buffers them for sorting.
type lsPrinter struct {
	f      *cmdutil.Factory
	opts   *lsOptions
	match  *remote.Pattern
	meta   bool
	buffer bool

	buffered []lsEntry
	json     *output.JSONArray
	count    int
}

type lsEntry struct {
	e    files.Entry
	rel  string // shown path, relative to the listed directory
	dirs bool   // sort directories first
}

// level prints one level of a directory, directories first.
func (p *lsPrinter) level(entries []files.Entry, base string) error {
	for _, e := range entries {
		if err := p.add(e, base); err != nil {
			return err
		}
	}
	return p.close()
}

// walk prints every file below dir, relative to base.
func (p *lsPrinter) walk(ctx context.Context, svc *files.Service, repo nexus.Repository, dir, base string) (int, error) {
	n := 0
	for e, err := range svc.Walk(ctx, repo, dir) {
		if err != nil {
			return n, err
		}
		n++
		if err := p.add(e, base); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (p *lsPrinter) add(e files.Entry, base string) error {
	rel, ok := remote.Rel(base, e.Path)
	if !ok {
		rel = e.Name
	}
	if p.match != nil && !p.match.Match(rel) {
		return nil
	}
	le := lsEntry{e: e, rel: rel, dirs: !p.opts.recursive}
	if p.buffer {
		p.buffered = append(p.buffered, le)
		return nil
	}
	return p.write(le)
}

func (p *lsPrinter) close() error {
	if p.buffer {
		sortEntries(p.buffered, p.opts.sortBy, p.opts.reverse)
		for _, le := range p.buffered {
			if err := p.write(le); err != nil {
				return err
			}
		}
	}
	if p.f.JSON() {
		if p.json == nil {
			p.json = output.NewJSONArray(p.f.IO.Out, p.f.IO.IsStdoutTTY())
		}
		return p.json.Close()
	}
	return nil
}

func sortEntries(list []lsEntry, by string, reverse bool) {
	less := func(a, b lsEntry) bool {
		if a.dirs && a.e.Dir != b.e.Dir {
			return a.e.Dir
		}
		switch by {
		case "size":
			if a.e.Size != b.e.Size {
				return a.e.Size < b.e.Size
			}
		case "time":
			if !a.e.LastModified.Equal(b.e.LastModified) {
				return a.e.LastModified.Before(b.e.LastModified)
			}
		}
		return a.rel < b.rel
	}
	sort.SliceStable(list, func(i, j int) bool {
		if reverse {
			return less(list[j], list[i])
		}
		return less(list[i], list[j])
	})
}

func (p *lsPrinter) write(le lsEntry) error {
	p.count++
	e := le.e
	out := p.f.IO.Out
	switch {
	case p.f.JSON():
		if p.json == nil {
			p.json = output.NewJSONArray(out, p.f.IO.IsStdoutTTY())
		}
		return p.json.Add(entryJSON(e))
	case p.f.Flags.Quiet:
		ref := e.Repository + "/" + e.Path
		if e.Dir {
			ref += "/"
		}
		_, err := fmt.Fprintln(out, ref)
		return err
	}
	name := le.rel
	if e.Dir {
		name += "/"
	}
	if !p.opts.long {
		_, err := fmt.Fprintln(out, name)
		return err
	}
	size := "-"
	if !e.Dir && e.Size >= 0 {
		size = output.HumanBytes(e.Size)
	}
	_, err := fmt.Fprintf(out, "%10s  %-16s  %s\n", size, output.HumanTime(e.LastModified), name)
	return err
}

type checksumJSON struct {
	SHA1   *string `json:"sha1"`
	SHA256 *string `json:"sha256"`
	SHA512 *string `json:"sha512"`
	MD5    *string `json:"md5"`
}

type dirJSON struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Type       string `json:"type"`
}

type fileJSON struct {
	Repository     string       `json:"repository"`
	Path           string       `json:"path"`
	Name           string       `json:"name"`
	Type           string       `json:"type"`
	Size           *int64       `json:"size"`
	ContentType    *string      `json:"content_type"`
	LastModified   *string      `json:"last_modified"`
	BlobCreated    *string      `json:"blob_created"`
	LastDownloaded *string      `json:"last_downloaded"`
	Uploader       *string      `json:"uploader"`
	Checksum       checksumJSON `json:"checksum"`
	AssetID        *string      `json:"asset_id"`
	DownloadURL    *string      `json:"download_url"`
}

// entryJSON renders an entry per FR-LS-5.
func entryJSON(e files.Entry) any {
	if e.Dir {
		return dirJSON{Repository: e.Repository, Path: e.Path, Name: e.Name, Type: "directory"}
	}
	j := fileJSON{
		Repository: e.Repository, Path: e.Path, Name: e.Name, Type: "file",
		ContentType:    cmdutil.NullString(e.ContentType),
		LastModified:   cmdutil.NullTime(e.LastModified),
		BlobCreated:    cmdutil.NullTime(e.BlobCreated),
		LastDownloaded: cmdutil.NullTime(e.LastDownloaded),
		Uploader:       cmdutil.NullString(e.Uploader),
		Checksum: checksumJSON{
			SHA1: cmdutil.NullString(e.Checksum.SHA1), SHA256: cmdutil.NullString(e.Checksum.SHA256),
			SHA512: cmdutil.NullString(e.Checksum.SHA512), MD5: cmdutil.NullString(e.Checksum.MD5),
		},
		AssetID:     cmdutil.NullString(e.AssetID),
		DownloadURL: cmdutil.NullString(e.DownloadURL),
	}
	if e.Size >= 0 {
		size := e.Size
		j.Size = &size
	}
	return j
}

// completePath completes repository names and remote paths.
func completePath(f *cmdutil.Factory) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		const directive = cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		nx, err := f.Nexus()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel := cmdutil.CompletionContext(cmd)
		defer cancel()
		repoName, rest, hasSlash := strings.Cut(toComplete, "/")
		if !hasSlash {
			repos, err := nx.Repositories(ctx)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var out []string
			for _, r := range repos {
				out = append(out, r.Name+"/")
			}
			return out, directive
		}
		dir := ""
		if i := strings.LastIndex(rest, "/"); i >= 0 {
			dir = rest[:i]
		}
		svc := files.New(nx)
		repo, err := svc.Repository(ctx, repoName)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		entries, _, err := svc.ListDir(ctx, repo, dir, false)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []string
		for _, e := range entries {
			s := repoName + "/" + e.Path
			if e.Dir {
				s += "/"
			}
			out = append(out, s)
		}
		return out, directive
	}
}
