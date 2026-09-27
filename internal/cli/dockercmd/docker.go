// Package dockercmd implements "nexr docker": listing images and tags and
// deleting tags in docker and oci repositories.
package dockercmd

import (
	"context"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/config"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/images"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

// repoFlags are the flags that select the repository of all docker commands.
type repoFlags struct {
	repo        string // -R/--repo
	registryURL string // --registry-url
}

// New returns the docker command group.
func New(f *cmdutil.Factory) *cobra.Command {
	rf := &repoFlags{}
	cmd := &cobra.Command{
		Use:   "docker",
		Short: "Work with container images",
		Long: `Work with container images in repositories of the docker and oci formats:
list images and tags, and delete tags one by one or with a retention policy.

The repository is taken from -R/--repo, the "docker.repository" setting of an
explicitly selected profile, NEXR_DOCKER_REPO, or the config file. An image
reference may also start with a registry host that is configured in
"docker.registry_urls", like registry.example.com/team/app:1.0.

nexr reads tags through the Registry API of the repository, by default at
<url>/repository/REPO/v2/. If a reverse proxy or a Docker connector serves the
registry elsewhere, set it with --registry-url, NEXR_DOCKER_REGISTRY_URL or
"docker.registry_urls".`,
		Example: `  nexr docker ls -R docker-hosted
  nexr docker tags team/app
  nexr docker rm team/app:1.0
  nexr docker rm team/app --keep 5 --dry-run`,
		Args: cmdutil.GroupArgs,
		RunE: cmdutil.GroupRunE,
	}
	pf := cmd.PersistentFlags()
	pf.StringVarP(&rf.repo, "repo", "R", "", "repository of the images (default: docker.repository, NEXR_DOCKER_REPO)")
	pf.StringVar(&rf.registryURL, "registry-url", "", "registry endpoint of the repository (default: <url>/repository/REPO/)")
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepo(f))
	cmd.AddCommand(newLsCmd(f, rf), newTagsCmd(f, rf), newRmCmd(f, rf))
	return cmd
}

// indexLagNote is added to the help of the commands that read tags.
const indexLagNote = `Nexus adds a pushed tag to its search index a few seconds after the push. Until
then, nexr lists the tag without a push time, and retention policies never
delete it.`

// selectedRepo returns the repository a command works on without a registry
// host in the image reference: -R, then the configuration. It is empty when
// none is set.
func selectedRepo(f *cmdutil.Factory, rf *repoFlags) (string, error) {
	if rf.repo != "" {
		return rf.repo, nil
	}
	cfg, err := f.Config()
	if err != nil {
		return "", err
	}
	return cfg.DockerRepository.Value, nil
}

// resolveRepo returns the repository of an image reference with the given
// registry host ("" for none), following FR-IMGREF-2 and FR-IMGREF-3.
func resolveRepo(ctx context.Context, f *cmdutil.Factory, rf *repoFlags, host string) (string, error) {
	selected, err := selectedRepo(f, rf)
	if err != nil {
		return "", err
	}
	if host == "" {
		if selected == "" {
			return "", noRepoError(ctx, f)
		}
		return selected, nil
	}
	cfg, err := f.Config()
	if err != nil {
		return "", err
	}
	repos, envMatch, notes := cfg.RepositoriesForHost(host)
	flagMatch := rf.registryURL != "" && config.HostOf(rf.registryURL) == config.HostOf("//"+host)
	switch {
	case flagMatch || envMatch:
		if selected == "" {
			return "", noRepoError(ctx, f)
		}
		return selected, nil
	case len(repos) == 1:
		if rf.repo != "" && rf.repo != repos[0] {
			return "", errs.Usage("the registry host %s belongs to repository %s, but -R selects %s", host, repos[0], rf.repo)
		}
		return repos[0], nil
	case len(repos) > 1:
		if rf.repo != "" && contains(repos, rf.repo) {
			return rf.repo, nil
		}
		return "", errs.Usage("the registry host %s belongs to several repositories (%s)", host, strings.Join(repos, ", ")).
			WithHint("select one with -R")
	}
	e := errs.Usage("the image reference starts with the registry host %s, which matches no configured registry URL", host)
	for _, n := range notes {
		e.WithHint("%s (credential scoping, see \"nexr config view\")", n)
	}
	return "", e.WithHint("add it to \"docker.registry_urls\" in the config file (REPO: https://%s), or leave out the host and select the repository with -R", host)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// noRepoError is the usage error for a command without a repository; it lists
// the docker and oci repositories the user may browse.
func noRepoError(ctx context.Context, f *cmdutil.Factory) error {
	e := errs.Usage("no Docker repository selected").
		WithHint("pass -R REPO, or set NEXR_DOCKER_REPO or \"docker.repository\" in the config file")
	if names := imageRepos(ctx, f); len(names) > 0 {
		e.WithHint("Docker repositories: %s", strings.Join(names, ", "))
	}
	return e
}

// imageRepos returns the names of the docker and oci repositories, or nil
// when they cannot be listed.
func imageRepos(ctx context.Context, f *cmdutil.Factory) []string {
	nx, err := f.Nexus()
	if err != nil {
		return nil
	}
	repos, err := nx.Repositories(ctx)
	if err != nil {
		return nil
	}
	var names []string
	for _, r := range repos {
		if images.IsImageFormat(r.Format) {
			names = append(names, r.Name)
		}
	}
	sort.Strings(names)
	return names
}

// open returns the images service for a repository.
func open(ctx context.Context, f *cmdutil.Factory, rf *repoFlags, repo string) (*images.Service, error) {
	nx, err := f.Nexus()
	if err != nil {
		return nil, err
	}
	reg, _, err := f.Registry(repo, rf.registryURL)
	if err != nil {
		return nil, err
	}
	svc, err := images.Open(ctx, nx, reg, repo)
	if err != nil {
		return nil, err
	}
	svc.Warn = f.IO.Warnf
	return svc, nil
}

// parseRef parses an image reference argument as a usage error.
func parseRef(arg string) (remote.ImageRef, error) {
	ref, err := remote.ParseImageRef(arg)
	if err != nil {
		return ref, errs.Wrap(errs.KindUsage, err, "")
	}
	return ref, nil
}

func completeRepo(f *cmdutil.Factory) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		ctx, cancel := cmdutil.CompletionContext(cmd)
		defer cancel()
		return imageRepos(ctx, f), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeImage completes image names of the selected repository.
func completeImage(f *cmdutil.Factory, rf *repoFlags) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		ctx, cancel := cmdutil.CompletionContext(cmd)
		defer cancel()
		repo, err := selectedRepo(f, rf)
		if err != nil || repo == "" {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		svc, err := open(ctx, f, rf, repo)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		svc.Warn = func(string, ...any) {}
		names, err := svc.Images(ctx)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}
