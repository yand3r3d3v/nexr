# Changelog

All notable changes to this project are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before v1.0.0, commands, flags and the
JSON output may change between minor versions.

## [Unreleased]

### Added

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
