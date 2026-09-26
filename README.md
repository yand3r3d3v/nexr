# nx

A fast, dependency-free command-line tool for
[Sonatype Nexus Repository 3](https://www.sonatype.com/products/sonatype-nexus-repository): manage
files, container images and storage cleanup without the web UI.

> **Status: design phase.** No code has been written yet. The technical specification and the
> architecture are in [`docs/`](docs/README.md) and open for review.

## Planned for v1.0

* One static binary for Linux, macOS and Windows (amd64/arm64), with nothing to install at run time.
* One configuration for everything: environment variables (`NEXUS_URL`, `NEXUS_USER`,
  `NEXUS_PASSWORD`), an optional YAML file with profiles for several Nexus instances, and flags.
* **Files (raw):** `nx ls`, `nx up`, `nx down`, `nx rm`, with recursive transfers, parallelism,
  checksum verification and `--dry-run`.
* **Docker/OCI:** `nx docker ls`, `nx docker tags`, `nx docker rm`, including retention
  (`--keep N`, `--older-than`) with previews.
* **Cleanup:** `nx gc` runs the Docker GC and compaction tasks and waits for them; `nx tasks` runs
  any server task.
* **Everything else:** `nx api` sends authenticated REST calls.
* Readable output, `--json` for scripts, documented exit codes.

```sh
nx up ./dist raw-releases/myapp/1.4.0/
nx docker rm team/app --keep 10 --older-than 30d --dry-run
nx gc --repo docker-hosted
```

## Documentation

* [Technical specification](docs/specification.md)
* [Architecture](docs/architecture.md)
* [Nexus API notes](docs/nexus-api.md)
* [Roadmap](docs/roadmap.md)

## License

[MIT](LICENSE)
