# Final Review Fix Report

## Status

Complete. All blocking final-review findings and the requested lint minor were implemented and verified. No tag, push, merge, publication, release creation, or other remote mutation was performed.

Implementation commits:

- `8e7dc2f fix: close final release hardening gaps`
- `2d67a5e refactor: split gateway startup stages`

## Implemented fixes

### Reproducible packages

- `scripts/package-release.sh` now uses `SOURCE_DATE_EPOCH`, falling back to the tag/current commit timestamp, for both the embedded build date and archive metadata.
- A Python 3 standard-library writer creates sorted GNU tar entries with normalized `mtime`, uid/gid, uname/gname, plus a gzip header using the same timestamp and no source filename.
- Archive naming and exact contents remain `daimon_<os>_<arch>.tar.gz` containing `LICENSE`, `README.md`, and `daimon`.
- `scripts/package-release_test.sh` builds twice, compares SHA-256 in Python, checks exact names and metadata, checks the gzip filename flag, checks the default commit timestamp, extracts the archive, and runs `daimon version`.

### Release workflow and documentation

- Added `.github/release-notes/v0.1.0.md` identifying the release as alpha and stating all three required limitations.
- `.github/workflows/release.yml` checks out the notes and publishes with `--notes-file`, `--prerelease`, and `--verify-tag`; it still requires the four named archives and `checksums.txt`.
- Both READMEs now show archive/checksum download, SHA-256 verification, extraction, and `daimon version` commands.
- `CHANGELOG.md` now keeps an empty `Unreleased` section first and scopes all pre-public-release changes under `0.1.0`.

### Admin token and lifecycle

- Config validation and `InitAdmin` both reject blank or unresolved `${...}` tokens when `server.enabled` is true. Disabled example config retains its unresolved placeholder without error.
- Gateway startup now owns a cancelable run context. Any startup error calls the idempotent shutdown path, stopping channels/subsystems, listeners, MCP/tool background resources, init work, and the database.
- A failing-channel regression captures the actual ephemeral admin address, proves the channel error is returned, verifies channels stop, and rebinds the same address after rollback.
- Admin shutdown preserves a caller deadline, derives five seconds only when none is supplied, retains state until graceful shutdown or forced close succeeds, joins graceful/forced-close errors, and retains state if both fail.
- Gateway startup stages were extracted after incremental lint correctly flagged the newly touched monolithic function; ordering is unchanged.

### One source of truth and lint test

- `server` was removed from Feature Registry registration and override resolution. YAML `server.enabled` is documented as authoritative and non-hot-toggleable.
- `scripts/lint-new_test.sh` retains its argument/invalid-base checks and now creates an isolated repository: the unchanged base passes real `golangci-lint`, while a newly added undefined symbol is detected and propagates a nonzero result. It skips only when the real linter is unavailable and never installs over the network.

## TDD evidence

All temporary mutations were restored before commits.

| Behavior | RED command/result | GREEN command/result |
|---|---|---|
| Enabled unresolved admin token | `CGO_ENABLED=1 go test -tags fts5 ./internal/config -run 'TestValidate.*Server' -count=1` — exit `1`; validation returned nil for `${DAIMON_ADMIN_TOKEN}`. | Same command — exit `0`. |
| Direct `InitAdmin` rejection and shutdown seams | `CGO_ENABLED=1 go test -tags fts5 ./internal/gateway -run 'TestAdminInitFailsClosedWithUnresolved|TestAdminStop|TestGatewayStartRollsBack' -count=1` — exit `1`; new shutdown/close behavior did not exist. | Focused admin/Gateway command below — exit `0`. |
| Package repeatability/metadata | `bash scripts/package-release_test.sh` — exit `1` before deterministic packaging (double-build SHA check failed). | `bash scripts/package-release_test.sh` — exit `0`; duplicate hashes and normalized metadata assertions passed. |
| `server` registry removal | `CGO_ENABLED=1 go test -tags fts5 ./internal/gateway -run TestServerIsNotRuntimeFeature -count=1` — exit `1`, registry still contained `server`. | Included in focused Gateway green — exit `0`. |
| Retain state when graceful and forced close fail | `CGO_ENABLED=1 go test -tags fts5 ./internal/gateway -run TestAdminStopRetainsListenerWhenForceCloseFails -count=1` — exit `1`, listener was cleared. | Included in focused Gateway green — exit `0`; joined errors also preserve `errors.Is`. |
| Transactional Gateway rollback | With only the rollback call temporarily removed: `CGO_ENABLED=1 go test -tags fts5 ./internal/gateway -run TestGatewayStartRollsBackAdminAfterChannelFailure -count=1` — exit `1`, `startup rollback must stop channels`. | After restoring production code, same command — exit `0`; exact bound admin address was bindable again. |

Focused final GREEN commands:

