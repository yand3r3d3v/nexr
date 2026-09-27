package config

import (
	"sort"
	"strconv"
	"strings"
)

// Entry is one line of "nexr config view". Secrets are always redacted.
type Entry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Origin string `json:"source"`
}

// Entries lists the effective settings in a stable order.
func (r *Resolved) Entries() []Entry {
	str := func(key string, s Setting[string]) Entry {
		return Entry{Key: key, Value: s.Value, Origin: s.Origin}
	}
	list := func(key string, s Setting[[]string]) Entry {
		return Entry{Key: key, Value: strings.Join(s.Value, ", "), Origin: s.Origin}
	}
	repos := make([]string, 0, len(r.registryURLs))
	for repo := range r.registryURLs {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	registries := []Entry{{Key: "docker.registry_urls"}}
	if len(repos) > 0 {
		registries = registries[:0]
	}
	for _, repo := range repos {
		e := r.registryURLs[repo]
		origin := e.origin
		if e.dropped != "" {
			origin += " (ignored: belongs to another URL)"
		}
		registries = append(registries, Entry{Key: "docker.registry_urls." + repo, Value: e.value, Origin: origin})
	}
	override := str("docker.registry_url", r.RegistryURLOverride)
	if e := r.registryEnv; e != nil {
		override.Value = e.value
		if e.dropped != "" {
			override.Origin += " (ignored: belongs to another URL)"
		}
	}
	password := Entry{Key: "password"}
	if r.password != nil {
		password.Value, password.Origin = "***", r.PasswordOrigin()
	}
	insecure := Entry{Key: "tls.insecure", Origin: r.TLSInsecure.Origin}
	if r.TLSInsecure.Set {
		insecure.Value = strconv.FormatBool(r.TLSInsecure.Value)
	}
	entries := []Entry{
		str("url", r.URL),
		str("user", r.User),
		password,
		insecure,
		str("tls.ca_file", r.CAFile),
		str("tls.client_cert", r.ClientCert),
		str("tls.client_key", r.ClientKey),
		{Key: "timeout", Value: r.Timeout.Value.String(), Origin: r.Timeout.Origin},
		{Key: "retries", Value: strconv.Itoa(r.Retries.Value), Origin: r.Retries.Origin},
		{Key: "concurrency", Value: strconv.Itoa(r.Concurrency.Value), Origin: r.Concurrency.Origin},
		str("output", r.Output),
		str("docker.repository", r.DockerRepository),
		list("docker.exclude", r.DockerExclude),
	}
	entries = append(entries, registries...)
	return append(entries,
		override,
		str("upload.method", r.UploadMethod),
		Entry{Key: "gc.wait_timeout", Value: r.GCWaitTimeout.Value.String(), Origin: r.GCWaitTimeout.Origin},
		list("gc.tasks", r.GCTasks),
	)
}
