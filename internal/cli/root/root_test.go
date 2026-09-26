package root_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/cli/root"
	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
	"github.com/yand3r3d3v/nexr/internal/output"
)

// invocation describes how nexr is run in a test.
type invocation struct {
	env       map[string]string
	stdin     string
	ctx       context.Context
	transport http.RoundTripper
}

type result struct {
	code   int
	stdout string
	stderr string
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\n--- stdout\n%s--- stderr\n%s", r.code, r.stdout, r.stderr)
}

func (inv invocation) run(t *testing.T, args ...string) result {
	t.Helper()
	ios, in, out, errOut := output.Test()
	in.WriteString(inv.stdin)
	f := cmdutil.New(ios)
	f.Getenv = func(k string) string { return inv.env[k] }
	f.Transport = inv.transport
	ctx := inv.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	code := root.Main(ctx, args, f)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// env returns an environment with an empty config directory plus kv pairs.
// The directory is the config home on every platform.
func env(t *testing.T, kv ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	e := map[string]string{"XDG_CONFIG_HOME": dir, "AppData": dir}
	for i := 0; i+1 < len(kv); i += 2 {
		e[kv[i]] = kv[i+1]
	}
	return e
}

// fixture starts a fake Nexus with a few repositories and a non-admin user.
func fixture(t *testing.T) *nexustest.Server {
	t.Helper()
	fake := nexustest.New(t)
	fake.AddUser("reader", "reader-pw", false)
	fake.AddRepo(nexustest.Repo{Name: "raw-hosted", Format: "raw", Type: "hosted", Online: true,
		Settings: map[string]any{
			"storage": map[string]any{"blobStoreName": "default", "strictContentTypeValidation": true, "writePolicy": "ALLOW"},
			"cleanup": map[string]any{"policyNames": []any{"weekly", "daily"}},
		}})
	fake.AddRepo(nexustest.Repo{Name: "docker-hosted", Format: "docker", Type: "hosted", Online: true,
		Settings: map[string]any{"docker": map[string]any{"v1Enabled": false, "forceBasicAuth": true, "httpPort": 8082}}})
	fake.AddRepo(nexustest.Repo{Name: "maven-central", Format: "maven2", Type: "proxy", Online: true,
		Settings: map[string]any{"proxy": map[string]any{"remoteUrl": "https://repo1.maven.org/maven2/"}}})
	fake.AddRepo(nexustest.Repo{Name: "maven-public", Format: "maven2", Type: "group", Online: true,
		Settings: map[string]any{"group": map[string]any{"memberNames": []any{"maven-releases", "maven-central"}}}})
	return fake
}

func admin(t *testing.T, fake *nexustest.Server) invocation {
	return invocation{env: env(t, "NEXUS_URL", fake.BaseURL(), "NEXUS_USER", "admin", "NEXUS_PASSWORD", "admin123")}
}

func mustContain(t *testing.T, r result, stream, want string) {
	t.Helper()
	got := r.stdout
	if stream == "stderr" {
		got = r.stderr
	}
	if !strings.Contains(got, want) {
		t.Fatalf("%s does not contain %q\n%s", stream, want, r)
	}
}

func mustExit(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("want exit %d\n%s", code, r)
	}
}

func decode[T any](t *testing.T, r result, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, r)
	}
	return v
}

func TestVersion(t *testing.T) {
	inv := invocation{env: env(t)}

	r := inv.run(t, "version")
	mustExit(t, r, 0)
	if !strings.HasPrefix(r.stdout, "nexr ") {
		t.Fatalf("unexpected output\n%s", r)
	}
	mustContain(t, r, "stdout", "platform: ")

	r = inv.run(t, "version", "--json")
	mustExit(t, r, 0)
	v := decode[map[string]string](t, r, r.stdout)
	if v["version"] == "" || v["go_version"] == "" || v["platform"] == "" {
		t.Fatalf("incomplete version JSON\n%s", r)
	}

	r = inv.run(t, "--version")
	mustExit(t, r, 0)
	if r.stdout != "nexr "+v["version"]+"\n" {
		t.Fatalf("--version printed %q", r.stdout)
	}
}

