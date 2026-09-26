# Contributing to nexr

Thank you for helping. Bug reports, ideas and pull requests are welcome.

## Before you start

* **Bugs:** open an issue with the output of `nexr version`, the Nexus version (`nexr status`), the
  command you ran, and the output with `-v`. `-v` redacts passwords, but check the log before you
  post it.
* **Features and design changes:** open an issue first. The behaviour of every command is written
  down in [docs/specification.md](docs/specification.md), and a change of behaviour starts with a
  change of that document. Decisions that change the architecture or add a dependency get an ADR in
  [docs/architecture.md](docs/architecture.md#11-architecture-decision-records).
* **Security problems:** do not open an issue; see [SECURITY.md](SECURITY.md).

## Development setup

You need Go 1.26 or newer and `make`. For `make lint` you also need
[golangci-lint](https://golangci-lint.run) v2, for `make e2e` Docker, and for `make snapshot`
[GoReleaser](https://goreleaser.com) v2.

```sh
make build        # bin/nexr
make test         # all tests
make test-race    # all tests with the race detector
make lint         # gofmt, go vet, golangci-lint
make cover        # coverage report in coverage.html
make e2e          # end-to-end tests against Nexus in Docker (NEXUS_VERSION=3.96.3)
make e2e-down     # remove the Nexus container of the end-to-end tests
make snapshot     # cross-compile all release targets into dist/
```

## How the code is organised

[docs/architecture.md](docs/architecture.md) describes the layers and packages. In short:

* `cmd/nexr` is the entry point; `internal/cli/...` holds the commands (one package per command
  group), built on `internal/cli/cmdutil`.
* `internal/nexus` is the REST client, `internal/httpx` the HTTP transport (TLS, retries,
  authentication, logging), `internal/config` the configuration.
* `internal/output` and `internal/errs` handle output, error messages and exit codes.
* `internal/archtest` enforces the dependency rules: dependencies point downwards, only the CLI
  packages use cobra, and only `httpx` builds HTTP clients. A new package must be added to the
  layer table in that test.

## Tests

* Unit and client tests live next to the code and use `net/http/httptest`.
* Command tests (`internal/cli/root`) run whole commands in-process against
  `internal/nexus/nexustest`, a fake Nexus that reproduces the behaviour recorded in
  [docs/nexus-api.md](docs/nexus-api.md). When you find that real Nexus behaves differently, fix
  the fake and add the observation to that document.
* End-to-end tests (`test/e2e`, build tag `e2e`) run the binary against a real Nexus container
  started by `scripts/e2e-nexus.sh`.

Every change needs tests. Run `make lint test` before you push; CI runs the same checks on Linux,
macOS and Windows.

## Pull requests

* Keep a pull request to one topic, and describe what changes for users.
* Start commit messages with the area they touch, for example `config: …`, `repos: …`, `docs: …`,
  `ci: …`, and write them in the imperative mood.
* Update the `--help` text, the README and the specification when behaviour changes, and add an
  entry to [CHANGELOG.md](CHANGELOG.md) under "Unreleased".
* Commands must follow the conventions of the specification: results on stdout and everything else
  on stderr, `--json` output that follows the JSON rules, and the documented exit codes.

By contributing, you agree that your contributions are licensed under the [MIT License](LICENSE).
