# Phase 1 Release Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Daimon's quality gates, admin HTTP surface, packaging, and eval path reliable enough to publish the reviewed `main` commit as the `v0.1.0` alpha release.

**Architecture:** Preserve the full lint backlog as a visible non-blocking report while adding a revision-based blocking gate. Replace the unmanaged admin goroutine with an authenticated `AdminSubsystem`, make eval CI hermetic, and package CGO binaries on native GitHub runners instead of relying on unsupported GoReleaser OSS split/merge.

**Tech Stack:** Go 1.25.11, SQLite/CGO with `fts5`, Bash, GitHub Actions, Docker Compose, GitHub CLI release workflow.

## Global Constraints

- Work only in `/Users/wuqisen/dev/Daimon/.worktrees/phase1-hardening` on `feat/phase1-hardening`.
- Keep historical lint debt visible; block only findings introduced relative to a validated Git base.
- Fix only the three confirmed findings named in the spec: post-tool hook error propagation, admin HTTP security/timeouts, and Soul archive mode validation.
- Keep `make eval` pointed at the operator corpus; make only `make eval-gate` hermetic.
- `server.enabled: true` requires a non-empty admin token even on loopback.
- Do not create or push `v0.1.0` until this branch is reviewed and merged.
- Use TDD for every Go behavior change: observe RED before implementation, then GREEN.
- Match existing Go and YAML style; do not refactor unrelated code.

---

### Task 1: Blocking Incremental Lint Gate

**Files:**
- Create: `scripts/lint-new.sh`
- Create: `scripts/lint-new_test.sh`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: a base commit as `$1`, `LINT_BASE_SHA`, `GITHUB_BASE_SHA`, or `GITHUB_EVENT_BEFORE` in that precedence order.
- Produces: `make lint-new LINT_BASE=<sha>` and a blocking CI job that executes `golangci-lint run --new-from-rev <sha> ./...`.

- [ ] **Step 1: Write the failing shell test**

Create `scripts/lint-new_test.sh` with a temporary fake `golangci-lint` that records its arguments. Assert that an explicit `HEAD^` becomes `run --new-from-rev HEAD^ ./...`, and that an invalid base exits non-zero before invoking the fake linter:

```bash
#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cat >"$tmp/golangci-lint" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$LINT_ARGS_FILE"
EOF
chmod +x "$tmp/golangci-lint"

export PATH="$tmp:$PATH" LINT_ARGS_FILE="$tmp/args"
"$root/scripts/lint-new.sh" HEAD^
test "$(cat "$tmp/args")" = "run --new-from-rev HEAD^ ./..."

rm -f "$tmp/args"
if "$root/scripts/lint-new.sh" definitely-not-a-commit; then
  echo "invalid base unexpectedly succeeded" >&2
  exit 1
fi
test ! -e "$tmp/args"
```

- [ ] **Step 2: Run the test and observe RED**

Run: `bash scripts/lint-new_test.sh`

Expected: FAIL because `scripts/lint-new.sh` does not exist.

- [ ] **Step 3: Implement strict base resolution**

Create `scripts/lint-new.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

base=${1:-${LINT_BASE_SHA:-${GITHUB_BASE_SHA:-${GITHUB_EVENT_BEFORE:-}}}}
if [[ -z "$base" ]]; then
  base=$(git merge-base HEAD main)
fi
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
  echo "lint-new: invalid base commit: $base" >&2
  exit 2
fi
exec golangci-lint run --new-from-rev "$base" ./...
```

Add `lint-new` and `lint-new-test` Make targets. In CI, set `fetch-depth: 0`; keep the existing `lint` job non-blocking and add a blocking `lint-new` job whose base is `${{ github.event.pull_request.base.sha || github.event.before }}`.

- [ ] **Step 4: Verify GREEN and the real repository behavior**

Run:

```bash
bash scripts/lint-new_test.sh
make lint-new LINT_BASE=main
```

Expected: shell test PASS; incremental lint exits 0 when no new production findings exist relative to `main`.

- [ ] **Step 5: Commit**

```bash
git add scripts/lint-new.sh scripts/lint-new_test.sh Makefile .github/workflows/ci.yml
git commit -m "ci: block newly introduced lint findings"
```