- `CGO_ENABLED=1 go test -tags fts5 ./internal/gateway -run 'TestAdminStop|TestAdminInitFailsClosedWithUnresolved|TestGatewayStartRollsBack|TestServerIsNotRuntimeFeature' -count=1` — exit `0`.
- `CGO_ENABLED=1 go test -tags fts5 ./internal/config -run 'TestValidate.*Server' -count=1` — exit `0`.
- `CGO_ENABLED=1 go test -tags fts5 ./internal/config ./internal/gateway -count=1` — exit `0`.

## Verification matrix

All listed final matrix commands were run against implementation commit `2d67a5e` unless noted.

| Command | Exit | Result |
|---|---:|---|
| `bash scripts/package-release_test.sh` | 0 | Three host builds completed; two fixed-epoch archives had identical SHA-256; exact content/metadata/default timestamp assertions passed; binary reported `daimon test-version (commit: 2d67a5e, built: 2023-11-14T22:13:20Z)`. |
| cached actionlint v1.7.7 on `.github/workflows/{ci,package,release}.yml` | 0 | No workflow findings. The binary is the existing Go build-cache executable because `actionlint` is not on PATH. |
| `bash -n scripts/package-release.sh scripts/package-release_test.sh scripts/lint-new.sh scripts/lint-new_test.sh scripts/compose-check.sh scripts/docker-runtime_test.sh` | 0 | Shell syntax passed. |
| `make compose-check` | 0 | Compose rendered; only expected warnings for unset Anthropic/Telegram environment variables. |
| `make docker-runtime-test` | 0 | Dockerfile/Compose non-root runtime and environment contract passed. |
| `bash scripts/lint-new_test.sh` | 0 | Invalid base rejected; isolated unchanged base reported `0 issues`; new undefined symbol produced one `typecheck` issue and nonzero inner lint result. |
| `make lint-new LINT_BASE=01c12ed` | 0 | `0 issues.` |
| `make eval-gate` | 0 | Checked-in replay corpus evaluated; coding-surface self-check `PASS`. |
| `make build-bin` | 0 | CGO/FTS5 binary built at commit `2d67a5e`. |
| `make vet` | 0 | No findings. |
| `make test` | 0 | Full `CGO_ENABLED=1 go test -tags fts5 ./... -v -race -count=1` passed with no `FAIL` or race report. |
| `git diff --check` / final `git status --short` | 0 | No whitespace errors; clean before writing this report. A final clean check follows the report commit. |

The first full-matrix lint attempt correctly failed at `make lint-new LINT_BASE=01c12ed` with `gocognit` complexity `39 > 30` on the newly touched `Gateway.Start`. Startup stages were extracted in `2d67a5e`; the focused lifecycle tests and incremental lint then passed. This was not waived.

The full race run emitted the known macOS linker `LC_DYSYMTAB` warnings for CGO test binaries. They did not fail linking or any test and are not introduced by this change.

## Changed files

- Release/package: `.github/release-notes/v0.1.0.md`, `.github/workflows/release.yml`, `scripts/package-release.sh`, `scripts/package-release_test.sh`.
- Runtime/config: `internal/config/config.go`, `internal/config/validate.go`, `internal/gateway/gateway.go`, `internal/gateway/subsystem_admin.go`, `internal/gateway/subsystem_feature.go`.
- Tests: `internal/config/validate_test.go`, `internal/gateway/e2e_lifecycle_test.go`, `internal/gateway/subsystem_admin_test.go`, `internal/gateway/subsystem_feature_test.go`, `scripts/lint-new_test.sh`.
- User/release docs: `README.md`, `README_zh.md`, `CHANGELOG.md`, `configs/daimon.example.yaml`, `docs/ARCHITECTURE_GUIDE.md`, `docs/architecture/14-gateway.md`, `docs/architecture/18-supporting.md`.

## External limitations / concerns

- `docker info --format '{{.ServerVersion}}'` exited `1`: Docker Desktop's daemon socket does not exist. A live image build/container write test remains external; the deterministic runtime contract test passed.
- Local package execution covered host Darwin/arm64. Linux amd64/arm64 and Darwin/amd64 execution remain the checked-in GitHub matrix responsibility.
- No tag workflow was dispatched and no GitHub Release/assets were created, by task boundary.
- `actionlint` v1.7.7 exists only in the local Go build cache rather than PATH; its exact cached executable passed all three workflows.

## Self-review

- Reviewed `main...HEAD` for the seven blocking finding areas plus the lint minor.
- Confirmed archive content and release asset checks remain exact, `--verify-tag` remains present, and `--generate-notes` is removed.
- Confirmed no operational Feature Registry registration/override for `server` remains outside historical design/plan artifacts.
- Confirmed temporary rollback mutation was restored (`git diff --exit-code` passed) before this report was written.
