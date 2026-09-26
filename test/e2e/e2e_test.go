//go:build e2e

// Package e2e runs the nexr binary against a real Nexus Repository. Start one
// with scripts/e2e-nexus.sh, or run everything with "make e2e".
//
// The tests read NEXUS_URL, NEXUS_USER and NEXUS_PASSWORD from the
// environment, and the path of the binary from NEXR_E2E_BINARY.
package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

type result struct {
	code   int
	stdout string
	stderr string
}

// nexr runs the binary with the Nexus environment, changed by env ("KEY=value"
// sets a variable, "KEY=" removes it). The user's config file is never read.
func nexr(t *testing.T, env []string, args ...string) result {
	t.Helper()
	bin := os.Getenv("NEXR_E2E_BINARY")
	if bin == "" {
		t.Fatal("NEXR_E2E_BINARY is not set; run \"make e2e\"")
	}
	if os.Getenv("NEXUS_URL") == "" {
		t.Fatal("NEXUS_URL is not set; start Nexus with scripts/e2e-nexus.sh")
	}
	vars := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	cfg := t.TempDir()
	vars["XDG_CONFIG_HOME"], vars["AppData"] = cfg, cfg
	delete(vars, "NEXR_PROFILE")
	delete(vars, "NEXR_CONFIG")
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if v == "" {
			delete(vars, k)
		} else {
			vars[k] = v
		}
	}
	cmd := exec.Command(bin, args...)
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return r
}

// stranger returns the environment of a user that does not exist, with a
// name that is new for every call. Nexus 3.96 blocks a user name after three
// failed sign-ins, so failures must never be caused with the admin user.
func stranger() []string {
	return []string{fmt.Sprintf("NEXUS_USER=e2e-stranger-%d", time.Now().UnixNano()), "NEXUS_PASSWORD=wrong"}
}

func check(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
}

func TestStatus(t *testing.T) {
	r := nexr(t, nil, "status", "--json")
	check(t, r, 0)
	var st struct {
		Readable bool   `json:"readable"`
		Writable bool   `json:"writable"`
		Auth     string `json:"auth"`
		Server   struct {
			Version string `json:"version"`
			Edition string `json:"edition"`
		} `json:"server"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Readable || !st.Writable || st.Auth != "accepted" || st.Server.Version == "" || st.Server.Edition == "" {
		t.Fatalf("unexpected status %+v", st)
	}

	r = nexr(t, stranger(), "status")
	check(t, r, 4)
	if !strings.Contains(r.stdout, "Read:    available") || !strings.Contains(r.stderr, "were rejected") {
		t.Fatalf("unexpected output\n%s\n%s", r.stdout, r.stderr)
	}

	r = nexr(t, []string{"NEXUS_USER=", "NEXUS_PASSWORD="}, "status")
	check(t, r, 4) // the bootstrap script disables anonymous access
}

func TestRepos(t *testing.T) {
	r := nexr(t, nil, "repos", "--json")
	check(t, r, 0)
	var repos []struct {
		Name, Format, Type, URL string
	}
	if err := json.Unmarshal([]byte(r.stdout), &repos); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, repo := range repos {
		names = append(names, repo.Name)
	}
	for _, want := range []string{"raw-e2e", "docker-e2e"} {
		if !slices.Contains(names, want) {
			t.Errorf("repository %s is missing from %v", want, names)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("repositories are not sorted: %v", names)
	}

	r = nexr(t, nil, "repos", "--format", "raw", "--type", "hosted", "-q")
	check(t, r, 0)
	if !slices.Contains(strings.Fields(r.stdout), "raw-e2e") || strings.Contains(r.stdout, "docker-e2e") {
		t.Fatalf("unexpected output\n%s", r.stdout)
	}

	r = nexr(t, nil, "repos", "show", "docker-e2e", "--json")
	check(t, r, 0)
	var show struct {
		Settings struct {
			Docker struct {
				ForceBasicAuth bool `json:"forceBasicAuth"`
			} `json:"docker"`
		} `json:"settings"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &show); err != nil || !show.Settings.Docker.ForceBasicAuth {
		t.Fatalf("unexpected output %v\n%s", err, r.stdout)
	}

	check(t, nexr(t, nil, "repos", "show", "no-such-repository"), 5)
	check(t, nexr(t, stranger(), "repos"), 4)
}

// Nexus 3.96 and newer answer the fourth failed sign-in of a user with
// "429 Too many authentication attempts"; nexr must fail at once.
func TestAuthRateLimit(t *testing.T) {
	env := stranger()
	for range 3 {
		check(t, nexr(t, env, "repos"), 4)
	}
	start := time.Now()
	r := nexr(t, env, "repos")
	check(t, r, 4)
	if strings.Contains(r.stderr, "401 Unauthorized") {
		t.Skip("this Nexus release has no authentication rate limit")
	}
	if !strings.Contains(r.stderr, "Too many authentication attempts") {
		t.Fatalf("unexpected output\n%s", r.stderr)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %s: the rate limit was retried", d)
	}
}
