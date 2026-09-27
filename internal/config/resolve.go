package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

// Options are the inputs of Resolve. Zero values select the real environment.
type Options struct {
	ConfigPath    string   // --config; empty when not given
	Profile       string   // --profile; empty when not given
	Flags         Settings // values given on the command line
	PasswordStdin bool     // --password-stdin
	Stdin         io.Reader
	Getenv        func(string) string
	HomeDir       func() (string, error)
	GOOS          string
}

// Setting is a resolved value together with its origin.
type Setting[T any] struct {
	Value  T
	Origin string // e.g. "flag --url", "env NEXUS_URL", "profile prod", "file", "default"
	Set    bool
}

// Resolved is the effective configuration of one invocation.
type Resolved struct {
	ConfigPath      string
	ConfigExists    bool
	Profile         string
	ProfileOrigin   string
	ProfileExplicit bool
	CurrentProfile  string            // current_profile from the file
	ProfileURLs     map[string]string // profile name → URL configured for it in the file

	URL                 Setting[string]
	User                Setting[string]
	TLSInsecure         Setting[bool]
	CAFile              Setting[string]
	ClientCert          Setting[string]
	ClientKey           Setting[string]
	Timeout             Setting[time.Duration]
	Retries             Setting[int]
	Concurrency         Setting[int]
	Output              Setting[string]
	DockerRepository    Setting[string]
	DockerExclude       Setting[[]string]
	RegistryURLs        Setting[map[string]string]
	RegistryURLOverride Setting[string] // NEXR_DOCKER_REGISTRY_URL
	UploadMethod        Setting[string]
	GCWaitTimeout       Setting[time.Duration]
	GCTasks             Setting[[]string]

	// Warnings are problems that do not stop the program (unknown keys,
	// file permissions).
	Warnings []string
	// DroppedCredentials explains credentials ignored by the scoping rule.
	DroppedCredentials []string

	registryURLs map[string]registryEntry // repository → endpoint from the config file
	registryEnv  *registryEntry           // NEXR_DOCKER_REGISTRY_URL

	password     *passwordSource
	getenv       func(string) string
	stdin        io.Reader
	home         string
	goos         string
	passwordOnce sync.Once
	passwordVal  string
	passwordErr  error
}

type domain int

const (
	domDefault domain = iota
	domFile
	domEnv
	domFlag
)

type layer struct {
	dom    domain
	name   string // "default", "file", "profile <name>", "env", "flag"
	s      Settings
	stdin  bool // --password-stdin
	envSet map[string]string
}

var flagNames = map[string]string{
	"url": "--url", "user": "--user", "password": "--password",
	"tls.insecure": "--insecure", "tls.ca_file": "--ca-cert",
	"tls.client_cert": "--client-cert", "tls.client_key": "--client-key",
	"timeout": "--timeout", "retries": "--retries", "output": "--json",
	"docker.repository": "--repo",
}

func (l layer) origin(key string) string {
	switch l.dom {
	case domFlag:
		if key == "password" && l.stdin {
			return "flag --password-stdin"
		}
		if n, ok := flagNames[key]; ok {
			return "flag " + n
		}
		return "flag"
	case domEnv:
		if n, ok := l.envSet[key]; ok {
			return "env " + n
		}
		return "env"
	default:
		return l.name
	}
}

type passwordSource struct {
	kind   string // "literal", "env", "file", "command", "stdin"
	value  string
	origin string
}

// DefaultPath returns the default location of the config file.
func DefaultPath(goos string, getenv func(string) string, homeDir func() (string, error)) (string, error) {
	if goos == "windows" {
		if dir := getenv("AppData"); dir != "" {
			return filepath.Join(dir, "nexr", "config.yaml"), nil
		}
		return "", errors.New("%AppData% is not set")
	}
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "nexr", "config.yaml"), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "nexr", "config.yaml"), nil
}