func TestNoColor(t *testing.T) {
	ios, _, _, _ := output.Test()
	ios.SetTTY(false, true, true)
	f := cmdutil.New(ios)
	e := env(t)
	f.Getenv = func(k string) string { return e[k] }
	if !ios.ColorEnabled() {
		t.Fatal("colour must be enabled on a terminal")
	}
	if code := root.Main(context.Background(), []string{"--no-color", "version"}, f); code != 0 || ios.ColorEnabled() {
		t.Fatalf("exit %d; --no-color must disable colour", code)
	}
}

func TestHelp(t *testing.T) {
	inv := invocation{env: env(t)}
	for _, args := range [][]string{nil, {"--help"}, {"-v"}, {"config"}, {"help", "repos"}} {
		r := inv.run(t, args...)
		mustExit(t, r, 0)
		mustContain(t, r, "stdout", "Usage:")
		if n := strings.Count(r.stdout, "Usage:"); n != 1 {
			t.Fatalf("%v printed the help %d times", args, n)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	inv := invocation{env: env(t)}
	tests := []struct {
		args []string
		want string
		hint string
	}{
		{[]string{"bogus"}, `unknown command "bogus" for "nexr"`, `run "nexr --help"`},
		{[]string{"stats"}, `unknown command "stats"`, `did you mean "nexr status"?`},
		{[]string{"config", "vew"}, `unknown command "vew"`, `did you mean "nexr config view"?`},
		{[]string{"repos", "shw", "raw"}, `unknown command "shw"`, `did you mean "nexr repos show"?`},
		{[]string{"repos", "--bogus"}, "unknown flag: --bogus", `run "nexr repos --help"`},
		{[]string{"repos", "show"}, "missing argument REPO", "usage: nexr repos show REPO"},
		{[]string{"repos", "show", "a", "b"}, `unexpected argument "b"`, "usage: nexr repos show REPO"},
		{[]string{"status", "extra"}, `unexpected argument "extra" for "nexr status"`, `run "nexr status --help"`},
		{[]string{"repos", "--type", "bogus"}, `invalid --type "bogus": use hosted, proxy or group`, ""},
		{[]string{"repos", "--match", "re:("}, "invalid --match", ""},
		{[]string{"--timeout", "soon", "status"}, `invalid argument "soon"`, `run "nexr status --help"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			r := inv.run(t, tt.args...)
			mustExit(t, r, 2)
			mustContain(t, r, "stderr", "nexr: "+tt.want)
			if tt.hint != "" {
				mustContain(t, r, "stderr", "hint: "+tt.hint)
			}
			if r.stdout != "" {
				t.Fatalf("usage errors must not write to stdout\n%s", r)
			}
		})
	}
}

func TestErrorsAsJSON(t *testing.T) {
	r := invocation{env: env(t)}.run(t, "--json", "bogus")
	mustExit(t, r, 2)
	doc := decode[output.ErrorJSON](t, r, r.stderr)
	if doc.Error.Code != "usage" || doc.Error.ExitCode != 2 || len(doc.Error.Hints) == 0 || doc.Error.HTTPStatus != nil {
		t.Fatalf("unexpected error document %+v", doc)
	}

	fake := fixture(t)
	r = invocation{env: env(t, "NEXUS_URL", fake.BaseURL(), "NEXUS_USER", "admin", "NEXUS_PASSWORD", "wrong")}.
		run(t, "repos", "--json")
	mustExit(t, r, 4)
	doc = decode[output.ErrorJSON](t, r, r.stderr)
	if doc.Error.Code != "auth" || doc.Error.HTTPStatus == nil || *doc.Error.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("unexpected error document %+v", doc)
	}
}

func TestMissingURL(t *testing.T) {
	r := invocation{env: env(t)}.run(t, "repos")
	mustExit(t, r, 3)
	mustContain(t, r, "stderr", "nexr: no Nexus URL configured")
	mustContain(t, r, "stderr", "hint: set NEXUS_URL")
}

func TestReposList(t *testing.T) {
	fake := fixture(t)
	inv := admin(t, fake)

	r := inv.run(t, "repos")
	mustExit(t, r, 0)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 5 || !strings.HasPrefix(lines[0], "NAME") || !strings.Contains(lines[0], "FORMAT") {
		t.Fatalf("unexpected table\n%s", r)
	}
	for i, name := range []string{"docker-hosted", "maven-central", "maven-public", "raw-hosted"} {
		if !strings.HasPrefix(lines[i+1], name+" ") {
			t.Fatalf("row %d is not %s\n%s", i+1, name, r)
		}
	}
	for _, alias := range [][]string{{"repos", "ls"}, {"repos", "list"}} {
		if again := inv.run(t, alias...); again.stdout != r.stdout {
			t.Fatalf("%v differs from repos\n%s", alias, again)
		}
	}

	r = inv.run(t, "repos", "-q")
	mustExit(t, r, 0)
	if r.stdout != "docker-hosted\nmaven-central\nmaven-public\nraw-hosted\n" {
		t.Fatalf("-q printed %q", r.stdout)
	}

	r = inv.run(t, "repos", "--json")
	mustExit(t, r, 0)
	list := decode[[]map[string]any](t, r, r.stdout)
	if len(list) != 4 || list[0]["name"] != "docker-hosted" || list[0]["online"] != true ||
		list[0]["url"] != fake.BaseURL()+"/repository/docker-hosted" {
		t.Fatalf("unexpected JSON\n%s", r)
	}

	filters := []struct {
		args []string
		want string
	}{
		{[]string{"--format", "maven2", "--type", "group"}, "maven-public\n"},
		{[]string{"--format", "RAW"}, "raw-hosted\n"},
		{[]string{"--match", "maven-*"}, "maven-central\nmaven-public\n"},
		{[]string{"--match", "re:^(raw|docker)-"}, "docker-hosted\nraw-hosted\n"},
		{[]string{"--type", "proxy"}, "maven-central\n"},
		{[]string{"--format", "npm"}, ""},
	}
	for _, tt := range filters {
		r := inv.run(t, append([]string{"repos", "-q"}, tt.args...)...)
		mustExit(t, r, 0)
		if r.stdout != tt.want {
			t.Errorf("%v printed %q, want %q", tt.args, r.stdout, tt.want)
		}
	}

	r = inv.run(t, "repos", "--format", "npm", "--json")
	if r.stdout != "[]\n" {
		t.Fatalf("an empty result must be an empty JSON array\n%s", r)
	}
}

func TestReposShow(t *testing.T) {
	fake := fixture(t)
	inv := admin(t, fake)

	r := inv.run(t, "repos", "show", "raw-hosted")
	mustExit(t, r, 0)
	for _, want := range []string{"Name:", "raw-hosted", "Blob store:", "default", "Write policy:", "ALLOW",
		"Strict content validation:", "Cleanup policies:", "daily, weekly", "Online:"} {
		mustContain(t, r, "stdout", want)
	}

	r = inv.run(t, "repos", "show", "maven-public")
	mustContain(t, r, "stdout", "maven-central, maven-releases")
	r = inv.run(t, "repos", "show", "maven-central")
	mustContain(t, r, "stdout", "https://repo1.maven.org/maven2/")
	r = inv.run(t, "repos", "show", "docker-hosted")
	mustContain(t, r, "stdout", "Docker HTTP port:")
	mustContain(t, r, "stdout", "8082")

	r = inv.run(t, "repos", "show", "docker-hosted", "--json")
	mustExit(t, r, 0)
	doc := decode[struct {
		Name     string `json:"name"`
		Settings struct {
			Docker map[string]any `json:"docker"`
		} `json:"settings"`
	}](t, r, r.stdout)
	if doc.Name != "docker-hosted" || doc.Settings.Docker["httpPort"] != float64(8082) {
		t.Fatalf("unexpected JSON\n%s", r)
	}

	reader := invocation{env: env(t, "NEXUS_URL", fake.BaseURL(), "NEXUS_USER", "reader", "NEXUS_PASSWORD", "reader-pw")}
	r = reader.run(t, "repos", "show", "raw-hosted")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "not available (reading the configuration needs administrative privileges)")
	r = reader.run(t, "repos", "show", "raw-hosted", "--json")
	mustExit(t, r, 0)
	if m := decode[map[string]any](t, r, r.stdout); m["settings"] != nil {
		t.Fatalf("settings must be null without privileges\n%s", r)
	}

	r = inv.run(t, "repos", "show", "nope")
	mustExit(t, r, 5)
	mustContain(t, r, "stderr", `repository "nope" not found`)
	mustContain(t, r, "stderr", `hint: run "nexr repos"`)
}

func TestAuthentication(t *testing.T) {
	fake := fixture(t)

	wrong := invocation{env: env(t, "NEXUS_URL", fake.BaseURL(), "NEXUS_USER", "admin", "NEXUS_PASSWORD", "wrong")}
	r := wrong.run(t, "repos")
	mustExit(t, r, 4)
	mustContain(t, r, "stderr", "401 Unauthorized")
	mustContain(t, r, "stderr", "hint: check the user and password")

	anonymous := invocation{env: env(t, "NEXUS_URL", fake.BaseURL())}
	r = anonymous.run(t, "repos")
	mustExit(t, r, 4)
	mustContain(t, r, "stderr", "hint: no credentials are configured")
	if strings.Contains(r.stderr, "check the user and password") {
		t.Fatalf("no credentials are configured, so there is no password to check\n%s", r)
	}

	fake.SetAnonymous(true)
	r = anonymous.run(t, "repos", "-q")
	mustExit(t, r, 0)
	fake.SetAnonymous(false)

	// Credentials from the environment belong to NEXUS_URL; a different URL
	// from a flag must not receive them (credential scoping).
	other := strings.Replace(fake.BaseURL(), "127.0.0.1", "localhost", 1)
	inv := admin(t, fake)
	r = inv.run(t, "--url", other, "repos")
	mustExit(t, r, 4)
	mustContain(t, r, "stderr", "ignored user from env NEXUS_USER")
	mustContain(t, r, "stderr", "credential scoping")

	r = inv.run(t, "--url", other, "-u", "admin", "--password-stdin", "repos", "-q")
	mustExit(t, r, 2)
	if r.stderr != "nexr: --password-stdin was given, but stdin is empty\n" {
		t.Fatalf("unexpected message\n%s", r)
	}
	r = invocation{env: inv.env, stdin: "admin123\n"}.run(t, "--url", other, "-u", "admin", "--password-stdin", "repos", "-q")
	mustExit(t, r, 0)

	r = inv.run(t, "--url", other, "-u", "admin", "--password", "admin123", "repos", "-q")
	mustExit(t, r, 0)
	mustContain(t, r, "stderr", "warning: --password is visible")
}

// Nexus 3.96 blocks a user after three failed sign-ins and counts every further
// attempt, so nexr must fail at once instead of retrying.
func TestAuthRateLimit(t *testing.T) {
	fake := fixture(t)
	wrong := invocation{env: env(t, "NEXUS_URL", fake.BaseURL(), "NEXUS_USER", "admin", "NEXUS_PASSWORD", "wrong")}
	for range 3 {
		r := wrong.run(t, "repos")
		mustExit(t, r, 4)
		mustContain(t, r, "stderr", "401 Unauthorized")
	}
	start := time.Now()
	r := wrong.run(t, "repos")
	mustExit(t, r, 4)
	mustContain(t, r, "stderr", "429 Too Many Requests: Too many authentication attempts")
	mustContain(t, r, "stderr", "hint: Nexus blocks a user after repeated failed sign-ins")
	mustContain(t, r, "stderr", "hint: an administrator can lift the block")
	mustContain(t, r, "stderr", "hint: check the password (from env NEXUS_PASSWORD) first")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s: the rate limit was retried", d)
	}

	// The right password does not help during the block.
	inv := admin(t, fake)
	mustExit(t, inv.run(t, "repos"), 4)
	r = inv.run(t, "status")
	mustExit(t, r, 4)
	mustContain(t, r, "stdout", "Auth:    blocked after failed sign-ins")
	mustContain(t, r, "stderr", `Nexus is blocking user "admin" after repeated failed sign-ins`)
	r = inv.run(t, "status", "--json")
	if st := decode[map[string]any](t, r, r.stdout); st["auth"] != "blocked" || st["repositories"] != nil {
		t.Fatalf("unexpected JSON\n%s", r)
	}

	fake.AddUser("admin", "admin123", true) // an administrator updates the user
	mustExit(t, inv.run(t, "repos"), 0)
}

// Nexus 3.71 answers anonymous requests with an empty list, or 403, when
// anonymous access is disabled.
func TestAnonymousWithoutAccess(t *testing.T) {
	fake := nexustest.New(t)
	fake.SetAnonymous(true) // but no repository is visible
	anonymous := invocation{env: env(t, "NEXUS_URL", fake.BaseURL())}
	r := anonymous.run(t, "repos")
	mustExit(t, r, 0)
	mustContain(t, r, "stderr", "warning: no repository is visible without credentials")
	r = anonymous.run(t, "status")
	mustExit(t, r, 4)
	mustContain(t, r, "stdout", "Repositories:  0 visible")
	mustContain(t, r, "stderr", "no credentials are configured and no repository is visible without them")

	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()
	r = invocation{env: env(t, "NEXUS_URL", forbidden.URL)}.run(t, "repos", "show", "raw-hosted")
	mustExit(t, r, 4)
	mustContain(t, r, "stderr", "hint: no credentials are configured, and anonymous access does not allow this")
	if strings.Contains(r.stderr, "lacks the Nexus privilege") {
		t.Fatalf("without credentials there is no user to lack a privilege\n%s", r)
	}
}

func TestStatus(t *testing.T) {
	fake := fixture(t)
	inv := admin(t, fake)

	r := inv.run(t, "status")
	mustExit(t, r, 0)
	for _, want := range []string{"URL:", fake.BaseURL() + " (from env NEXUS_URL)", "Nexus Repository 3.96.3-01 (COMMUNITY)",
		"Read:", "available", "credentials accepted for user admin (from env NEXUS_USER)", "Repositories:  4 visible"} {
		mustContain(t, r, "stdout", want)
	}

	r = inv.run(t, "status", "--json")
	mustExit(t, r, 0)
	st := decode[map[string]any](t, r, r.stdout)
	if st["auth"] != "accepted" || st["readable"] != true || st["writable"] != true || st["user"] != "admin" ||
		st["repositories"] != float64(4) || st["server"].(map[string]any)["version"] != "3.96.3-01" {
		t.Fatalf("unexpected JSON\n%s", r)
	}

	r = inv.run(t, "status", "-q")
	mustExit(t, r, 0)
	if r.stdout != "" {
		t.Fatalf("-q must print nothing on success\n%s", r)
	}

	wrong := invocation{env: env(t, "NEXUS_URL", fake.BaseURL(), "NEXUS_USER", "admin", "NEXUS_PASSWORD", "wrong")}
	r = wrong.run(t, "status")
	mustExit(t, r, 4)
	mustContain(t, r, "stdout", "Read:    available") // Nexus answers 401 on /v1/status with wrong credentials
	mustContain(t, r, "stdout", "rejected")
	mustContain(t, r, "stderr", `the credentials of user "admin" were rejected`)
	mustContain(t, r, "stderr", "hint: check the password (from env NEXUS_PASSWORD)")

	anonymous := invocation{env: env(t, "NEXUS_URL", fake.BaseURL())}
	r = anonymous.run(t, "status")
	mustExit(t, r, 4)
	mustContain(t, r, "stderr", "anonymous access is disabled")
	fake.SetAnonymous(true)
	r = anonymous.run(t, "status", "--json")
	mustExit(t, r, 0)
	if st := decode[map[string]any](t, r, r.stdout); st["auth"] != "anonymous" || st["user"] != nil {
		t.Fatalf("unexpected JSON\n%s", r)
	}

	fake.SetHealth(false, false)
	r = inv.run(t, "status")
	mustExit(t, r, 1)
	mustContain(t, r, "stdout", "not available")
	mustContain(t, r, "stderr", "reports that it is not available")
	if n := len(fake.Requests()); n > 100 {
		t.Fatalf("%d requests; a 503 from the status endpoint must not be retried", n)
	}
}

func TestNetworkErrors(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	inv := invocation{env: env(t, "NEXUS_URL", closed.URL)}
	r := inv.run(t, "--retries", "0", "status")
	mustExit(t, r, 7)
	mustContain(t, r, "stderr", "hint: ")

	hanging := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hanging.Close()
	inv = invocation{env: env(t, "NEXUS_URL", hanging.URL)}
	r = inv.run(t, "--timeout", "100ms", "repos")
	mustExit(t, r, 8)
	mustContain(t, r, "stderr", "--timeout")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inv.ctx = ctx
	r = inv.run(t, "repos")
	mustExit(t, r, 130)
}

func TestTransportWarnings(t *testing.T) {
	fake := fixture(t)
	target, _ := url.Parse(fake.URL)
	// Route requests for a non-loopback host to the fake.
	route := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(req)
	})
	inv := invocation{
		env:       env(t, "NEXUS_URL", "http://nexus.example.com", "NEXUS_USER", "admin", "NEXUS_PASSWORD", "admin123"),
		transport: route,
	}
	r := inv.run(t, "repos", "-q")
	mustExit(t, r, 0)
	mustContain(t, r, "stderr", "warning: credentials are sent over plain HTTP to nexus.example.com")

	r = inv.run(t, "--insecure", "repos", "-q")
	mustContain(t, r, "stderr", "warning: TLS certificate verification is disabled (from flag --insecure)")

	r = admin(t, fake).run(t, "repos", "-q")
	if r.stderr != "" {
		t.Fatalf("no warnings expected for a loopback URL\n%s", r)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestVerboseLogging(t *testing.T) {
	fake := fixture(t)
	r := admin(t, fake).run(t, "-vv", "repos", "-q")
	mustExit(t, r, 0)
	mustContain(t, r, "stderr", "/service/rest/v1/repositories")
	mustContain(t, r, "stderr", "status=200")
	for _, secret := range []string{"admin123", "YWRtaW46YWRtaW4xMjM="} {
		if strings.Contains(r.stderr, secret) {
			t.Fatalf("the log leaks the password\n%s", r)
		}
	}
	if !strings.HasPrefix(r.stdout, "docker-hosted\n") {
		t.Fatalf("logging must not change stdout\n%s", r)
	}
}

const configFile = `current_profile: prod
url: https://top.example.com
profiles:
  prod:
    url: https://nexus.example.com
    user: ci
    password_env: PROD_PASSWORD
  staging:
    url: https://staging.example.com
    unknown_key: 1
`

func withConfig(t *testing.T, content string, kv ...string) invocation {
	t.Helper()
	e := env(t, kv...)
	dir := filepath.Join(e["XDG_CONFIG_HOME"], "nexr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return invocation{env: e}
}

func TestConfigCommands(t *testing.T) {
	inv := withConfig(t, configFile, "PROD_PASSWORD", "s3cr3t-value")
	path := filepath.Join(inv.env["XDG_CONFIG_HOME"], "nexr", "config.yaml")

	r := inv.run(t, "config", "path")
	mustExit(t, r, 0)
	if r.stdout != path+"\n" {
		t.Fatalf("config path printed %q", r.stdout)
	}
	mustContain(t, r, "stderr", `warning: unknown config key "profiles.staging.unknown_key" (line 10)`)
	r = inv.run(t, "--json", "config", "path")
	if doc := decode[map[string]any](t, r, r.stdout); doc["path"] != path || doc["exists"] != true {
		t.Fatalf("unexpected JSON\n%s", r)
	}
	r = invocation{env: env(t)}.run(t, "config", "path")
	mustContain(t, r, "stdout", "(not found)")

	r = inv.run(t, "config", "profiles")
	mustExit(t, r, 0)
	want := `NAME     URL                          ACTIVE  CURRENT
prod     https://nexus.example.com    *       *
staging  https://staging.example.com
`
	if r.stdout != want {
		t.Fatalf("unexpected table\n%s", r)
	}
	r = inv.run(t, "--profile", "staging", "config", "profiles", "--json")
	list := decode[[]map[string]any](t, r, r.stdout)
	if len(list) != 2 || list[1]["name"] != "staging" || list[1]["active"] != true || list[0]["current"] != true {
		t.Fatalf("unexpected JSON\n%s", r)
	}
	r = invocation{env: env(t)}.run(t, "config", "profiles")
	mustExit(t, r, 0)
	mustContain(t, r, "stderr", "no profiles defined")

	r = inv.run(t, "config", "view")
	mustExit(t, r, 0)
	for _, want := range []string{"Config file: " + path, "Profile:     prod (from file current_profile)",
		"https://nexus.example.com", "***", "profile prod, password_env PROD_PASSWORD"} {
		mustContain(t, r, "stdout", want)
	}
	r2 := inv.run(t, "config", "view", "--json")
	mustExit(t, r2, 0)
	for _, out := range []string{r.stdout, r.stderr, r2.stdout, r2.stderr} {
		if strings.Contains(out, "s3cr3t-value") {
			t.Fatalf("config view leaks the password\n%s\n%s", r, r2)
		}
	}
	doc := decode[struct {
		Profile  map[string]any `json:"profile"`
		Settings []map[string]string
		Warnings []string `json:"warnings"`
	}](t, r2, r2.stdout)
	if doc.Profile["name"] != "prod" || doc.Settings[0]["key"] != "url" ||
		doc.Settings[0]["value"] != "https://nexus.example.com" || len(doc.Warnings) != 1 {
		t.Fatalf("unexpected JSON\n%s", r2)
	}

	r = inv.run(t, "--profile", "nope", "config", "view")
	mustExit(t, r, 3)
	mustContain(t, r, "stderr", `profile "nope" (from flag --profile) is not defined`)
}

func TestCompletion(t *testing.T) {
	r := invocation{env: env(t)}.run(t, "completion", "bash")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "bash completion")

	fake := fixture(t)
	r = admin(t, fake).run(t, "__complete", "repos", "show", "")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "raw-hosted\traw hosted")
	mustContain(t, r, "stdout", ":4") // no file completion

	// Completion never fails loudly, even without a server.
	r = invocation{env: env(t)}.run(t, "__complete", "repos", "show", "")
	mustExit(t, r, 0)

	r = withConfig(t, configFile).run(t, "__complete", "--profile", "")
	mustContain(t, r, "stdout", "prod\nstaging\n")
}
