# nexr: Roadmap

| | |
|---|---|
| **Status** | M0 (foundation) and M1 (raw files) implemented; M2 (Docker/OCI images) next |
| **Date** | 2026-09-26 |
| **Related documents** | [Specification](specification.md) · [Architecture](architecture.md) · [Nexus API notes](nexus-api.md) |

## Principles

* **Vertical slices.** Every milestone ends with a tagged, usable release. Nothing is left
  half-integrated between milestones.
* **Latest Nexus first.** Milestones M0–M3 target the latest Nexus release (3.96 at the time of
  writing). Compatibility with older supported releases (3.71–3.9x) is added in M4, before v1.0
  (spec §3.2, [ADR-013](architecture.md#adr-013-supported-nexus-releases-and-priority)).
* **Order.** Foundation, then raw files, then Docker images, then tasks and GC, as agreed in the
  original task.
* **Definition of done** for every feature:
  * code with unit tests;
  * domain and command tests against the fake Nexus (the *latest* dialect; from M4 also the
    *baseline* dialect);
  * end-to-end tests where the feature talks to Nexus;
  * `--help` text with examples;
  * the JSON contract documented;
  * README and specification updated;
  * a `CHANGELOG.md` entry.

## Milestones

### M0: Foundation → `v0.1.0`

| Area | Deliverables |
|---|---|
| Project | `go.mod` (`github.com/yand3r3d3v/nexr`), `Makefile`, `.goreleaser.yaml`, `.golangci.yml`, GitHub Actions (`ci.yml`, `release.yml`), `README.md`, `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md` |
| Core packages | `buildinfo`, `errs`, `output`, `config` (env, file, flags, profiles, credential scoping, secrets, CA bundle and optional client certificates), `httpx` (TLS, proxy, retries, logging), `nexus` core (errors, pagination, server info), `nexustest` skeleton (*latest* dialect), `archtest` |
| Commands | `nexr version`, `nexr completion`, `nexr repos [ls\|show]`, `nexr status`, `nexr config view\|path\|profiles` |
| Tests | `scripts/e2e-nexus.sh` (bootstrap of a fresh Nexus container) and a first `test/e2e` suite for `status` and `repos` |
| Exit criteria | AC-1, AC-2, AC-3 |

### M1: Raw files → `v0.2.0`

| Area | Deliverables |
|---|---|
| Packages | `remote` (path parsing and sanitisation), `files` (listing engine: browse, group search, scan, stat; transfer engine; removal), `formats/raw`, `workpool` |
| Commands | `nexr ls`, `nexr up`, `nexr down`, `nexr rm` |
| Tests | `test/e2e` for raw against the latest release; `e2e.yml` workflow (nightly and on demand). The suite already passes against 3.71.0 as well, so the nightly run covers both |
| Exit criteria | AC-4, AC-5, AC-6, AC-7 |

### M2: Docker/OCI images → `v0.3.0`

| Area | Deliverables |
|---|---|
| Packages | `registry` (catalog, tags, HEAD, Bearer challenge, `Link` handling), `images`, `retention` |
| Commands | `nexr docker ls`, `nexr docker tags`, `nexr docker rm` (explicit tags, `--keep`, `--older-than`, `--all`, `--match`, `--exclude`, `--sort`) |
| Registry endpoint | default `<base>/repository/<repo>/v2/`, overrides via `--registry-url`, `NEXR_DOCKER_REGISTRY_URL` and `docker.registry_urls`; image references with a registry host |
| Tests | e2e fixtures pushed with `crane` (single-arch, multi-arch index, attestations); a reverse-proxy setup for the registry URL override |
| Exit criteria | AC-8, AC-9 |

### M3: Tasks, GC and API → `v0.4.0`

| Area | Deliverables |
|---|---|
| Packages | `tasks` (runner, GC orchestration, task creation from templates) |
| Commands | `nexr tasks ls\|show\|run\|stop`, `nexr gc` (including `--create-missing`, opt-in), `nexr api` |
| Investigation | Storage reclamation for raw deletions (the delay applied by *Admin - Cleanup unused asset blobs*; see [nexus-api.md](nexus-api.md#storage-reclamation)) |
| Exit criteria | AC-10 |

### M4: Compatibility with 3.71+ → `v0.5.0`

| Area | Deliverables |
|---|---|
| Fake | *baseline* dialect in `nexustest`: page size 10, no Browse API, no task creation API or task properties, no Docker attributes, the 3.71 registry `Link` header, anonymous access enabled by default |
| Fallbacks | Listing without the Browse API (group search and scan), `nexr gc` without task properties and without task creation, optional columns hidden when attributes are missing |
| Tests | e2e matrix extended to 3.71; JSON fixtures captured from 3.71 |
| Exit criteria | AC-11 |

### M5: Hardening → `v1.0.0`

| Area | Deliverables |
|---|---|
| Quality | Performance checks (1 GiB upload memory, large scans), Windows-specific tests (paths, reserved names, console) |
| Docs | Complete README (quick start, configuration, examples, role definitions for CI), command reference generated from cobra (`docs/cli/`) |
| Release | GoReleaser pipeline with checksums and the Homebrew tap (`homebrew_casks`) |
| Contract | JSON output and exit codes frozen for 1.x |
| Exit criteria | AC-12 |

## After v1.0

1. **Upload for more formats**: Maven (path `PUT` with layout validation, or the Components API with
   GAV fields) and Helm first, as decided in review; later apt, yum, nuget, pypi, npm (publish
   protocol), cargo, conda and others. Each format is a `formats.Adapter` (see
   [architecture.md §10](architecture.md#10-extending-nexr)).
2. **Repository management**: `nexr repos create | update | delete` using per-format JSON templates;
   `invalidate-cache`, `rebuild-index`, `health-check`.
3. **Blob stores**: `nexr blobstores ls | show | quota`.
4. **Cleanup policies**: listing and editing, where the edition supports it.
5. **Security administration**: users, roles, privileges, content selectors, realms, anonymous
   access, user tokens.
6. **Task management**: `nexr tasks create | update | rm` on servers with the task API.
7. **Search**: `nexr search` across repositories and formats (keyword, name, version, checksum).
8. **Copy and sync**: `nexr cp` and `nexr sync` between repositories or instances (for example, to
   promote artifacts from staging to release).
9. **Docker extras**: image patterns in `docker rm` (`'team/*'`), deletion by digest,
   `nexr docker inspect`.
10. **Credentials**: OS keychain integration (`nexr login`), `password_command` hardening.
11. **Distribution**: Scoop bucket, `.deb`/`.rpm`, container image, signed releases, SBOM.
12. **Transfers**: resumable downloads (HTTP `Range`), checksum-based sync mode for `up` and `down`.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Nexus releases roughly monthly; APIs change between releases | Capability detection instead of version checks ([ADR-006](architecture.md#adr-006-runtime-capability-detection-instead-of-version-checks)); e2e against the latest release; refresh test fixtures on every Nexus minor release |
| Search and browse indexes lag behind writes | Strategies chosen so that lag cannot cause over-deletion (FR-DRM-4); documented behaviour |
| Slow listings on releases without the Browse API and with page size 10 (3.71) | Group search instead of full scans where possible; progress indication; documented recommendation to upgrade |
| Pro-only features cannot be tested with Community Edition | Mark Pro-only behaviour in docs; rely on capability detection; ask the community for reports |
| Storage reclamation is asynchronous on the server | Investigation in M3; `nexr gc` reports blob store sizes before and after and explains possible delays |
