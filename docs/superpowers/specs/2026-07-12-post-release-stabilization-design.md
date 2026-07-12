# Post-release Stabilization Design

## Objective

Close the operational gaps found after `v0.1.0` without changing Daimon's runtime behavior. This slice adds a repeatable release smoke test, moves repository-owned GitHub Actions workflows to Node.js 24-compatible action releases, and protects `main` with an active GitHub ruleset.

## Scope

This slice includes only:

- a host-native smoke script for an already-published GitHub Release;
- tests for the smoke script that do not depend on GitHub or the network;
- Node.js 24-compatible upgrades for JavaScript actions referenced by repository workflows;
- an active `main` ruleset requiring pull requests and the agreed blocking checks;
- operator documentation for running and diagnosing the smoke test.

It does not include application features, changes to release archive contents, a new release, dependency upgrades unrelated to action runtime compatibility, or durable task recovery. Durable recovery is specified separately in [2026-07-12-durable-step-recovery-design.md](2026-07-12-durable-step-recovery-design.md).

## Release smoke test

### Interface and platform selection

Add `scripts/smoke-release.sh`. It accepts an explicit tag and defaults the repository to `Forest-Isle/daimon`; CI and release verification always pass both values explicitly. It uses a temporary directory and removes it on every exit path.

The script maps the current host to one released archive:

| Host value | Release value |
|---|---|
| `Darwin` | `darwin` |
| `Linux` | `linux` |
| `x86_64` / `amd64` | `amd64` |
| `arm64` / `aarch64` | `arm64` |

Any other OS or architecture fails before downloading or executing a file. The selected name is exactly `daimon_<os>_<arch>.tar.gz`, matching `scripts/package-release.sh` and the four jobs in `.github/workflows/release.yml`.

### Verification sequence

The smoke script performs these steps in order:

1. Download the selected archive and `checksums.txt` from the requested GitHub Release using `gh release download`.
2. Require exactly one checksum entry for the selected archive and reject malformed or duplicate entries.
3. Verify the archive with `sha256sum` when available, otherwise `shasum -a 256`; absence of both tools is an error.
4. Inspect the tar member list before extraction. Only `daimon`, `LICENSE`, and `README.md` are accepted; absolute paths, parent traversal, symlinks, hard links, devices, and unexpected members are rejected.
5. Extract into the temporary directory and require `daimon` to be a regular executable file.
6. Run `daimon version` and require its version field to equal the requested tag. The current binary format is `daimon <version> (commit: <commit>, built: <date>)`, and the release workflow builds with `VERSION=${{ github.ref_name }}`.

The script never starts Gateway, reads the operator's Daimon home, or modifies repository files. A smoke failure reports the failed stage and exits non-zero; it does not retry with a different platform or a different release.

### Hermetic tests

Add `scripts/smoke-release_test.sh`. A fake `gh` executable and local fixture archives drive all paths, so the tests work without credentials or network access. The suite covers:

- Darwin/Linux and amd64/arm64 name mapping;
- successful checksum, archive validation, extraction, and exact version match;
- checksum mismatch;
- missing or duplicate checksum entries;
- unsafe or unexpected tar members;
- unsupported host values;
- binary version mismatch;
- cleanup after both success and failure.

The host detector and command locations are injectable only through documented test environment variables. Production defaults always query the real host and use the normal command search path.

Expose the test through a Make target and document the operator command separately from the hermetic test command. The post-release run records the tag, archive name, checksum result, and version output in the delivery report rather than committing downloaded artifacts.

## GitHub Actions Node.js 24 compatibility

Inventory every `uses:` reference under `.github/workflows`, including CI, coverage, package, release, and security workflows. For each JavaScript action, select its maintained stable major whose action metadata declares the Node.js 24 runtime. Docker and composite actions are not rewritten merely because they do not declare a Node runtime.

The upgrade preserves all existing workflow triggers, permissions, job names, matrices, inputs, artifact names, and failure policy. In particular:

- the historical full lint report remains non-blocking;
- `Incremental Lint`, `Layer Boundaries`, `Test`, `Eval Gate`, and `Vet` remain blocking;
- package and release matrices remain Linux/Darwin by amd64/arm64;
- coverage and security steps keep their current optional/non-blocking behavior where explicitly configured;
- the release workflow remains scoped to its existing release tag policy in this slice.