// Resolve computes the effective configuration.
func Resolve(opts Options) (*Resolved, error) {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.HomeDir == nil {
		opts.HomeDir = os.UserHomeDir
	}
	if opts.GOOS == "" {
		opts.GOOS = goosDefault
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	home, _ := opts.HomeDir()
	r := &Resolved{getenv: opts.Getenv, stdin: opts.Stdin, home: home, goos: opts.GOOS}

	// Locate and read the config file.
	path, explicitPath := opts.ConfigPath, opts.ConfigPath != ""
	if !explicitPath {
		if p := opts.Getenv("NEXR_CONFIG"); p != "" {
			path, explicitPath = p, true
		}
	}
	if !explicitPath {
		p, err := DefaultPath(opts.GOOS, opts.Getenv, opts.HomeDir)
		if err != nil {
			return nil, errs.Wrap(errs.KindConfig, err, "cannot determine the config file location")
		}
		path = p
	}
	path = expandHome(path, home)
	r.ConfigPath = path
	var file File
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		r.ConfigExists = true
		if err := parseFile(data, &file, &r.Warnings); err != nil {
			return nil, errs.Wrap(errs.KindConfig, err, "config file %s", path)
		}
		if w := permissionWarning(path, file, opts.GOOS); w != "" {
			r.Warnings = append(r.Warnings, w)
		}
	case errors.Is(err, fs.ErrNotExist) && !explicitPath:
		// A missing default config file is fine.
	default:
		return nil, errs.Wrap(errs.KindConfig, err, "cannot read config file")
	}
	r.CurrentProfile = file.CurrentProfile
	r.ProfileURLs = map[string]string{}
	for name, p := range file.Profiles {
		switch {
		case p.URL != nil:
			r.ProfileURLs[name] = *p.URL
		case file.URL != nil:
			r.ProfileURLs[name] = *file.URL
		default:
			r.ProfileURLs[name] = ""
		}
	}

	// Select the profile.
	switch {
	case opts.Profile != "":
		r.Profile, r.ProfileOrigin, r.ProfileExplicit = opts.Profile, "flag --profile", true
	case opts.Getenv("NEXR_PROFILE") != "":
		r.Profile, r.ProfileOrigin, r.ProfileExplicit = opts.Getenv("NEXR_PROFILE"), "env NEXR_PROFILE", true
	case file.CurrentProfile != "":
		r.Profile, r.ProfileOrigin = file.CurrentProfile, "file current_profile"
	}
	var profile *Settings
	if r.Profile != "" {
		p, ok := file.Profiles[r.Profile]
		if !ok {
			e := errs.Config("profile %q (from %s) is not defined", r.Profile, r.ProfileOrigin)
			if names := r.ProfileNames(); len(names) > 0 {
				e.WithHint("available profiles: %s", strings.Join(names, ", "))
			} else {
				e.WithHint("no profiles are defined in %s", path)
			}
			return nil, e
		}
		profile = &p
	}

	// Build the layers in ascending order of precedence.
	envLayer, err := envSettings(opts.Getenv)
	if err != nil {
		return nil, err
	}
	fileLayers := []layer{{dom: domFile, name: "file", s: file.Settings}}
	if profile != nil {
		fileLayers = append(fileLayers, layer{dom: domFile, name: "profile " + r.Profile, s: *profile})
	}
	flagLayer := layer{dom: domFlag, name: "flag", s: opts.Flags, stdin: opts.PasswordStdin}
	layers := []layer{defaultLayer()}
	if r.ProfileExplicit {
		layers = append(layers, envLayer)
		layers = append(layers, fileLayers...)
	} else {
		layers = append(layers, fileLayers...)
		layers = append(layers, envLayer)
	}
	layers = append(layers, flagLayer)

	for _, l := range layers {
		if _, err := passwordSourceOf(l); err != nil {
			return nil, err
		}
	}

	r.URL = pick(layers, "url", func(s Settings) *string { return s.URL })
	r.TLSInsecure = pick(layers, "tls.insecure", func(s Settings) *bool { return s.TLS.Insecure })
	r.CAFile = pick(layers, "tls.ca_file", func(s Settings) *string { return s.TLS.CAFile })
	r.ClientCert = pick(layers, "tls.client_cert", func(s Settings) *string { return s.TLS.ClientCert })
	r.ClientKey = pick(layers, "tls.client_key", func(s Settings) *string { return s.TLS.ClientKey })
	r.Timeout = pickDuration(layers, "timeout", func(s Settings) *Duration { return s.Timeout })
	r.Retries = pick(layers, "retries", func(s Settings) *int { return s.Retries })
	r.Concurrency = pick(layers, "concurrency", func(s Settings) *int { return s.Concurrency })
	r.Output = pick(layers, "output", func(s Settings) *string { return s.Output })
	r.DockerRepository = pick(layers, "docker.repository", func(s Settings) *string { return s.Docker.Repository })
	r.DockerExclude = pick(layers, "docker.exclude", func(s Settings) *[]string { return s.Docker.Exclude })
	r.RegistryURLs, r.registryURLs = pickRegistryURLs(layers)
	if v := opts.Getenv("NEXR_DOCKER_REGISTRY_URL"); v != "" {
		r.RegistryURLOverride = Setting[string]{Value: v, Origin: "env NEXR_DOCKER_REGISTRY_URL", Set: true}
		for i, l := range layers {
			if l.dom == domEnv {
				r.registryEnv = &registryEntry{value: v, origin: r.RegistryURLOverride.Origin, rank: i, dom: domEnv}
			}
		}
	}
	r.UploadMethod = pick(layers, "upload.method", func(s Settings) *string { return s.Upload.Method })
	r.GCWaitTimeout = pickDuration(layers, "gc.wait_timeout", func(s Settings) *Duration { return s.GC.WaitTimeout })
	r.GCTasks = pick(layers, "gc.tasks", func(s Settings) *[]string { return s.GC.Tasks })

	for _, p := range []*Setting[string]{&r.CAFile, &r.ClientCert, &r.ClientKey} {
		p.Value = expandHome(p.Value, home)
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	r.scopeCredentials(layers)
	r.scopeRegistryURLs(layers)
	return r, nil
}

// scope returns a check of the scoping rule (FR-CFG-2): settings that belong
// to a server, such as credentials, are only combined with a URL from the same
// or a lower-precedence source, unless both sources name the same URL. When
// the rule forbids it, target is the URL that the domain names ("no URL" if
// none).
func (r *Resolved) scope(layers []layer) func(d domain) (ok bool, target string) {
	order := []domain{domDefault, domFile, domEnv, domFlag}
	if r.ProfileExplicit {
		order = []domain{domDefault, domEnv, domFile, domFlag}
	}
	rank := func(d domain) int {
		for i, o := range order {
			if o == d {
				return i
			}
		}
		return -1
	}
	urlDom := domDefault
	for i := len(layers) - 1; i >= 0; i-- {
		if layers[i].s.URL != nil {
			urlDom = layers[i].dom
			break
		}
	}
	domainURL := func(d domain) (string, bool) {
		for i := len(layers) - 1; i >= 0; i-- {
			if layers[i].dom == d && layers[i].s.URL != nil {
				return *layers[i].s.URL, true
			}
		}
		return "", false
	}
	return func(d domain) (bool, string) {
		if !r.URL.Set || rank(d) >= rank(urlDom) {
			return true, ""
		}
		du, ok := domainURL(d)
		if ok && SameURL(du, r.URL.Value) {
			return true, ""
		}
		if !ok {
			return false, "no URL"
		}
		return false, du
	}
}

// scopeCredentials applies the scoping rule to the user and the password.
func (r *Resolved) scopeCredentials(layers []layer) {
	check := r.scope(layers)
	allowed := func(l layer, what string) bool {
		ok, target := check(l.dom)
		if !ok {
			r.DroppedCredentials = append(r.DroppedCredentials, fmt.Sprintf(
				"ignored %s from %s: it belongs to %s, but the URL %s comes from %s",
				what, l.origin(what), target, r.URL.Value, r.URL.Origin))
		}
		return ok
	}
	for i := len(layers) - 1; i >= 0; i-- {
		l := layers[i]
		if l.s.User == nil {
			continue
		}
		if allowed(l, "user") {
			r.User = Setting[string]{Value: *l.s.User, Origin: l.origin("user"), Set: true}
			break
		}
	}
	for i := len(layers) - 1; i >= 0; i-- {
		l := layers[i]
		src, _ := passwordSourceOf(l)
		if src == nil {
			continue
		}
		if allowed(l, "password") {
			r.password = src
			break
		}
	}
}

func (r *Resolved) validate() error {
	if r.URL.Set {
		u, err := NormalizeURL(r.URL.Value)
		if err != nil {
			return errs.Wrap(errs.KindConfig, err, "invalid url from %s", r.URL.Origin)
		}
		r.URL.Value = u
	}
	switch r.Output.Value {
	case "table", "json":
	default:
		return errs.Config("invalid output %q from %s: use table or json", r.Output.Value, r.Output.Origin)
	}
	switch r.UploadMethod.Value {
	case "put", "components":
	default:
		return errs.Config("invalid upload.method %q from %s: use put or components", r.UploadMethod.Value, r.UploadMethod.Origin)
	}
	if c := r.Concurrency.Value; c < 1 || c > 32 {
		return errs.Config("invalid concurrency %d from %s: use a value from 1 to 32", c, r.Concurrency.Origin)
	}
	if n := r.Retries.Value; n < 0 || n > 10 {
		return errs.Config("invalid retries %d from %s: use a value from 0 to 10", n, r.Retries.Origin)
	}
	if r.Timeout.Value <= 0 {
		return errs.Config("invalid timeout from %s: must be positive", r.Timeout.Origin)
	}
	if r.GCWaitTimeout.Value <= 0 {
		return errs.Config("invalid gc.wait_timeout from %s: must be positive", r.GCWaitTimeout.Origin)
	}
	if r.ClientCert.Set != r.ClientKey.Set {
		return errs.Config("tls.client_cert and tls.client_key must be set together")
	}
	for repo, e := range r.registryURLs {
		u, err := NormalizeURL(e.value)
		if err != nil {
			return errs.Wrap(errs.KindConfig, err, "invalid docker.registry_urls entry for %q (from %s)", repo, e.origin)
		}
		e.value = u
		r.registryURLs[repo] = e
	}
	if e := r.registryEnv; e != nil {
		u, err := NormalizeURL(e.value)
		if err != nil {
			return errs.Wrap(errs.KindConfig, err, "invalid NEXR_DOCKER_REGISTRY_URL")
		}
		e.value = u
	}
	return nil
}

// RequireURL returns an error when no Nexus URL is configured.
func (r *Resolved) RequireURL() error {
	if r.URL.Set && r.URL.Value != "" {
		return nil
	}
	return errs.Config("no Nexus URL configured").
		WithHint("set NEXUS_URL, pass --url, or add \"url\" to %s", r.ConfigPath).
		WithHint("run \"nexr config view\" to see where settings come from")
}

// ProfileNames returns the profiles defined in the config file, sorted.
func (r *Resolved) ProfileNames() []string {
	names := make([]string, 0, len(r.ProfileURLs))
	for n := range r.ProfileURLs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// HasPassword reports whether a password source is configured.
func (r *Resolved) HasPassword() bool { return r.password != nil }

// PasswordOrigin describes where the password comes from, for example
// "profile prod, password_env PROD_PASSWORD". It never includes the password.
func (r *Resolved) PasswordOrigin() string {
	src := r.password
	if src == nil {
		return ""
	}
	switch {
	case src.kind == "env":
		return src.origin + ", password_env " + src.value
	case src.kind == "file" && strings.HasPrefix(src.origin, "env "):
		return src.origin + ", file " + src.value
	case src.kind == "file":
		return src.origin + ", password_file " + src.value
	case src.kind == "command":
		return src.origin + ", password_command"
	}
	return src.origin
}

// Password returns the password, reading it from its source on first use.
func (r *Resolved) Password() (string, error) {
	r.passwordOnce.Do(func() {
		if r.password == nil {
			return
		}
		r.passwordVal, r.passwordErr = r.readPassword(*r.password)
	})
	return r.passwordVal, r.passwordErr
}

func (r *Resolved) readPassword(src passwordSource) (string, error) {
	switch src.kind {
	case "literal":
		return src.value, nil
	case "env":
		v := r.getenv(src.value)
		if v == "" {
			return "", errs.Config("environment variable %s (from %s) is not set", src.value, src.origin)
		}
		return v, nil
	case "file":
		data, err := os.ReadFile(expandHome(src.value, r.home))
		if err != nil {
			return "", errs.Wrap(errs.KindConfig, err, "cannot read password file (from %s)", src.origin)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	case "command":
		return r.runPasswordCommand(src)
	case "stdin":
		data, err := io.ReadAll(io.LimitReader(r.stdin, 64<<10))
		if err != nil {
			return "", errs.Wrap(errs.KindUsage, err, "cannot read the password from stdin")
		}
		pw := strings.TrimRight(string(data), "\r\n")
		if pw == "" {
			return "", errs.Usage("--password-stdin was given, but stdin is empty")
		}
		return pw, nil
	}
	return "", fmt.Errorf("unknown password source %q", src.kind)
}

func (r *Resolved) runPasswordCommand(src passwordSource) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if r.goos == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", src.value)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", src.value)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return "", errs.Wrap(errs.KindConfig, err, "password_command (from %s) failed", src.origin)
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

// NormalizeURL validates a base URL and returns it in canonical form:
// lower-case scheme and host, no default port, no trailing slash.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%q must start with http:// or https://", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%q has no host", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("%q must not contain credentials; use the user and password settings", redactUserinfo(u))
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%q must not contain a query or fragment", raw)
	}
	host := strings.ToLower(u.Host)
	if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) || (u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	u.Host = host
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func redactUserinfo(u *url.URL) string {
	c := *u
	c.User = url.User("***")
	return c.String()
}

// SameURL reports whether two base URLs name the same location.
func SameURL(a, b string) bool {
	na, err1 := NormalizeURL(a)
	nb, err2 := NormalizeURL(b)
	return err1 == nil && err2 == nil && na == nb
}

func defaultLayer() layer {
	timeout := Duration{60 * time.Second}
	wait := Duration{time.Hour}
	retries, concurrency := 3, 4
	output, method := "table", "put"
	exclude := []string{"latest"}
	return layer{dom: domDefault, name: "default", s: Settings{
		Timeout: &timeout, Retries: &retries, Concurrency: &concurrency, Output: &output,
		Docker: DockerSettings{Exclude: &exclude},
		Upload: UploadSettings{Method: &method},
		GC:     GCSettings{WaitTimeout: &wait},
	}}
}

func envSettings(getenv func(string) string) (layer, error) {
	l := layer{dom: domEnv, name: "env", envSet: map[string]string{}}
	str := func(key, name string) *string {
		if v := getenv(name); v != "" {
			l.envSet[key] = name
			return &v
		}
		return nil
	}
	l.s.URL = str("url", "NEXUS_URL")
	l.s.User = str("user", "NEXUS_USER")
	l.s.TLS.CAFile = str("tls.ca_file", "NEXUS_CA_CERT")
	l.s.TLS.ClientCert = str("tls.client_cert", "NEXUS_CLIENT_CERT")
	l.s.TLS.ClientKey = str("tls.client_key", "NEXUS_CLIENT_KEY")
	l.s.Docker.Repository = str("docker.repository", "NEXR_DOCKER_REPO")
	pw, pwFile := getenv("NEXUS_PASSWORD"), getenv("NEXUS_PASSWORD_FILE")
	switch {
	case pw != "" && pwFile != "":
		return l, errs.Config("both NEXUS_PASSWORD and NEXUS_PASSWORD_FILE are set; use only one")
	case pw != "":
		l.s.Password, l.envSet["password"] = &pw, "NEXUS_PASSWORD"
	case pwFile != "":
		l.s.PasswordFile, l.envSet["password"] = &pwFile, "NEXUS_PASSWORD_FILE"
	}
	if v := getenv("NEXUS_INSECURE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return l, errs.Config("invalid NEXUS_INSECURE value %q: use true or false", v)
		}
		l.s.TLS.Insecure, l.envSet["tls.insecure"] = &b, "NEXUS_INSECURE"
	}
	return l, nil
}

