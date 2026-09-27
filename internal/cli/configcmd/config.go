// Package configcmd implements "nexr config".
package configcmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/config"
	"github.com/yand3r3d3v/nexr/internal/output"
)

// New returns the config command group.
func New(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the effective configuration",
		Long: `Inspect the configuration that nexr uses.

Settings come from, highest precedence first: command-line flags, the profile
selected with --profile or NEXR_PROFILE, environment variables, the default
profile and top-level settings of the config file, and built-in defaults.`,
		Args: cmdutil.GroupArgs,
		RunE: cmdutil.GroupRunE,
	}
	cmd.AddCommand(newViewCmd(f), newPathCmd(f), newProfilesCmd(f))
	return cmd
}

type fileJSON struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

type profileJSON struct {
	Name   *string `json:"name"`
	Source *string `json:"source"`
}

type viewJSON struct {
	ConfigFile         fileJSON       `json:"config_file"`
	Profile            profileJSON    `json:"profile"`
	Settings           []config.Entry `json:"settings"`
	Warnings           []string       `json:"warnings"`
	DroppedCredentials []string       `json:"dropped_credentials"`
}

func newViewCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "Show the effective settings and where they come from",
		Long:  "Show every setting with its value and its source. Passwords are always redacted.",
		Example: `  nexr config view
  NEXUS_URL=https://nexus.example.com nexr config view --json`,
		Args: cmdutil.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := f.Config()
			if err != nil {
				return err
			}
			if f.JSON() {
				v := viewJSON{
					ConfigFile:         fileJSON{Path: cfg.ConfigPath, Exists: cfg.ConfigExists},
					Settings:           cfg.Entries(),
					Warnings:           nonNil(cfg.Warnings),
					DroppedCredentials: nonNil(cfg.DroppedCredentials),
				}
				if cfg.Profile != "" {
					v.Profile = profileJSON{Name: &cfg.Profile, Source: &cfg.ProfileOrigin}
				}
				return output.WriteJSON(f.IO.Out, v, f.IO.IsStdoutTTY())
			}
			fmt.Fprintf(f.IO.Out, "Config file: %s%s\n", cfg.ConfigPath, existsNote(cfg.ConfigExists))
			if cfg.Profile != "" {
				fmt.Fprintf(f.IO.Out, "Profile:     %s (from %s)\n", cfg.Profile, cfg.ProfileOrigin)
			} else {
				fmt.Fprintln(f.IO.Out, "Profile:     -")
			}
			fmt.Fprintln(f.IO.Out)
			t := output.NewTable(f.IO.Out, "key", "value", "source")
			for _, e := range cfg.Entries() {
				t.AddRow(e.Key, output.OrDash(e.Value), output.OrDash(e.Origin))
			}
			if err := t.Render(); err != nil {
				return err
			}
			for _, d := range cfg.DroppedCredentials {
				fmt.Fprintf(f.IO.Out, "\nnote: %s\n", d)
			}
			return nil
		},
	}
}

func newPathCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "path",
		Short:   "Print the location of the config file",
		Example: `  nexr config path`,
		Args:    cmdutil.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := f.Config()
			if err != nil {
				return err
			}
			if f.JSON() {
				return output.WriteJSON(f.IO.Out, fileJSON{Path: cfg.ConfigPath, Exists: cfg.ConfigExists}, f.IO.IsStdoutTTY())
			}
			if f.Flags.Quiet {
				fmt.Fprintln(f.IO.Out, cfg.ConfigPath)
				return nil
			}
			fmt.Fprintf(f.IO.Out, "%s%s\n", cfg.ConfigPath, existsNote(cfg.ConfigExists))
			return nil
		},
	}
}

type profileEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Active  bool   `json:"active"`
	Current bool   `json:"current"`
}

func newProfilesCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "List the profiles of the config file",
		Long: `List the profiles of the config file. ACTIVE marks the profile used by this
invocation; CURRENT marks current_profile from the file.`,
		Example: `  nexr config profiles
  nexr --profile staging config profiles`,
		Args: cmdutil.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := f.Config()
			if err != nil {
				return err
			}
			list := []profileEntry{}
			for _, name := range cfg.ProfileNames() {
				list = append(list, profileEntry{
					Name: name, URL: cfg.ProfileURLs[name],
					Active: name == cfg.Profile, Current: name == cfg.CurrentProfile,
				})
			}
			if f.JSON() {
				return output.WriteJSON(f.IO.Out, list, f.IO.IsStdoutTTY())
			}
			if f.Flags.Quiet {
				for _, p := range list {
					fmt.Fprintln(f.IO.Out, p.Name)
				}
				return nil
			}
			if len(list) == 0 {
				fmt.Fprintf(f.IO.ErrOut, "no profiles defined in %s\n", cfg.ConfigPath)
				return nil
			}
			t := output.NewTable(f.IO.Out, "name", "url", "active", "current")
			for _, p := range list {
				t.AddRow(p.Name, output.OrDash(p.URL), mark(p.Active), mark(p.Current))
			}
			return t.Render()
		},
	}
}

func existsNote(exists bool) string {
	if exists {
		return ""
	}
	return " (not found)"
}

func mark(b bool) string {
	if b {
		return "*"
	}
	return ""
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
