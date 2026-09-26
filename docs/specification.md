# nexr: Technical Specification

| | |
|---|---|
| **Status** | Draft; review decisions of 2026-09-26 applied (§11.2) |
| **Version** | 0.2 |
| **Date** | 2026-09-26 |
| **Related documents** | [Architecture](architecture.md) · [Nexus API notes](nexus-api.md) · [Roadmap](roadmap.md) |

This document says **what** `nexr` must do. It is the reference for implementation, code review and
acceptance testing. [architecture.md](architecture.md) covers how the tool is built.
[nexus-api.md](nexus-api.md) records the Nexus behaviour we observed while designing it.

The key words **MUST**, **SHOULD** and **MAY** follow [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119).
Requirement IDs (`FR-…` for functional, `NFR-…` for non-functional) are referenced from issues,
tests and the roadmap. Priorities use MoSCoW: **M**ust, **S**hould, **C**ould, **W**on't (for v1.0).

---

## Contents

1. [Introduction](#1-introduction)
2. [Users and use cases](#2-users-and-use-cases)
3. [Operating environment and compatibility](#3-operating-environment-and-compatibility)
4. [General CLI behaviour](#4-general-cli-behaviour)
5. [Configuration](#5-configuration)
6. [Commands](#6-commands)
7. [Output, errors and exit codes](#7-output-errors-and-exit-codes)
8. [Non-functional requirements](#8-non-functional-requirements)
9. [Required Nexus privileges](#9-required-nexus-privileges)
10. [Acceptance criteria](#10-acceptance-criteria)
11. [Assumptions, decisions and open questions](#11-assumptions-decisions-and-open-questions)
12. [Appendix A: Complete configuration example](#appendix-a-complete-configuration-example)
13. [Appendix B: Command cheat sheet](#appendix-b-command-cheat-sheet)

---

## 1. Introduction

### 1.1 Product vision

`nexr` (short for **Nex**us **R**epository) is a single, self-contained command-line tool for
working with [Sonatype Nexus Repository 3](https://www.sonatype.com/products/sonatype-nexus-repository)
without opening the web UI. It serves developers at a terminal, CI/CD pipelines and Nexus
administrators. It offers one configuration, one set of conventions and consistent commands across
repository formats and across the Nexus instances a user works with.

The first release covers the most requested workflows:

* **raw repositories**: list, upload, download and delete files and directories;
* **Docker/OCI repositories**: list images and tags, delete tags, enforce retention ("keep the
  last N tags");
* **storage reclamation**: run the server-side cleanup tasks that actually free disk space.

The architecture is built to grow into a full Nexus CLI: more formats (Maven, npm, PyPI, Helm, apt,
yum and others) and more administrative resources (repositories, tasks, blob stores, cleanup
policies, security). Adding them should not require redesigning the tool. See
[roadmap.md](roadmap.md).

### 1.2 Background and alternatives

| Tool | Language | raw | Docker | Static binary | Why it is not enough |
|---|---|---|---|---|---|
| `nexus3-cli` | Python | yes | only as plain assets | no (needs Python runtime) | Runtime dependency; no image/tag semantics |
| `nexus-cli` (EugenMayer fork) | Go | no | yes | yes | Docker only |
| `nexus_util` | Go | yes | no | yes | raw only |

We decided to write a new tool in **Go**. Go produces static cross-compiled binaries, starts fast,
and has a strong standard library for HTTP and JSON. We considered Rust and rejected it: the tool
is an I/O-bound HTTP client, and Rust would add complexity without real benefit.

### 1.3 Scope

**In scope for v1.0:**

* configuration from environment variables, a YAML file and flags, with named profiles for several
  Nexus instances;
* repository listing;
* path-based file operations (`ls`, `up`, `down`, `rm`), with upload for **raw** repositories;
* container image operations for repositories of format `docker` and `oci`;
* running and waiting for server tasks, including the Docker GC plus blob store compaction sequence;
* a generic authenticated REST escape hatch (`nexr api`) so that anything not yet covered by a
  dedicated command can still be done without the web UI;
* human-readable output, a stable `--json` output and documented exit codes;
* release binaries for Linux, macOS and Windows, and a Homebrew tap.

**Out of scope for v1.0 (non-goals):**

* pushing or pulling container images. Use `docker`, `podman`, `crane` or `skopeo`; `nexr` manages
  what is already stored in Nexus;
* uploading to formats other than raw (planned, see roadmap);
* administering repositories, security, cleanup policies or blob stores through dedicated commands
  (planned; available meanwhile through `nexr api`);
* Nexus Repository 2.x;
* graphical or full-screen terminal UIs;
* Sonatype IQ Server, Firewall and Lifecycle integrations;
* project-local configuration files that are picked up implicitly from the working directory (a
  security risk, see §5.1).

### 1.4 Terminology

| Term | Meaning |
|---|---|
| **Nexus** | Sonatype Nexus Repository 3, any edition (Community Edition, Pro, legacy OSS). |
| **Repository** | Named storage with a *format* (raw, docker, maven2, …) and a *type* (hosted, proxy, group). |
| **Component** | Logical unit stored in a repository: a raw file, a Docker image tag, a Maven GAV, etc. |
| **Asset** | A stored file. Usually belongs to a component; Docker layer blobs are stand-alone assets. |
| **Blob store** | Physical storage that holds asset content. |
| **Task** | Server-side job, e.g. *Docker - Delete unused manifests and images* or *Admin - Compact blob store*. |
| **Connector** | Dedicated HTTP(S) port or sub-domain configured for a Docker repository. |
| **Remote path** | `REPO/PATH`: address of a file or directory inside a repository. |
| **Image reference** | `NAME[:TAG]`: a container image (and optionally a tag) inside a docker/oci repository. |
| **Profile** | Named set of connection settings for one Nexus instance. |
| **Capability** | A server feature that `nexr` detects at runtime (see §3.3). |

---

## 2. Users and use cases

### 2.1 Personas

| Persona | Needs |
|---|---|
| **Developer** | Interactive use, readable output, occasional uploads/downloads, inspecting images and tags. |
| **CI/CD pipeline** | Non-interactive, configured by environment variables, `--json` output, reliable exit codes, idempotent commands, never prompts. |
| **Administrator / SRE** | Retention and cleanup, storage reclamation, task operations, several Nexus instances. |

### 2.2 Key use cases

| ID | Use case | Example |
|---|---|---|
| UC-1 | Publish build artifacts | `nexr up ./dist raw-releases/myapp/1.4.0/` |
| UC-2 | Fetch artifacts during deployment | `nexr down raw-releases/myapp/1.4.0/myapp.tar.gz /opt/myapp/` |
| UC-3 | Explore repository content | `nexr ls raw-releases/myapp/` · `nexr ls -rl raw-releases/myapp/` |
| UC-4 | Remove obsolete files safely | `nexr rm -r raw-releases/myapp/1.0.0/ --dry-run` |
| UC-5 | Inspect images and tags | `nexr docker ls -R docker-hosted` · `nexr docker tags team/app` |
| UC-6 | Enforce image retention in CI | `nexr docker rm team/app --keep 10 --older-than 30d --yes --json` |
| UC-7 | Reclaim disk space | `nexr gc --repo docker-hosted` |
| UC-8 | Work with several Nexus instances | `nexr --profile staging repos` |
| UC-9 | Do anything else without the web UI | `nexr api /v1/blobstores` · `nexr tasks run "Compact blob store" --wait` |

---

## 3. Operating environment and compatibility

### 3.1 Client platforms

**NFR-PLAT-1 (M).** `nexr` MUST be released as one static binary per platform:

| OS | Architectures |
|---|---|
| Linux | amd64, arm64 |
| macOS | amd64, arm64 |
| Windows | amd64, arm64 (arm64: S) |

**NFR-PLAT-2 (M).** Binaries MUST be built with `CGO_ENABLED=0` and MUST NOT need anything at run
time: no interpreter, no shared libraries, no config files. They MUST run in minimal containers such
as `scratch`, `distroless` and Alpine.

### 3.2 Nexus Repository versions and editions

* **Supported:** Nexus Repository **3.71 and newer**, Community Edition and Pro. These are the
  releases built on the SQL datastore (H2 or PostgreSQL).
* **Not supported:** 3.70 and older (OrientDB) and Nexus Repository 2. `nexr` contains no workarounds
  for them.
* **Priority:** the latest release (3.96 at the time of writing) is the primary target. Milestones
  M0–M3 are developed and tested against it. Compatibility with older supported releases
  (3.71–3.9x) follows in milestone M4, before v1.0 (see [roadmap.md](roadmap.md)).
* Behaviour was verified during design against **3.96.3-01 CE** (the latest release as of September
  2026) and **3.71.0-06 OSS** (the oldest supported release), both on H2. Details are in
  [nexus-api.md](nexus-api.md).

**FR-COMPAT-1 (M).** `nexr` MUST NOT branch on version numbers for functional behaviour. It MUST detect
capabilities at run time and fall back gracefully (§3.3). The server version, taken from the
`Server` response header, is informational only.

### 3.3 Capability matrix

Differences observed between the oldest supported and the latest release, and how `nexr` handles
them. Releases in between may have any mix of these features, which is why `nexr` detects capabilities
instead of comparing version numbers.

| Capability | 3.71.0 | 3.96.3 | `nexr` behaviour |
|---|---|---|---|
| Page size of `components`/`assets` listings | 10 | 100 | Never assume a page size; always follow `continuationToken`. |
| Raw asset `path` / component `name` | `/dir/file.txt` | `/dir/file.txt` | Normalise internally; display without the leading slash. |
| Search: trailing wildcard | any length | at least 3 characters before `*`; no leading `*` | Build queries that satisfy both; fall back to a full scan. |
| Search: unquoted values with spaces | break wildcard queries | break wildcard queries | Quote exact values; use other strategies for such paths. |
| Search index consistency | not measured | about 2 s lag after an upload | Documented; retention logic stays safe (§6.8). |
| Browse REST API (`/v1/repositories/{repo}/browse`) | absent | present, including folder delete | Used when present for directory listings and `--server-side` deletion. |
| Task create, update and delete API | absent (`405`) | present | `nexr gc --create-missing` only where supported. |
| Task `properties` in task listings | absent | present | Filtering tasks by repository or blob store only where exposed. |
| System tasks *Admin - Cleanup unused asset blobs* (`assetBlob.cleanup`) | present | present | `nexr gc` runs them between Docker GC and compaction. |
| Docker image attributes (created, OS/arch, total size) | absent | present | Optional columns, shown when available. |
| Asset `blobCreated`, `blobStoreName` | `null` | set | Optional fields; retention uses `lastModified`, which both provide. |
| Docker Registry API under `/repository/<repo>/v2/` | yes | yes | Default registry endpoint (§3.4). |
| Registry pagination `Link` header on that path | points to `/v2/…` without the repository prefix (unusable) | correct | `Link` URLs are never followed; the next request is rebuilt from `n` and `last`. |
| `oci` format, Docker `pathEnabled` routing | absent | present | `oci` repositories are handled like `docker` ones. |

### 3.4 Network

**FR-NET-1 (M).** Each profile has one base URL, e.g. `https://nexus.example.com` or, with a context
path, `https://example.com/nexus`. Every endpoint is derived from it:

* REST API: `<base>/service/rest/…`
* repository content: `<base>/repository/<repo>/<path>`
* Docker Registry v2 API: `<base>/repository/<repo>/v2/…`

**FR-NET-2 (M).** `nexr` MUST honour `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`.

**FR-NET-3 (M), registry endpoint.** The Docker Registry API endpoint of a repository defaults to
`<base>/repository/<repo>/`, and `nexr` appends `v2/…`. It can be overridden with:

* `--registry-url URL` on `nexr docker` commands;
* `NEXR_DOCKER_REGISTRY_URL` (applies to the selected repository);
* `docker.registry_urls` in the config file or a profile: a map from repository name to registry
  URL.

These sources follow the precedence rules of §5.1.

Example: a reverse proxy forwards `https://registry.example.com/v2/…` to
`https://registry.example.com/repository/docker-hosted/v2/…`. It is configured with
`docker.registry_urls: {docker-hosted: https://registry.example.com}`. An override may also point
to a Docker connector (`https://nexus.example.com:8443`). Requests to the registry endpoint use the
profile's TLS settings and credentials, because the user configured that endpoint explicitly for the
profile.

---

## 4. General CLI behaviour

### 4.1 Command syntax

```
nexr [global flags] <command> [<subcommand>] [flags] [arguments]
```

Flags may appear before or after arguments. `--` ends flag parsing, which allows paths that start
with `-`. Command names are lower-case verbs or nouns; the main commands have short aliases
(`upload` for `up`, `download`/`get` for `down`, `delete` for `rm`, `list` for `ls`).

### 4.2 Remote paths

**FR-ADDR-1 (M).** A remote path is `REPO[/PATH]`:

* `REPO` is a repository name matching `^[A-Za-z0-9-][A-Za-z0-9_.-]*$`.
* `PATH` segments are separated by `/`. Paths are case-sensitive and may contain spaces and any
  Unicode characters; `nexr` percent-encodes them on the wire.

**FR-ADDR-2 (M).** A trailing `/` means "directory". Without a trailing slash, each command defines
how the path is resolved (file, directory or name prefix).

**FR-ADDR-3 (M).** A leading `/` in `PATH` is ignored. Empty segments (`a//b`) and `.`/`..` segments
are rejected as usage errors.

**FR-ADDR-4 (M).** Remote paths are always displayed as `REPO/PATH`, without a leading slash,
whatever the server's internal representation (§3.3).

**FR-ADDR-5 (M).** Local paths use the OS conventions (`\` is accepted on Windows). Remote paths
always use `/`.

### 4.3 Container image references

**FR-IMGREF-1 (M).** An image reference is `NAME[:TAG]`:

* `NAME` follows the OCI distribution grammar: lower-case components separated by `/`, containing
  `a-z0-9` and the separators `.`, `_`, `__`, `-`.
* `TAG` matches `[A-Za-z0-9_][A-Za-z0-9._-]{0,127}`.
* A `:` after the last `/` starts the tag.

**FR-IMGREF-2 (M).** The repository that holds the image is the effective `docker.repository`
setting, resolved with the precedence rules of §5.1: the `--repo`/`-R` flag, an explicitly selected
profile, the `NEXR_DOCKER_REPO` environment variable, then the config file. If it is not set anywhere,
the command fails with a usage error that lists the docker/oci repositories visible to the user.

**FR-IMGREF-3 (S).** An image reference MAY start with a registry host, exactly as it is used with
`docker pull` (`registry.example.com/team/app:1.0`). The host is matched against the hosts of the
configured registry URLs (FR-NET-3), which selects the repository. A host that matches no configured
registry URL is a usage error with a hint.

### 4.4 Patterns and durations

**FR-PAT-1 (M).** Every pattern flag (`--match`, `--include`, `--exclude`) accepts glob syntax:
`*`, `?`, `[…]`, and `**` for any number of path segments. A pattern prefixed with `re:` is an RE2
regular expression instead, e.g. `--exclude 're:^v\d+\.\d+\.\d+$'`. Path patterns match the path
relative to the command's base directory, using `/` separators, with the rules of `.gitignore`: a
glob without `/` matches the last segment at any depth (`--exclude '*.log'` excludes `a.log` and
`logs/b.log`, `--exclude node_modules` skips every `node_modules` directory), a glob with `/`
matches the whole relative path (`build/*.map`), and a leading `/` anchors a glob to the base
directory (`/*.log` excludes only top-level `.log` files). Regular expressions always match the
whole relative path.

**FR-PAT-2 (M).** Duration flags accept Go duration syntax plus days and weeks: `90m`, `36h`, `30d`,
`2w`.

### 4.5 Safety of destructive operations

**FR-SAFE-1 (M).** Every command that deletes data supports `--dry-run`. A dry run performs all
read-only steps (resolution, listing, planning), prints exactly what would be deleted, changes
nothing, and exits with 0.

**FR-SAFE-2 (M).** Deleting a directory requires `-r/--recursive`.

**FR-SAFE-3 (M).** Bulk deletions (more than one item, e.g. `rm -r`, `docker rm --keep`, `--all`)
need confirmation. On an interactive terminal (stdin and stderr are TTYs) `nexr` shows a summary and
asks `[y/N]`. Otherwise `-y/--yes` is required; without it the command fails with exit code 2 and
deletes nothing.

**FR-SAFE-4 (M).** Deleting the entire content of a repository (`nexr rm -r REPO`) needs stronger
confirmation: interactively the user must type the repository name; non-interactively `--yes` is
required.

**FR-SAFE-5 (M).** Deleting one explicitly named item (`nexr rm REPO/file.txt`,
`nexr docker rm app:1.0`) does not prompt.

**FR-SAFE-6 (M).** Bulk operations continue after individual failures, report every failure, and
end with a summary (succeeded / failed / skipped). Exit codes follow §7.4.

### 4.6 Idempotency and scripting

**FR-SCRIPT-1 (M).** `nexr` MUST NOT prompt or wait for input when stdin is not a terminal, apart from
reading data explicitly requested from stdin (`-`, `--password-stdin`, `nexr api -d @-`).

**FR-SCRIPT-2 (S).** Deletions accept `--ignore-missing`, which makes a missing target a success.
Uploads accept `--skip-existing`. Together they make repeated runs of a pipeline safe.

---

## 5. Configuration

### 5.1 Sources and precedence

**FR-CFG-1 (M).** Settings come from the following sources, highest precedence first:

1. **command-line flags**;
2. the **profile selected explicitly** with `--profile` or `NEXR_PROFILE`;
3. **environment variables** (`NEXUS_*`, `NEXR_*`);
4. the **config file**: the default profile (`current_profile`) merged over the top-level settings;
5. **built-in defaults**.

Why this order: environment variables override the ambient defaults from the config file, which is
the usual 12-factor/CI expectation. An explicitly selected profile, however, shows that the user
wants that particular instance, so it wins over ambient environment variables. The AWS CLI resolves
`--profile` the same way.

**FR-CFG-2 (M), credential scoping.** Credentials must never be sent to a server they were not meant
for. They MAY be combined with a URL that comes from the same source or from a lower-precedence
source. Credentials from a lower-precedence source MUST NOT be combined with a URL from a
higher-precedence source unless both URLs are identical after normalisation.

| URL comes from | Credentials come from | Used? |
|---|---|---|
| config file (`url`) | `NEXUS_USER`/`NEXUS_PASSWORD` | yes: typical CI setup (URL in the file, secrets in env) |
| `NEXUS_URL` | config file default profile | only if the file's URL equals `NEXUS_URL` |
| `--profile staging` | `NEXUS_USER`/`NEXUS_PASSWORD` | only if `NEXUS_URL` equals the staging URL |
| anything | `--user` + `--password`/`--password-stdin` | yes (explicit) |

If credentials are dropped because of this rule, `nexr` prints a warning in verbose mode. If the
server then answers 401, the error hint mentions the rule.

**FR-CFG-3 (M).** The config file location is, in order:

* `--config PATH` or `NEXR_CONFIG` (the file MUST exist);
* Linux and macOS: `$XDG_CONFIG_HOME/nexr/config.yaml`, default `~/.config/nexr/config.yaml`;
* Windows: `%AppData%\nexr\config.yaml`.

A missing default config file is not an error. `nexr` MUST NOT read configuration implicitly from the
current working directory: a repository-controlled file could otherwise redirect credentials to
another server.

### 5.2 Environment variables

| Variable | Meaning |
|---|---|
| `NEXUS_URL` | Base URL of the Nexus instance. |
| `NEXUS_USER` | User name, or the *name code* of a Nexus user token. |
| `NEXUS_PASSWORD` | Password, or the *pass code* of a user token. |
| `NEXUS_PASSWORD_FILE` | Path of a file that contains the password (e.g. a mounted secret). Trailing newline is trimmed. |
| `NEXUS_CA_CERT` | Path of a PEM bundle with additional trusted CA certificates. |
| `NEXUS_INSECURE` | `true` disables TLS certificate verification (not recommended). |
| `NEXUS_CLIENT_CERT`, `NEXUS_CLIENT_KEY` | Client certificate and key (PEM) for servers that require mutual TLS. |
| `NEXR_CONFIG` | Config file path. |
| `NEXR_PROFILE` | Profile to use. |
| `NEXR_DOCKER_REPO` | Default repository for `nexr docker` commands. |
| `NEXR_DOCKER_REGISTRY_URL` | Registry endpoint for the selected repository (FR-NET-3). |
| `NO_COLOR` | Disables coloured output ([no-color.org](https://no-color.org)). |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | Standard proxy settings. |

### 5.3 Config file

**FR-CFG-4 (M).** The config file is YAML. Top-level settings are defaults inherited by every profile.
Keys inside `profiles.<name>` override them.

```yaml
# ~/.config/nexr/config.yaml
current_profile: prod            # used when --profile / NEXR_PROFILE are not given

# Defaults for all profiles
timeout: 60s
concurrency: 4

profiles:
  prod:
    url: https://nexus.example.com
    user: ci-bot
    password_env: NEXUS_PROD_PASSWORD     # read the secret from this environment variable
    docker:
      repository: docker-hosted
  staging:
    url: https://staging.example.com/nexus
    user: alice
    password_file: ~/.config/nexr/staging.secret
    tls:
      ca_file: /etc/ssl/certs/corp-root-ca.pem
```

| Key | Type | Default | Description |
|---|---|---|---|
| `current_profile` | string | none | Default profile (top level only). |
| `url` | string | none | Base URL, including an optional context path. |
| `user` | string | none | User name or user-token name code. |
| `password` | string | none | Password in plain text (discouraged; see FR-CFG-7). |
| `password_env` | string | none | Name of an environment variable that holds the password. |
| `password_file` | path | none | File that holds the password. |
| `password_command` | string | none | (S) Command whose stdout is the password (e.g. `pass show nexus/prod`). |
| `tls.insecure` | bool | `false` | Skip TLS certificate verification. |
| `tls.ca_file` | path | none | Additional trusted CA bundle (PEM), added to the system roots. |
| `tls.client_cert`, `tls.client_key` | path | none | (S) Client certificate and key (PEM) for servers that require mutual TLS. |
| `timeout` | duration | `60s` | Timeout of a single API request (not of file transfers). |
| `retries` | int | `3` | Retries of idempotent requests (§8.4). |
| `concurrency` | int (1–32) | `4` | Parallel transfers and deletions. |
| `output` | `table`\|`json` | `table` | Default output format. |
| `docker.repository` | string | none | Default repository for `nexr docker`. |
| `docker.exclude` | list of patterns | `["latest"]` | Tags never deleted by bulk image deletion (§6.8). |
| `docker.registry_urls` | map repo→URL | none | Registry endpoint per repository (FR-NET-3); also resolves registry hosts in image references (FR-IMGREF-3). |
| `upload.method` | `put`\|`components` | `put` | Default upload method (§6.3). |
| `gc.wait_timeout` | duration | `1h` | Maximum time to wait for each task in `nexr gc`. |
| `gc.tasks` | list of task IDs or names | none | Pins the tasks `nexr gc` runs, in order. |
| `profiles` | map | none | Named profiles; each accepts every key above except `current_profile` and `profiles`. |

**FR-CFG-5 (M).** Only one password source may be set per source level; two or more is a
configuration error (exit 3).

**FR-CFG-6 (S).** Unknown keys produce a warning on stderr that names the key path. The file is
still loaded, so older `nexr` versions can read newer files.

**FR-CFG-7 (S).** On Unix, if the file contains `password` and is readable by group or others, `nexr`
prints a warning that recommends `chmod 600`.

### 5.4 Profiles

**FR-CFG-8 (M).** A profile is selected by `--profile`, then `NEXR_PROFILE`, then `current_profile`. If
none of them is set, only the top-level settings (plus environment and flags) apply. An unknown
profile name is a configuration error (exit 3).

### 5.5 Credentials and authentication

**FR-AUTH-1 (M).** `nexr` authenticates with HTTP Basic auth. This works with a user name and password,
and with Nexus **user tokens** (name code and pass code), which are the recommended choice for CI.
When credentials are configured, Basic auth is sent pre-emptively; otherwise requests are anonymous,
which works if anonymous access is enabled on the server.

**FR-AUTH-2 (M).** Password sources, highest precedence first: `--password-stdin`, `--password`
(prints a warning, because the value is visible in the process list and shell history),
`NEXUS_PASSWORD`, `NEXUS_PASSWORD_FILE`, then the config keys `password`, `password_env`,
`password_file` and `password_command`. This order applies within the precedence levels of §5.1.

**FR-AUTH-3 (M).** For Docker Registry API calls `nexr` also supports the Bearer-token challenge
(`WWW-Authenticate: Bearer realm=…`). This happens when anonymous or Docker Bearer Token Realm access
is configured.

**FR-AUTH-4 (M).** If credentials would be sent over plain `http://` to a host that is not a loopback
address, `nexr` prints a warning.

**FR-AUTH-5 (C).** Storing credentials in the OS keychain (`nexr login`) is planned after v1.0.

---

## 6. Commands

Each command section lists its synopsis, behaviour, output, exit codes and examples. Global flags
(§7.1) are available on every command. Privileges are listed in §9.

| Command | Purpose | Priority | Milestone |
|---|---|---|---|
| `nexr repos [ls]` | List repositories | M | M0 |
| `nexr repos show REPO` | Repository details | S | M0 |
| `nexr status` | Connectivity, version and authentication check | S | M0 |
| `nexr config view \| path \| profiles` | Inspect the effective configuration | S | M0 |
| `nexr version`, `nexr completion` | Version info, shell completion | M | M0 |
| `nexr ls` | List files and directories | M | M1 |
| `nexr up` | Upload files and directories (raw) | M | M1 |
| `nexr down` | Download files and directories | M | M1 |
| `nexr rm` | Delete files and directories | M | M1 |
| `nexr docker ls` | List images | M | M2 |
| `nexr docker tags` | List tags with metadata | M | M2 |
| `nexr docker rm` | Delete tags, apply retention | M | M2 |
| `nexr tasks ls \| show \| run \| stop` | Server task operations | S | M3 |
| `nexr gc` | Docker GC plus blob store compaction | M | M3 |
| `nexr api` | Authenticated REST escape hatch | S | M3 |

### 6.1 `nexr repos`

```
nexr repos [ls] [--format FORMAT] [--type hosted|proxy|group] [--match PATTERN]
nexr repos show REPO
```

**FR-REPOS-1 (M).** Lists the repositories the user can browse (`GET /v1/repositories`), sorted by
name. `--format`, `--type` and `--match` filter on the client side.

Human output:

```
NAME             FORMAT   TYPE    URL
docker-hosted    docker   hosted  https://nexus.example.com/repository/docker-hosted
maven-public     maven2   group   https://nexus.example.com/repository/maven-public
raw-releases     raw      hosted  https://nexus.example.com/repository/raw-releases
```

JSON output (`--json`): an array of
`{"name", "format", "type", "url", "online"}`. `online` is `null` when the server does not report it.
With `-q`, repository names are printed, one per line.

**FR-REPOS-2 (S).** `nexr repos show REPO` prints details: format, type, URL and online status. If the
user may read the repository configuration, it also prints the blob store, write policy, cleanup
policies and, for Docker, the connector settings (`GET /v1/repositories/{format}/{type}/{name}`).

### 6.2 `nexr ls`

```
nexr ls REPO[/PATH] [-r|--recursive] [-l|--long] [--match PATTERN]
                  [--sort name|size|time] [--reverse]
```

Lists the browse tree of a repository. The command is designed for raw repositories but works for
every format, because every asset has a path (e.g. `maven-releases/com/example/app/1.0/`).

**FR-LS-1 (M), path resolution.**

1. `REPO` or `REPO/` lists the repository root.
2. `REPO/PATH/` lists the contents of directory `PATH`.
3. `REPO/PATH` without a trailing slash: if a directory `PATH` exists, its contents are listed; else,
   if a file `PATH` exists, that file is shown; else `PATH` is treated as a **name prefix**. The
   entries of the parent directory whose names start with the last segment are listed, so
   `nexr ls raw/app/1.` shows `1.0/` and `1.1/`.
4. If nothing matches, the command fails with exit code 5.

**FR-LS-2 (M).** Without `-r`, one level is shown: directories first, each with a trailing `/`, then
files. With `-r`, every file below the directory is listed with its path relative to the listed
directory, and directory entries are omitted.

**FR-LS-3 (M).** `-l` adds size and last-modified time. `--json` always includes the full metadata
(checksums, uploader, IDs and so on), as if `-l` had been given.

```
$ nexr ls -l raw-releases/myapp/
       -  -                 1.3.0/
       -  -                 1.4.0/
  1.2 KiB 2026-09-20 14:03  README.md
```

**FR-LS-4 (M).** `-q` prints full remote references (`REPO/PATH`), one per line, so the output can be
piped into other `nexr` commands (e.g. `xargs nexr rm`).

**FR-LS-5 (M).** JSON output is an array of entries:

```json
[
  {"repository": "raw-releases", "path": "myapp/1.4.0", "name": "1.4.0", "type": "directory"},
  {
    "repository": "raw-releases",
    "path": "myapp/README.md",
    "name": "README.md",
    "type": "file",
    "size": 1234,
    "content_type": "text/markdown",
    "last_modified": "2026-09-20T11:03:12Z",
    "blob_created": "2026-09-20T11:03:12Z",
    "last_downloaded": null,
    "uploader": "ci-bot",
    "checksum": {"sha1": "…", "sha256": "…", "md5": "…"},
    "asset_id": "…",
    "download_url": "https://nexus.example.com/repository/raw-releases/myapp/README.md"
  }
]
```

**FR-LS-6 (M).** Listing results are streamed, so memory does not grow with repository size, except
where `--sort` requires buffering. Very large listings MUST work: at least 1,000,000 assets when a
full scan is required.

**FR-LS-7 (M).** The Nexus search and browse indexes can lag a few seconds behind uploads (§3.3). The
command help and README MUST mention this.

### 6.3 `nexr up`

```
nexr up SRC... REPO[/PATH]
nexr up - REPO/PATH                         # upload from stdin
      [--include PATTERN]... [--exclude PATTERN]... [--follow-symlinks]
      [--skip-existing] [--method put|components] [--content-type TYPE]
      [--verify] [--concurrency N] [--dry-run]
```

Uploads files and directory trees. **In v1.0 the target repository must be a hosted raw
repository.** Other formats are rejected with a clear message and a hint about the planned support
(roadmap).

**FR-UP-1 (M), target mapping.**

| Source | Destination | Result |
|---|---|---|
| file `a.txt` | `REPO` | `REPO/a.txt` |
| file `a.txt` | `REPO/dir/` | `REPO/dir/a.txt` |
| file `a.txt` | `REPO/dir/b.txt` | `REPO/dir/b.txt` (exact target) |
| directory `./build` | `REPO/app/1.0` or `REPO/app/1.0/` | the **contents** of `build` go under `REPO/app/1.0/`, e.g. `build/bin/x` becomes `REPO/app/1.0/bin/x` |
| several sources | must be a directory (`REPO` or ending in `/`) | each source mapped as above; if two sources map to the same target path, the command fails before uploading anything |
| `-` (stdin) | `REPO/PATH` (exact file path) | streamed upload; no retries, because the stream cannot be replayed |

Directory contents are uploaded without the directory name itself, as `aws s3 cp --recursive` does.
This makes `nexr up ./build R/x/` and `nexr down R/x/ ./build` exact inverses. To keep the directory
name, include it in the destination (`nexr up ./build R/x/build/`).

**FR-UP-2 (M).** Directories are walked recursively. `--include`/`--exclude` patterns are matched
against the path relative to the source directory. Hidden files are included by default. Symbolic
links are skipped with a warning unless `--follow-symlinks` is given; loops are detected.

**FR-UP-3 (M), pre-flight checks.** The repository must exist, be of a supported format (raw in
v1.0) and be of type hosted. For group repositories the error names the hosted members.

**FR-UP-4 (M), transport.** The default method, `put`, streams each file with
`PUT <base>/repository/REPO/PATH`. It sets `Content-Length` and a `Content-Type` derived from the
file extension (`application/octet-stream` as a fallback, `--content-type` to override for a single
file). The optional method `components` uses the Components API
(`POST /v1/components?repository=REPO`, multipart fields `raw.directory`, `raw.assetN`,
`raw.assetN.filename`) and also streams the file. Rationale in
[architecture.md, ADR-003](architecture.md#adr-003-raw-upload-uses-http-put-by-default); confirmed
in review (Q5).

**FR-UP-5 (M).** Existing remote files are governed by the repository's write policy: *allow
redeploy* overwrites, *disable redeploy* yields a per-file conflict error (exit code 9 for a single
file). With `--skip-existing`, `nexr` issues `HEAD` for each target first and skips files that
already exist.

**FR-UP-6 (M).** Files are uploaded in parallel (`--concurrency`, default 4). Failed files are retried
according to §8.4, re-reading the file from disk.

**FR-UP-7 (S).** With `--verify`, after each upload `nexr` compares the local SHA-1 with the SHA-1 that
Nexus returns as the `ETag` of the stored file (observed behaviour, see
[nexus-api.md](nexus-api.md#content-endpoints)).

**FR-UP-8 (M).** `--dry-run` resolves the file list and prints the `local → remote` mapping without
uploading. Only the read-only pre-flight requests are made.

Human output (per file on stdout, progress on stderr when it is a TTY):

```
uploaded  raw-releases/myapp/1.4.0/myapp.tar.gz   (48.2 MiB)
uploaded  raw-releases/myapp/1.4.0/checksums.txt  (312 B)
2 files, 48.2 MiB uploaded in 3.1s
```

JSON output:

```json
{
  "dry_run": false,
  "uploaded": [
    {"source": "dist/myapp.tar.gz", "repository": "raw-releases", "path": "myapp/1.4.0/myapp.tar.gz", "size": 50541231},
    {"source": "dist/checksums.txt", "repository": "raw-releases", "path": "myapp/1.4.0/checksums.txt", "size": 312}
  ],
  "skipped": [],
  "failed": [],
  "summary": {"files": 2, "bytes": 50541543, "duration_ms": 3120}
}
```

### 6.4 `nexr down`

```
nexr down REPO/PATH [DEST]
        [--include PATTERN]... [--exclude PATTERN]... [--skip-existing]
        [--no-verify] [--concurrency N] [--dry-run]
```

Downloads one file or a directory tree. It works for every format that stores files under paths.

**FR-DOWN-1 (M), resolution.** If `PATH` has no trailing slash and names a file (`HEAD` returns
200), the file is downloaded. Otherwise `PATH` is treated as a directory and every file below it is
downloaded. A missing path gives exit code 5.

**FR-DOWN-2 (M), destination.**

| Case | `DEST` omitted | `DEST` is an existing directory or ends with a separator | `DEST` is `-` | other `DEST` |
|---|---|---|---|---|
| file | `./<name>` | `DEST/<name>` | written to stdout | exact file path |
| directory | `./` | `DEST/` | not allowed (usage error) | directory created if missing |

For directories, the structure relative to `PATH` is recreated under `DEST`, so
`REPO/PATH/sub/x` becomes `DEST/sub/x`.

**FR-DOWN-3 (M), integrity.** Each file is verified against a checksum from Nexus: SHA-256 from the
listing metadata when available, otherwise the SHA-1 `ETag`. On a mismatch the file is discarded and
reported as failed. `--no-verify` disables the check.

**FR-DOWN-4 (M), atomic writes.** Data is written to a temporary file in the destination directory
and renamed into place only after a successful, verified download. An interrupted download never
leaves a truncated file under the final name.

**FR-DOWN-5 (M), path safety.** Remote paths are sanitised before they are mapped to local paths.
Absolute paths, `..` segments, drive letters and names that are invalid on the local OS (e.g.
`CON`, `aux.txt`, `a:b` on Windows) are rejected per file. `nexr` MUST NEVER write outside `DEST`.

**FR-DOWN-6 (S).** The local modification time is set to the remote `Last-Modified`.
`--skip-existing` skips files that already exist locally.

**FR-DOWN-7 (C).** Interrupted downloads of large files are resumed with HTTP `Range`, which Nexus
supports.

Output mirrors `nexr up`: `downloaded` lines, a summary, and a JSON object with `downloaded`,
`skipped`, `failed` and `summary`.

### 6.5 `nexr rm`

```
nexr rm REPO/PATH... [-r|--recursive] [--include PATTERN]... [--exclude PATTERN]...
                   [--ignore-missing] [--server-side [--wait-timeout DURATION]]
                   [--concurrency N] [--dry-run] [-y|--yes]
```

**FR-RM-1 (M).** A file target deletes that file. A directory target (trailing `/`, or a path that
exists only as a directory) requires `-r` and deletes every file below it. In the rare case where a
path is both a file and a directory, `rm` without `-r` deletes the file, and `rm -r` deletes both.

**FR-RM-2 (M).** The command first builds a deletion plan, then asks for confirmation (§4.5), then
executes the plan in parallel. `--dry-run` prints the plan and stops.

**FR-RM-3 (M), deletion method.** For raw repositories, each file is deleted with
`DELETE <base>/repository/REPO/PATH`, which needs only the *delete* privilege. For other formats,
`nexr` deletes assets by ID (`DELETE /v1/assets/{id}`), with IDs taken from the listing. Deleting the
last asset of a raw component also removes the component (verified).

**FR-RM-4 (S).** `--server-side` deletes a directory with the Browse API folder delete
(`DELETE /v1/repositories/{repo}/browse?path=`). The request only *starts* an asynchronous deletion
on the server, so `nexr` polls the listing until the folder is gone or `--wait-timeout` (default
10 minutes) expires. This mode
needs a Nexus version that provides the Browse API and higher privileges (§9). It is meant for very
large folders.

**FR-RM-5 (M).** Empty directories disappear from the browse tree automatically on H2 databases. On
PostgreSQL, automatic trimming is disabled by default (Nexus 3.88+); empty folders may remain
visible until the *Repair - Repository trim browse tree* task runs. The command help mentions this.

Human output lists `deleted REPO/PATH` lines and a summary. JSON:
`{"dry_run", "deleted": [...], "missing": [...], "failed": [...], "summary": {...}}`.

### 6.6 `nexr docker ls`

```
nexr docker ls [-R|--repo REPO] [--registry-url URL] [--match PATTERN] [-l|--long]
```

**FR-DOCKER-1 (M).** All `nexr docker` commands work for repositories of format `docker` and `oci`, and
accept `-R/--repo REPO` (FR-IMGREF-2) and `--registry-url URL` (FR-NET-3).

**FR-DLS-1 (M).** Lists image names in a repository of format `docker` or `oci` using the Registry
API catalog (`GET <base>/repository/REPO/v2/_catalog`), following `Link` pagination. If the registry
endpoint is unavailable, `nexr` falls back to deriving names from the Components API.

**FR-DLS-2 (S).** `-l` adds the tag count and the most recent push time for each image.

JSON: `[{"repository": "docker-hosted", "name": "team/app", "tag_count": 3, "last_pushed": "…"}]`.
Without `-l`, the fields `tag_count` and `last_pushed` are `null`. `-q` prints names only.

### 6.7 `nexr docker tags`

```
nexr docker tags IMAGE [-R|--repo REPO] [--match PATTERN]
                     [--sort pushed|name|semver] [--reverse] [-l|--long]
```

**FR-DTAGS-1 (M).** Lists the tags of `IMAGE` with metadata taken from the Search API (one component
per tag, carrying the manifest asset):

| Field | Source | Availability |
|---|---|---|
| tag | component `version` | all versions |
| digest | manifest asset `checksum.sha256` (the manifest digest) | all versions |
| pushed | manifest asset `lastModified` | all versions |
| last pulled | manifest asset `lastDownloaded` | all versions |
| uploader | manifest asset `uploader` | all versions |
| media type | manifest asset `contentType` (image manifest vs. index) | all versions |
| created (build time) | asset `docker.created` | recent releases (3.96; not 3.71) |
| size, OS/architecture | asset `docker.totalSize`, `docker.os`, `docker.architecture` | recent releases (3.96; not 3.71) |

**FR-DTAGS-2 (M).** Tags are also read from the Registry API (`/v2/<name>/tags/list`). Tags that the
search index does not contain yet (pushed seconds ago) are still listed, with unknown metadata.

**FR-DTAGS-3 (M).** Default order: newest push first. `--sort semver` orders tags that are valid
SemVer (optionally prefixed with `v`) by version precedence and lists all other tags after them.

```
$ nexr docker tags team/app
TAG      DIGEST               PUSHED            SIZE
latest   sha256:b7f3d86d6e84  2026-09-26 16:14  2.10 MB
1.1      sha256:b7f3d86d6e84  2026-09-26 16:13  2.10 MB
1.0      sha256:c64c687cbea9  2026-09-26 16:13  3.46 MB
```

JSON: array of
`{"repository", "image", "tag", "digest", "media_type", "pushed", "created", "last_pulled", "size", "os", "architecture", "uploader", "component_id"}`.
`size` is reported as the server provides it (a string) or `null`.

### 6.8 `nexr docker rm`

```
nexr docker rm IMAGE:TAG [IMAGE:TAG...]   [-R REPO] [--ignore-missing] [--dry-run]
nexr docker rm IMAGE --keep N             [retention flags] [--dry-run] [-y]
nexr docker rm IMAGE --older-than DURATION [retention flags] [--dry-run] [-y]
nexr docker rm IMAGE --all                [retention flags] [--dry-run] [-y]

retention flags: [--match PATTERN]... [--exclude PATTERN]... [--sort pushed|semver|name]
                 [--concurrency N]
```

**FR-DRM-1 (M), explicit tags.** Each `IMAGE:TAG` is resolved to its component ID through the Search
API (`name`, `version`) and deleted with `DELETE /v1/components/{id}`. Only that tag is removed.
Other tags that point to the same digest are not affected (verified). A missing tag gives exit code
5 unless `--ignore-missing` is set.

**FR-DRM-2 (M), retention.** For a bare `IMAGE` with `--keep`, `--older-than` or `--all`, `nexr` computes
a deletion plan:

1. Collect all tags of the image (§6.7).
2. **Candidates:** the tags matching `--match` (all tags if it is not given).
3. **Protected:** candidates matching any `--exclude` pattern or the configured `docker.exclude` list
   (default `["latest"]`). They are never deleted and **do not count** toward `N`.
4. Order the remaining candidates by `--sort` (default `pushed`, newest first). With
   `--sort semver`, tags that are not valid SemVer are never deleted and are reported as skipped.
5. `--keep N` keeps the first `N` of them.
6. `--older-than D` additionally keeps every candidate pushed less than `D` ago.
7. `--all` selects every non-protected candidate.
8. Everything not kept is deleted.

At least one of `--keep`, `--older-than` or `--all` is required; `--keep` and `--all` are mutually
exclusive.

Example: the tags `latest, v5, v4, v3, v2, v1` (newest first) with `--keep 2` keep `latest`
(protected), `v5` and `v4`, and delete `v3`, `v2` and `v1`.

**FR-DRM-3 (M).** The default ordering is by push date (`lastModified` of the tag's manifest asset),
as decided in review (Q4). It is not the image build date, which can be fixed or zero in
reproducible builds.

**FR-DRM-4 (M).** Tags pushed in the last seconds may not be in the search index yet (§3.3). They are
never candidates, so eventual consistency can only make `nexr` delete *less*, never more.

**FR-DRM-5 (M).** `nexr docker rm` never deletes manifests referenced by digest (the children of
multi-arch indexes and attestations). Unreferenced manifests and layers are cleaned up by the
server-side Docker GC task (`nexr gc`). After a successful deletion `nexr` prints the reminder
`hint: run "nexr gc" to reclaim storage`.

**FR-DRM-6 (M).** The plan is shown before confirmation and in `--dry-run` mode:

```
$ nexr docker rm team/app --keep 2 --dry-run
TAG      PUSHED            ACTION  REASON
latest   2026-09-26 16:14  keep    protected (latest)
v5       2026-09-25 10:02  keep    newest 2
v4       2026-09-20 09:41  keep    newest 2
v3       2026-09-12 18:30  delete  beyond newest 2
v2       2026-09-01 07:12  delete  beyond newest 2
v1       2026-08-14 12:00  delete  beyond newest 2
dry run: 3 of 6 tags would be deleted from docker-hosted/team/app
```

JSON: `{"repository", "image", "dry_run", "decisions": [{"tag", "pushed", "action", "reason"}], "deleted": [...], "failed": [...], "summary": {...}}`.

**FR-DRM-7 (C).** `IMAGE` MAY be a pattern (e.g. `'team/*'`). The policy is then applied to each
matching image separately.

### 6.9 `nexr gc`

```
nexr gc [--repo REPO]... [--blobstore NAME]... [--task TASK]...
      [--skip-docker] [--skip-compact] [--create-missing]
      [--no-wait] [--wait-timeout DURATION] [--dry-run]
```

Deleting Docker tags or files does not free disk space right away. Space is reclaimed by server
tasks run in order: **Docker - Delete unused manifests and images** (`repository.docker.gc`), the
system tasks **Admin - Cleanup unused asset blobs** (`assetBlob.cleanup`, created automatically by
Nexus for each format in use), and then **Admin - Compact blob store** (`blobstore.compact`). `nexr gc` runs this sequence and waits for
it.

**FR-GC-1 (M), discovery.** `nexr` lists tasks (`GET /v1/tasks`) and selects Docker GC tasks, then
asset blob cleanup tasks (S; only where the server has them), then compaction tasks. `--task`
(repeatable, task ID or exact name) or the `gc.tasks` config key pin an explicit list and order.

**FR-GC-2 (S), filtering.** Where the server exposes task properties (3.96; not 3.71):

* `--repo` selects Docker GC tasks whose `repositoryName` is that repository or `*` (all);
* `--blobstore` selects compaction tasks by `blobstoreName`;
* if `--repo` is given without `--blobstore`, the blob stores of those repositories are looked up
  when the user may read the repository configuration.

Where properties are not exposed, all tasks of each type are run and a warning is printed, unless
`--task` is used.

**FR-GC-3 (S).** If no suitable task exists, `nexr` fails with exit code 5 and explains how to create
the tasks. `nexr` never creates tasks unless asked (Q10). With `--create-missing`, on servers that
support the task creation API, `nexr` creates
manual tasks named `nexr: Docker GC <repo>` and `nexr: Compact <blobstore>`, using the defaults from the
server's task templates.

**FR-GC-4 (M), execution.** Tasks run sequentially, in the order of FR-GC-1. For each task:

1. If it is already running, wait for that run to finish, because it may have started before the
   latest deletions.
2. Trigger it (`POST /v1/tasks/{id}/run`).
3. Wait until it has finished: its state is no longer `RUNNING` and `lastRun` differs from the value
   seen before the trigger.
4. Check that `lastRunResult` is `OK`.

`--wait-timeout` (default 1h) limits the wait for each task. When it expires, `nexr` reports the task as
still running and exits with code 8; the task keeps running on the server.

**FR-GC-5 (M).** `--no-wait` triggers all selected tasks and returns at once. `nexr` warns that
compaction started this way may not reclaim space released by a GC that is still running.

**FR-GC-6 (M).** The Docker GC task does not delete data deployed within its *deploy offset* (24 hours
by default). `nexr` shows the configured offset when the server exposes it.

**FR-GC-7 (M).** `--dry-run` shows the selected tasks in execution order without running them.

**FR-GC-8 (M).** The server may release some storage only later: for example, on 3.96 the blobs of
deleted raw files were not released by an immediate run of the cleanup tasks (see
[nexus-api.md](nexus-api.md#storage-reclamation)). `nexr gc` reports the blob store sizes before and
after the run when the user may read them, and its help text explains that disk usage can drop only
after later scheduled runs.

```
$ nexr gc --repo docker-hosted
TASK                                    TYPE                  RESULT  DURATION
nexr: Docker GC docker-hosted           repository.docker.gc  OK      1m12s
Cleanup unused docker blobs from nexus  assetBlob.cleanup     OK      3s
Compact default blob store              blobstore.compact     OK      4m03s
blob store default: 65.6 MiB -> 32.9 MiB (355 -> 230 blobs)
```

JSON: `{"tasks": [{"id", "name", "type", "result", "started", "finished", "duration_ms"}],
"blob_stores": [{"name", "blob_count_before", "size_before", "blob_count_after", "size_after"}]}`.
`blob_stores` is empty when the user may not read blob store metrics.

### 6.10 `nexr tasks`

```
nexr tasks [ls] [--type TYPE]
nexr tasks show TASK
nexr tasks run TASK [--wait] [--wait-timeout DURATION]
nexr tasks stop TASK
```

**FR-TASKS-1 (S).** `TASK` is a task ID or an exact task name. An ambiguous name is an error that lists
the matching IDs. Listing columns: `ID`, `NAME`, `TYPE`, `STATE`, `LAST RESULT`, `LAST RUN`,
`NEXT RUN`. `show` also prints schedule and properties when the server exposes them.

**FR-TASKS-2 (S).** `run --wait` uses the same wait algorithm as `nexr gc` (FR-GC-4). Triggering a
task that is already running is reported as such. Nexus answers such a request with HTTP 500, so
`nexr` checks the task state first.

**FR-TASKS-3 (C).** `nexr tasks create` and `nexr tasks rm` are planned for servers that support the task
creation API.

### 6.11 `nexr api`

```
nexr api PATH [-X|--method METHOD] [-H|--header 'Name: value']...
            [-d|--data DATA|@FILE|@-] [--paginate] [-i|--include]
```

**FR-API-1 (S).** Sends an authenticated request with the active profile's connection settings and
prints the response body to stdout. JSON is pretty-printed when stdout is a TTY.

* `PATH` starting with `/service/` or `/repository/` is relative to the base URL; any other path is
  relative to `<base>/service/rest` (e.g. `nexr api /v1/blobstores`).
* The method defaults to `GET`, or `POST` when `--data` is given. JSON bodies get
  `Content-Type: application/json` unless a header overrides it.
* `--paginate` follows `continuationToken` pages of `GET` responses and prints one merged JSON array
  of the `items`.
* `-i` also prints the status line and response headers.
* Non-2xx responses print the body and map the status to an exit code (§7.4).
* Credentials are only sent to the configured host; absolute URLs pointing to another host are
  rejected.

### 6.12 `nexr status`

**FR-STATUS-1 (S).** Checks and prints the effective URL, the profile and where it came from, the
server version and edition (from the `Server` header), read availability (`GET /v1/status`), write
availability (`GET /v1/status/writable`), whether the configured credentials are accepted, and how
many repositories the user may browse. The health endpoints are called without credentials, so
that wrong credentials do not hide the health of the server
([nexus-api.md](nexus-api.md#server-identification-and-health)). The authentication state is one
of `accepted`, `anonymous`, `rejected`, or `blocked` (the authentication rate limit of Nexus 3.96,
see [nexus-api.md](nexus-api.md#authentication)).

Exit codes: 0 when the server is reachable and the credentials (if any) are accepted; 7 when the
server is unreachable; 4 when the credentials are rejected, the user is blocked, or no credentials
are configured and no repository is visible without them; 1 when the server reports that it is
unavailable.

### 6.13 `nexr config`

**FR-CONFIGCMD-1 (S).**

* `nexr config view` prints the effective settings, each annotated with its source (flag, env,
  profile, file, default). Secrets are always redacted.
* `nexr config path` prints the config file in use and says whether it exists.
* `nexr config profiles` lists the profiles and marks the active one.

**FR-CONFIGCMD-2 (C).** `nexr config use PROFILE` sets `current_profile` in the config file.

### 6.14 `nexr version` and `nexr completion`

**FR-VERSION-1 (M).** `nexr version` (and `nexr --version`) prints the version, commit, build date, Go
version and OS/architecture. `--json` is supported.

**FR-COMPLETION-1 (M).** `nexr completion bash|zsh|fish|powershell` prints a shell completion script.

**FR-COMPLETION-2 (S).** Completion is dynamic for repository names, profile names and image names:
it queries the server with a short timeout and fails silently.

---

## 7. Output, errors and exit codes

### 7.1 Global flags

| Flag | Environment | Config key | Default | Meaning |
|---|---|---|---|---|
| `--profile NAME` | `NEXR_PROFILE` | `current_profile` | none | Profile to use |
| `--config PATH` | `NEXR_CONFIG` | n/a | OS default | Config file |
| `--url URL` | `NEXUS_URL` | `url` | none | Base URL |
| `-u, --user NAME` | `NEXUS_USER` | `user` | none | User name / token name code |
| `--password VALUE` | `NEXUS_PASSWORD` | `password` | none | Password (discouraged on the command line) |
| `--password-stdin` | n/a | n/a | off | Read the password from stdin |
| `--ca-cert PATH` | `NEXUS_CA_CERT` | `tls.ca_file` | none | Extra trusted CA bundle (PEM) |
| `--insecure` | `NEXUS_INSECURE` | `tls.insecure` | off | Skip TLS verification (prints a warning) |
| `--client-cert PATH`, `--client-key PATH` | `NEXUS_CLIENT_CERT`, `NEXUS_CLIENT_KEY` | `tls.client_cert`, `tls.client_key` | none | Client certificate for mutual TLS |
| `--timeout DURATION` | n/a | `timeout` | `60s` | Per-request API timeout |
| `--retries N` | n/a | `retries` | `3` | Retries of idempotent requests |
| `--json` | n/a | `output: json` | off | JSON output |
| `-q, --quiet` | n/a | n/a | off | Identifiers only / no output on success |
| `-v, --verbose` | n/a | n/a | off | Debug logging to stderr (`-vv` for more) |
| `--no-color` | `NO_COLOR` | n/a | auto | Disable colour |

### 7.2 Output conventions

**FR-OUT-1 (M).** stdout carries only command results. Progress, warnings, prompts, hints and logs go
to stderr.

**FR-OUT-2 (M).** The default output is human-readable: aligned columns without borders and an
upper-case header row. Colour is used sparingly, and only when the output is a TTY and colour has not
been disabled.

**FR-OUT-3 (M).** With `--json`, each invocation writes exactly one JSON document (an object or an
array) to stdout, UTF-8 encoded and newline-terminated. It is indented when stdout is a TTY and
compact otherwise.

**FR-OUT-4 (M).** JSON conventions: `snake_case` field names; timestamps in RFC 3339 UTC; sizes in
bytes as integers, except server-provided strings such as Docker `size`; unknown values are `null`;
lists are never `null` (empty lists are `[]`).

**FR-OUT-5 (M), JSON contract.** From v1.0, JSON fields are neither removed nor renamed, and their
types do not change, within a major version. New fields may be added at any time, so consumers must
ignore unknown fields. The contract is documented for every command.

**FR-OUT-6 (M).** With `--json`, a failure also writes a JSON error object to stderr:

```json
{"error": {"code": "not_found", "message": "repository \"raw-relases\" not found", "exit_code": 5, "http_status": 404, "hints": ["available raw repositories: raw-releases, raw-snapshots"]}}
```

**FR-OUT-7 (M).** `-q`: list commands print one identifier per line; mutating commands print nothing
on success. Errors are still printed.

**FR-OUT-8 (M).** Human-readable sizes use IEC units with one decimal (`48.2 MiB`). Human-readable
times use local time `YYYY-MM-DD HH:MM`.

**FR-OUT-9 (M).** `-v` logs each HTTP request (method, URL, status, duration, retries) to stderr.
`-vv` adds request and response headers and truncated API response bodies. `Authorization`,
`Cookie` and `Set-Cookie` values are always redacted, and content transfer bodies are never logged.

**FR-OUT-10 (S).** Progress (files, bytes, rate, ETA) is shown on stderr only when stderr is a TTY and
neither `-q` nor `--json` is set. It is redrawn at most 10 times per second.

### 7.3 Error messages

**FR-ERR-1 (M).** An error is printed as `nexr: <message>`, optionally followed by `hint: <text>` lines,
e.g.:

```
nexr: repository "raw-relases" not found
hint: available raw repositories: raw-releases, raw-snapshots
```

**FR-ERR-2 (M).** HTTP errors include the method, the path (without credentials), the status, the
server's message (from the JSON body, the plain-text body or the reason phrase) and, when present,
the Nexus fault ID (`siesta-faultid`), which lets administrators find the entry in the server log.

**FR-ERR-3 (M).** 401 errors hint at credentials, profiles and the credential scoping rule. 403 errors
name the privilege that is typically required (§9); when no credentials are configured, they hint
at configuring them instead (Nexus 3.71 answers 403 rather than 401 when anonymous access is
disabled). A `429 Too many authentication attempts` explains the authentication rate limit: how
the block ends and that an administrator can lift it. TLS errors hint at `--ca-cert` and
`--insecure`.

### 7.4 Exit codes

| Code | Name | Meaning |
|---|---|---|
| 0 | ok | Success, including dry runs and "nothing to do". |
| 1 | error | Unexpected error, server error (5xx after retries), or failed server task. |
| 2 | usage | Invalid arguments or flags; confirmation required but not given. |
| 3 | config | Invalid or incomplete configuration (missing URL, unknown profile, bad config file). |
| 4 | auth | Authentication failed (401) or permission denied (403). |
| 5 | not_found | Repository, path, image, tag or task not found. |
| 6 | partial | A bulk operation where some items succeeded and some failed. |
| 7 | network | Connection or TLS failure (DNS, refused, reset, certificate). |
| 8 | timeout | Operation timed out (request timeout, task wait timeout). |
| 9 | rejected | The server rejected the request: conflict or policy (400 validation, 405, 409, 413, 422). |
| 130 | interrupted | Cancelled by the user (Ctrl-C / SIGINT). |

**FR-EXIT-1 (M).** For bulk operations, if every item fails with the same category, that category's
code is used. If the failures have different categories, or some items succeeded, the code is 6.

---

## 8. Non-functional requirements

### 8.1 Build and distribution

* **NFR-BUILD-1 (M).** Static binaries for all platforms of §3.1, built with `-trimpath` and with
  version, commit and date embedded through `-ldflags`.
* **NFR-BUILD-2 (M).** Releases are produced by GoReleaser: `.tar.gz` archives (`.zip` on Windows) and
  a `checksums.txt` with SHA-256 sums, published as GitHub Releases.
* **NFR-BUILD-3 (S).** Reproducible builds: identical inputs give identical binaries.
* **NFR-BUILD-4 (M).** A Homebrew tap, updated by GoReleaser on every release, so that users can
  install and upgrade with `brew`.
* **NFR-BUILD-5 (C).** Planned after v1.0: signed checksums (cosign), SBOM, Scoop bucket, `.deb`/`.rpm`
  packages and a container image.
* **NFR-BUILD-6 (S).** Binary size ≤ 15 MB. `nexr version` starts in ≤ 50 ms.

### 8.2 Dependencies

* **NFR-DEP-1 (M).** Direct dependencies are limited to `github.com/spf13/cobra` (with its transitive
  dependencies `spf13/pflag` and, on Windows, `inconshreveable/mousetrap`) and a YAML parser (`go.yaml.in/yaml/v3`, the maintained successor of
  `gopkg.in/yaml.v3`). Everything else uses the Go standard library (`net/http`, `encoding/json`,
  `mime/multipart`, `text/tabwriter`, `log/slog`, `crypto/*`, …).
* **NFR-DEP-2 (M).** Any new dependency needs a written justification (an ADR in
  [architecture.md](architecture.md)). Test code uses only the standard library.

### 8.3 Performance and scalability

* **NFR-PERF-1 (M).** File transfers are streamed. Memory use does not depend on file size.
* **NFR-PERF-2 (M).** Listings are paginated and streamed (FR-LS-6).
* **NFR-PERF-3 (M).** HTTP connections are reused (keep-alive, HTTP/2 where the server offers it).
  Default concurrency is 4, configurable from 1 to 32.
* **NFR-PERF-4 (S).** A long scan (more than 2 s) shows progress on a TTY.

### 8.4 Reliability

* **NFR-REL-1 (M), retries.** Idempotent requests (`GET`, `HEAD`, `PUT`, `DELETE`) are retried on
  connection errors and on HTTP 429, 500, 502, 503 and 504, with exponential backoff and jitter. The
  default is 3 retries, starting at 500 ms, and `Retry-After` is honoured. `POST` requests are never
  retried automatically. A `429 Too many authentication attempts` from the authentication rate limit
  of Nexus 3.96 is never retried, because every further request keeps the user blocked.
* **NFR-REL-2 (M), timeouts.** Connect: 10 s. TLS handshake: 10 s. API requests: `--timeout` (60 s).
  File transfers have no total timeout, but fail when no data flows for 5 minutes.
* **NFR-REL-3 (M), cancellation.** On SIGINT, in-flight operations stop within 2 s, temporary files are
  removed, a partial summary is printed, and `nexr` exits with 130. A second SIGINT exits immediately.
* **NFR-REL-4 (M).** Downloads are atomic and verified (FR-DOWN-3, FR-DOWN-4).

### 8.5 Security

* **NFR-SEC-1 (M).** TLS certificate verification is on by default, with TLS 1.2 as the minimum
  version. `--insecure` prints a warning on every run.
* **NFR-SEC-2 (M).** Secrets never appear in output, logs, error messages or `nexr config view`.
* **NFR-SEC-3 (M).** Credentials are not forwarded on redirects to other hosts, and never sent to hosts
  other than the configured one (`nexr api`).
* **NFR-SEC-4 (M).** Path traversal protection for downloads (FR-DOWN-5). No implicit per-directory
  configuration (FR-CFG-3).
* **NFR-SEC-5 (M).** No telemetry of any kind.
* **NFR-SEC-6 (M).** CI runs `govulncheck`. Dependencies are pinned through `go.sum`. `SECURITY.md`
  describes how to report vulnerabilities.

### 8.6 Usability and portability

* **NFR-USE-1 (M).** Every command has `--help` with a description, the flags and at least two
  examples. Flag names are consistent across commands. Cobra suggests the intended command on typos.
* **NFR-USE-2 (M).** All messages and documentation are in English.
* **NFR-PORT-1 (M).** Windows is fully supported: path handling, long paths, console colour through VT
  sequences, and no Unix-only system calls.

### 8.7 Quality

* **NFR-QA-1 (M).** Unit tests cover at least 80% of the statements in `internal/`, excluding thin
  command wiring.
* **NFR-QA-2 (M).** CI runs `gofmt`, `go vet`, `golangci-lint` and the race detector, and runs the unit
  tests on Linux, macOS and Windows.
* **NFR-QA-3 (M).** An end-to-end suite runs against a real Nexus container of the latest release
  from M1 on. From M4 on, it also runs against 3.71, the oldest supported release, before every
  release.
* **NFR-DOC-1 (M).** README with a quick start, configuration and examples; this `docs/` folder;
  `CHANGELOG.md`; `CONTRIBUTING.md`; `SECURITY.md`.
* **NFR-LIC-1 (M).** MIT licence. All dependencies have MIT-compatible licences.

---

## 9. Required Nexus privileges

Observed on Nexus 3.96.3 with users that held only the listed privileges (details in
[nexus-api.md](nexus-api.md#privileges)). `<fmt>` and `<repo>` stand for the repository format and
name, e.g. `nx-repository-view-raw-raw-releases-read`.

| Command | Privileges |
|---|---|
| `nexr repos` | none specific; only repositories with *browse* permission are listed |
| `nexr ls` | `nx-repository-view-<fmt>-<repo>-browse` |
| `nexr up` (`put`) | `…-add` (new files), `…-edit` (overwrite) |
| `nexr up --method components` | `…-add`, `…-edit`, `…-read`, `…-browse` |
| `nexr down` | `…-read` (plus `…-browse` for directories) |
| `nexr rm` (raw) | `…-delete` (plus `…-browse` for directories) |
| `nexr rm` (other formats, asset API) | `…-browse`, `…-delete` |
| `nexr rm --server-side` | more than `browse`/`read`/`delete` (403 observed with those); exact privilege to be confirmed |
| `nexr docker ls`, `nexr docker tags` | `nx-repository-view-<fmt>-<repo>-browse` (and `-read`), where `<fmt>` is `docker` or `oci` |
| `nexr docker rm` | `…-browse`, `…-delete` |
| `nexr tasks`, `nexr gc` | `nx-tasks-read`, `nx-tasks-run` (plus the task create privilege for `--create-missing`, repository admin read for blob store lookup) |
| `nexr status` | none (status endpoints allow anonymous access) |
| `nexr api` | depends on the endpoint |

The README MUST include example role definitions for a CI uploader, a read-only consumer and a
cleanup operator.

---

## 10. Acceptance criteria

| ID | Criterion | Milestone |
|---|---|---|
| AC-1 | `nexr version` runs on all release platforms (verified in CI by building all targets and running the Linux, macOS and Windows binaries). | M0 |
| AC-2 | Configuration precedence and credential scoping pass a table-driven test suite that covers every row of §5.1. | M0 |
| AC-3 | `nexr repos --json` against the latest Nexus release returns the expected repositories; `nexr status` detects version and authentication state. | M0 |
| AC-4 | Uploading a 1 GiB file keeps resident memory below 64 MiB; round trips `up` → `down` preserve content (checksums) and directory layout, including names with spaces and non-ASCII characters. | M1 |
| AC-5 | A directory of 1,000 files uploads with `--concurrency 8`; injected server failures (500/503) are retried; permanent failures are reported with exit code 6. | M1 |
| AC-6 | `rm -r --dry-run` lists exactly the files that `rm -r` then deletes; bulk deletion without `--yes` in a non-interactive shell deletes nothing and exits with 2. | M1 |
| AC-7 | Remote paths containing `..`, absolute paths or Windows-reserved names never cause writes outside `DEST`. | M1 |
| AC-8 | `nexr docker tags` shows correct digests and push times for images pushed with `docker push` and for multi-arch indexes copied with `crane`. | M2 |
| AC-9 | `--keep N` keeps the N newest non-protected tags; `latest` is protected by default; deleting a tag leaves other tags with the same digest intact; multi-arch images stay pullable after deletions of other tags plus `nexr gc`. | M2 |
| AC-10 | `nexr gc` runs Docker GC, asset blob cleanup (where present) and compaction in this order, waits for each, reports their results, handles a task that is already running, and exits with 8 on timeout. | M3 |
| AC-11 | Every command passes the end-to-end suite against 3.71: listing falls back to search and scan without the Browse API, `nexr gc` works without task properties and without the task creation API, and registry pagination works despite the 3.71 `Link` header. | M4 |
| AC-12 | The end-to-end suite passes against the latest Nexus release and 3.71; GoReleaser publishes the release artifacts and updates the Homebrew tap; README and command help are complete. | M5 |

---

## 11. Assumptions, decisions and open questions

### 11.1 Assumptions

* **A-1.** Users have Nexus accounts with suitable privileges. For CI, dedicated service accounts or
  user tokens are provisioned.
* **A-2.** The REST API (`/service/rest`) and repository content (`/repository/`) are reachable under
  the same base URL.
* **A-3.** Nexus tasks for Docker GC and compaction are either present or may be created by an
  administrator (or by `nexr gc --create-missing` where supported).
* **A-4.** Clock skew between client and server is small compared to retention durations
  (`--older-than` is evaluated against server timestamps using the client clock).

### 11.2 Decisions from the review of 2026-09-26

| # | Topic | Decision | Specified in |
|---|---|---|---|
| Q1 | Supported Nexus versions | 3.71 and newer only; older releases are not supported. The latest release (3.96) comes first; 3.71–3.9x compatibility follows in M4. | §3.2 |
| Q2 | Docker registry endpoint | Default `<base>/repository/<repo>/v2/`; overridable per repository by flag, environment variable or config, e.g. for a reverse proxy that serves the registry at `https://<domain>/v2/`. | FR-NET-3, FR-IMGREF-3 |
| Q3 | TLS | `--ca-cert` and `--insecure`; mutual TLS (client certificates) as an optional setting for servers that require it. | §5.3, §7.1 |
| Q4 | Order of "keep the last N tags" | Push date. `--sort semver` and `--sort name` are options. `latest` stays protected by default and can be unprotected through `docker.exclude`. | FR-DRM-2, FR-DRM-3 |
| Q5 | Raw upload method | HTTP `PUT` by default; Components API with `--method components`. | FR-UP-4, ADR-003 |
| Q6 | Name of the tool | `nexr`, short for **Nex**us **R**epository. The working name `nx` was dropped because it clashes with the Nx build system and with another Nexus CLI that installs a binary named `nx` ([addozhang/nexus-cli](https://github.com/addozhang/nexus-cli)). No conflicts for `nexr` were found on npm, PyPI, crates.io, homebrew-core or in a web search (2026-09-26). | whole document |
| Q7 | Profiles for several Nexus instances | Included in v1.0. | §5.4 |
| Q8 | Next formats and admin features | Maven and Helm upload, then repository management. | [roadmap.md](roadmap.md) |
| Q9 | Distribution | GitHub Releases and a Homebrew tap in v1.0; Scoop, `.deb`/`.rpm`, container image and signing later. | NFR-BUILD-2, NFR-BUILD-4, NFR-BUILD-5 |
| Q10 | Creating server tasks | Only with `nexr gc --create-missing`; never by default. | FR-GC-3 |

### 11.3 Open questions

None at the moment.

---

## Appendix A: Complete configuration example

```yaml
# ~/.config/nexr/config.yaml
current_profile: prod

# Top-level defaults, inherited by all profiles
timeout: 60s
retries: 3
concurrency: 4
output: table
docker:
  exclude: ["latest", "re:^release-.*$"]
upload:
  method: put
gc:
  wait_timeout: 1h

profiles:
  prod:
    url: https://nexus.example.com
    user: ci-bot
    password_env: NEXUS_PROD_PASSWORD
    docker:
      repository: docker-hosted
      registry_urls:
        # reverse proxy: https://registry.example.com/v2/ → /repository/docker-hosted/v2/
        docker-hosted: https://registry.example.com
    gc:
      tasks: ["Docker GC docker-hosted", "Compact default blob store"]

  staging:
    url: https://staging.example.com/nexus
    user: alice
    password_file: ~/.config/nexr/staging.secret
    tls:
      ca_file: /etc/ssl/certs/corp-root-ca.pem
      client_cert: ~/.config/nexr/staging-client.pem   # only if the server requires mutual TLS
      client_key: ~/.config/nexr/staging-client.key

  lab:
    url: https://nexus.lab.local
    tls:
      insecure: true
```

## Appendix B: Command cheat sheet

```sh
# Connection check and configuration
nexr status
nexr config view
nexr --profile staging repos

# Repositories
nexr repos --format raw
nexr repos show docker-hosted

# Files (raw)
nexr ls raw-releases/myapp/
nexr ls -rl raw-releases/myapp/1.4.0/
nexr up ./dist raw-releases/myapp/1.4.0/
nexr up ./report.pdf raw-releases/docs/report-2026-09.pdf
tar czf - ./site | nexr up - raw-releases/backups/site.tgz
nexr down raw-releases/myapp/1.4.0/ ./release/
nexr down raw-releases/myapp/1.4.0/myapp.tar.gz - | tar xz
nexr rm raw-releases/myapp/1.4.0/checksums.txt
nexr rm -r raw-releases/myapp/1.0.0/ --dry-run

# Docker / OCI
nexr docker ls -R docker-hosted
nexr docker ls -R docker-hosted --registry-url https://registry.example.com
nexr docker tags team/app
nexr docker tags registry.example.com/team/app      # host resolved through docker.registry_urls
nexr docker rm team/app:1.0 team/app:1.1
nexr docker rm team/app --keep 10 --older-than 30d --dry-run
nexr docker rm team/app --keep 10 --exclude 're:^v\d+\.\d+\.\d+$' --yes

# Storage reclamation and tasks
nexr gc --repo docker-hosted
nexr tasks
nexr tasks run "Compact default blob store" --wait

# Everything else
nexr api /v1/blobstores
nexr api '/v1/components?repository=raw-releases' --paginate
```
