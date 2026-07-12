# Task 3 GitHub Actions Inventory Evidence

Verified at `2026-07-12T05:58:15Z` from each action's official GitHub repository at the exact selected ref.

## Before inventory

```text
actions/checkout@v4
actions/download-artifact@v4
actions/setup-go@v5
actions/upload-artifact@v4
codecov/codecov-action@v4
github/codeql-action/upload-sarif@v4
golangci/golangci-lint-action@v7
madrapps/jacoco-report@v1.6.1
securego/gosec@master
```

The inventory contained exactly nine unique references. Comparing it with the target inventory returned `diff` exit 1 before editing, establishing the planned RED check.

## Exact upstream metadata

| Selected reference | Metadata path | Ref object SHA | `runs.using` |
|---|---|---|---|
| `actions/checkout@v7` | `action.yml` | `9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0` | `node24` |
| `actions/setup-go@v6` | `action.yml` | `924ae3a1cded613372ab5595356fb5720e22ba16` | `node24` |
| `golangci/golangci-lint-action@v9` | `action.yml` | `db9de0fc1a667e1a49d2291a1a042dff081d78f6` | `node24` |
| `actions/upload-artifact@v7` | `action.yml` | `043fb46d1a93c77aae656e7c1c64a875d1fc6a0a` | `node24` |
| `actions/download-artifact@v8` | `action.yml` | `3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c` | `node24` |
| `madrapps/jacoco-report@v1.8.0` | `action.yml` | `e51ce1f46f7f8b5331593f935e59cbaf44b84920` | `node24` |
| `github/codeql-action/upload-sarif@v4` | `upload-sarif/action.yml` | `1ad29ea4a422cce9a242a9fae469541dcd08addc` | `node24` |
| `codecov/codecov-action@v7` | `action.yml` | `a99c28d3f0da835de33ff2feb2e15691c7b9641f` | `composite` |
| `securego/gosec@v2.27.1` | `action.yml` | `5a2f0a6455f43d8ac8efaf1591ee0fb400dce662` | `docker` |

Codecov v7's composite metadata invokes `actions/github-script@ed597411d8f924073f98dfc5c65a23a2325f34cd` (`v8.0.0`). The nested action's official `action.yml` declares `runs.using: node24`.

## After inventory

```text
actions/checkout@v7
actions/download-artifact@v8
actions/setup-go@v6
actions/upload-artifact@v7
codecov/codecov-action@v7
github/codeql-action/upload-sarif@v4
golangci/golangci-lint-action@v9
madrapps/jacoco-report@v1.8.0
securego/gosec@v2.27.1
```

The after inventory contains exactly nine unique references, exactly matches the planned target, and contains no floating `@main` or `@master` reference.