---

### Task 2: Hermetic Eval Gate

**Files:**
- Create: `evals/fixtures/replays/2026-07-11.jsonl`
- Modify: `evals/runtime_paths_test.go`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `evals/README.md`

**Interfaces:**
- Consumes: the existing `-replays` and `-score` flags in `evals/cmd/eval`.
- Produces: `make eval-gate`, which reads `evals/fixtures/replays` and writes its score only beneath a temporary directory.

- [ ] **Step 1: Add the failing hermetic-path test**

Extend `evals/runtime_paths_test.go` with a subprocess test that runs `make eval-gate` under an empty temporary HOME and checks that no `.daimon` directory appears there:

```go
func TestEvalGateDoesNotUseUserDir(t *testing.T) {
    root := repoRoot(t)
    home := t.TempDir()
    cmd := exec.Command("make", "eval-gate")
    cmd.Dir = root
    cmd.Env = append(os.Environ(), "HOME="+home)
    out, err := cmd.CombinedOutput()
    if err != nil {
        t.Fatalf("make eval-gate: %v\n%s", err, out)
    }
    if _, err := os.Stat(filepath.Join(home, ".daimon")); !os.IsNotExist(err) {
        t.Fatalf("eval gate touched user dir: %v", err)
    }
}
```

- [ ] **Step 2: Run the test and observe RED**

Run: `go test ./evals -run TestEvalGateDoesNotUseUserDir -count=1`

Expected: FAIL because the current Make target defaults to `$HOME/.daimon/.eval_score.json`.

- [ ] **Step 3: Add the deterministic replay fixture**

Create a valid JSONL corpus with one successful tool round-trip, one governance denial, and a closing turn. Use the telemetry envelope and exact replay event type names:

```json
{"ts":"2026-07-11T00:00:00Z","type":"replay.tool_round_trip","payload":{"SessionID":"fixture_success","Iteration":0,"ToolName":"file_list","ArgsJSON":{},"ResultJSON":{"output":"ok"},"Succeeded":true,"DurationMs":1}}
{"ts":"2026-07-11T00:00:01Z","type":"replay.tool_round_trip","payload":{"SessionID":"fixture_denied","Iteration":0,"ToolName":"bash","ArgsJSON":{},"ResultJSON":{"error":"tool execution denied by security policy"},"Succeeded":false,"DurationMs":1}}
{"ts":"2026-07-11T00:00:02Z","type":"replay.turn_closed","payload":{"SessionID":"fixture_success","FinalReply":"done"}}
```

The event structs use Go's default exported-field JSON names, so the checked-in payload keys above are exactly `SessionID`, `Iteration`, `ToolName`, `ArgsJSON`, `ResultJSON`, `Succeeded`, `DurationMs`, and `FinalReply`.

- [ ] **Step 4: Make `eval-gate` use explicit paths**

Change the target to create a temporary score path and always remove it:

```make
eval-gate:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	CGO_ENABLED=1 go run -tags "$(TAGS)" ./evals/cmd/eval \
		-replays "$(CURDIR)/evals/fixtures/replays" \
		-score "$$tmp/score.json" -gate
```

