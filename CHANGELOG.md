# Changelog

All notable changes to this project are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before v1.0.0, commands, flags and the
JSON output may change between minor versions.

## [Unreleased]

### Added

- `nexr docker ls`: list the images of docker and oci repositories, with tag counts and last
  pushes (`-l`); falls back to the components when the registry endpoint fails.
- `nexr docker tags`: list tags with digest, push time, size, platform, build time, last pull and
  uploader; tags not indexed yet are included; sort by push time, version or name.
- `nexr docker rm`: delete tags by name, or by retention rules (`--keep`, `--older-than`, `--all`,
  `--match`, `--exclude`, `docker.exclude`, `--sort pushed|semver|name`) with a plan, dry runs and
  confirmation. Only the tag is deleted; tags of the same manifest and the manifests of multi-arch
  images stay. A tag pushed again after the plan was made is skipped.
- Registry endpoints per repository (`--registry-url`, `NEXR_DOCKER_REGISTRY_URL`,
  `docker.registry_urls`) for reverse proxies and Docker connectors, subject to credential scoping;
  image references may start with a configured registry host.
- `nexr config view` shows each registry endpoint with its source.
- `nexr ls`: list raw (and other) repositories by path, one level or recursively (`-r`), with
  sizes, times and checksums (`-l`, `--json`), name-prefix matching and sorting.
- `nexr up`: upload files, directory trees and stdin to hosted raw repositories, in parallel, with
  `--include`/`--exclude`, `--skip-existing`, `--verify`, `--dry-run` and the Components API as an
  alternative method.
- `nexr down`: download files and directory trees, verified against the checksums of Nexus and
  written atomically; remote names that would leave the destination are rejected.
- `nexr rm`: delete files and directories (`-r`) with dry runs, confirmation of bulk deletions,
  `--ignore-missing` and server-side folder deletion.
- Patterns follow the `.gitignore` rules (`*.log` matches at any depth).
- Idempotent requests are also retried after `500` responses.

- Configuration from flags, environment variables (`NEXUS_*`, `NEXR_*`) and an optional YAML config
  file with profiles, with a documented precedence order.
- Credential scoping: credentials are only sent to the URL they were configured for.
- Password sources: `--password-stdin`, `NEXUS_PASSWORD`, `NEXUS_PASSWORD_FILE`, and the config keys
  `password`, `password_env`, `password_file` and `password_command`.
- TLS options: extra CA bundle, client certificates for mutual TLS, and `--insecure`.
- Retries of idempotent requests with backoff and `Retry-After` support; `-v`/`-vv` request logging
  with secrets redacted.
- `nexr status`: server version and edition, read/write availability, credential check and the
  number of visible repositories; detects the authentication rate limit of Nexus 3.96. Behind a
  reverse proxy that replaces the `Server` header, the version is read from the API description.
- A `429 Too many authentication attempts` from Nexus 3.96 is never retried and is explained,
  because every further request would keep the user blocked.
- `nexr repos`, `nexr repos ls` and `nexr repos show`.
- `nexr config view`, `nexr config path` and `nexr config profiles`.
- `nexr version` and `nexr completion` (bash, zsh, fish, PowerShell), with completion of repository
  and profile names.
- `--json` output for every command, JSON error objects on stderr, and documented exit codes.

[Unreleased]: https://github.com/yand3r3d3v/nexr/commits/main
