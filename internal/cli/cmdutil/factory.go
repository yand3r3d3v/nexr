// Package cmdutil holds what all nexr commands share: global flags, lazily
// built dependencies, argument validation and error decoration.
package cmdutil

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/yand3r3d3v/nexr/internal/buildinfo"
	"github.com/yand3r3d3v/nexr/internal/config"
	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/httpx"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/output"
)

// GlobalFlags are the flags available on every command.
type GlobalFlags struct {
	Profile       string
	ConfigPath    string
	URL           string
	User          string
	Password      string
	PasswordStdin bool
	CACert        string
	Insecure      bool
	ClientCert    string
	ClientKey     string
	Timeout       time.Duration
	Retries       int
	JSON          bool
	Quiet         bool
	Verbose       int
	NoColor       bool

	// Changed reports whether a flag was given on the command line.
	Changed func(name string) bool
}

// Factory builds the dependencies of a command lazily and at most once.
type Factory struct {
	IO     *output.IOStreams
	Flags  *GlobalFlags
	Getenv func(string) string
	// Transport replaces the network transport (tests). Nil uses the real one.
	Transport http.RoundTripper

	cfgOnce sync.Once
	cfg     *config.Resolved
	cfgErr  error

	nexusOnce sync.Once
	nexus     *nexus.Client
	nexusErr  error
}

// New returns a factory for the given streams.
func New(ios *output.IOStreams) *Factory {
	return &Factory{
		IO:     ios,
		Flags:  &GlobalFlags{Changed: func(string) bool { return false }},
		Getenv: os.Getenv,
	}
}

// Config resolves the configuration. Warnings are printed once to stderr.
func (f *Factory) Config() (*config.Resolved, error) {
	f.cfgOnce.Do(func() {
		fl := f.Flags
		var s config.Settings
		str := func(name string, v *string) *string {
			if fl.Changed(name) {
				return v
			}
			return nil
		}
		s.URL = str("url", &fl.URL)
		s.User = str("user", &fl.User)
		s.Password = str("password", &fl.Password)
		s.TLS.CAFile = str("ca-cert", &fl.CACert)
		s.TLS.ClientCert = str("client-cert", &fl.ClientCert)
		s.TLS.ClientKey = str("client-key", &fl.ClientKey)
		if fl.Changed("insecure") {
			s.TLS.Insecure = &fl.Insecure
		}
		if fl.Changed("timeout") {
			s.Timeout = &config.Duration{Duration: fl.Timeout}
		}
		if fl.Changed("retries") {
			s.Retries = &fl.Retries
		}
		if fl.JSON {
			j := "json"
			s.Output = &j
		}
		f.cfg, f.cfgErr = config.Resolve(config.Options{
			ConfigPath:    fl.ConfigPath,
			Profile:       fl.Profile,
			Flags:         s,
			PasswordStdin: fl.PasswordStdin,
			Stdin:         f.IO.In,
			Getenv:        f.Getenv,
		})
		if f.cfgErr != nil {
			return
		}
		for _, w := range f.cfg.Warnings {
			f.IO.Warnf("%s", w)
		}
		if fl.Changed("password") {
			f.IO.Warnf("--password is visible in the process list and shell history; prefer --password-stdin or NEXUS_PASSWORD")
		}
		if fl.Verbose > 0 {
			for _, d := range f.cfg.DroppedCredentials {
				f.IO.Warnf("%s", d)
			}
		}
	})
	return f.cfg, f.cfgErr
}

// JSON reports whether JSON output was requested by flag or configuration.
func (f *Factory) JSON() bool {
	if f.Flags.JSON {
		return true
	}
	if f.cfg != nil {
		return f.cfg.Output.Value == "json"
	}
	return false
}

// Logger returns the debug logger, or nil when -v is not given.
func (f *Factory) Logger() *slog.Logger {
	if f.Flags.Verbose == 0 {
		return nil
	}
	return httpx.NewLogger(f.IO.ErrOut)
}

// Nexus returns the client for the configured Nexus instance.
func (f *Factory) Nexus() (*nexus.Client, error) {
	f.nexusOnce.Do(func() {
		cfg, err := f.Config()
		if err != nil {
			f.nexusErr = err
			return
		}
		if err := cfg.RequireURL(); err != nil {
			f.nexusErr = err
			return
		}
		opts := httpx.Options{
			Insecure:   cfg.TLSInsecure.Value,
			CAFile:     cfg.CAFile.Value,
			ClientCert: cfg.ClientCert.Value,
			ClientKey:  cfg.ClientKey.Value,
			Retries:    cfg.Retries.Value,
			UserAgent:  buildinfo.UserAgent(),
			Username:   cfg.User.Value,
			Password:   cfg.Password,
			AuthURLs:   []string{cfg.URL.Value},
			Logger:     f.Logger(),
			LogDetails: f.Flags.Verbose > 1,
			Transport:  f.Transport,
		}
		hc, err := httpx.NewClient(opts)
		if err != nil {
			f.nexusErr = err
			return
		}
		if cfg.TLSInsecure.Value {
			f.IO.Warnf("TLS certificate verification is disabled (from %s)", cfg.TLSInsecure.Origin)
		}
		if u, err := url.Parse(cfg.URL.Value); err == nil && u.Scheme == "http" && cfg.User.Set && !isLoopback(u.Hostname()) {
			f.IO.Warnf("credentials are sent over plain HTTP to %s", u.Host)
		}
		f.nexus, f.nexusErr = nexus.New(cfg.URL.Value, hc, cfg.Timeout.Value)
	})
	return f.nexus, f.nexusErr
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DecorateError adds configuration-specific hints to authentication errors.
func (f *Factory) DecorateError(err error) error {
	var apiErr *nexus.APIError
	if err == nil || f.cfg == nil || !errors.As(err, &apiErr) {
		return err
	}
	noUser := !f.cfg.User.Set
	e := errs.Wrap(errs.KindAuth, err, "")
	switch {
	case apiErr.AuthThrottled:
		if f.cfg.HasPassword() {
			e.WithHint("check the password (from %s) first", f.cfg.PasswordOrigin())
		}
		return e
	case apiErr.StatusCode == http.StatusUnauthorized && noUser:
		e.ReplaceHints().WithHint("no credentials are configured and anonymous access is not allowed; set NEXUS_USER and NEXUS_PASSWORD or use a profile")
	case apiErr.StatusCode == http.StatusForbidden && noUser:
		// Nexus 3.71 answers 403 instead of 401 when anonymous access is disabled.
		e.ReplaceHints().WithHint("no credentials are configured, and anonymous access does not allow this; set NEXUS_USER and NEXUS_PASSWORD or use a profile")
	case apiErr.StatusCode != http.StatusUnauthorized:
		return err
	}
	for _, d := range f.cfg.DroppedCredentials {
		e.WithHint("%s (credential scoping, see \"nexr config view\")", d)
	}
	return e
}