Add a blocking `Eval Gate` step/job to CI that calls only `make eval-gate`. Update `evals/README.md` to distinguish live diagnostic eval from the hermetic gate.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
go test ./evals -run TestEvalGateDoesNotUseUserDir -count=1
HOME=$(mktemp -d) make eval-gate
```

Expected: both commands PASS; scorecard reports the checked-in fixture corpus and the coding-surface self-check passes.

- [ ] **Step 6: Commit**

```bash
git add evals/fixtures/replays evals/runtime_paths_test.go evals/README.md Makefile .github/workflows/ci.yml
git commit -m "ci: make eval gate hermetic"
```

---

### Task 3: Authenticated Admin Subsystem

**Files:**
- Modify: `internal/config/config_infra.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/config/config_test.go`
- Create: `internal/gateway/subsystem_admin.go`
- Create: `internal/gateway/subsystem_admin_test.go`
- Delete: `internal/gateway/http.go`

**Interfaces:**
- Produces: `config.ServerConfig{Addr, Token, Enabled}`.
- Produces: `InitAdmin(config.ServerConfig, *store.DB) (*AdminSubsystem, error)`.
- Produces: `(*AdminSubsystem).Name()`, `Start(context.Context) error`, and `Stop(context.Context) error`.
- Private routes accept `Authorization: Bearer <token>`; `GET /health` is public.

- [ ] **Step 1: Write failing config tests**

Add tests asserting the default address is `127.0.0.1:8080`, environment expansion fills `server.token`, and enabled-without-token fails validation:

```go
func TestServerEnabledRequiresToken(t *testing.T) {
    cfg := validConfigForTest()
    cfg.Server.Enabled = true
    cfg.Server.Token = "  "
    if err := validate(&cfg); err == nil || !strings.Contains(err.Error(), "server.token") {
        t.Fatalf("validate() error = %v, want server.token error", err)
    }
}
```

- [ ] **Step 2: Observe config RED**

Run: `go test ./internal/config -run 'TestServer' -count=1`

Expected: compile failure because `ServerConfig.Token` does not exist, or assertion failure on the old default.

- [ ] **Step 3: Implement config fields and validation**

Use:

```go
type ServerConfig struct {
    Addr    string `yaml:"addr"`
    Token   string `yaml:"token"`
    Enabled bool   `yaml:"enabled"`
}
```

Set the default address to `127.0.0.1:8080`. In `validate`, reject `cfg.Server.Enabled && strings.TrimSpace(cfg.Server.Token) == ""`.

- [ ] **Step 4: Write failing admin handler tests**

Construct an in-memory test database and call `admin.handler().ServeHTTP` through `httptest`. Cover public health, missing/wrong/correct bearer tokens, `405`, generic database `500`, and non-zero timeout fields. The successful request must decode session JSON without exposing the token.

```go
req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
req.Header.Set("Authorization", "Bearer correct-token")
rr := httptest.NewRecorder()
admin.handler().ServeHTTP(rr, req)
if rr.Code != http.StatusOK { t.Fatalf("status = %d", rr.Code) }
```

- [ ] **Step 5: Observe admin RED**

Run: `go test ./internal/gateway -run 'TestAdmin' -count=1`

Expected: compile failure because `AdminSubsystem` and `InitAdmin` do not exist.

- [ ] **Step 6: Implement the minimal subsystem**

`InitAdmin` validates the token again for callers that construct `config.Config` without `config.Load`. The subsystem builds a private mux, wraps `/api/` with bearer authentication using `subtle.ConstantTimeCompare`, and owns an `http.Server` with finite `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout`.

`Start` must call `net.Listen` synchronously before starting `srv.Serve(listener)` in a goroutine so bind errors reach Gateway. `Stop` performs a five-second bounded shutdown. Log database errors, return only `http.StatusText(http.StatusInternalServerError)` to clients, and treat `http.ErrServerClosed` as expected.

Use this structure, keeping session serialization in a small handler:

```go
type AdminSubsystem struct {
    enabled  bool
    addr     string
    token    string
    db       *store.DB
    srv      *http.Server
    listener net.Listener
}

func InitAdmin(cfg config.ServerConfig, db *store.DB) (*AdminSubsystem, error) {
    if cfg.Enabled && strings.TrimSpace(cfg.Token) == "" {
        return nil, errors.New("server.token is required when server is enabled")
    }
    a := &AdminSubsystem{enabled: cfg.Enabled, addr: cfg.Addr, token: cfg.Token, db: db}
    a.srv = &http.Server{
        Addr: cfg.Addr, Handler: a.handler(),
        ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
        WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
    }
    return a, nil
}

func (a *AdminSubsystem) Name() string { return "admin" }

func (a *AdminSubsystem) handler() http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
        writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
    })
    mux.Handle("/api/", a.requireBearer(http.HandlerFunc(a.handleAPI)))
    return mux
}

func (a *AdminSubsystem) requireBearer(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        const prefix = "Bearer "
        auth := r.Header.Get("Authorization")
        supplied := ""
        if strings.HasPrefix(auth, prefix) { supplied = strings.TrimPrefix(auth, prefix) }
        if len(supplied) != len(a.token) || subtle.ConstantTimeCompare([]byte(supplied), []byte(a.token)) != 1 {
            w.Header().Set("WWW-Authenticate", "Bearer")
            http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r)
    })
}

