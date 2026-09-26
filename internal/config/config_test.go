package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

const testFile = `
current_profile: prod
timeout: 30s
user: alice
docker:
  registry_urls:
    docker-hosted: https://registry.example.com
profiles:
  prod:
    url: https://prod.example.com
    password: prod-secret
    docker:
      repository: docker-prod
  staging:
    url: https://staging.example.com/nexus/
    user: bob
    password_env: STAGING_PW
    docker:
      registry_urls:
        docker-staging: https://registry-staging.example.com
  empty: {}
`

type env map[string]string

func (e env) get(k string) string { return e[k] }

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func resolve(t *testing.T, opts Options) *Resolved {
	t.Helper()
	r, err := Resolve(opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return r
}

func baseOpts(path string, e env) Options {
	return Options{
		ConfigPath: path,
		Getenv:     e.get,
		HomeDir:    func() (string, error) { return "/home/test", nil },
		GOOS:       "linux",
		Stdin:      strings.NewReader(""),
	}
}

func TestDefaults(t *testing.T) {
	o := baseOpts("", env{"XDG_CONFIG_HOME": t.TempDir()})
	r := resolve(t, o)
	if r.ConfigExists || r.URL.Set || r.User.Set || r.HasPassword() {
		t.Fatalf("unexpected values: %+v", r)
	}
	if r.Timeout.Value != 60*time.Second || r.Timeout.Origin != "default" {
		t.Errorf("timeout = %v (%s)", r.Timeout.Value, r.Timeout.Origin)
	}
	if r.Retries.Value != 3 || r.Concurrency.Value != 4 || r.Output.Value != "table" ||
		r.UploadMethod.Value != "put" || r.GCWaitTimeout.Value != time.Hour {
		t.Errorf("defaults: %+v", r)
	}
	if got := r.DockerExclude.Value; len(got) != 1 || got[0] != "latest" {
		t.Errorf("docker.exclude = %v", got)
	}
	if err := r.RequireURL(); errs.Classify(err) != errs.KindConfig {
		t.Errorf("RequireURL() = %v, want config error", err)
	}
}

func TestPrecedence(t *testing.T) {
	path := writeConfig(t, testFile)
	tests := []struct {
		name       string
		profile    string
		env        env
		flags      Settings
		wantURL    string
		wantOrigin string
		wantRepo   string
	}{
		{"current profile", "", env{}, Settings{}, "https://prod.example.com", "profile prod", "docker-prod"},
		{"env beats default profile", "", env{"NEXUS_URL": "https://env.example.com"}, Settings{}, "https://env.example.com", "env NEXUS_URL", "docker-prod"},
		{"explicit profile beats env", "staging", env{"NEXUS_URL": "https://env.example.com"}, Settings{}, "https://staging.example.com/nexus", "profile staging", ""},
		{"NEXR_PROFILE is explicit", "", env{"NEXR_PROFILE": "staging", "NEXUS_URL": "https://env.example.com"}, Settings{}, "https://staging.example.com/nexus", "profile staging", ""},
		{"flag beats everything", "staging", env{"NEXUS_URL": "https://env.example.com"}, Settings{URL: strp("https://flag.example.com")}, "https://flag.example.com", "flag --url", ""},
		{"env repo beats default profile", "", env{"NEXR_DOCKER_REPO": "docker-env"}, Settings{}, "https://prod.example.com", "profile prod", "docker-env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := baseOpts(path, tt.env)
			o.Profile, o.Flags = tt.profile, tt.flags
			r := resolve(t, o)
			if r.URL.Value != tt.wantURL || r.URL.Origin != tt.wantOrigin {
				t.Errorf("url = %q (%s), want %q (%s)", r.URL.Value, r.URL.Origin, tt.wantURL, tt.wantOrigin)
			}
			if r.DockerRepository.Value != tt.wantRepo {
				t.Errorf("docker.repository = %q, want %q", r.DockerRepository.Value, tt.wantRepo)
			}
			if r.Timeout.Value != 30*time.Second || r.Timeout.Origin != "file" {
				t.Errorf("timeout = %v (%s), want 30s from file", r.Timeout.Value, r.Timeout.Origin)
			}
		})
	}
}

