# nx: Roadmap

| | |
|---|---|
| **Status** | Draft for review |
| **Date** | 2026-09-26 |
| **Related documents** | [Specification](specification.md) · [Architecture](architecture.md) · [Nexus API notes](nexus-api.md) |

## Principles

* **Vertical slices.** Every milestone ends with a tagged, usable release. Nothing is left
  half-integrated between milestones.
* **Order.** Foundation, then raw files, then Docker images, then tasks and GC, as agreed in the
  original task.
* **Definition of done** for every feature:
  * code with unit tests;
  * domain and command tests against the fake Nexus in both dialects;
  * end-to-end tests where the feature talks to Nexus;
  * `--help` text with examples;
  * the JSON contract documented;
  * README and specification updated;
  * a `CHANGELOG.md` entry.

## Milestones

### M0: Foundation → `v0.1.0`

| Area | Deliverables |
|---|---|
| Project | `go.mod` (`github.com/yand3r3d3v/nx`), `Makefile`, `.goreleaser.yaml`, `.golangci.yml`, GitHub Actions (`ci.yml`, `release.yml`), `README.md`, `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md` |
| Core packages | `buildinfo`, `errs`, `output`, `config` (env, file, flags, profiles, credential scoping, secrets), `httpx` (TLS, proxy, retries, logging), `nexus` core (errors, pagination, server info), `nexustest` skeleton, `archtest` |
| Commands | `nx version`, `nx completion`, `nx repos [ls\|show]`, `nx status`, `nx config view\|path\|profiles` |
| Exit criteria | AC-1, AC-2, AC-3 |

### M1: Raw files → `v0.2.0`

| Area | Deliverables |
|---|---|
| Packages | `remote` (path parsing and sanitisation), `files` (listing engine: browse, group search, scan, stat; transfer engine; removal), `formats/raw`, `workpool` |
| Commands | `nx ls`, `nx up`, `nx down`, `nx rm` |
| Tests | `scripts/e2e-nexus.sh`, `test/e2e` for raw (both Nexus versions) |
| Exit criteria | AC-4, AC-5, AC-6, AC-7 |

### M2: Docker/OCI images → `v0.3.0`

| Area | Deliverables |
|---|---|
| Packages | `registry` (catalog, tags, HEAD, Bearer challenge), `images`, `retention` |
| Commands | `nx docker ls`, `nx docker tags`, `nx docker rm` (explicit tags, `--keep`, `--older-than`, `--all`, `--match`, `--exclude`, `--sort`) |
| Tests | e2e fixtures pushed with `crane` (single-arch, multi-arch index, attestations) |
| Exit criteria | AC-8, AC-9 |

### M3: Tasks, GC and API → `v0.4.0`

| Area | Deliverables |
|---|---|
| Packages | `tasks` (runner, GC orchestration, task creation from templates) |
| Commands | `nx tasks ls\|show\|run\|stop`, `nx gc`, `nx api` |
| Investigation | Storage reclamation for raw deletions on SQL-datastore servers (the delay applied by *Admin - Cleanup unused asset blobs*; see [nexus-api.md](nexus-api.md#storage-reclamation)) |
| Exit criteria | AC-10 |

### M4: Hardening → `v1.0.0`

| Area | Deliverables |
|---|---|
| Quality | e2e matrix (latest Nexus release and oldest supported release), performance checks (1 GiB upload memory, large scans), Windows-specific tests (paths, reserved names, console) |
| Docs | Complete README (quick start, configuration, examples, role definitions for CI), command reference generated from cobra (`docs/cli/`) |
| Release | GoReleaser pipeline with checksums; optional signing and packages, depending on Q9 |
| Contract | JSON output and exit codes frozen for 1.x |
| Exit criteria | AC-11 |

## After v1.0

The proposed order is still subject to open question Q8 in the specification.

1. **Upload for more formats**: maven2 (path `PUT` with layout validation, or the Components API with
   GAV fields), helm, apt, yum, nuget, pypi, npm (publish protocol), cargo, conda, etc. Each format
   is a `formats.Adapter` (see [architecture.md §10](architecture.md#10-extending-nx)).
2. **Repository management**: `nx repos create | update | delete` using per-format JSON templates;
   `invalidate-cache`, `rebuild-index`, `health-check`.
3. **Blob stores**: `nx blobstores ls | show | quota`.
4. **Cleanup policies**: listing and editing, where the edition supports it.
5. **Security administration**: users, roles, privileges, content selectors, realms, anonymous
   access, user tokens.
6. **Task management**: `nx tasks create | update | rm` on servers with the task API.
7. **Search**: `nx search` across repositories and formats (keyword, name, version, checksum).
8. **Copy and sync**: `nx cp` and `nx sync` between repositories or instances (for example, to
   promote artifacts from staging to release).
9. **Docker extras**: image patterns in `docker rm` (`'team/*'`), registry host mapping, deletion by
   digest, `nx docker inspect`.
10. **Credentials**: OS keychain integration (`nx login`), `password_command` hardening.
11. **Distribution**: Homebrew tap, Scoop bucket, `.deb`/`.rpm`, container image, signed releases,
    SBOM.
12. **Transfers**: resumable downloads (HTTP `Range`), checksum-based sync mode for `up` and `down`.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Nexus releases roughly monthly; APIs change between versions | Capability detection instead of version checks ([ADR-006](architecture.md#adr-006-runtime-capability-detection-instead-of-version-checks)); e2e against the latest release; refresh test fixtures on every Nexus minor release |
| Search and browse indexes lag behind writes | Strategies chosen so that lag cannot cause over-deletion (FR-DRM-4); documented behaviour |
| Slow listings on old versions (page size 10) | Progress indication; Browse traversal where available; documented recommendation to upgrade |
| Binary name `nx` clashes with the Nx build system | Open question Q6; decide before v0.1.0, because renaming later is costly |
| Pro-only features cannot be tested with Community Edition | Mark Pro-only behaviour in docs; rely on capability detection; ask the community for reports |
| Storage reclamation semantics differ by database | Investigation in M3; `nx gc` reports blob store sizes before and after and explains possible delays |