func (a *AdminSubsystem) handleAPI(w http.ResponseWriter, r *http.Request) {
    if r.URL.Path != "/api/sessions" { http.NotFound(w, r); return }
    if r.Method != http.MethodGet {
        w.Header().Set("Allow", http.MethodGet)
        http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
        return
    }
    rows, err := a.db.QueryContext(r.Context(),
        `SELECT id, channel, channel_id, created_at, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 50`)
    if err != nil {
        slog.Error("admin: list sessions failed", "err", err)
        http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
        return
    }
    defer func() { _ = rows.Close() }()
    type sessionInfo struct { ID, Channel, ChannelID, CreatedAt, UpdatedAt string }
    sessions := make([]sessionInfo, 0)
    for rows.Next() {
        var s sessionInfo
        if err := rows.Scan(&s.ID, &s.Channel, &s.ChannelID, &s.CreatedAt, &s.UpdatedAt); err != nil {
            slog.Error("admin: scan session failed", "err", err)
            http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
            return
        }
        sessions = append(sessions, s)
    }
    if err := rows.Err(); err != nil {
        slog.Error("admin: iterate sessions failed", "err", err)
        http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
        return
    }
    writeJSON(w, http.StatusOK, sessions)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if err := json.NewEncoder(w).Encode(value); err != nil {
        slog.Warn("admin: encode response failed", "err", err)
    }
}

func (a *AdminSubsystem) Start(context.Context) error {
    if !a.enabled { return nil }
    ln, err := net.Listen("tcp", a.addr)
    if err != nil { return err }
    a.listener = ln
    go func() {
        if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
            slog.Error("admin server failed", "err", err)
        }
    }()
    return nil
}

func (a *AdminSubsystem) Stop(context.Context) error {
    if a.listener == nil { return nil }
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    return a.srv.Shutdown(ctx)
}
```

`handleAPI` must accept only `GET /api/sessions`, query with `QueryContext`, check `rows.Err`, and otherwise return `404` or `405` without routing to the database.

- [ ] **Step 7: Verify GREEN and focused lint**

Run:

```bash
go test ./internal/config ./internal/gateway -run 'TestServer|TestAdmin' -count=1
make lint-new LINT_BASE=HEAD
```

Expected: focused tests PASS; no new admin/config lint findings.

- [ ] **Step 8: Commit**

```bash
git add internal/config internal/gateway
git commit -m "feat: secure the admin HTTP subsystem"
```

---

### Task 4: Wire Admin Lifecycle Through Gateway

**Files:**
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/e2e_lifecycle_test.go`

**Interfaces:**
- Consumes: `InitAdmin` and the `Subsystem` lifecycle from Task 3.
- Produces: `Gateway.admin *AdminSubsystem`; enabled admin starts synchronously and stops through `Subsystems.StopAll`.

- [ ] **Step 1: Write failing lifecycle tests**

Add one test with `Server.Enabled=true`, token set, and address `127.0.0.1:0`; assert `New` wires `gw.admin`, `Start` creates a listener, and `Stop` clears it. Add one construction test proving direct config construction with enabled-but-empty token fails closed.

- [ ] **Step 2: Observe RED**

Run: `go test ./internal/gateway -run 'TestGatewayAdmin|TestGatewayRejectsAdmin' -count=1`

Expected: compile/assertion failure because Gateway has no admin field or wiring.

- [ ] **Step 3: Implement Gateway wiring**

In `New`, call `InitAdmin(cfg.Server, gw.db)` after database initialization and return `fmt.Errorf("admin: %w", err)` on validation failure. Append the admin subsystem to `gw.subsystems` only when enabled. In `Start`, call `gw.admin.Start(ctx)` before channels and return its bind error. Remove the `go startHTTPServer(...)` branch. Ensure `StopAll` owns shutdown exactly once.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./internal/gateway -run 'TestGatewayFullLifecycle|TestGatewayAdmin|TestGatewayRejectsAdmin' -count=1
go test ./internal/gateway -count=1
```

Expected: all gateway tests PASS without leaked listeners.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/gateway.go internal/gateway/e2e_lifecycle_test.go
git commit -m "refactor: manage admin server lifecycle"
```