func passwordSourceOf(l layer) (*passwordSource, error) {
	if l.dom == domFlag && l.stdin {
		if l.s.Password != nil {
			return nil, errs.Usage("--password and --password-stdin cannot be used together")
		}
		return &passwordSource{kind: "stdin", origin: "flag --password-stdin"}, nil
	}
	keys := l.s.passwordKeys()
	switch len(keys) {
	case 0:
		return nil, nil
	case 1:
	default:
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k[0]
		}
		return nil, errs.Config("%s sets more than one password source (%s); use only one", l.name, strings.Join(names, ", "))
	}
	kind := map[string]string{"password": "literal", "password_env": "env", "password_file": "file", "password_command": "command"}[keys[0][0]]
	return &passwordSource{kind: kind, value: keys[0][1], origin: l.origin("password")}, nil
}

func pick[T any](layers []layer, key string, get func(Settings) *T) Setting[T] {
	for i := len(layers) - 1; i >= 0; i-- {
		if p := get(layers[i].s); p != nil {
			return Setting[T]{Value: *p, Origin: layers[i].origin(key), Set: true}
		}
	}
	return Setting[T]{}
}

func pickDuration(layers []layer, key string, get func(Settings) *Duration) Setting[time.Duration] {
	s := pick(layers, key, get)
	return Setting[time.Duration]{Value: s.Value.Duration, Origin: s.Origin, Set: s.Set}
}