// TestCredentialScoping covers every row of the table in FR-CFG-2.
func TestCredentialScoping(t *testing.T) {
	path := writeConfig(t, testFile)
	tests := []struct {
		name        string
		profile     string
		env         env
		flags       Settings
		wantUser    string
		wantUserOr  string
		wantPwOr    string
		wantDropped int
	}{
		{
			name:       "url from file, credentials from env",
			env:        env{"NEXUS_USER": "ci", "NEXUS_PASSWORD": "ci-secret"},
			wantUser:   "ci",
			wantUserOr: "env NEXUS_USER",
			wantPwOr:   "env NEXUS_PASSWORD",
		},
		{
			name:        "url from env, credentials from file with another url",
			env:         env{"NEXUS_URL": "https://other.example.com"},
			wantDropped: 2,
		},
		{
			name:       "url from env, credentials from file with the same url",
			env:        env{"NEXUS_URL": "HTTPS://PROD.example.com:443/"},
			wantUser:   "alice",
			wantUserOr: "file",
			wantPwOr:   "profile prod",
		},
		{
			// No URL is configured anywhere, so there is nothing to protect:
			// the file's user wins (explicit profile ranks above env) and the
			// only password comes from env.
			name:       "explicit profile without any url",
			profile:    "empty",
			env:        env{"NEXUS_USER": "ci", "NEXUS_PASSWORD": "ci-secret"},
			wantUser:   "alice",
			wantUserOr: "file",
			wantPwOr:   "env NEXUS_PASSWORD",
		},
		{
			name:        "explicit profile with url, credentials from env with another url",
			profile:     "staging",
			env:         env{"NEXUS_URL": "https://env.example.com", "NEXUS_USER": "ci", "NEXUS_PASSWORD": "ci-secret", "STAGING_PW": "x"},
			wantUser:    "bob",
			wantUserOr:  "profile staging",
			wantPwOr:    "profile staging, password_env STAGING_PW",
			wantDropped: 0,
		},
		{
			name:       "explicit profile, env credentials for the same url",
			profile:    "staging",
			env:        env{"NEXUS_URL": "https://staging.example.com/nexus", "NEXUS_USER": "ci"},
			wantUser:   "bob",
			wantUserOr: "profile staging",
			wantPwOr:   "profile staging, password_env STAGING_PW",
		},
		{
			name:       "flag credentials are always used",
			env:        env{"NEXUS_URL": "https://env.example.com"},
			flags:      Settings{User: strp("flaguser"), Password: strp("flagpw")},
			wantUser:   "flaguser",
			wantUserOr: "flag --user",
			wantPwOr:   "flag --password",
		},
		{
			name:        "flag url, env credentials without url are dropped",
			env:         env{"NEXUS_USER": "ci", "NEXUS_PASSWORD": "ci-secret"},
			flags:       Settings{URL: strp("https://flag.example.com")},
			wantDropped: 4, // env and file user, env and file password
		},
		{
			name:        "flag url equal to the file url falls back to file credentials",
			env:         env{"NEXUS_USER": "ci"},
			flags:       Settings{URL: strp("https://prod.example.com")},
			wantUser:    "alice",
			wantUserOr:  "file",
			wantPwOr:    "profile prod",
			wantDropped: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := baseOpts(path, tt.env)
			o.Profile, o.Flags = tt.profile, tt.flags
			r := resolve(t, o)
			if r.User.Value != tt.wantUser || r.User.Origin != tt.wantUserOr {
				t.Errorf("user = %q (%s), want %q (%s)", r.User.Value, r.User.Origin, tt.wantUser, tt.wantUserOr)
			}
			if r.PasswordOrigin() != tt.wantPwOr {
				t.Errorf("password origin = %q, want %q", r.PasswordOrigin(), tt.wantPwOr)
			}
			if len(r.DroppedCredentials) != tt.wantDropped {
				t.Errorf("dropped = %q, want %d entries", r.DroppedCredentials, tt.wantDropped)
			}
		})
	}
}

