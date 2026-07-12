# Task 1 Report: Hermetic Release Smoke Contract

## Result

DONE

## Changes

- `scripts/smoke-release_test.sh`: added a credential-free, network-free nine-case suite with fake `gh`/`uname`, local release fixtures, hostile archive fixtures, and cleanup assertions. Fixture tar creation sets `COPYFILE_DISABLE=1` so macOS does not inject AppleDouble `._*` members.
- `scripts/smoke-release.sh`: added host-native release selection, temporary download staging, strict checksum parsing and verification, pre-extraction tar inspection, executable validation, exact tag validation, stage-prefixed failures, and unconditional cleanup.
- `Makefile`: added the phony `smoke-release-test` target.
- `README.md`: replaced manual release extraction with the smoke command, documented validation/failure behavior, and documented the hermetic test command and test-only injection variables.

## TDD Evidence

### RED

Command:

```text
bash scripts/smoke-release_test.sh
```

Observed exit 127 before production implementation:

```text
env: /Users/wuqisen/dev/Daimon/.worktrees/post-release-stabilization/scripts/smoke-release.sh: No such file or directory
```

This was the intended missing-feature failure; the fake `gh` was not invoked.

### GREEN

The first implementation run correctly rejected macOS-generated AppleDouble members (`._daimon`, `._LICENSE`, and `._README.md`) in the local fixture. The test fixture was made platform-hermetic by setting `COPYFILE_DISABLE=1` only around its `tar` creation commands; production archive validation remained strict.

Final command:

```text
make smoke-release-test
```

Observed exit 0 and final output:

```text
smoke-release tests: 9 cases passed
```

The output also showed the exact success fields and every expected stage-prefixed failure. All per-case temporary-directory cleanup assertions passed.

## Verification

- `/bin/bash --version | head -1` → `GNU bash, version 3.2.57(1)-release (arm64-apple-darwin24)`
- `/bin/bash -n scripts/smoke-release.sh scripts/smoke-release_test.sh` → exit 0, no output
- `make smoke-release-test` → exit 0, `smoke-release tests: 9 cases passed`
- `git diff --check` → exit 0, no output
- Final staged diff/status checked before commit; only the four requested implementation files and this required report were committed.

## Self-review

- The unsupported-host path exits before `gh` and before creating a work directory.
- Download, checksum, inspection, extraction, and version stages all fail closed and identify the current stage.
- Checksum parsing requires one well-formed 64-hex entry for the selected asset; duplicate, missing, malformed, mismatch, and missing-tool paths fail.
- Tar inspection permits exactly three regular files and rejects absolute/traversal paths, links, devices, and extra members before extraction.
- The downloaded binary is invoked only after checksum and archive validation, and only with `version`; the script neither starts Daimon nor reads or writes a Daimon home.
- `DAIMON_SMOKE_TEST_*` injection is opt-in and production defaults remain `uname`, normal `PATH`, and the system temporary parent.
- Syntax and behavior were exercised with the system macOS Bash 3.2.

## Concerns

None. The `COPYFILE_DISABLE=1` test-fixture adjustment is required for deterministic safe archives on macOS and does not weaken production validation.
