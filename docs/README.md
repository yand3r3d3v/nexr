# nexr design documentation

These documents describe `nexr`, a command-line tool for Sonatype Nexus Repository 3, before
implementation starts. They are drafts; the decisions from the first review and the remaining open questions
are listed in [specification.md §11](specification.md#11-assumptions-decisions-and-open-questions).

| Document | What it covers |
|---|---|
| [specification.md](specification.md) | **Technical specification.** Scope, use cases, compatibility, configuration, every command with its flags, behaviour and output, exit codes, non-functional requirements, privileges, acceptance criteria, open questions. |
| [architecture.md](architecture.md) | **Architecture.** Layers and packages, main components (config, HTTP transport, Nexus and registry clients, listing and transfer engines, retention planner, task runner), runtime flows, testing, build and release, architecture decision records. |
| [nexus-api.md](nexus-api.md) | **Nexus API notes.** Observed behaviour of the Nexus REST, content and Docker Registry APIs on real servers (3.96.3 and 3.71.0): pagination, search rules, browse API, errors, tasks, privileges, version differences. |
| [roadmap.md](roadmap.md) | **Roadmap.** Milestones M0–M5 up to v1.0 (latest Nexus first, then 3.71+ compatibility), backlog after v1.0, risks. |

## Suggested reading order

1. Specification §1–§4 for the scope and the general rules.
2. Specification §6 for the commands.
3. Architecture §3–§5 for how the code will be organised.
4. Nexus API notes when implementing or reviewing a specific integration.

## Changing these documents

Design changes are proposed through pull requests that edit these files. A decision that changes the
architecture or adds a dependency gets a new ADR in
[architecture.md §11](architecture.md#11-architecture-decision-records).
