# Security policy

## Supported versions

Until v1.0.0, only the latest release receives security fixes. From v1.0.0 on, the latest minor
release of the current major version is supported.

## Reporting a vulnerability

Please do not open a public issue for a security problem. Report it privately through
[GitHub private vulnerability reporting](https://github.com/yand3r3d3v/nexr/security/advisories/new)
and include:

* the affected version (`nexr version`) and platform;
* what an attacker can achieve, and the steps to reproduce it;
* the Nexus Repository version, if it matters.

You will get an answer within seven days. Once a fix is released, the advisory is published and you
are credited unless you prefer otherwise.

## Scope

`nexr` handles credentials for Nexus Repository. Reports about these areas are especially welcome:

* credentials sent to a host they were not configured for (see "credential scoping" in the
  [specification](docs/specification.md#51-sources-and-precedence));
* secrets written to logs, error messages or `nexr config view`;
* TLS verification that is weaker than configured;
* paths from the server that make `nexr` write outside the target directory (from the file commands
  on).

Vulnerabilities in Nexus Repository itself should be reported to Sonatype, not here.