---

### Task 5: Confirmed Runtime and Archive Safety Findings

**Files:**
- Modify: `internal/hook/hook.go`
- Modify: `internal/hook/hook_test.go`
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/hook_integration_test.go`
- Modify: `internal/soul/archive.go`
- Modify: `internal/soul/soul_test.go`

**Interfaces:**
- `FirePostToolUse` continues calling every handler and returns `errors.Join` of handler failures.
- Tool execution logs a returned post-hook error without changing the tool result.
- Soul import accepts only archive permission modes in `0..0777` and fails closed otherwise.

- [ ] **Step 1: Write failing hook aggregation tests**

Register a failing post handler followed by a successful tracking handler. Assert the tracker still runs and `FirePostToolUse` returns the failing handler's error. In the Agent integration test, assert a post-hook failure does not change a successful tool result or prevent `ToolExecuted` publication.

- [ ] **Step 2: Observe hook RED**

Run: `go test ./internal/hook ./internal/agent -run 'TestPostToolUse.*Error' -count=1`

Expected: FAIL because the manager currently swallows errors and Agent ignores the result.

- [ ] **Step 3: Implement hook error propagation**

Collect errors while continuing through every handler:

```go
var errs []error
for _, h := range m.postToolUse {
    result, err := h.OnPostToolUse(ctx, event)
    if err != nil {
        errs = append(errs, err)
        continue
    }
    if result.ModifiedOutput != nil { finalResult.ModifiedOutput = result.ModifiedOutput }
}
return finalResult, errors.Join(errs...)
```

At the Agent call site, check the error and log `slog.Warn("agent: post-tool hook failed", "tool", tc.Name, "err", err)` without altering `content` or `isError`.

- [ ] **Step 4: Write failing archive mode tests**

Use `sealMalicious` to add regular-file and directory entries with mode `-1` and `1<<40`. Assert import fails with `invalid permission mode` and writes no target entry.

- [ ] **Step 5: Observe archive RED**

Run: `go test ./internal/soul -run TestArchiveRejectsInvalidMode -count=1`

Expected: FAIL because the current conversion truncates the attacker-controlled `int64`.

- [ ] **Step 6: Implement validated archive modes**

Add:

```go
func archiveMode(mode int64) (fs.FileMode, error) {
    if mode < 0 || mode > 0o777 {
        return 0, fmt.Errorf("invalid permission mode %d", mode)
    }
    return fs.FileMode(mode), nil // #nosec G115 -- range checked above
}
```

Resolve the mode before `MkdirAll` and `OpenFile`; propagate the error with the archive entry name. The local `#nosec` documents a proven bound rather than suppressing the rule globally.

- [ ] **Step 7: Verify GREEN and targeted analyzers**

Run:

```bash
go test ./internal/hook ./internal/agent ./internal/soul -count=1
make lint-new LINT_BASE=HEAD
```

Expected: focused tests PASS; the production errcheck finding for `FirePostToolUse` and G115 archive findings are absent.

- [ ] **Step 8: Commit**

```bash
git add internal/hook internal/agent internal/soul
git commit -m "fix: propagate hook errors and validate archive modes"
```

---

### Task 6: Native CGO Packaging and Docker Compose

**Files:**
- Create: `scripts/package-release.sh`
- Create: `scripts/package-release_test.sh`
- Create: `.github/workflows/package.yml`
- Rewrite: `.github/workflows/release.yml`
- Delete: `.goreleaser.yml`
- Modify: `docker-compose.yml`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `GOOS`, `GOARCH`, `VERSION`, `OUTPUT_DIR`, and optionally `CC`.
- Produces: `daimon_<os>_<arch>.tar.gz` containing `daimon`, `LICENSE`, and `README.md`.
- Produces: four CI artifacts and a tag-only release job that publishes them plus `checksums.txt`.

- [ ] **Step 1: Write the failing package script test**

The shell test runs the packaging script for the host `go env GOOS/GOARCH`, extracts the archive, verifies the three required files, executes `daimon version`, and checks the filename.

- [ ] **Step 2: Observe RED**

