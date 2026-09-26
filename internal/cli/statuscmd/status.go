// Package statuscmd implements "nexr status".
package statuscmd

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/output"
)

type statusJSON struct {
	URL          string            `json:"url"`
	Profile      *string           `json:"profile"`
	Server       nexus.ServerInfo  `json:"server"`
	Readable     bool              `json:"readable"`
	Writable     bool              `json:"writable"`
	Auth         *string           `json:"auth"` // "accepted", "anonymous", "rejected", "blocked"
	User         *string           `json:"user"`
	Repositories *int              `json:"repositories"` // repositories the user may browse
	Sources      map[string]string `json:"sources"`
}

// New returns the status command.
func New(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check the connection to Nexus",
		Long: `Check that Nexus is reachable, show its version and edition, and verify that
the configured credentials are accepted.

Exit codes: 0 when everything works, 7 when the server cannot be reached,
4 when the credentials are rejected or no repository is visible without them,
1 when the server reports that it is not available.`,
		Example: `  nexr status
  nexr --profile staging status --json`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return run(cmd, f) },
	}
}

func run(cmd *cobra.Command, f *cmdutil.Factory) error {
	nx, err := f.Nexus()
	if err != nil {
		return err
	}
	cfg, _ := f.Config()
	ctx := cmd.Context()
	health, err := nx.Health(ctx)
	if err != nil {
		return err
	}
	st := statusJSON{
		URL: cfg.URL.Value, Readable: health.Readable, Writable: health.Writable,
		Sources: map[string]string{"url": cfg.URL.Origin},
	}
	if cfg.Profile != "" {
		st.Profile = &cfg.Profile
		st.Sources["profile"] = cfg.ProfileOrigin
	}
	if cfg.User.Set {
		st.User = &cfg.User.Value
		st.Sources["user"] = cfg.User.Origin
	}
	var failure error
	if health.Readable {
		auth, visible, err := checkAuth(cmd, nx)
		if err != nil {
			return err
		}
		if auth == "accepted" && !cfg.User.Set {
			auth = "anonymous"
		}
		st.Auth, st.Repositories = &auth, visible
		failure = authFailure(f, auth, visible)
	} else {
		failure = errs.New(errs.KindGeneric, "Nexus at %s reports that it is not available", cfg.URL.Value)
	}
	st.Server = nx.Server()

	if f.JSON() {
		if err := output.WriteJSON(f.IO.Out, st, f.IO.IsStdoutTTY()); err != nil {
			return err
		}
		return failure
	}
	if f.Flags.Quiet {
		return failure
	}
	if err := printTable(f, st); err != nil {
		return err
	}
	return failure
}

// checkAuth lists the repositories, which any valid user may do, and returns
// the authentication state and the number of repositories the user may browse.
func checkAuth(cmd *cobra.Command, nx *nexus.Client) (string, *int, error) {
	repos, err := nx.Repositories(cmd.Context())
	var apiErr *nexus.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.AuthThrottled:
		return "blocked", nil, nil
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized:
		return "rejected", nil, nil
	case err != nil:
		return "", nil, err
	}
	n := len(repos)
	return "accepted", &n, nil
}

func authFailure(f *cmdutil.Factory, auth string, visible *int) error {
	cfg, _ := f.Config()
	var e *errs.Error
	switch {
	case auth == "blocked":
		e = errs.New(errs.KindAuth, "Nexus is blocking user %q after repeated failed sign-ins", cfg.User.Value).
			WithHint("the block ends after 15 minutes without any request for this user (the default); every request, even with the right password, starts that time again").
			WithHint("an administrator can lift the block at once by updating the user or changing its password").
			WithHint("check the password (from %s) first", cfg.PasswordOrigin())
	case auth == "rejected" && cfg.User.Set:
		e = errs.New(errs.KindAuth, "the credentials of user %q were rejected", cfg.User.Value).
			WithHint("check the password (from %s)", cfg.PasswordOrigin())
	case auth == "rejected":
		e = errs.New(errs.KindAuth, "no credentials are configured and anonymous access is disabled").
			WithHint("set NEXUS_USER and NEXUS_PASSWORD, or configure a profile")
	case auth == "anonymous" && visible != nil && *visible == 0:
		// Nexus 3.71 answers anonymous requests with an empty list when
		// anonymous access is disabled.
		e = errs.New(errs.KindAuth, "no credentials are configured and no repository is visible without them").
			WithHint("set NEXUS_USER and NEXUS_PASSWORD, or configure a profile")
	default:
		return nil
	}
	for _, d := range cfg.DroppedCredentials {
		e.WithHint("%s", d)
	}
	return e
}

func printTable(f *cmdutil.Factory, st statusJSON) error {
	cfg, _ := f.Config()
	t := output.NewTable(f.IO.Out)
	t.AddRow("URL:", fmt.Sprintf("%s (from %s)", st.URL, cfg.URL.Origin))
	if st.Profile != nil {
		t.AddRow("Profile:", fmt.Sprintf("%s (from %s)", cfg.Profile, cfg.ProfileOrigin))
	}
	server := "unknown"
	if st.Server.Version != "" {
		server = "Nexus Repository " + st.Server.Version
		if st.Server.Edition != "" {
			server += " (" + st.Server.Edition + ")"
		}
	}
	t.AddRow("Server:", server)
	t.AddRow("Read:", availability(st.Readable))
	t.AddRow("Write:", availability(st.Writable))
	if st.Auth != nil {
		switch *st.Auth {
		case "accepted":
			t.AddRow("Auth:", fmt.Sprintf("credentials accepted for user %s (from %s)", cfg.User.Value, cfg.User.Origin))
		case "anonymous":
			t.AddRow("Auth:", "anonymous access")
		case "blocked":
			t.AddRow("Auth:", "blocked after failed sign-ins")
		default:
			t.AddRow("Auth:", "rejected")
		}
	}
	if st.Repositories != nil {
		t.AddRow("Repositories:", fmt.Sprintf("%d visible", *st.Repositories))
	}
	return t.Render()
}

func availability(ok bool) string {
	if ok {
		return "available"
	}
	return "not available"
}
