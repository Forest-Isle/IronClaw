# Phase 1 Release Hardening Design

## Objective

Prepare Daimon for its first alpha release without turning the work into a full repository cleanup. The phase makes release artifacts reproducible, prevents new quality regressions, makes the eval gate hermetic, and secures the optional admin HTTP service. After the branch is reviewed and merged, the same verified main commit will be tagged and published as `v0.1.0`.

## Scope

This phase includes:

- replace the non-viable CGO GoReleaser flow and repair Docker Compose configuration;
- add native-platform snapshot packaging checks;
- preserve the existing lint backlog while blocking newly introduced findings;
- fix only confirmed runtime and security findings encountered in the changed paths;
- run the deterministic eval gate against a repository-owned replay fixture;
- replace the unmanaged admin HTTP goroutine with a lifecycle-managed subsystem;
- require authentication for private admin endpoints and apply HTTP timeouts;
- update English and Chinese user documentation, security guidance, configuration examples, and the changelog;
- prepare and, after merge, publish `v0.1.0` as an alpha release.

The phase does not include:

- clearing all historical lint findings;
- broad complexity or duplicate-code refactors;
- OpenTelemetry integration;
- durable per-tool-call execution or event retry redesign;
- new channels, providers, or user interfaces;
- automatic skill-to-reflex promotion.

## Delivery Boundary

Implementation happens on `feat/phase1-hardening` in an isolated worktree. The branch contains code, tests, CI, packaging, documentation, and the `0.1.0` changelog entry, but no release tag.

After review and merge:

1. update local `main` to the reviewed merge commit;
2. rerun the full verification matrix on that exact commit;
3. create the annotated tag `v0.1.0`;
4. push the tag;
5. let the release workflow publish GitHub artifacts;
6. verify the GitHub Release, checksums, and downloadable archives.

No tag or release is created from the feature branch.

## Release and Packaging

The existing GoReleaser configuration will be removed. GoReleaser OSS cannot merge partial releases produced by native platform runners; its supported split/merge flow for CGO is a Pro feature. A single Linux runner cannot reliably link Daimon's macOS CGO binaries, so merely correcting the repository owner would leave the release path structurally broken.

A repository-owned packaging script will instead build one explicit `GOOS/GOARCH` target using the native runner toolchain, create a deterministic `daimon_<os>_<arch>.tar.gz`, and emit artifact metadata. GitHub Actions will run Linux targets on Ubuntu and Darwin targets on macOS. A final release job will download the four archives, generate `checksums.txt`, and publish them against the triggering tag.

Pull requests and pushes to `main` will run the same native matrix in snapshot mode without publishing. This validates the actual commands used by the tag workflow rather than a separate approximation.

Docker Compose will use the service, container, volume, and configuration names for Daimon. It will mount the existing `configs/daimon.yaml` path expected by the image and persist the actual user state directory. The Compose file must pass `docker compose config`; documentation will instruct users to copy `configs/daimon.example.yaml` before starting it.

Actual publishing remains restricted to `v*` tags and requires all four build jobs to succeed.

## Incremental Quality Gate

The current full-repository lint job remains visible as a non-blocking debt report. A separate blocking job evaluates only findings introduced relative to a trusted Git revision:

- pull request: the PR base SHA;
- push to `main`: the event's before SHA;
- local use: an explicit base argument, defaulting to the merge base with `main`.

The implementation will use golangci-lint's revision-aware analysis rather than a checked-in list of warning strings. CI must fetch complete enough history to resolve the selected base. A missing or invalid base is an error; the script must not silently fall back to a non-blocking full lint.

Historical findings remain visible. Editing a line that contains an existing finding may require fixing that finding, which is acceptable because the changed code is now in scope.

This phase fixes three confirmed findings: unchecked post-tool hook errors, the admin HTTP timeout/authentication gaps, and unsafe archive mode conversion when restoring Soul archives. Findings that are deliberate capabilities, demonstrable false positives, or unrelated historical style issues remain in the backlog and are not suppressed globally.

## Hermetic Eval Gate

`make eval` continues to analyze the operator's live replay corpus. `make eval-gate` becomes the deterministic CI entry point and will pass explicit repository paths for:

- a small valid replay corpus under `evals/fixtures/`;
- a temporary or repository-scoped baseline path that does not read or write `~/.daimon`.

The fixture will exercise replay parsing and failure classification while the existing coding-surface self-check remains the pass/fail acceptance gate. Tests will verify that the gate succeeds with an empty HOME and does not create files outside its temporary directory.