Run: `bash scripts/package-release_test.sh`

Expected: FAIL because `package-release.sh` does not exist.

- [ ] **Step 3: Implement native packaging**

The script must use `set -euo pipefail`, require all target variables, build with `CGO_ENABLED=1` and `-tags fts5`, stage only the binary/license/readme, and create the archive beneath `OUTPUT_DIR`. It must not modify the working tree or run `go mod tidy`.

Implement it as:

```bash
#!/usr/bin/env bash
set -euo pipefail
: "${GOOS:?GOOS is required}"
: "${GOARCH:?GOARCH is required}"
: "${VERSION:?VERSION is required}"
: "${OUTPUT_DIR:?OUTPUT_DIR is required}"

root=$(git rev-parse --show-toplevel)
output=$(mkdir -p "$OUTPUT_DIR" && cd "$OUTPUT_DIR" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/package"

commit=$(git -C "$root" rev-parse --short HEAD)
ldflags="-s -w -X main.version=$VERSION -X main.commit=$commit -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
(
  cd "$root"
  CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" go build -tags fts5 -trimpath \
    -ldflags "$ldflags" -o "$stage/package/daimon" ./cmd/daimon
)
cp "$root/LICENSE" "$root/README.md" "$stage/package/"
tar -C "$stage/package" -czf "$output/daimon_${GOOS}_${GOARCH}.tar.gz" daimon LICENSE README.md
```

- [ ] **Step 4: Build the snapshot workflow**

Create this exact four-entry matrix:

```yaml
include:
  - runner: ubuntu-24.04
    goos: linux
    goarch: amd64
    cc: x86_64-linux-gnu-gcc
  - runner: ubuntu-24.04
    goos: linux
    goarch: arm64
    cc: aarch64-linux-gnu-gcc
  - runner: macos-14
    goos: darwin
    goarch: amd64
    cc: clang
    cgo_arch: x86_64
  - runner: macos-14
    goos: darwin
    goarch: arm64
    cc: clang
    cgo_arch: arm64
```

Install `gcc-x86-64-linux-gnu` or `gcc-aarch64-linux-gnu` for Linux targets and set `CC`. For Darwin, set `CGO_CFLAGS=-arch ${{ matrix.cgo_arch }}` and `CGO_LDFLAGS=-arch ${{ matrix.cgo_arch }}` before calling the packaging script. Each job uploads one archive. This workflow runs on PRs and pushes to `main` and never publishes.

Rewrite the tag workflow with the same matrix. A final Ubuntu job downloads all archives, runs `sha256sum daimon_*.tar.gz > checksums.txt`, and executes:

```bash
gh release create "$GITHUB_REF_NAME" daimon_*.tar.gz checksums.txt \
  --repo "$GITHUB_REPOSITORY" \
  --title "Daimon $GITHUB_REF_NAME" \
  --generate-notes \
  --verify-tag
```

- [ ] **Step 5: Repair Compose and add validation**

Rename the service/container/volume to `daimon`; mount `./configs/daimon.yaml:/app/configs/daimon.yaml:ro` and `daimon-data:/home/daimon/.daimon`; set `HOME=/home/daimon`. Add `package-test` and `compose-check` Make targets. `compose-check` copies the example config into a temporary Compose override or creates/restores `configs/daimon.yaml` without leaving a tracked change, then runs `docker compose config --quiet`.

- [ ] **Step 6: Verify packaging and workflow syntax**

Run:

```bash
bash scripts/package-release_test.sh
make compose-check
git diff --check
```

Expected: host package test PASS; Compose config PASS; no generated config remains in `git status`.

- [ ] **Step 7: Commit**

```bash
git add scripts/package-release.sh scripts/package-release_test.sh .github/workflows/package.yml .github/workflows/release.yml docker-compose.yml Makefile
git rm .goreleaser.yml
git commit -m "build: package native CGO release artifacts"
```

---

### Task 7: Configuration and Release Documentation

