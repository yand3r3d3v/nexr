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
	"github.com/yand3r3d3v/nexr/internal/registry"
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
		hc, err := httpx.NewClient(f.httpOptions(cfg, cfg.URL.Value))
		if err != nil {
			f.nexusErr = err
			return
		}
		if cfg.TLSInsecure.Value {
			f.IO.Warnf("TLS certificate verification is disabled (from %s)", cfg.TLSInsecure.Origin)
		}
		f.warnPlainHTTP(cfg, cfg.URL.Value)
		f.nexus, f.nexusErr = nexus.New(cfg.URL.Value, hc, cfg.Timeout.Value)
	})
	return f.nexus, f.nexusErr
}

// httpOptions returns the HTTP client options of the configuration; authURLs
// are the base URLs that receive the credentials.
func (f *Factory) httpOptions(cfg *config.Resolved, authURLs ...string) httpx.Options {
	return httpx.Options{
		Insecure:   cfg.TLSInsecure.Value,
		CAFile:     cfg.CAFile.Value,
		ClientCert: cfg.ClientCert.Value,
		ClientKey:  cfg.ClientKey.Value,
		Retries:    cfg.Retries.Value,
		UserAgent:  buildinfo.UserAgent(),
		Username:   cfg.User.Value,
		Password:   cfg.Password,
		AuthURLs:   authURLs,
		Logger:     f.Logger(),
		LogDetails: f.Flags.Verbose > 1,
		Transport:  f.Transport,
	}
}

func (f *Factory) warnPlainHTTP(cfg *config.Resolved, target string) {
	if u, err := url.Parse(target); err == nil && u.Scheme == "http" && cfg.User.Set && !isLoopback(u.Hostname()) {
		f.IO.Warnf("credentials are sent over plain HTTP to %s", u.Host)
	}
}

// Registry returns a client for the Docker Registry API of a repository and
// where its endpoint comes from: flagURL (--registry-url) when given, else the
// configured endpoint (spec FR-NET-3), else <url>/repository/REPO/. Requests
// to the endpoint use the TLS settings and the credentials of the profile.
func (f *Factory) Registry(repo, flagURL string) (*registry.Client, config.Setting[string], error) {
	var endpoint config.Setting[string]
	cfg, err := f.Config()
	if err != nil {
		return nil, endpoint, err
	}
	if err := cfg.RequireURL(); err != nil {
		return nil, endpoint, err
	}
	endpoint, notes := cfg.RegistryURL(repo)
	if flagURL != "" {
		u, err := config.NormalizeURL(flagURL)
		if err != nil {
			return nil, endpoint, errs.Wrap(errs.KindUsage, err, "invalid --registry-url")
		}
		endpoint, notes = config.Setting[string]{Value: u, Origin: "flag --registry-url", Set: true}, nil
	}
	if f.Flags.Verbose > 0 {
		for _, n := range notes {
			f.IO.Warnf("%s (credential scoping, see \"nexr config view\")", n)
		}
	}
	if !endpoint.Set {
		endpoint = config.Setting[string]{Value: cfg.URL.Value + "/repository/" + url.PathEscape(repo) + "/", Origin: "default"}
	}
	hc, err := httpx.NewClient(f.httpOptions(cfg, cfg.URL.Value, endpoint.Value))
	if err != nil {
		return nil, endpoint, err
	}
	if config.HostOf(endpoint.Value) != config.HostOf(cfg.URL.Value) {
		f.warnPlainHTTP(cfg, endpoint.Value)
	}
	c, err := registry.New(endpoint.Value, hc, cfg.Timeout.Value)
	return c, endpoint, err
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DecorateError adds configuration-specific hints to authentication errors
// of the REST API and the Registry API.
func (f *Factory) DecorateError(err error) error {
	if err == nil || f.cfg == nil {
		return err
	}
	var status int
	var throttled bool
	var apiErr *nexus.APIError
	var regErr *registry.Error
	switch {
	case errors.As(err, &apiErr):
		status, throttled = apiErr.StatusCode, apiErr.AuthThrottled
	case errors.As(err, &regErr):
		status, throttled = regErr.StatusCode, regErr.AuthThrottled
	default:
		return err
	}
	noUser := !f.cfg.User.Set
	e := errs.Wrap(errs.KindAuth, err, "")
	switch {
	case throttled:
		if f.cfg.HasPassword() {
			e.WithHint("check the password (from %s) first", f.cfg.PasswordOrigin())
		}
		return e
	case status == http.StatusUnauthorized && noUser:
		e.ReplaceHints().WithHint("no credentials are configured and anonymous access is not allowed; set NEXUS_USER and NEXUS_PASSWORD or use a profile")
	case status == http.StatusForbidden && noUser:
		// Nexus 3.71 answers 403 instead of 401 when anonymous access is disabled.
		e.ReplaceHints().WithHint("no credentials are configured, and anonymous access does not allow this; set NEXUS_USER and NEXUS_PASSWORD or use a profile")
	case status != http.StatusUnauthorized:
		return err
	}
	for _, d := range f.cfg.DroppedCredentials {
		e.WithHint("%s (credential scoping, see \"nexr config view\")", d)
	}
	return e
}