The CI job will call `make eval-gate`; it will not duplicate the command inline.

## Admin HTTP Subsystem

The current `startHTTPServer` function starts an unmanaged goroutine and exposes session metadata without authentication. It will be replaced by an `AdminSubsystem` owned by Gateway lifecycle management.

### Configuration

The existing `server.enabled` flag remains the feature switch. Configuration adds:

- `server.addr`, default `127.0.0.1:8080`;
- `server.token`, supplied through environment expansion such as `${DAIMON_ADMIN_TOKEN}`.

Enabling the admin server without a non-empty token is a configuration error. This fail-closed rule applies even on loopback so that a later bind-address change cannot accidentally convert an unauthenticated installation into a remote exposure.

### Routes

- `GET /health` remains unauthenticated and returns liveness only.
- `/api/*` requires `Authorization: Bearer <token>`.
- unsupported HTTP methods return `405`.
- authentication failures return `401` without revealing whether a token was missing or incorrect.
- database failures are logged internally and return a generic `500` response.

Token comparison will avoid ordinary string equality. No token, authorization header, or database error text may be written into an HTTP response or log field.

### Lifecycle and Timeouts

The subsystem owns an `http.Server` configured with finite read-header, read, write, and idle timeouts. `Start` binds and serves; `Stop` performs bounded graceful shutdown. Expected `http.ErrServerClosed` is not logged as an error.

Gateway will register the subsystem only when the server feature is enabled. If binding or validation fails, Gateway startup fails instead of logging the error and continuing with a partially started runtime.

## Tests

Behavior changes follow red-green-refactor cycles.

Admin subsystem tests cover:

- enabled server without a token fails closed;
- `/health` works without authentication;
- missing, malformed, and incorrect bearer tokens return `401`;
- the correct token permits `GET /api/sessions`;
- unsupported methods return `405`;
- database errors are not returned to clients;
- server timeout fields are non-zero;
- graceful shutdown completes within its bound.

Eval tests cover:

- the repository fixture loads deterministically;
- `make eval-gate` does not depend on the real user directory;
- the coding-surface self-check still accepts the clean fixture and rejects reward hacking.

Packaging and CI checks cover:

- `docker compose config` with a prepared local config;
- native Linux and macOS snapshot package generation;
- incremental lint failing on a deliberately introduced finding in script-level tests or a controlled fixture, while the unchanged historical backlog does not fail the blocking job.

Final branch verification includes `make test`, `make vet`, `make build-bin`, `make eval-gate`, the incremental lint command, Docker Compose validation, and native package snapshots on Linux and macOS CI.

## Documentation and Versioning

`README.md` and `README_zh.md` will document the alpha status, corrected Docker workflow, admin token requirement, and release installation path. `SECURITY.md` will describe the authenticated admin surface and loopback default. The example configuration will include `${DAIMON_ADMIN_TOKEN}` without embedding a secret.

`CHANGELOG.md` will gain a `0.1.0` entry dated 2026-07-11 using Keep a Changelog sections. The release notes will state that this is an alpha release and list known limitations: single-user/local-first scope, autonomous features disabled by default, and no exact mid-episode durable resume.

## Failure Handling

- CI cannot resolve a lint base: fail the blocking job with a diagnostic.
- Admin token missing while enabled: fail configuration/startup before listening.
- Admin bind failure: fail Gateway startup and clean up already-started subsystems.
- Eval fixture missing or malformed: `make eval-gate` exits non-zero.
- Any native package snapshot failure: block merge and do not create a tag.
- Tag workflow failure after merge: do not move or recreate the tag; diagnose and rerun the workflow against the same tagged commit after fixing only workflow infrastructure when safe.

## Acceptance Criteria

The phase is ready for review when:

1. historical lint debt remains reported but does not block unchanged code;
2. a new lint finding blocks CI;
3. admin private routes cannot be accessed without the configured token;
4. the admin server is lifecycle-managed and bounded by timeouts;
5. `make eval-gate` passes with an isolated HOME using only repository fixtures;
6. Docker Compose validation and all native package snapshots succeed;
7. the full Go test, race, vet, and build matrix passes;
8. documentation matches the implemented configuration and release process;
9. every file in the worktree is committed before handoff.

The phase is fully delivered only after the reviewed branch is merged and `v0.1.0` is published from the verified `main` commit.