// registryEntry is a registry endpoint with the source that set it.
type registryEntry struct {
	value   string
	origin  string
	rank    int    // position of the source in the layers; higher wins
	dom     domain // domain of the source
	dropped string // why the scoping rule ignores the entry
}

// pickRegistryURLs merges docker.registry_urls per repository: each entry
// comes from the highest-precedence source that sets it.
func pickRegistryURLs(layers []layer) (Setting[map[string]string], map[string]registryEntry) {
	out := Setting[map[string]string]{Value: map[string]string{}}
	entries := map[string]registryEntry{}
	for i, l := range layers {
		if len(l.s.Docker.RegistryURLs) == 0 {
			continue
		}
		for k, v := range l.s.Docker.RegistryURLs {
			out.Value[k] = v
			entries[k] = registryEntry{value: v, origin: l.origin("docker.registry_urls"), rank: i, dom: l.dom}
		}
		out.Origin, out.Set = l.origin("docker.registry_urls"), true
	}
	return out, entries
}

// scopeRegistryURLs applies the scoping rule to registry endpoints: an
// endpoint from a source with lower precedence than the Nexus URL may belong
// to another server.
func (r *Resolved) scopeRegistryURLs(layers []layer) {
	check := r.scope(layers)
	drop := func(e *registryEntry, what string) {
		if ok, target := check(e.dom); !ok {
			e.dropped = fmt.Sprintf("ignored %s from %s: it belongs to %s, but the URL %s comes from %s",
				what, e.origin, target, r.URL.Value, r.URL.Origin)
		}
	}
	for repo, e := range r.registryURLs {
		drop(&e, fmt.Sprintf("the registry URL %s of %s", e.value, repo))
		r.registryURLs[repo] = e
	}
	if r.registryEnv != nil {
		drop(r.registryEnv, "the registry URL "+r.registryEnv.value)
	}
}

