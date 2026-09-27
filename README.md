# nexr

[![CI](https://github.com/yand3r3d3v/nexr/actions/workflows/ci.yml/badge.svg)](https://github.com/yand3r3d3v/nexr/actions/workflows/ci.yml)

A command-line tool for
[Sonatype Nexus Repository 3](https://www.sonatype.com/products/sonatype-nexus-repository): work
with repositories, files, container images and storage cleanup from a terminal or a CI job, without
the web UI.

> **Status: early development.** Milestones M0 to M2 are done: configuration and profiles,
> `nexr status`, `nexr repos`, `nexr config`, the file commands `ls`, `up`, `down` and `rm` for raw
> repositories, and `nexr docker ls`, `tags` and `rm` for container images, with retention rules.
> Storage cleanup (`nexr gc`, `nexr tasks`) follows next; see the [roadmap](docs/roadmap.md). Until
> v1.0, commands and JSON output may still change.

* One static binary for Linux, macOS and Windows (amd64 and arm64). Nothing else to install.
* Nexus Repository 3.71 and newer. The latest release (3.96) is supported first; older releases
  get full support before v1.0.
* One configuration for everything: environment variables, an optional YAML file with profiles for
  several Nexus instances, and flags.
* Readable tables, `--json` for scripts, and [documented exit codes](#exit-codes).

## Install

From source, with Go 1.26 or newer:

```sh
go install github.com/yand3r3d3v/nexr/cmd/nexr@latest
```

Prebuilt binaries will be attached to the [GitHub releases](https://github.com/yand3r3d3v/nexr/releases)
from v0.1.0 on. A Homebrew tap is planned for v1.0.

## Quick start

```sh
export NEXUS_URL=https://nexus.example.com
export NEXUS_USER=ci-bot            # a user name, or the name code of a user token
export NEXUS_PASSWORD=...           # or NEXUS_PASSWORD_FILE=/run/secrets/nexus

nexr status
```

```text
URL:           https://nexus.example.com (from env NEXUS_URL)
Server:        Nexus Repository 3.96.3-01 (COMMUNITY)
Read:          available
Write:         available
Auth:          credentials accepted for user ci-bot (from env NEXUS_USER)
Repositories:  13 visible
```

List and inspect repositories:

```console
$ nexr repos --format docker
NAME           FORMAT  TYPE    URL
docker-hosted  docker  hosted  https://nexus.example.com/repository/docker-hosted
docker-proxy   docker  proxy   https://nexus.example.com/repository/docker-proxy

$ nexr repos show docker-hosted
Name:                       docker-hosted
Format:                     docker
Type:                       hosted
URL:                        https://nexus.example.com/repository/docker-hosted
Online:                     yes
Blob store:                 default
Write policy:               ALLOW
Strict content validation:  yes
Docker HTTP port:           8082
Docker force basic auth:    yes
Docker V1 API:              no

$ nexr repos --type hosted --match 'raw-*' -q
raw-releases
raw-snapshots

$ nexr repos --json | jq -r '.[] | select(.format == "maven2") | .name'
maven-central
maven-public
```

Storage, cleanup and Docker settings in `repos show` need a user who may read the repository
configuration (an administrative privilege); other users see the basic fields.

Work with files in raw repositories:

```console
$ nexr up ./dist raw-releases/myapp/1.4.0/
uploaded  raw-releases/myapp/1.4.0/myapp.tar.gz  (48.2 MiB)
uploaded  raw-releases/myapp/1.4.0/checksums.txt  (312 B)
2 files, 48.2 MiB uploaded in 3.1s

$ nexr ls -l raw-releases/myapp/
         -  -                 1.3.0/
         -  -                 1.4.0/
   1.2 KiB  2026-09-20 14:03  README.md

$ nexr down raw-releases/myapp/1.4.0/ ./release
$ nexr down raw-releases/myapp/config.json - | jq .version
$ nexr rm -r raw-releases/myapp/1.3.0/ --dry-run
```

* `nexr up DIR REPO/PATH/` uploads the *contents* of `DIR` below `PATH/` (like
  `aws s3 cp --recursive`), so `nexr down REPO/PATH/ DIR` is its exact inverse. `nexr up - REPO/FILE`
  uploads stdin.
* Transfers run in parallel (`--concurrency`, default 4), are retried after network errors and 5xx
  responses, and stream files without holding them in memory. Downloads are verified against the
  checksums from Nexus and written atomically.
* `--include` and `--exclude` use `.gitignore` rules: `--exclude '*.log'` skips `.log` files at any
  depth, `--exclude node_modules` skips every such directory.
* `nexr rm` asks before deleting more than one file (or needs `--yes` in scripts); `--dry-run` shows
  what would be deleted.
* **Listings can lag behind uploads.** Nexus updates its search and browse indexes a few seconds
  after an upload, so a file uploaded in the last seconds may be missing from `ls`, or from
  `down`/`rm -r` of a directory.

Work with container images in docker and oci repositories:

```console
$ export NEXR_DOCKER_REPO=docker-hosted    # or -R docker-hosted, or docker.repository in the config

$ nexr docker ls -l
IMAGE         TAGS  LAST PUSHED
team/app      6     2026-09-26 16:14
team/worker   2     2026-09-25 09:30

$ nexr docker tags team/app
TAG     DIGEST               PUSHED            SIZE
latest  sha256:b7f3d86d6e84  2026-09-26 16:14  2.1 MiB
v5      sha256:b7f3d86d6e84  2026-09-26 16:13  2.1 MiB
v4      sha256:c64c687cbea9  2026-09-20 09:41  3.3 MiB
multi   sha256:ce64758a109e  2026-09-12 18:30  multi-arch

$ nexr docker rm team/app --keep 2 --dry-run
TAG     PUSHED            ACTION  REASON
latest  2026-09-26 16:14  keep    protected (latest)
v5      2026-09-26 16:13  keep    newest 2
v4      2026-09-20 09:41  keep    newest 2
v3      2026-09-12 18:30  delete  beyond newest 2
v2      2026-09-01 07:12  delete  beyond newest 2
v1      2026-08-14 12:00  delete  beyond newest 2
dry run: 3 of 6 tags would be deleted from docker-hosted/team/app

$ nexr docker rm team/app:v1 team/app:v2 --yes
$ nexr docker rm team/app --older-than 30d --match 'feature-*' --yes
$ nexr docker rm 'team/*' --keep 10 --sort semver --yes
```

* Tags are listed through the Registry API, with push times, digests and sizes from the search
  index. Build time, platform and size need Nexus 3.96 (3.71 does not record them).
* Deleting a tag removes only that tag: other tags of the same manifest (like `latest` and `v5`
  above) and the platform manifests of multi-arch images stay.
* Retention rules keep the `--keep N` newest tags (by push time, or `--sort semver`/`name`), keep
  tags pushed within `--older-than`, or select `--all`; `--match` limits the candidates, and
  `--exclude` plus the `docker.exclude` setting (default `latest`) protect tags. The plan is shown
  before a confirmation, which scripts answer with `--yes`.
* Tags pushed in the last seconds are not in the search index yet: they are listed without a push
  time, and retention rules never delete them.
* Deleted tags free no storage by themselves: the Nexus tasks *Docker - Delete unused manifests and
  images* and *Admin - Compact blob store* reclaim it (`nexr gc` will run them).

## Commands

| Command | Description |
|---|---|
| `nexr status` | Check the connection: URL, server version and edition, read/write availability, credentials. |
| `nexr repos [ls]` | List repositories; filter with `--format`, `--type` and `--match` (glob or `re:REGEX`). |
| `nexr repos show REPO` | Show repository details. |
| `nexr ls REPO[/PATH]` | List files and directories (`-r` recursive, `-l` sizes and times, `--sort`). |
| `nexr up SRC... REPO[/PATH]` | Upload files and directory trees to a hosted raw repository (`--dry-run`, `--skip-existing`, `--verify`). |
| `nexr down REPO/PATH [DEST]` | Download a file or a directory tree; `-` writes a file to stdout. |
| `nexr rm REPO/PATH...` | Delete files, or directories with `-r` (`--dry-run`, `--yes`, `--ignore-missing`). |
| `nexr docker ls` | List the images of a docker or oci repository (`-l` tag counts and last push, `--match`). |
| `nexr docker tags IMAGE[:TAG]` | List tags with digests, push times and sizes (`--sort pushed\|semver\|name`, `-l`, `--match`). |
| `nexr docker rm IMAGE:TAG...` | Delete tags. |
| `nexr docker rm IMAGE --keep N` | Delete tags by a retention rule (`--keep`, `--older-than`, `--all`, `--match`, `--exclude`, `--sort`, `--dry-run`). |
| `nexr config view` | Show the effective settings and where each comes from. Passwords are always redacted. |
| `nexr config path` | Print the location of the config file. |
| `nexr config profiles` | List the profiles of the config file. |
| `nexr version` | Print version information (`--json` supported). |
| `nexr completion SHELL` | Print a completion script for bash, zsh, fish or PowerShell. |

Planned: `nexr gc` and `nexr tasks` for cleanup tasks, and `nexr api` for any REST call. The
[specification](docs/specification.md) describes them in detail.

Global flags work with every command:

| Flag | Meaning |
|---|---|
| `--profile NAME` | Profile from the config file (env `NEXR_PROFILE`). |
| `--config PATH` | Config file (env `NEXR_CONFIG`). |
| `--url URL`, `-u, --user NAME` | Nexus base URL and user (env `NEXUS_URL`, `NEXUS_USER`). |
| `--password-stdin` | Read the password from stdin. `--password VALUE` also works, but the value is visible to other users. |
| `--ca-cert PATH`, `--insecure` | Trust an extra CA bundle, or skip TLS verification. |
| `--client-cert PATH`, `--client-key PATH` | Client certificate for servers that require mutual TLS. |
| `--timeout DURATION`, `--retries N` | Per-request timeout (default `60s`) and retries of idempotent requests (default 3). |
| `--json`, `-q, --quiet` | JSON output; identifiers only, or nothing on success. |
| `-v, --verbose` | Log HTTP requests to stderr; `-vv` adds headers and bodies. Secrets are always redacted. |

## Configuration

Settings come from these sources, highest precedence first:

1. command-line flags;
2. the profile selected with `--profile` or `NEXR_PROFILE`;
3. environment variables;
4. the config file: its default profile (`current_profile`) over its top-level settings;
5. built-in defaults.

The config file is optional. It lives at `~/.config/nexr/config.yaml` (or
`$XDG_CONFIG_HOME/nexr/config.yaml`) on Linux and macOS, and at `%AppData%\nexr\config.yaml` on Windows;
`nexr config path` prints the location. It is never read from the current directory.

```yaml
current_profile: prod            # used when neither --profile nor NEXR_PROFILE is given

timeout: 60s                     # top-level settings apply to every profile

profiles:
  prod:
    url: https://nexus.example.com
    user: ci-bot
    password_env: NEXUS_PROD_PASSWORD      # read the password from this variable
  staging:
    url: https://staging.example.com/nexus # a context path is fine
    user: alice
    password_file: ~/.config/nexr/staging.secret
    tls:
      ca_file: /etc/ssl/certs/corp-root-ca.pem
```

```sh
nexr --profile staging repos
NEXR_PROFILE=staging nexr status
nexr config profiles
```

A profile takes its password from exactly one of `password_env`, `password_file`,
`password_command` (a command that prints the password, such as `pass show nexus/prod`) or
`password` (plain text; `nexr` warns if the file is readable by others).

Settings of the `nexr docker` commands:

```yaml
profiles:
  prod:
    url: https://nexus.example.com
    docker:
      repository: docker-hosted          # default for -R/--repo (env NEXR_DOCKER_REPO)
      exclude: [latest, "release-*"]     # tags that retention rules never delete (default: [latest])
      registry_urls:                     # where the Registry API of a repository is served
        docker-hosted: https://registry.example.com
```

`nexr` reads tags through the Registry API of a repository, by default at
`<url>/repository/REPO/v2/`. When a reverse proxy serves a repository's registry elsewhere (for
example `https://registry.example.com/v2/` forwarded to `/repository/docker-hosted/v2/`), or a
Docker connector port does (`https://nexus.example.com:8443`), set that endpoint in
`docker.registry_urls`, with `NEXR_DOCKER_REGISTRY_URL` or with `--registry-url`. The endpoint gets
the TLS settings and the credentials of the profile. Image references may then start with the
registry host, as for `docker pull`: `nexr docker tags registry.example.com/team/app`.

Environment variables:

| Variable | Meaning |
|---|---|
| `NEXUS_URL` | Base URL of the Nexus instance, including an optional context path. |
| `NEXUS_USER` | User name, or the name code of a user token. |
| `NEXUS_PASSWORD`, `NEXUS_PASSWORD_FILE` | Password (or token pass code), or a file that contains it. |
| `NEXUS_CA_CERT`, `NEXUS_INSECURE` | Extra trusted CA bundle (PEM); `true` skips TLS verification. |
| `NEXUS_CLIENT_CERT`, `NEXUS_CLIENT_KEY` | Client certificate and key (PEM) for mutual TLS. |
| `NEXR_CONFIG`, `NEXR_PROFILE` | Config file and profile. |
| `NEXR_DOCKER_REPO` | Repository of the `nexr docker` commands. |
| `NEXR_DOCKER_REGISTRY_URL` | Registry endpoint of that repository. |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | Proxy settings. |
| `NO_COLOR` | Disables colour. |

**Credentials go only to the server they belong to.** Credentials from a source are used only with a
URL from the same source or from a lower-precedence one. For example, `NEXUS_USER` and
`NEXUS_PASSWORD` are used with the URL from the config file (the typical CI setup), but not with a
different URL given by `--url` or by an explicitly selected profile. Registry endpoints follow the
same rule, because they receive the credentials. `nexr config view` shows every setting with its
source, and `-v` reports credentials and endpoints that were left out.

## Output and exit codes

Tables go to stdout; warnings, hints and logs go to stderr. With `--json`, each command writes one
JSON document, and failures write an error object to stderr:

```json
{"error":{"code":"not_found","message":"repository \"nope\" not found: GET /service/rest/v1/repositories/nope: 404 Not Found (fault id 713ec2ec-e2f7-4ee9-a821-701fbbf1ebff)","exit_code":5,"http_status":404,"hints":["run \"nexr repos\" to list the repositories you may browse"]}}
```

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Unexpected or server error, or the server reports that it is not available. |
| 2 | Invalid arguments or flags. |
| 3 | Invalid or incomplete configuration (no URL, unknown profile, bad config file). |
| 4 | Authentication failed (401) or permission denied (403). |
| 5 | Not found. |
| 6 | Partial failure of a bulk operation. |
| 7 | Network or TLS failure. |
| 8 | Timeout. |
| 9 | The server rejected the request (validation, conflict, policy). |
| 130 | Interrupted (Ctrl-C). |

## Shell completion

Completion includes repository names, remote paths and profile names.

```sh
# bash
source <(nexr completion bash)
# zsh
nexr completion zsh > "${fpath[1]}/_nexr"
# fish
nexr completion fish > ~/.config/fish/completions/nexr.fish
# PowerShell
nexr completion powershell | Out-String | Invoke-Expression
```

## Troubleshooting

* `nexr status` checks the URL, the server and the credentials in one go.
* **"429 Too many authentication attempts"** means that Nexus 3.96 or newer has blocked the user
  after more than three failed sign-ins. The block ends after 15 minutes without *any* request for
  that user; every request, even with the right password, starts that time again. Stop the jobs
  that use the account, fix the password, and wait, or ask an administrator to update the user,
  which lifts the block at once. `nexr` never retries this error.
* `nexr config view` shows which setting comes from where.
* **A tag just pushed has no push time**, or a deleted tag still shows up for a moment: the search
  index of Nexus follows pushes and deletions after a few seconds.
* **`nexr docker` warns that the registry endpoint failed**: check `--registry-url`,
  `NEXR_DOCKER_REGISTRY_URL` or `docker.registry_urls`. Without a working endpoint, `nexr` lists
  images and tags from the search index and the components.
* `nexr -v …` logs every HTTP request; `-vv` adds headers and truncated bodies. Passwords and
  cookies are redacted.
* Error messages include the Nexus fault ID when the server reports one; administrators can look it
  up in the server log.

## Documentation

* [Technical specification](docs/specification.md)
* [Architecture](docs/architecture.md)
* [Nexus API notes](docs/nexus-api.md)
* [Roadmap](docs/roadmap.md)
* [Changelog](CHANGELOG.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Please report security issues as described in
[SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
