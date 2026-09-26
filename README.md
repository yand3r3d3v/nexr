# nexr

[![CI](https://github.com/yand3r3d3v/nexr/actions/workflows/ci.yml/badge.svg)](https://github.com/yand3r3d3v/nexr/actions/workflows/ci.yml)

A command-line tool for
[Sonatype Nexus Repository 3](https://www.sonatype.com/products/sonatype-nexus-repository): work
with repositories, files, container images and storage cleanup from a terminal or a CI job, without
the web UI.

> **Status: early development.** The foundation (milestone M0) is done: configuration and profiles,
> `nexr status`, `nexr repos` and `nexr config`. Raw files, Docker images and cleanup follow in the
> next milestones; see the [roadmap](docs/roadmap.md). Until v1.0, commands and JSON output may still
> change.

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

## Commands

| Command | Description |
|---|---|
| `nexr status` | Check the connection: URL, server version and edition, read/write availability, credentials. |
| `nexr repos [ls]` | List repositories; filter with `--format`, `--type` and `--match` (glob or `re:REGEX`). |
| `nexr repos show REPO` | Show repository details. |
| `nexr config view` | Show the effective settings and where each comes from. Passwords are always redacted. |
| `nexr config path` | Print the location of the config file. |
| `nexr config profiles` | List the profiles of the config file. |
| `nexr version` | Print version information (`--json` supported). |
| `nexr completion SHELL` | Print a completion script for bash, zsh, fish or PowerShell. |

Planned: `nexr ls`, `up`, `down` and `rm` for raw repositories; `nexr docker ls`, `tags` and `rm` with
retention rules (`--keep N`, `--older-than`); `nexr gc` and `nexr tasks` for cleanup tasks; `nexr api`
for any REST call. The [specification](docs/specification.md) describes them in detail.

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

Environment variables:

| Variable | Meaning |
|---|---|
| `NEXUS_URL` | Base URL of the Nexus instance, including an optional context path. |
| `NEXUS_USER` | User name, or the name code of a user token. |
| `NEXUS_PASSWORD`, `NEXUS_PASSWORD_FILE` | Password (or token pass code), or a file that contains it. |
| `NEXUS_CA_CERT`, `NEXUS_INSECURE` | Extra trusted CA bundle (PEM); `true` skips TLS verification. |
| `NEXUS_CLIENT_CERT`, `NEXUS_CLIENT_KEY` | Client certificate and key (PEM) for mutual TLS. |
| `NEXR_CONFIG`, `NEXR_PROFILE` | Config file and profile. |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | Proxy settings. |
| `NO_COLOR` | Disables colour. |

**Credentials go only to the server they belong to.** Credentials from a source are used only with a
URL from the same source or from a lower-precedence one. For example, `NEXUS_USER` and
`NEXUS_PASSWORD` are used with the URL from the config file (the typical CI setup), but not with a
different URL given by `--url` or by an explicitly selected profile. `nexr config view` shows every
setting with its source, and `-v` reports credentials that were left out.

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

Completion includes repository and profile names.

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