func TestProfileErrors(t *testing.T) {
	path := writeConfig(t, testFile)
	o := baseOpts(path, env{})
	o.Profile = "nope"
	_, err := Resolve(o)
	if errs.Classify(err) != errs.KindConfig {
		t.Fatalf("err = %v, want config error", err)
	}
	if hints := errs.HintsOf(err); len(hints) != 1 || !strings.Contains(hints[0], "empty, prod, staging") {
		t.Fatalf("hints = %q", hints)
	}

	o = baseOpts(filepath.Join(t.TempDir(), "missing.yaml"), env{})
	if _, err := Resolve(o); errs.Classify(err) != errs.KindConfig {
		t.Fatalf("missing explicit config: err = %v, want config error", err)
	}
	o = baseOpts("", env{"NEXR_CONFIG": filepath.Join(t.TempDir(), "missing.yaml")})
	if _, err := Resolve(o); errs.Classify(err) != errs.KindConfig {
		t.Fatalf("missing NEXR_CONFIG: err = %v, want config error", err)
	}
}

func TestPasswordSources(t *testing.T) {
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "pw")
	if err := os.WriteFile(pwFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		file    string
		env     env
		stdin   string
		flags   Settings
		useIn   bool
		want    string
		wantErr errs.Kind
	}{
		{name: "literal", file: "url: https://n.example.com\npassword: lit\n", want: "lit"},
		{name: "password_env", file: "url: https://n.example.com\npassword_env: MY_PW\n", env: env{"MY_PW": "from-env"}, want: "from-env"},
		{name: "password_env unset", file: "url: https://n.example.com\npassword_env: MY_PW\n", wantErr: errs.KindConfig},
		{name: "password_file", file: "url: https://n.example.com\npassword_file: " + pwFile + "\n", want: "from-file"},
		{name: "NEXUS_PASSWORD_FILE", env: env{"NEXUS_PASSWORD_FILE": pwFile}, want: "from-file"},
		{name: "stdin", useIn: true, stdin: "piped\n", want: "piped"},
		{name: "empty stdin", useIn: true, wantErr: errs.KindUsage},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct {
			name    string
			file    string
			env     env
			stdin   string
			flags   Settings
			useIn   bool
			want    string
			wantErr errs.Kind
		}{name: "password_command", file: "url: https://n.example.com\npassword_command: printf 'from-cmd\\n'\n", want: "from-cmd"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.file)
			o := baseOpts(path, tt.env)
			o.GOOS = runtime.GOOS
			o.Flags, o.PasswordStdin, o.Stdin = tt.flags, tt.useIn, strings.NewReader(tt.stdin)
			r := resolve(t, o)
			got, err := r.Password()
			if tt.wantErr != errs.KindGeneric {
				if errs.Classify(err) != tt.wantErr {
					t.Fatalf("Password() err = %v, want kind %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Password() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestPasswordOriginDetails(t *testing.T) {
	tests := []struct {
		file string
		env  env
		want string
	}{
		{"url: https://n.example.com\npassword: x\n", nil, "file"},
		{"url: https://n.example.com\npassword_env: MY_PW\n", nil, "file, password_env MY_PW"},
		{"url: https://n.example.com\npassword_file: /run/pw\n", nil, "file, password_file /run/pw"},
		{"url: https://n.example.com\npassword_command: pass show nexus\n", nil, "file, password_command"},
		{"", env{"NEXUS_URL": "https://n.example.com", "NEXUS_PASSWORD_FILE": "/run/pw"}, "env NEXUS_PASSWORD_FILE, file /run/pw"},
		{"", env{"NEXUS_URL": "https://n.example.com", "NEXUS_PASSWORD": "x"}, "env NEXUS_PASSWORD"},
		{"", env{"NEXUS_URL": "https://n.example.com"}, ""},
	}
	for _, tt := range tests {
		r := resolve(t, baseOpts(writeConfig(t, tt.file), tt.env))
		if got := r.PasswordOrigin(); got != tt.want {
			t.Errorf("PasswordOrigin() = %q, want %q", got, tt.want)
		}
		for _, e := range r.Entries() {
			if e.Key == "password" && e.Origin != tt.want {
				t.Errorf("config view source = %q, want %q", e.Origin, tt.want)
			}
		}
	}
}

func TestPasswordConflicts(t *testing.T) {
	path := writeConfig(t, "profiles:\n  a:\n    password: x\n    password_env: Y\ncurrent_profile: a\n")
	if _, err := Resolve(baseOpts(path, env{})); errs.Classify(err) != errs.KindConfig {
		t.Errorf("two password keys: err = %v, want config error", err)
	}
	o := baseOpts("", env{"XDG_CONFIG_HOME": t.TempDir(), "NEXUS_PASSWORD": "a", "NEXUS_PASSWORD_FILE": "b"})
	if _, err := Resolve(o); errs.Classify(err) != errs.KindConfig {
		t.Errorf("both env passwords: err = %v, want config error", err)
	}
	o = baseOpts("", env{"XDG_CONFIG_HOME": t.TempDir()})
	o.Flags.Password, o.PasswordStdin = strp("x"), true
	if _, err := Resolve(o); errs.Classify(err) != errs.KindUsage {
		t.Errorf("--password and --password-stdin: err = %v, want usage error", err)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct{ name, file string }{
		{"bad output", "output: yaml\n"},
		{"bad concurrency", "concurrency: 0\n"},
		{"bad retries", "retries: 11\n"},
		{"bad upload method", "upload:\n  method: scp\n"},
		{"bad scheme", "url: ftp://nexus.example.com\n"},
		{"credentials in url", "url: https://user:pw@nexus.example.com\n"},
		{"bad duration", "timeout: forever\n"},
		{"client cert without key", "tls:\n  client_cert: /c.pem\n"},
		{"bad registry url", "docker:\n  registry_urls:\n    r: registry.example.com\n"},
		{"invalid yaml", "url: [\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(baseOpts(writeConfig(t, tt.file), env{}))
			if errs.Classify(err) != errs.KindConfig {
				t.Fatalf("err = %v, want config error", err)
			}
		})
	}
	o := baseOpts("", env{"XDG_CONFIG_HOME": t.TempDir(), "NEXUS_INSECURE": "maybe"})
	if _, err := Resolve(o); errs.Classify(err) != errs.KindConfig {
		t.Errorf("NEXUS_INSECURE=maybe: err = %v, want config error", err)
	}
}

func TestWarnings(t *testing.T) {
	path := writeConfig(t, "url: https://n.example.com\npasword: typo\ntls:\n  insecure_skip: true\nprofiles:\n  a:\n    dockr: {}\n")
	r := resolve(t, baseOpts(path, env{}))
	want := []string{`"pasword" (line 2)`, `"tls.insecure_skip" (line 4)`, `"profiles.a.dockr" (line 7)`}
	if len(r.Warnings) != len(want) {
		t.Fatalf("warnings = %q", r.Warnings)
	}
	for i, w := range want {
		if !strings.Contains(r.Warnings[i], w) {
			t.Errorf("warning %d = %q, want it to mention %s", i, r.Warnings[i], w)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	path = writeConfig(t, "url: https://n.example.com\npassword: secret\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	r = resolve(t, baseOpts(path, env{}))
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "chmod 600") {
		t.Fatalf("warnings = %q, want permission warning", r.Warnings)
	}
}

func TestRegistryURLsAndExpansion(t *testing.T) {
	path := writeConfig(t, testFile+"\ntls:\n  ca_file: ~/ca.pem\n")
	o := baseOpts(path, env{"NEXR_DOCKER_REGISTRY_URL": "https://override.example.com"})
	o.Profile = "staging"
	r := resolve(t, o)
	want := map[string]string{"docker-hosted": "https://registry.example.com", "docker-staging": "https://registry-staging.example.com"}
	if len(r.RegistryURLs.Value) != 2 || r.RegistryURLs.Value["docker-hosted"] != want["docker-hosted"] ||
		r.RegistryURLs.Value["docker-staging"] != want["docker-staging"] {
		t.Errorf("registry_urls = %v", r.RegistryURLs.Value)
	}
	if r.RegistryURLOverride.Value != "https://override.example.com" {
		t.Errorf("override = %+v", r.RegistryURLOverride)
	}
	if r.CAFile.Value != filepath.Join("/home/test", "ca.pem") {
		t.Errorf("ca_file = %q", r.CAFile.Value)
	}
}

func TestEntriesRedactPassword(t *testing.T) {
	path := writeConfig(t, testFile)
	r := resolve(t, baseOpts(path, env{}))
	for _, e := range r.Entries() {
		if strings.Contains(e.Value, "prod-secret") {
			t.Fatalf("entry %q leaks the password", e.Key)
		}
		if e.Key == "password" && (e.Value != "***" || e.Origin != "profile prod") {
			t.Fatalf("password entry = %+v", e)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"https://Nexus.Example.com/", "https://nexus.example.com"},
		{"HTTPS://nexus.example.com:443/x/", "https://nexus.example.com/x"},
		{"http://nexus.example.com:80", "http://nexus.example.com"},
		{"http://nexus.example.com:8081/", "http://nexus.example.com:8081"},
		{" https://nexus.example.com/nexus ", "https://nexus.example.com/nexus"},
	} {
		got, err := NormalizeURL(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"nexus.example.com", "https://", "https://a.example.com/?q=1", "ftp://x.example.com"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) succeeded", bad)
		}
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"90s": 90 * time.Second, "36h": 36 * time.Hour, "30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "1h30m": 90 * time.Minute} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "3x", "-1d5"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) succeeded", bad)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	home := func() (string, error) { return "/home/u", nil }
	if p, _ := DefaultPath("linux", env{}.get, home); p != filepath.Join("/home/u", ".config", "nexr", "config.yaml") {
		t.Errorf("linux default = %q", p)
	}
	if p, _ := DefaultPath("darwin", env{"XDG_CONFIG_HOME": "/xdg"}.get, home); p != filepath.Join("/xdg", "nexr", "config.yaml") {
		t.Errorf("xdg = %q", p)
	}
	if p, _ := DefaultPath("windows", env{"AppData": `C:\Users\u\AppData\Roaming`}.get, home); !strings.HasSuffix(p, filepath.Join("nexr", "config.yaml")) {
		t.Errorf("windows = %q", p)
	}
	if _, err := DefaultPath("linux", env{}.get, func() (string, error) { return "", errors.New("no home") }); err == nil {
		t.Error("expected error without home directory")
	}
}

func TestInsecureFromEnv(t *testing.T) {
	o := baseOpts("", env{"XDG_CONFIG_HOME": t.TempDir(), "NEXUS_INSECURE": "true"})
	r := resolve(t, o)
	if !r.TLSInsecure.Value || r.TLSInsecure.Origin != "env NEXUS_INSECURE" {
		t.Fatalf("insecure = %+v", r.TLSInsecure)
	}
	o.Flags.TLS.Insecure = boolp(false)
	r = resolve(t, o)
	if r.TLSInsecure.Value || r.TLSInsecure.Origin != "flag --insecure" {
		t.Fatalf("insecure = %+v", r.TLSInsecure)
	}
}