// RegistryURL returns the registry endpoint configured for a repository (spec
// FR-NET-3): from an explicitly selected profile, NEXR_DOCKER_REGISTRY_URL
// (which applies to the repository a command works on), or the config file,
// in the order of precedence of §5.1. It is not Set when the default
// <url>/repository/REPO/ applies; the --registry-url flag is up to the
// caller. Notes explain endpoints that the scoping rule ignored.
func (r *Resolved) RegistryURL(repo string) (endpoint Setting[string], notes []string) {
	var cands []registryEntry
	if e, ok := r.registryURLs[repo]; ok {
		cands = append(cands, e)
	}
	if r.registryEnv != nil {
		cands = append(cands, *r.registryEnv)
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rank > cands[j].rank })
	for _, e := range cands {
		if e.dropped != "" {
			notes = append(notes, e.dropped)
			continue
		}
		return Setting[string]{Value: e.value, Origin: e.origin, Set: true}, notes
	}
	return Setting[string]{}, notes
}

// RepositoriesForHost returns the repositories whose configured registry
// endpoint is at host (host or host:port), for image references that start
// with a registry host (FR-IMGREF-3). selected reports that the endpoint from
// NEXR_DOCKER_REGISTRY_URL, which belongs to the selected repository, matches.
// Notes explain matching endpoints that the scoping rule ignored.
func (r *Resolved) RepositoriesForHost(host string) (repos []string, selected bool, notes []string) {
	want := HostOf("//" + host)
	for repo, e := range r.registryURLs {
		switch {
		case HostOf(e.value) != want:
		case e.dropped != "":
			notes = append(notes, e.dropped)
		default:
			repos = append(repos, repo)
		}
	}
	sort.Strings(repos)
	sort.Strings(notes)
	if e := r.registryEnv; e != nil && HostOf(e.value) == want {
		if e.dropped != "" {
			notes = append(notes, e.dropped)
		} else {
			selected = true
		}
	}
	return repos, selected, notes
}