Floating references are replaced with a stable compatible release where the upstream publishes one. If a referenced third-party JavaScript action has no Node.js 24-compatible maintained release, replace only that step with a maintained action or an equivalent shell command; preserve its current inputs and failure semantics.

Verification consists of `actionlint` over every workflow, inspection of each selected action's `action.yml` runtime, and successful GitHub runs with no Node.js 20 deprecation annotation attributable to repository-owned action references. A clean local `actionlint` run alone is not evidence of runtime compatibility.

## `main` ruleset

### Rule definition

Create one repository ruleset targeted exactly at `refs/heads/main`, with active enforcement and no bypass actors. It contains:

- require changes through a pull request;
- zero required approving reviews, appropriate for the single-maintainer repository;
- require all configured status checks to pass;
- require the pull request branch to be up to date before merge;
- block force pushes through the non-fast-forward rule;
- block branch deletion.

The required check contexts are the stable job display names already emitted by the workflows:

- `Incremental Lint`
- `Layer Boundaries`
- `Test`
- `Eval Gate`
- `Vet`
- `Package linux/amd64`
- `Package linux/arm64`
- `Package darwin/amd64`
- `Package darwin/arm64`

The non-blocking `Lint` debt report, `Build`, coverage, and scheduled security jobs are intentionally not required. The package contexts must be observed on a successful pull request run before activating the ruleset; guessed or stale context names are not accepted.

### Safe application and verification

Before mutation, export the repository's existing rulesets as JSON. Create or update the Daimon `main` ruleset through `gh api` using a checked payload, then read it back and compare target, enforcement, bypass actors, pull-request policy, strict status-check policy, and all nine required contexts. Do not delete unrelated repository or organization rulesets.

Acceptance requires external verification, not only a successful API response:

1. `main` rejects a non-fast-forward update and deletion under the authenticated maintainer identity.
2. A pull request cannot merge while a required check is pending or failing.
3. A pull request becomes mergeable only after all nine contexts pass on the current head.
4. The repository's normal pull-request merge path remains usable without a second reviewer.

If the current GitHub plan cannot enforce a selected rule, stop after restoring the prior ruleset payload and report the unsupported rule. Do not silently weaken the requested policy.

## Delivery order

1. Implement and test the hermetic smoke script.
2. Run the smoke script against the published `v0.1.0` host artifact.
3. Upgrade action references and verify the workflows on a pull request.
4. Record the exact successful check contexts from that pull request.
5. Apply and read back the `main` ruleset.
6. Verify merge blocking with the same pull request before merging it.

The ruleset is applied last because requiring contexts that have not yet run successfully can make the repository impossible to merge through the intended path.

## Failure handling

- Release asset or checksum missing: fail without executing any downloaded binary.
- Checksum, archive-shape, or version mismatch: fail, remove temporary files, and identify the mismatched field.
- Unsupported smoke-test host: fail before download and direct the operator to a supported runner.
- Action runtime still emits a Node.js 20 warning: identify the owning `uses:` reference and keep the upgrade slice unmerged.
- Workflow behavior changes after an action upgrade: revert or adapt that individual step; do not relax its existing gate.
- Ruleset payload or read-back differs: restore the exported prior state and leave `main` protection unchanged.
- Required check names differ from the design: update the payload from observed check runs before activation, not from assumptions.

## Acceptance criteria

The slice is complete when:

1. the hermetic smoke suite covers success and all fail-closed paths;
2. the published host-native `v0.1.0` archive passes checksum, archive, and version verification;
3. every repository-owned JavaScript action reference uses a maintained Node.js 24-compatible runtime and GitHub emits no Node.js 20 deprecation warning for those references;
4. all workflow files pass `actionlint` and their existing jobs retain the same semantics;
5. the active `main` ruleset requires pull requests, strict current-head checks, the nine agreed contexts, and prohibits force pushes and deletion;
6. the ruleset is read back and its merge-blocking behavior is verified on GitHub;
7. no release asset, tag, runtime feature, or unrelated dependency is changed.
