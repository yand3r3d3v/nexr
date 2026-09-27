package dockercmd

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/images"
	"github.com/yand3r3d3v/nexr/internal/output"
	"github.com/yand3r3d3v/nexr/internal/remote"
	"github.com/yand3r3d3v/nexr/internal/retention"
)

type tagsOptions struct {
	match   []string
	sort    string
	reverse bool
	long    bool
}

func newTagsCmd(f *cmdutil.Factory, rf *repoFlags) *cobra.Command {
	opts := &tagsOptions{}
	cmd := &cobra.Command{
		Use:   "tags IMAGE[:TAG]",
		Short: "List the tags of an image",
		Long: `List the tags of an image with the digest, push time and size of the manifest
each tag points to. The Registry API decides which tags exist; the search index
adds the metadata. Build time, platform and size are recorded by recent Nexus
releases only (3.96, not 3.71). A tag of a multi-platform image points to an
index, which has no single size or platform.

--sort pushed (the default) lists the newest push first, --sort semver the
highest version first, then the tags that are not versions, and --sort name
sorts by name. --reverse reverses the order. With IMAGE:TAG, only that tag is
listed.

` + indexLagNote,
		Example: `  nexr docker tags team/app
  nexr docker tags team/app --sort semver -l
  nexr docker tags team/app:1.4.0 --json
  nexr docker tags team/app -q --match 'feature-*'`,
		Args:              cmdutil.ExactArgs("image"),
		ValidArgsFunction: completeImage(f, rf),
		RunE:              func(cmd *cobra.Command, args []string) error { return runTags(cmd, f, rf, opts, args[0]) },
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&opts.match, "match", nil, "only tags matching this glob or re:REGEX (repeatable)")
	fl.StringVar(&opts.sort, "sort", "pushed", "order: pushed, semver or name")
	fl.BoolVar(&opts.reverse, "reverse", false, "reverse the order")
	fl.BoolVarP(&opts.long, "long", "l", false, "show media type, platform, build time, last pull and uploader")
	_ = cmd.RegisterFlagCompletionFunc("sort", cobra.FixedCompletions([]string{"pushed", "semver", "name"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

type tagJSON struct {
	Repository   string  `json:"repository"`
	Image        string  `json:"image"`
	Tag          string  `json:"tag"`
	Digest       *string `json:"digest"`
	MediaType    *string `json:"media_type"`
	Pushed       *string `json:"pushed"`
	Created      *string `json:"created"`
	LastPulled   *string `json:"last_pulled"`
	Size         *int64  `json:"size"`
	OS           *string `json:"os"`
	Architecture *string `json:"architecture"`
	Uploader     *string `json:"uploader"`
	ComponentID  *string `json:"component_id"`
}

func tagToJSON(t images.Tag) tagJSON {
	j := tagJSON{
		Repository: t.Repository, Image: t.Image, Tag: t.Name,
		Digest: cmdutil.NullString(t.Digest), MediaType: cmdutil.NullString(t.MediaType),
		Pushed: cmdutil.NullTime(t.Pushed), Created: cmdutil.NullTime(t.Created), LastPulled: cmdutil.NullTime(t.LastPulled),
		OS: cmdutil.NullString(t.OS), Architecture: cmdutil.NullString(t.Architecture),
		Uploader: cmdutil.NullString(t.Uploader), ComponentID: cmdutil.NullString(t.ComponentID),
	}
	if t.Size >= 0 {
		size := t.Size
		j.Size = &size
	}
	return j
}

func runTags(cmd *cobra.Command, f *cmdutil.Factory, rf *repoFlags, opts *tagsOptions, arg string) error {
	ref, err := parseRef(arg)
	if err != nil {
		return err
	}
	key, err := retention.ParseSort(opts.sort)
	if err != nil {
		return errs.Wrap(errs.KindUsage, err, "")
	}
	match, err := cmdutil.ParsePatterns("--match", opts.match)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	repo, err := resolveRepo(ctx, f, rf, ref.Host)
	if err != nil {
		return err
	}
	svc, err := open(ctx, f, rf, repo)
	if err != nil {
		return err
	}
	tags, err := svc.Tags(ctx, ref.Name)
	if err != nil {
		return err
	}
	tags = slices.DeleteFunc(tags, func(t images.Tag) bool {
		return (ref.Tag != "" && t.Name != ref.Tag) || (len(match) > 0 && !remote.MatchAny(match, t.Name))
	})
	if ref.Tag != "" && len(tags) == 0 {
		return errs.NotFound("tag %s not found in %s", ref.Name+":"+ref.Tag, repo).
			WithHint("run \"nexr docker tags -R %s %s\" to list its tags", repo, ref.Name)
	}
	sortTags(tags, key)
	if opts.reverse {
		slices.Reverse(tags)
	}

	switch {
	case f.JSON():
		list := make([]tagJSON, 0, len(tags))
		for _, t := range tags {
			list = append(list, tagToJSON(t))
		}
		return output.WriteJSON(f.IO.Out, list, f.IO.IsStdoutTTY())
	case f.Flags.Quiet:
		for _, t := range tags {
			fmt.Fprintln(f.IO.Out, t.Name)
		}
		return nil
	}
	headers := []string{"TAG", "DIGEST", "PUSHED", "SIZE"}
	if opts.long {
		headers = append(headers, "TYPE", "PLATFORM", "CREATED", "LAST PULLED", "UPLOADER")
	}
	tbl := output.NewTable(f.IO.Out, headers...)
	for _, t := range tags {
		row := []string{t.Name, shortDigest(t.Digest), output.HumanTime(t.Pushed), sizeText(t)}
		if opts.long {
			row = append(row, typeText(t.MediaType), platform(t), output.HumanTime(t.Created),
				output.HumanTime(t.LastPulled), output.OrDash(t.Uploader))
		}
		tbl.AddRow(row...)
	}
	return tbl.Render()
}

// sortTags orders tags for listing: by push time or version, newest first, or
// by name in ascending order.
func sortTags(tags []images.Tag, key retention.SortKey) {
	if key == retention.SortName {
		sort.SliceStable(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
		return
	}
	sort.SliceStable(tags, func(i, j int) bool {
		return retention.Compare(retentionTag(tags[i]), retentionTag(tags[j]), key) < 0
	})
}

func retentionTag(t images.Tag) retention.Tag {
	return retention.Tag{Name: t.Name, Pushed: t.Pushed}
}

// shortDigest abbreviates a digest to 12 hex digits, like docker does.
func shortDigest(d string) string {
	algo, hex, ok := strings.Cut(d, ":")
	if !ok {
		return output.OrDash(d)
	}
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return algo + ":" + hex
}

func sizeText(t images.Tag) string {
	switch {
	case t.IsIndex():
		return "multi-arch"
	case t.Size < 0:
		return "-"
	}
	return output.HumanBytes(t.Size)
}

func typeText(mediaType string) string {
	switch {
	case mediaType == "":
		return "-"
	case strings.Contains(mediaType, "index") || strings.Contains(mediaType, "list"):
		return "index"
	case strings.Contains(mediaType, "manifest"):
		return "image"
	}
	return mediaType
}

func platform(t images.Tag) string {
	if t.OS == "" {
		return "-"
	}
	if t.Architecture == "" {
		return t.OS
	}
	return t.OS + "/" + t.Architecture
}