// HostOf returns the lower-case host[:port] of a URL, without a default port.
// It accepts "//host:port" for a bare host.
func HostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"), u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = host[:strings.LastIndex(host, ":")]
	}
	return host
}

func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

func permissionWarning(path string, f File, goos string) string {
	if goos == "windows" {
		return ""
	}
	hasPassword := f.Password != nil
	for _, p := range f.Profiles {
		hasPassword = hasPassword || p.Password != nil
	}
	if !hasPassword {
		return ""
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm()&0o077 == 0 {
		return ""
	}
	return fmt.Sprintf("config file %s contains a password and is readable by other users; run: chmod 600 %s", path, path)
}

func parseFile(data []byte, f *File, warnings *[]string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return nil // empty file
	}
	if err := doc.Decode(f); err != nil {
		return err
	}
	unknownKeys(doc.Content[0], fileSchema, "", warnings)
	return nil
}

// schema describes the allowed keys; a nil value is a leaf, "*" allows any key.
type schema map[string]schema

var settingsSchema = schema{
	"url": nil, "user": nil, "password": nil, "password_env": nil, "password_file": nil, "password_command": nil,
	"tls":     {"insecure": nil, "ca_file": nil, "client_cert": nil, "client_key": nil},
	"timeout": nil, "retries": nil, "concurrency": nil, "output": nil,
	"docker": {"repository": nil, "exclude": nil, "registry_urls": {"*": nil}},
	"upload": {"method": nil},
	"gc":     {"wait_timeout": nil, "tasks": nil},
}

var fileSchema = func() schema {
	s := schema{"current_profile": nil, "profiles": {"*": settingsSchema}}
	for k, v := range settingsSchema {
		s[k] = v
	}
	return s
}()

func unknownKeys(n *yaml.Node, sc schema, prefix string, out *[]string) {
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		child, ok := sc[k.Value]
		if !ok {
			child, ok = sc["*"]
		}
		if !ok {
			*out = append(*out, fmt.Sprintf("unknown config key %q (line %d)", prefix+k.Value, k.Line))
			continue
		}
		if child != nil {
			unknownKeys(v, child, prefix+k.Value+".", out)
		}
	}
}
