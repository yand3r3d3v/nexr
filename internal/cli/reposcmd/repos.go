// Package reposcmd implements "nexr repos".
package reposcmd

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/output"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

type listOptions struct {
	format string
	typ    string
	match  string
}

// New returns the repos command group.
func New(f *cmdutil.Factory) *cobra.Command {
	opts := &listOptions{}
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "List repositories",
		Long: `List the repositories you may browse, sorted by name.

"nexr repos" and "nexr repos ls" are the same command.`,
		Example: `  nexr repos
  nexr repos --format raw --type hosted
  nexr repos --match 'docker-*' --json
  nexr repos show docker-hosted`,
		Args: cmdutil.GroupArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runList(cmd, f, opts) },
	}
	addListFlags(cmd, opts)

	ls := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List repositories",
		Example: `  nexr repos ls --format docker`,
		Args:    cmdutil.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return runList(cmd, f, opts) },
	}
	addListFlags(ls, opts)
	cmd.AddCommand(ls, newShowCmd(f))
	return cmd
}

func addListFlags(cmd *cobra.Command, opts *listOptions) {
	cmd.Flags().StringVar(&opts.format, "format", "", "only repositories of this format (raw, docker, maven2, …)")
	cmd.Flags().StringVar(&opts.typ, "type", "", "only repositories of this type (hosted, proxy, group)")
	cmd.Flags().StringVar(&opts.match, "match", "", "only names matching this glob (or re:REGEX)")
}

type repoJSON struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Type   string `json:"type"`
	URL    string `json:"url"`
	Online *bool  `json:"online"`
}

func runList(cmd *cobra.Command, f *cmdutil.Factory, opts *listOptions) error {
	switch opts.typ {
	case "", "hosted", "proxy", "group":
	default:
		return errs.Usage("invalid --type %q: use hosted, proxy or group", opts.typ)
	}
	var match *remote.Pattern
	if opts.match != "" {
		p, err := remote.ParsePattern(opts.match)
		if err != nil {
			return errs.Wrap(errs.KindUsage, err, "invalid --match")
		}
		match = &p
	}
	nx, err := f.Nexus()
	if err != nil {
		return err
	}
	repos, err := nx.Repositories(cmd.Context())
	if err != nil {
		return err
	}
	if cfg, _ := f.Config(); len(repos) == 0 && !cfg.User.Set {
		// Nexus 3.71 answers an anonymous request with an empty list when
		// anonymous access is disabled.
		f.IO.Warnf("no repository is visible without credentials; set NEXUS_USER and NEXUS_PASSWORD, or use a profile")
	}
	out := make([]repoJSON, 0, len(repos))
	for _, r := range repos {
		if opts.format != "" && !strings.EqualFold(r.Format, opts.format) {
			continue
		}
		if opts.typ != "" && r.Type != opts.typ {
			continue
		}
		if match != nil && !match.Match(r.Name) {
			continue
		}
		out = append(out, repoJSON{Name: r.Name, Format: r.Format, Type: r.Type, URL: r.URL, Online: r.Online})
	}
	switch {
	case f.JSON():
		return output.WriteJSON(f.IO.Out, out, f.IO.IsStdoutTTY())
	case f.Flags.Quiet:
		for _, r := range out {
			fmt.Fprintln(f.IO.Out, r.Name)
		}
		return nil
	}
	t := output.NewTable(f.IO.Out, "name", "format", "type", "url")
	for _, r := range out {
		t.AddRow(r.Name, r.Format, r.Type, r.URL)
	}
	return t.Render()
}

func newShowCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show REPO",
		Short: "Show repository details",
		Long: `Show the details of a repository.

The storage, cleanup and Docker settings are shown only if you may read the
repository configuration (an administrative privilege).`,
		Example: `  nexr repos show raw-releases
  nexr repos show docker-hosted --json`,
		Args: cmdutil.ExactArgs("repo"),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return completeRepos(cmd, f)
		},
		RunE: func(cmd *cobra.Command, args []string) error { return runShow(cmd, f, args[0]) },
	}
}

func completeRepos(cmd *cobra.Command, f *cmdutil.Factory) ([]string, cobra.ShellCompDirective) {
	nx, err := f.Nexus()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := cmdutil.CompletionContext(cmd)
	defer cancel()
	repos, err := nx.Repositories(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.Name+"\t"+r.Format+" "+r.Type)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

type showJSON struct {
	repoJSON
	Settings map[string]any `json:"settings"`
}

func runShow(cmd *cobra.Command, f *cmdutil.Factory, name string) error {
	nx, err := f.Nexus()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	repo, err := nx.Repository(ctx, name)
	if err != nil {
		if errs.Classify(err) == errs.KindNotFound {
			return errs.Wrap(errs.KindNotFound, err, "repository %q not found", name).
				WithHint("run \"nexr repos\" to list the repositories you may browse")
		}
		return err
	}
	settings, err := nx.RepositorySettings(ctx, repo)
	if err != nil {
		var apiErr *nexus.APIError
		if !errors.As(err, &apiErr) || (apiErr.StatusCode != http.StatusForbidden && apiErr.StatusCode != http.StatusNotFound) {
			return err
		}
		settings = nil // no permission to read the configuration
	}
	if f.JSON() {
		return output.WriteJSON(f.IO.Out, showJSON{
			repoJSON: repoJSON{Name: repo.Name, Format: repo.Format, Type: repo.Type, URL: repo.URL, Online: repo.Online},
			Settings: settings,
		}, f.IO.IsStdoutTTY())
	}
	rows := [][2]string{
		{"Name", repo.Name}, {"Format", repo.Format}, {"Type", repo.Type}, {"URL", repo.URL},
	}
	if repo.Online != nil {
		rows = append(rows, [2]string{"Online", yesNo(*repo.Online)})
	}
	if settings == nil {
		rows = append(rows, [2]string{"Settings", "not available (reading the configuration needs administrative privileges)"})
	} else {
		rows = append(rows, settingsRows(settings)...)
	}
	t := output.NewTable(f.IO.Out)
	for _, r := range rows {
		t.AddRow(r[0]+":", r[1])
	}
	return t.Render()
}

func settingsRows(s map[string]any) [][2]string {
	var rows [][2]string
	get := func(path ...string) (any, bool) {
		var cur any = s
		for _, p := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = m[p]; !ok || cur == nil {
				return nil, false
			}
		}
		return cur, true
	}
	add := func(label string, path ...string) {
		if v, ok := get(path...); ok {
			rows = append(rows, [2]string{label, format(v)})
		}
	}
	add("Blob store", "storage", "blobStoreName")
	add("Write policy", "storage", "writePolicy")
	add("Strict content validation", "storage", "strictContentTypeValidation")
	add("Cleanup policies", "cleanup", "policyNames")
	add("Group members", "group", "memberNames")
	add("Remote URL", "proxy", "remoteUrl")
	add("Docker HTTP port", "docker", "httpPort")
	add("Docker HTTPS port", "docker", "httpsPort")
	add("Docker subdomain", "docker", "subdomain")
	add("Docker path routing", "docker", "pathEnabled")
	add("Docker force basic auth", "docker", "forceBasicAuth")
	add("Docker V1 API", "docker", "v1Enabled")
	return rows
}

func format(v any) string {
	switch x := v.(type) {
	case bool:
		return yesNo(x)
	case float64:
		return fmt.Sprintf("%g", x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, fmt.Sprint(e))
		}
		sort.Strings(parts)
		if len(parts) == 0 {
			return "none"
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(x)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
