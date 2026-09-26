# Nexus Repository API notes

| | |
|---|---|
| **Status** | Reference for implementers |
| **Date** | 2026-09-26 |
| **Verified against** | Nexus Repository **3.96.3-01 Community Edition** (H2) and **3.70.1-02 OSS** (OrientDB), fresh `sonatype/nexus3` containers |
| **Related documents** | [Specification](specification.md) · [Architecture](architecture.md) |

This page records how the Nexus APIs used by `nx` actually behave. It comes from experiments with
real servers, not only from the documentation. Statements are verified on both versions unless
marked **(3.96)** or **(3.70)**. Unverified statements are marked *(not verified)*.

The full OpenAPI definition of a running server is available at `<base>/service/rest/swagger.json`.
It is OpenAPI 3.0 on 3.96 and Swagger 2.0 on 3.70.

---

## Contents

1. [Surfaces and base paths](#surfaces-and-base-paths)
2. [Server identification and health](#server-identification-and-health)
3. [Authentication](#authentication)
4. [Error responses](#error-responses)
5. [Pagination](#pagination)
6. [Repositories](#repositories)
7. [Components and assets](#components-and-assets)
8. [Search](#search)
9. [Browse API](#browse-api)
10. [Content endpoints](#content-endpoints)
11. [Component upload (multipart)](#component-upload-multipart)
12. [Docker Registry v2 API](#docker-registry-v2-api)
13. [Tasks](#tasks)
14. [Privileges](#privileges)
15. [Version differences](#version-differences)
16. [Reproducing the observations](#reproducing-the-observations)

---

## Surfaces and base paths

| Surface | Base path | Used for |
|---|---|---|
| REST API | `<base>/service/rest/v1/…` (a few endpoints under `/beta/` and `/internal/`) | repositories, components, assets, search, browse, tasks, status |
| Repository content | `<base>/repository/<repo>/<path>` | download, upload (`PUT`), delete for raw and other path-based formats |
| Docker Registry v2 | `<base>/repository/<repo>/v2/…` | image catalog, tags, manifests |
| Docker connectors | `http(s)://<host>:<port>/v2/…`, sub-domain, or `<base>/v2/<repo>/…` with `pathEnabled` **(3.96)** | Docker clients; `nx` does not need them |

`<base>` may include a context path (e.g. `https://example.com/nexus`).

## Server identification and health

* Every response carries a `Server` header: `Nexus/3.96.3-01 (COMMUNITY)` and `Nexus/3.70.1-02 (OSS)`.
  Pro servers report their edition in the same place *(not verified)*.
* `GET /v1/status` → `200` when the server can serve reads, `503` otherwise. **Anonymous access is
  allowed**, even when anonymous access to repositories is disabled.
* `GET /v1/status/writable` → `200`/`503` for writes. Also anonymous.
* `GET /v1/status/check` → detailed health checks (`{"Available CPUs": {"healthy": true, …}, …}`).
  Requires authentication (anonymous request → `401`).

## Authentication

* HTTP Basic auth. Unauthenticated requests to protected resources get:

  ```
  HTTP/1.1 401 Unauthorized
  WWW-Authenticate: BASIC realm="Sonatype Nexus Repository Manager"
  ```

* Anonymous access is **disabled** on fresh installations (`GET /v1/security/anonymous` →
  `{"enabled": false, …}`).
* User tokens (name code + pass code) are used as Basic credentials.
* **Community Edition EULA (3.96).** A fresh CE server reports `GET /v1/system/eula` →
  `{"accepted": false, "disclaimer": "…"}`. Automation (e2e bootstrap) accepts it with
  `POST /v1/system/eula` and body `{"accepted": true, "disclaimer": "<the same text>"}` → `204`.
* **Docker repositories** (Registry API, also under `/repository/<repo>/v2/`):
  * `forceBasicAuth: true` (the common setting): a `401` challenge is `BASIC realm=…`.
  * `forceBasicAuth: false` with the *Docker Bearer Token Realm* active: the challenge is

    ```
    WWW-Authenticate: Bearer realm="http://nexus:8083/v2/token",service="http://nexus:8083/v2/token",scope="repository:*:pull"
    ```

    For the path-based endpoint the realm is `<base>/v2/token`. The token endpoint returns
    `{"token": "DockerToken.<uuid>"}`. **Basic credentials are still accepted directly**, so the
    token flow is only needed for anonymous access.

## Error responses

Nexus uses three body shapes. The HTTP **reason phrase** often carries the useful message as well.

1. **Siesta fault** (JSON, most REST 404/403/500):

   ```json
   {"status-code": 404, "siesta-faultid": "4821f00d-3cec-4699-94e4-9b070aaddbf6", "status-message": "NOT_FOUND"}
   ```

2. **Validation errors** (JSON array, 400):

   ```json
   [{"id": "*", "message": "3 characters or more are required with a trailing wildcard (*)"}]
   ```

   The Components API upload reports conflicts as text: `ValidationErrorXO{id='*', message='…'}`.

3. **Plain text or HTML** (content endpoints). The status line carries the message:

   ```
   HTTP/1.1 409 raw-once/x.txt -  cannot be updated as asset already exists and redeploy is not allowed
   HTTP/1.1 400 Detected content type [text/plain], but expected [image/png]: /fake.png
   HTTP/1.1 404 /nope.txt
   ```

   Unknown UI paths return an HTML 404 page.

Status codes seen or documented: `204` for successful deletes and runs, `201` for created content
and tasks, `400` for validation and wildcard errors, `401`, `403`, `404`, `405` (task disabled; task
creation on 3.70), `409` (redeploy disabled; stopping an idle task), `422` (malformed ID, missing
`repository` parameter), `500` (running a task that is already running).

## Pagination

`GET` list endpoints return `{"items": [...], "continuationToken": "<opaque>" | null}`. Pass the token
back as `continuationToken=<token>`. The token is opaque: it looks like a hex hash for
components/assets and like an offset (`"50"`) for search. Never parse it.

| Endpoint | 3.70.1 page size | 3.96.3 page size |
|---|---|---|
| `GET /v1/components` | 10 | 100 |
| `GET /v1/assets` | 10 | 100 |
| `GET /v1/search`, `GET /v1/search/assets` | 50 | 50 |
| `GET /v1/tasks` | all in one page | all in one page |
| `GET /v1/repositories` | not paginated (array) | not paginated (array) |
| `GET /v1/repositories/{repo}/browse` **(3.96)** | n/a | not paginated (150 entries returned in one response) |

The page size is not configurable. A full scan of a repository with 100,000 assets needs 10,000
requests on 3.70 and 1,000 on 3.96.

## Repositories

* `GET /v1/repositories` → array of `{name, format, type, url, attributes}`. On 3.96 it also returns
  `online` and `size` (reported as `0` on a fresh server). **Only repositories the user may browse
  are listed.** A user with only `read` saw an empty list.
* `GET /v1/repositories/{name}` → one repository (same fields).
* `GET /v1/repositorySettings` → full settings of every repository, including Docker connector
  attributes. Requires administrative read privileges.
* `GET /v1/repositories/{format}/{type}/{name}` → full settings of one repository, e.g. for Docker:

  ```json
  {
    "name": "docker-hosted", "format": "docker", "type": "hosted", "online": true,
    "url": "http://localhost:8081/repository/docker-hosted",
    "storage": {"blobStoreName": "default", "strictContentTypeValidation": true, "writePolicy": "ALLOW", "latestPolicy": false},
    "docker": {"v1Enabled": false, "forceBasicAuth": true, "httpPort": 8082, "httpsPort": null, "subdomain": null, "pathEnabled": null}
  }
  ```

* `pathEnabled: true` **(3.96)** cannot be combined with `httpPort`, `httpsPort` or `subdomain`
  (`400`).
* Repository names match `^[a-zA-Z0-9\-]{1}[a-zA-Z0-9_\-\.]*$`. `oci` repository names must be
  lower-case.
* Formats on 3.96 include `oci` (separate from `docker`), `cargo`, `huggingface`, `swift`,
  `terraform` and others. `bower` was removed after 3.70.

## Components and assets

### Models

Component: `{id, repository, format, group, name, version, assets[]}`.

Asset fields by version:

| Field | 3.70 | 3.96 |
|---|---|---|
| `id`, `path`, `downloadUrl`, `repository`, `format`, `contentType` | yes | yes |
| `checksum` (`sha1`, `sha256`, `sha512`, `md5`) | yes | yes |
| `lastModified`, `blobCreated`, `lastDownloaded`, `uploader`, `uploaderIp`, `fileSize` | yes | yes |
| `blobStoreName`, `blobUpdated`, `blobRef`, `lastVerified`, `registryUrl` | no | yes |
| format attributes (`raw: {}`, `docker: {…}`) | no | yes |

IDs are opaque strings. They look like base64url of `<repo>:<internal-id>`, but that is not a
contract. A raw component and its asset may have the same or different IDs.

### Raw

| | 3.70 (OrientDB) | 3.96 (H2) |
|---|---|---|
| component `group` | `/dir/sub` (`/` for files at the root) | `/dir/sub` (`/` for the root) |
| component `name` | `dir/sub/c.txt` | `/dir/sub/c.txt` |
| component `version` | `null` | `""` |
| asset `path` | `dir/sub/c.txt` | `/dir/sub/c.txt` |

* One component per file, one asset per component.
* Deleting the asset also deletes the component, so no orphan remains **(3.96)**.
* A path can be both a file and a directory at the same time (e.g. `dir/sub` and `dir/sub/c.txt`).

### Docker / OCI

* One component per **tag**: `name` = image name (`team/app`), `version` = tag, `group` = `""`.
* The component's only asset is the tag's manifest: `path` = `/v2/team/app/manifests/1.1` (3.96) or
  `v2/team/app/manifests/1.1` (3.70). `checksum.sha256` equals the manifest digest
  (`Docker-Content-Digest`). `contentType` is the manifest media type (OCI/Docker manifest or index).
* Manifests addressed by digest (the per-platform children of a multi-arch index, attestation
  manifests, and manifests that a client pushes by digest) are stored as assets
  `…/manifests/sha256:<digest>` **without their own component**.
  Layers and configs are component-less blobs (`/v2/-/blobs/sha256:…` on 3.96).
* **(3.96)** The manifest asset carries `docker` attributes:

  ```json
  "docker": {
    "content_digest": "sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
    "created": "2023-05-18T22:34:17Z",
    "os": "linux", "architecture": "amd64",
    "totalSize": "2.10 MB",
    "cmd": ["sh"], "env": ["PATH=…"], "history": [...]
  }
  ```

  `totalSize` is a human-readable string. For an index it describes one platform.
* The `oci` format **(3.96)** uses the same model (`format: "oci"`).
* **Deleting a tag component removes only that tag.** Other tags pointing to the same digest remain
  pullable, and the manifest by digest stays until the Docker GC task removes unreferenced data.

## Search

`GET /v1/search` (components) and `GET /v1/search/assets` (assets). Parameters relevant to `nx`:
`repository`, `format`, `group`, `name`, `version`, `q`, `sort`, `direction`, `continuationToken`,
`docker.imageName`, `docker.imageTag`, `docker.contentDigest`, `raw.name` **(3.96)**,
`oci.imageName`/`oci.imageTag` **(3.96)**. `GET /v1/search/versions` **(3.96)** lists the distinct
versions of a component across all repositories. It cannot filter by repository.

### Matching rules

| Behaviour | 3.70 (Elasticsearch) | 3.96 (SQL) |
|---|---|---|
| `name=X`, `group=X` without wildcard | exact match | exact match |
| Trailing wildcard `X*` | any length | needs ≥ 3 characters before `*`, otherwise `400` |
| Leading wildcard | not used | rejected |
| Quoted value `"X"` | exact phrase | exact phrase; a wildcard outside the quotes does **not** make it a prefix (`"/dir/sub"*` matched only `/dir/sub`) |
| Unquoted value with spaces | not tested | split into terms; `group=/space dir*` and `group=/space dir/sub dir` returned **nothing** (false negatives) |
| Values with `-`, `,`, non-ASCII | fine for `group` | fine for `group` |

Observed raw queries on 3.96 (files under `dir/`, `dir-sibling/`, `other/dir/`):

| Query | Result |
|---|---|
| `name=dir/sub/c.txt` | none (3.96 names start with `/`); on 3.70 → the file |
| `name=/dir/sub/c.txt` | the file (3.96); none on 3.70 |
| `name=/dir/sub*` | `/dir/sub`, `/dir/sub/c.txt`, `/dir/sub/deeper/d.bin` |
| `group=/dir/sub` | files directly in `/dir/sub` (both versions) |
| `group=/dir/sub*` | every file below `/dir/sub`, but also `/dir/subway/…` if it existed (both versions) |
| `group=/dir*` | also matches `/dir-sibling/e.txt`, so the client must filter |
| `raw.name=c.txt` | files with that base name |
| `q=sub` | keyword search over tokens; also matches `/other/dir/sub-x.txt` |

**Conclusion for `nx`:** `group` is stable across versions. Use a quoted exact `group` for one
directory, an unquoted `group=<dir>*` for recursive listing (only when the value is ≥ 3 characters
and contains no whitespace or quotes), and always filter results by exact path prefix on the client.

### Consistency

Search results are **eventually consistent** for inserts: a file uploaded with `PUT` appeared in
search and browse results after about **2 seconds** (3.96), while `GET /v1/components` showed it
immediately. Deletions disappeared from search immediately.

### Sorting

`sort` accepts `group`, `name`, `version` and `repository`; `direction` accepts `asc` and `desc`
(version defaults to `desc`). `sort=version` orders lexically (`latest`, `1.1`, `1.0`), which is not
useful for SemVer, so `nx` sorts on the client.

## Browse API

**(3.96 only; absent on 3.70.)**

* `GET /v1/repositories/{repo}/browse?path=<dir>` lists one level. `path` accepts `/`, `/dir` or
  `dir`. A non-existent path returns `[]`. An unknown repository returns a siesta `404`. The response
  is not paginated.

  ```json
  [
    {"id": "/dir/sub/c.txt", "text": "c.txt", "type": "asset", "leaf": true, "componentId": "ed533697", "assetId": "ed533697", "packageUrl": null},
    {"id": "/dir/sub/deeper", "text": "deeper", "type": "folder", "leaf": false, "componentId": null, "assetId": null, "packageUrl": null}
  ]
  ```

  * Use `text` (the decoded name). `id` is not a reliable path: it contains `+` for spaces and
    doubled slashes when `path` ends with `/`.
  * `componentId`/`assetId` are **internal** IDs, not the REST IDs used by `/v1/components/{id}`.
  * A path that is a file *and* has children appears as `type: "asset", leaf: false`.
* `DELETE /v1/repositories/{repo}/browse?path=<dir>` → `204` "Folder deletion initiated". It is
  asynchronous; in tests a folder of two files was gone within 2 seconds. A user with
  `browse`+`read`+`delete` on the repository got `403`, so it needs more privileges.
* Empty folders disappear automatically on H2. Since 3.88, automatic trimming is disabled by default
  on PostgreSQL; the *Repair - Repository trim browse tree* task (`repository.trim-browse-tree`)
  removes empty folders on demand.

## Content endpoints

`<base>/repository/<repo>/<path>`. Paths must be percent-encoded, and UTF-8 and spaces work.

| Request | Result |
|---|---|
| `GET` file | `200`, body; headers `ETag: "<sha1>"`, `Last-Modified`, `Content-Type`, `Content-Length`, `Content-Disposition: attachment` |
| `GET` with `Range: bytes=0-9` | `206 Partial Content`, `Content-Range: bytes 0-9/100000` |
| `HEAD` file | `200` with the same headers and no body |
| `GET`/`HEAD` directory (`dir`, `dir/`) | `404` |
| `PUT` new file (raw) | `201` |
| `PUT` existing file, redeploy allowed | `201` (overwrites) |
| `PUT` existing file, `ALLOW_ONCE` | `409 <repo>/<path> -  cannot be updated as asset already exists and redeploy is not allowed` |
| `PUT` with strict content validation mismatch | `400 Detected content type [text/plain], but expected [image/png]: /fake.png` |
| `PUT` to unknown repository | `404 Repository not found` |
| `PUT` of an invalid path to a Maven repository | `400 Invalid mavenPath for a Maven 2 repository` |
| `DELETE` file (raw) | `204`; again → `404` |

**The `ETag` is the SHA-1 of the content** (verified with `sha1sum`), which allows integrity checks
without an extra API call.

## Component upload (multipart)

`POST /v1/components?repository=<repo>`, `multipart/form-data`. Raw fields:

| Field | Meaning |
|---|---|
| `raw.directory` | Target directory; with or without a leading `/` |
| `raw.asset1` … `raw.assetN` | File parts |
| `raw.asset1.filename` … | Target file names; may contain sub-directories (`sub/three.txt`) |

* Success: `204` with no body. Several assets per request are accepted (`multipleUpload: true`).
* Conflict (redeploy disabled): `409` with `ValidationErrorXO{…}` text.
* `GET /v1/formats/upload-specs` and `GET /v1/formats/{format}/upload-specs` describe the fields of
  every format:

  ```json
  {"format": "raw", "multipleUpload": true,
   "componentFields": [{"name": "directory", "type": "STRING", "optional": false}],
   "assetFields": [{"name": "filename", "type": "STRING", "optional": false},
                   {"name": "asset", "type": "FILE", "optional": false}]}
  ```

## Docker Registry v2 API

Reached through `<base>/repository/<repo>/v2/` for every Docker/OCI repository. This works for
repositories with port connectors, for `pathEnabled` repositories, and for `oci` repositories.

| Request | Result |
|---|---|
| `GET …/v2/_catalog` | `{"repositories": ["multi/alpine", "other", "team/app"]}` |
| `GET …/v2/_catalog?n=2` | first two names and `Link: <…/v2/_catalog?n=2&last=other>; rel="next"` |
| `GET …/v2/<name>/tags/list?n=1` | `{"name": "team/app", "tags": ["1.0"]}` and a `Link` header |
| `HEAD …/v2/<name>/manifests/<tag>` with an `Accept` list of OCI/Docker manifest and index types | `200`, `Docker-Content-Digest: sha256:…`, `Content-Type` |
| `GET …/v2/<unknown>/tags/list` | `404` `{"errors": [{"code": "NAME_UNKNOWN", …}]}` |
| `DELETE …/v2/<name>/manifests/<digest>` | `202`; removes **every tag** that points to the digest |
| `DELETE …/v2/<name>/manifests/<tag>` | `202`; removes only that tag (Nexus-specific, not in the Registry spec) |

`nx` deletes tags through `DELETE /v1/components/{id}` instead (precise, documented REST API).

With `pathEnabled: true` **(3.96)**, Docker clients use `<host>/<repo>/<image>:<tag>` and the
Registry API is also served at `<base>/v2/<repo>/<image>/…`. `<base>/v2/_catalog` is not
available on that route.

## Tasks

| Endpoint | 3.70 | 3.96 | Notes |
|---|---|---|---|
| `GET /v1/tasks?type=<type>` | yes | yes | `{"items": [...], "continuationToken": null}` |
| `GET /v1/tasks/{id}` | yes | yes | `404` if unknown |
| `POST /v1/tasks/{id}/run` | yes | yes | `204`; `404` unknown; `405` disabled; **`500` if already running** |
| `POST /v1/tasks/{id}/stop` | yes | yes | `204`; `409` if not running |
| `POST /v1/tasks` | **`405`** | yes (`201`) | create |
| `PUT /v1/tasks/{id}`, `DELETE /v1/tasks/{id}` | no | yes | update and delete |
| `GET /v1/tasks/templates`, `GET /v1/tasks/templates/{type}` | no | yes | form fields and defaults |

Task fields: 3.70 has `id, name, type, message, currentState, lastRunResult, nextRun, lastRun`.
3.96 adds `typeName, schedule, properties, enabled, alertEmail, notificationCondition, startDate,
recurringDays, cronExpression, timeZoneOffset`.

Observed run of a manual task (3.96):

```
before:  currentState=WAITING  lastRunResult=null  lastRun=null
t+1s:    currentState=RUNNING  lastRunResult=null  lastRun=null
t+2s:    currentState=WAITING  lastRunResult=OK    lastRun=2026-09-26T16:15:25.622+00:00
```

During the first run `lastRun` is still `null`, so completion is detected by "state is no longer
`RUNNING` **and** `lastRun` changed", not by timestamps compared with the client clock.

Relevant task types (from the 3.96 templates):

| Type | Name | Properties (default) |
|---|---|---|
| `repository.docker.gc` | Docker - Delete unused manifests and images | `repositoryName` (required; a repository or `*`), `deployOffset` (hours, `24`) |
| `blobstore.compact` | Admin - Compact blob store | `blobstoreName` (required), `blobsOlderThan` (optional) |
| `repository.docker.upload-purge` | Docker - Delete incomplete uploads | `age` (hours, `24`) |
| `blobstore.delete-temp-files` | Admin - Delete blob store temporary files | `blobstoreName`, `olderThanDays` |
| `repository.cleanup` | Admin - Cleanup repositories using their associated policies | none (exists by default as "Cleanup service") |
| `repository.trim-browse-tree` | Repair - Repository trim browse tree | `repositoryName` |

Creating a manual task (3.96):

```json
POST /v1/tasks
{
  "type": "repository.docker.gc",
  "name": "nx: Docker GC docker-hosted",
  "enabled": true,
  "notificationCondition": "FAILURE",
  "frequency": {"schedule": "manual"},
  "properties": {"repositoryName": "docker-hosted", "deployOffset": "24"}
}
→ 201 {"id": "f3a1c37a-…", "currentState": "WAITING", "properties": {…}, …}
```

Fresh 3.70 and 3.96 servers have **no** Docker GC or compaction task, so an administrator (or
`nx gc --create-missing` on 3.96) has to create them.

### Storage reclamation

* Deleting components or assets does not free disk space immediately.
* Docker: running *Docker - Delete unused manifests and images* and then *Admin - Compact blob
  store* shrank the blob store from 355 blobs / 68.8 MB to 230 blobs / 34.5 MB in our test (3.96).
  The metrics in `GET /v1/blobstores` (`blobCount`, `totalSizeInBytes`) were updated a few seconds
  after the tasks finished.
* **(3.96)** Nexus automatically creates one system task *Admin - Cleanup unused asset blobs*
  (`assetBlob.cleanup`, properties `{"contentStore": "nexus", "format": "<format>"}`) per format in
  use, scheduled every 30 minutes. This task is not among the templates, so it cannot be created
  through the API. Judging by its name, it releases the blobs of deleted assets. In our test, the blob
  of a deleted raw file was still on disk (not marked deleted) after a manual run of this task
  followed by compaction, both for a blob created one minute and 31 minutes before the deletion. The
  server applies a delay we could not determine, so space released by raw deletions is reclaimed
  asynchronously. This is to be verified further during milestone M3.
* Compaction logs `Begin deleted blobs processing for blob store '<name>' before <timestamp>` and
  prunes empty directories. It only removes blobs that were already soft-deleted.

## Privileges

Tested on 3.96.3 with users that held only the listed privileges, for repository `raw-hosted`
(`P = nx-repository-view-raw-raw-hosted`).

| Privileges | list repos | `GET /v1/repositories/{r}` | components | search | browse | content `GET` | content `PUT` | multipart upload | tasks |
|---|---|---|---|---|---|---|---|---|---|
| none | 200 (empty) | 403 | 403 | 403 | 403 | 403 | 403 | 403 | 403 |
| `P-browse` | 200 (1 repo) | 200 | 200 | 200 | 200 | 403 | 403 | 400 | 403 |
| `P-read` | 200 (empty) | 200 | 200 | 200 (0 hits) | 403 | 200 | 403 | 400 | 403 |
| `P-browse`, `P-read` | 200 | 200 | 200 | 200 | 200 | 200 | 403 | 400 | 403 |
| `P-add`, `P-edit` | 200 (empty) | 403 | 403 | 403 | 403 | 403 | **201** | 403 | 403 |
| `P-add`, `P-edit`, `P-read`, `P-browse` | · | · | · | · | · | · | · | **204** | · |
| `P-delete` | 200 (empty) | 403 | 403 | 403 | 403 | 403 | 403 | 403 | 403 |
| `nx-tasks-read`, `nx-tasks-run` | 200 (empty) | 403 | 403 | 403 | 403 | 403 | 403 | 403 | **200** |

Deletion:

| Privileges | `DELETE /repository/r/path` | `DELETE /v1/components/{id}` | `DELETE /v1/assets/{id}` | folder delete (browse API) |
|---|---|---|---|---|
| `P-delete` | **204** | 403 | not tested | not tested |
| `P-browse`, `P-read`, `P-delete` | not tested | **204** | **204** | 403 |

`·` means not tested. `nx-component-upload` was **not** required for the multipart upload.
`P-add` + `P-browse` + `nx-component-upload` (without `edit` and `read`) was rejected with
`400 Not authorized for requested path`.

## Version differences

| Area | 3.70.1 (OrientDB) | 3.96.3 (H2) |
|---|---|---|
| OpenAPI document | Swagger 2.0 | OpenAPI 3.0.1 |
| Component/asset page size | 10 | 100 |
| Raw `name`/`path` | no leading `/` | leading `/` |
| Component `version` for raw | `null` | `""` |
| Search wildcard | any length | ≥ 3 characters before `*` |
| Browse API | no | yes |
| Task create/update/delete/templates | no (`405`) | yes |
| Task `properties` in responses | no | yes |
| Asset `docker` attributes, `blobStoreName`, `registryUrl` | no | yes |
| Repository `online`, `size` fields | no | yes |
| `raw.name`, `oci.*`, `docker.os/architecture` search parameters | no | yes |
| `/v1/search/versions`, `/v1/search/suggest` | no | yes |
| Docker `pathEnabled` routing | no | yes |
| Registry API under `/repository/<repo>/v2/` | yes | yes |
| Content `ETag` = SHA-1 | yes | yes |
| Community Edition EULA endpoint | no | yes |

## Reproducing the observations

```sh
# Nexus 3.96.3 (H2) with a Docker connector on 8082
docker run -d --name nexus --network host sonatype/nexus3:3.96.3
until curl -fs http://localhost:8081/service/rest/v1/status; do sleep 5; done
PASS=$(docker exec nexus cat /nexus-data/admin.password)
curl -u "admin:$PASS" -X PUT -H 'Content-Type: text/plain' --data 'admin123' \
     http://localhost:8081/service/rest/v1/security/users/admin/change-password

# raw hosted repository
curl -u admin:admin123 -H 'Content-Type: application/json' -X POST \
     http://localhost:8081/service/rest/v1/repositories/raw/hosted \
     -d '{"name":"raw-hosted","online":true,"storage":{"blobStoreName":"default","strictContentTypeValidation":false,"writePolicy":"ALLOW"}}'

# docker hosted repository with an HTTP connector
curl -u admin:admin123 -H 'Content-Type: application/json' -X POST \
     http://localhost:8081/service/rest/v1/repositories/docker/hosted \
     -d '{"name":"docker-hosted","online":true,"storage":{"blobStoreName":"default","strictContentTypeValidation":true,"writePolicy":"ALLOW"},"docker":{"v1Enabled":false,"forceBasicAuth":true,"httpPort":8082}}'

# multi-arch image fixture
crane auth login localhost:8082 -u admin -p admin123
crane copy --insecure index.docker.io/library/alpine:3.21 localhost:8082/multi/alpine:3.21
```
