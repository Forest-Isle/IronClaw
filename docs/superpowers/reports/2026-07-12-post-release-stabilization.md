# Post-release Stabilization Delivery Report

## Published release smoke

- Repository: `Forest-Isle/daimon`
- Executed at (UTC): `2026-07-12T05:44:16Z`
- daimon_darwin_arm64.tar.gz: OK
- tag: v0.1.0
- archive: daimon_darwin_arm64.tar.gz
- checksum: verified
- daimon v0.1.0 (commit: 32766c0, built: 2026-07-11T16:23:41Z)

## GitHub Actions verification

- Pull request: `#7`
- Verified pre-report head: `783e86f46934d6642a1133e75623f27ce800537a`
- `actionlint`: passed for all five workflow files
- Node.js 20 deprecation logs: none
- Successful required contexts:
  - `Eval Gate`
  - `Incremental Lint`
  - `Layer Boundaries`
  - `Package darwin/amd64`
  - `Package darwin/arm64`
  - `Package linux/amd64`
  - `Package linux/arm64`
  - `Test`
  - `Vet`

The ruleset ID and post-activation read-only verification are delivery handoff data; they are intentionally not committed after policy activation.