**Files:**
- Modify: `configs/daimon.example.yaml`
- Modify: `README.md`
- Modify: `README_zh.md`
- Modify: `SECURITY.md`
- Modify: `docs/architecture/14-gateway.md`
- Modify: `docs/architecture/20-security-governance.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Documents: `server.addr`, `server.token`, `${DAIMON_ADMIN_TOKEN}`, Docker startup, native release archives, alpha limitations.

- [ ] **Step 1: Update the example configuration**

Use:

```yaml
server:
  addr: "127.0.0.1:8080"
  enabled: false
  token: "${DAIMON_ADMIN_TOKEN}"
```

Explain that the token is required only when enabled and must not be committed.

- [ ] **Step 2: Run the documentation sync skill against the actual diff**

Read `git diff main...HEAD`, then update English and Chinese README sections symmetrically. Document the corrected Docker flow, admin authentication, alpha status, and downloadable archive naming. Update only the two architecture documents directly affected by Gateway/admin security.

- [ ] **Step 3: Add the `0.1.0` changelog entry**

Insert `## [0.1.0] - 2026-07-11` above `Unreleased`, with `Added`, `Changed`, `Fixed`, `Security`, and `Known Limitations`. State explicitly: single-user/local-first, autonomous Heart/Sleep/SelfOps disabled by default, and no exact mid-Episode durable resume.

- [ ] **Step 4: Verify documentation consistency**

Run:

```bash
rg -n 'ironclaw.yaml|container_name: ironclaw|punkopunko' README.md README_zh.md SECURITY.md docs configs docker-compose.yml .github .goreleaser.yml 2>/dev/null && exit 1 || true
rg -n 'DAIMON_ADMIN_TOKEN|127.0.0.1:8080' README.md README_zh.md SECURITY.md configs/daimon.example.yaml
git diff --check
```

Expected: no stale packaging identifiers; admin configuration appears in both languages and security docs; Markdown has no whitespace errors.

- [ ] **Step 5: Commit**

```bash
git add configs/daimon.example.yaml README.md README_zh.md SECURITY.md docs/architecture/14-gateway.md docs/architecture/20-security-governance.md CHANGELOG.md
git commit -m "docs: prepare the v0.1.0 alpha release"
```

---

### Task 8: Completeness Audit and Branch Verification

**Files:**
- Modify only files required to fix findings discovered by this audit.

**Interfaces:**
- Consumes: every commit from Tasks 1–7.
- Produces: a clean committed branch ready for external review; it does not create the release tag.

- [ ] **Step 1: Audit implementation completeness**

Run the implementation-completeness-checker workflow over `main...HEAD`: inspect every changed file, scan for placeholders/stubs, confirm every config field is documented, verify Admin is constructed/started/stopped, and check deleted GoReleaser references are gone.

- [ ] **Step 2: Run focused quality gates**

```bash
bash scripts/lint-new_test.sh
bash scripts/package-release_test.sh
make lint-new LINT_BASE=main
make eval-gate
make compose-check
```

Expected: all exit 0.

- [ ] **Step 3: Run the full verification matrix**

```bash
make build-bin
make vet
make test
```

Expected: build succeeds; vet reports no findings; all CGO/FTS5 race tests pass.

- [ ] **Step 4: Inspect release and repository state**

```bash
git diff --check main...HEAD
git status --short
git log --oneline main..HEAD
```

Expected: no whitespace errors; no untracked or unstaged files; all task commits present.

- [ ] **Step 5: Commit any audit-only corrections**

If the audit required changes, rerun the exact failed verification command, then:

```bash
git add -A
git commit -m "fix: close phase one integration gaps"
```

If no corrections were required, do not create an empty commit.

- [ ] **Step 6: Hand off for review**

Report changed files, verification commands, known historical lint debt, and the post-merge release procedure. Stop before merging, tagging, pushing, or creating a GitHub Release.

---

## Post-Merge Release Procedure

Run only after the reviewed branch has been merged to `main`:

```bash
git switch main
git pull --ff-only
make build-bin
make vet
make test
make eval-gate
git status --short
git tag -a v0.1.0 -m "Daimon v0.1.0"
git push origin v0.1.0
gh run watch --repo Forest-Isle/daimon --exit-status
gh release view v0.1.0 --repo Forest-Isle/daimon
```

Before reporting release completion, verify four archives and `checksums.txt` are attached to the GitHub Release and that each archive name matches `daimon_<os>_<arch>.tar.gz`.
