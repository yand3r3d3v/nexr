// Package root assembles the nexr command tree and runs it.
package root

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/buildinfo"
	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/cli/configcmd"
	"github.com/yand3r3d3v/nexr/internal/cli/filescmd"
	"github.com/yand3r3d3v/nexr/internal/cli/reposcmd"
	"github.com/yand3r3d3v/nexr/internal/cli/statuscmd"
	"github.com/yand3r3d3v/nexr/internal/cli/versioncmd"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/output"
)

// NewCmd returns the root command.
func NewCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nexr",
		Short: "Command-line tool for Sonatype Nexus Repository 3",
		Long: `nexr works with Sonatype Nexus Repository 3 from the command line: files,
container images and storage cleanup, without the web UI.

Configure the connection with NEXUS_URL, NEXUS_USER and NEXUS_PASSWORD, with
profiles in the config file ("nexr config path" shows where it is), or with
flags. Run "nexr status" to check the connection.`,
		Example: `  export NEXUS_URL=https://nexus.example.com NEXUS_USER=ci NEXUS_PASSWORD=...
  nexr status
  nexr repos --format raw
  nexr up ./dist raw-releases/myapp/1.4.0/
  nexr ls -l raw-releases/myapp/
  nexr --profile staging repos --json`,
		Version:       buildinfo.Get().Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cmdutil.GroupArgs,
		RunE:          cmdutil.GroupRunE,
	}
	cmd.SetVersionTemplate("nexr {{.Version}}\n")
	cmd.SetFlagErrorFunc(cmdutil.FlagError)
	cmd.PersistentPreRun = func(*cobra.Command, []string) {
		if f.Flags.NoColor {
			f.IO.SetNoColor(true)
		}
	}

	fl := f.Flags
	pf := cmd.PersistentFlags()
	pf.StringVar(&fl.Profile, "profile", "", "profile from the config file")
	pf.StringVar(&fl.ConfigPath, "config", "", "config file (default: $XDG_CONFIG_HOME/nexr/config.yaml)")
	pf.StringVar(&fl.URL, "url", "", "Nexus base URL (env NEXUS_URL)")
	pf.StringVarP(&fl.User, "user", "u", "", "user name or user-token name code (env NEXUS_USER)")
	pf.StringVar(&fl.Password, "password", "", "password (visible to other users; prefer --password-stdin or NEXUS_PASSWORD)")
	pf.BoolVar(&fl.PasswordStdin, "password-stdin", false, "read the password from stdin")
	pf.StringVar(&fl.CACert, "ca-cert", "", "additional trusted CA bundle, PEM (env NEXUS_CA_CERT)")
	pf.BoolVar(&fl.Insecure, "insecure", false, "skip TLS certificate verification (env NEXUS_INSECURE)")
	pf.StringVar(&fl.ClientCert, "client-cert", "", "client certificate for mutual TLS, PEM (env NEXUS_CLIENT_CERT)")
	pf.StringVar(&fl.ClientKey, "client-key", "", "client key for mutual TLS, PEM (env NEXUS_CLIENT_KEY)")
	pf.DurationVar(&fl.Timeout, "timeout", 0, "timeout of a single API request (default 60s)")
	pf.IntVar(&fl.Retries, "retries", 0, "retries of idempotent requests (default 3)")
	pf.BoolVar(&fl.JSON, "json", false, "print JSON")
	pf.BoolVarP(&fl.Quiet, "quiet", "q", false, "print only identifiers, or nothing on success")
	pf.CountVarP(&fl.Verbose, "verbose", "v", "log HTTP requests to stderr (-vv adds headers and bodies)")
	pf.BoolVar(&fl.NoColor, "no-color", false, "disable colour (env NO_COLOR)")
	fl.Changed = pf.Changed
	cmd.Flags().Bool("version", false, "print the version")

	_ = cmd.RegisterFlagCompletionFunc("profile", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		cfg, err := f.Config()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return cfg.ProfileNames(), cobra.ShellCompDirectiveNoFileComp
	})

	cmd.AddCommand(filescmd.New(f)...)
	cmd.AddCommand(
		reposcmd.New(f),
		statuscmd.New(f),
		configcmd.New(f),
		versioncmd.New(f),
	)
	return cmd
}

// Main runs nexr with args and returns the exit code.
func Main(ctx context.Context, args []string, f *cmdutil.Factory) int {
	cmd := NewCmd(f)
	cmd.SetArgs(args)
	cmd.SetIn(f.IO.In)
	cmd.SetOut(f.IO.Out)
	cmd.SetErr(f.IO.ErrOut)
	_, err := cmd.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	err = normalize(err)
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		err = errs.Wrap(errs.KindInterrupted, err, "interrupted")
	}
	err = f.DecorateError(err)
	output.PrintError(f.IO.ErrOut, err, f.JSON())
	return errs.ExitCode(err)
}

// normalize turns cobra's plain errors into usage errors.
func normalize(err error) error {
	var kinded errs.Kinder
	if errors.As(err, &kinded) {
		return err
	}
	msg := err.Error()
	for _, prefix := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "flag needs an argument", "invalid argument", "required flag"} {
		if strings.HasPrefix(msg, prefix) {
			return errs.Wrap(errs.KindUsage, err, "").WithHint("run \"nexr --help\" for usage")
		}
	}
	return err
}
