# Durable Step Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make marked multi-step TaskRuns recoverable at committed step boundaries without replaying uncertain writes or changing legacy scheduler and `/resume` behavior.

**Architecture:** Extend the existing `task_ledger` with normalized versioned plans, immutable step/effect attempts, and CAS-only durable transitions. Keep policy and transactions inside `internal/taskruntime`; let Gateway synchronously reconcile before ingress, then run a bounded dispatcher that rebuilds a fresh Episode from persisted facts and routes every write through a plan-bound effect gate before the existing permission/action/tool pipeline.

**Tech Stack:** Go 1.25.11, `database/sql`, SQLite 3 via `github.com/mattn/go-sqlite3`, SHA-256 canonical JSON, existing Episode/World/Agent runtime, Go subprocess tests.

## Global Constraints

- Work only in `/Users/wuqisen/dev/Daimon/.worktrees/post-release-stabilization`.
- Use migration `internal/store/migrations/044_durable_step_recovery.sql`; migration discovery remains the existing embedded lexical runner.
- A row is durable only when `Metadata.Durable != nil`, `SchemaVersion == 1`, and the metadata plan version equals the joined plan version.
- Keep unmarked `Ledger.Create`, `EnsureScheduledTask`, `MarkRunning`, `Complete`, `Fail`, `Cancel`, `AddEvidence`, `Get`, and `List` behavior compatible, including the scheduler's current call order and terminal-reopen behavior.
- Keep `task_checkpoints`, `Gateway.saveTaskCheckpoint`, and `/resume` unchanged; durable recovery must never read `observations_json`, `plan_json`, or `subtask_index`.
- Persist no resolved secret value, raw credential, raw secret-bearing input, provider response body, or full transcript; store reconstruction references, SHA-256 digests, IDs, and compact redacted summaries only.
- A complete plan, cursor `0`, and attempt `1/pending` are created in one transaction; no partial plan row may survive.
- Every durable state mutation uses compare-and-swap SQL with the expected state/version in `WHERE`; zero affected rows returns `ErrConflict`.
- Completed steps, ordinals, episode IDs, instantiated effects, and existing effect contracts are immutable; amendments only append a descriptor to the cursor or a future step.
- Derive each episode ID as lowercase hex of the full SHA-256 digest of `taskRunID + "\x00" + stepKey`.
- Derive each provider idempotency key as lowercase hex of the full SHA-256 digest of `taskRunID + "\x00" + effectKey`.
- Classify a call as read-only only when resolved `tool.ToolCapabilities.IsReadOnly` is true; missing tools, malformed operations, and undeclared capability contracts fail closed.
- No write reaches permission, hold, action interception, or `Tool.Execute` until its effect key and complete persisted plan contract validate and its `invoking` observation commits.
- All ordinary failure, startup recovery, and operator retry paths call the same attempt-budget transaction helper; every created attempt consumes budget.
- `resolved_committed`, `resolved_skipped`, and `resolved_failed` are final; uncertainty never becomes replay permission on a timer.
- Reconciliation is synchronous and finishes before Admin, health, MCP watchers, hold drain, channels, or Heart start; any invariant error aborts `Gateway.Start` and uses existing rollback.
- Daimon remains single-active-Gateway; distributed leases, active/active dispatch, checkpoint deserialization, and transcript restoration remain out of scope.
- Use TDD for every Go behavior change: run the named focused test and observe RED before implementation, then run it again for GREEN.
- Every task ends in its own commit and must be independently reviewable; do not combine later tasks into an earlier commit.

---

## File Responsibility Map

| File | Responsibility |
|---|---|
| `internal/store/migrations/044_durable_step_recovery.sql` | Add normalized plan, attempt, logical-effect, invocation, and decision tables with enum checks, foreign keys, uniqueness, and recovery indexes. |
| `internal/store/sqlite_test.go` | Prove fresh/upgrade/idempotent migration behavior and preservation of legacy rows. |
| `internal/taskruntime/model.go` | Define durable enums, descriptors, persisted views, inputs, errors, deterministic IDs, canonical digests, and validation. |
| `internal/taskruntime/ledger.go` | Preserve legacy entry points while round-tripping `Metadata.Durable` and delegating marked rows to strict CAS transitions. |
| `internal/taskruntime/metadata_test.go` | Cover durable metadata deep-copy/round-trip and every generic marked/unmarked mutation. |
| `internal/taskruntime/plan.go` | Atomically create/load a TaskRun plan and perform additive versioned effect amendments. |
| `internal/taskruntime/plan_test.go` | Exercise complete-plan validation, ordering, immutability, deterministic IDs, and amendment CAS. |
| `internal/taskruntime/attempt.go` | Claim attempts, apply the shared retry decision, commit step success/cursor advance, and fail a cursor step. |
| `internal/taskruntime/attempt_test.go` | Prove one CAS claimant, atomic cursor transitions, terminal closure, and attempt-budget consistency. |
| `internal/taskruntime/effect.go` | Validate plan-bound writes and atomically prepare/resolve logical Effects and EffectAttempts. |
| `internal/taskruntime/effect_test.go` | Prove mismatch rejection before invocation, stable keys, ordered observations, and resolution races. |
| `internal/taskruntime/decision.go` | Apply retry/skip/mark-failed/cancel operator decisions and evidence atomically. |
| `internal/taskruntime/decision_test.go` | Verify all decisions, duplicate/stale CAS loss, multi-effect behavior, budget exhaustion, and cancel races. |
| `internal/taskruntime/recovery.go` | Validate the full startup matrix and reconcile each durable TaskRun in one transaction. |
| `internal/taskruntime/recovery_test.go` | Table-test every matrix row plus marker/plan corruption and multi-effect recovery policy. |
| `internal/taskruntime/observe.go` | Emit redacted durable transition events and in-process counters through a small observer interface. |
| `internal/agent/cognitive.go` | Add durable request/envelope types without changing existing chat/internal request behavior. |
| `internal/agent/agent.go` | Add `RunDurableEpisode`, using a fresh internal session/request and the existing private `invokeTool` pipeline. |
| `internal/episode/episode.go` | Decode the durable invocation envelope, pass the stable EpisodeID, and keep ordinary Episodes unchanged. |
| `internal/gateway/durable_runtime.go` | Build fresh step prompts from goal, committed summaries, current World retrieval, and coordinate effect gating/World completion. |
| `internal/gateway/durable_dispatcher.go` | Bounded pending-attempt poll/claim/execute loop and shutdown cancellation ownership. |
| `internal/gateway/gateway.go` | Construct the durable runtime; reconcile before all ingress; start dispatcher only after reconciliation. |
| `internal/gateway/durable_runtime_test.go` | Test stable EpisodeID, fresh World reconstruction, effect gate ordering, existing-outcome completion, and dispatcher CAS. |
| `internal/gateway/e2e_lifecycle_test.go` | Prove reconciliation precedes every ingress subsystem and failure rolls startup back. |
| `internal/taskruntime/failpoint.go` | Test-only/environment-controlled process exit at named durability boundaries; inert unless explicitly enabled. |
| `internal/taskruntime/failpoint_test.go` | Child-process restart matrix against one SQLite file with exact row and invocation assertions. |
| `evals/durable_recovery_test.go` | Deterministic recovery score fixture: safe completion, stable episode IDs, zero duplicate effects, unknown-write blocking. |
| `docs/architecture/19-data-layer.md` | Document durable tables, authority split, and atomic boundary. |
| `docs/architecture/04-episode.md` | Document fresh Episode reconstruction, stable IDs, and non-restoration rules. |
| `docs/architecture/14-gateway.md` | Document startup reconciliation and bounded dispatcher ordering. |
| `docs/architecture/21-cli-reference.md` | State that `/resume` remains checkpoint inspection only and is not durable continuation. |
| `docs/SOAK_RUNBOOK.md` | Add rollout, confirmation-queue inspection, metrics, rollback, and no-evidence-deletion checks. |

## Shared Interfaces and Constants

Tasks introduce these signatures in the order shown; later tasks consume only interfaces produced by earlier tasks:

```go
const DurableSchemaVersion = 1

var (
    ErrConflict       = errors.New("taskruntime: compare-and-swap conflict")
    ErrCorrupt        = errors.New("taskruntime: durable state corrupt")
    ErrContract       = errors.New("taskruntime: plan contract violation")
    ErrAwaitingEffect = errors.New("taskruntime: effect awaiting confirmation")
)

type DurableMetadata struct {
    SchemaVersion int `json:"schema_version"`
    PlanVersion   int `json:"plan_version"`
}

type RetryPolicy string
const ( RetryNever RetryPolicy = "never"; RetryTransient RetryPolicy = "transient" )

type RecoveryClass string
const (
    RecoveryReadOnly RecoveryClass = "read_only"
    RecoveryIdempotentWrite RecoveryClass = "idempotent_write"
    RecoveryUnknownNonIdempotent RecoveryClass = "unknown_non_idempotent"
)

type PlanStepState string
const (StepPlanned PlanStepState="planned"; StepRunning PlanStepState="running"; StepSucceeded PlanStepState="succeeded"; StepFailed PlanStepState="failed"; StepCancelled PlanStepState="cancelled")
type StepAttemptState string
const (AttemptPending StepAttemptState="pending"; AttemptRunning StepAttemptState="running"; AttemptSucceeded StepAttemptState="succeeded"; AttemptFailed StepAttemptState="failed"; AttemptInterrupted StepAttemptState="interrupted"; AttemptCancelled StepAttemptState="cancelled")
type EffectState string
const (EffectPrepared EffectState="prepared"; EffectRetrying EffectState="retrying"; EffectUnknown EffectState="unknown"; EffectResolvedCommitted EffectState="resolved_committed"; EffectResolvedFailed EffectState="resolved_failed"; EffectResolvedSkipped EffectState="resolved_skipped")
type EffectAttemptState string
const (EffectInvoking EffectAttemptState="invoking"; EffectCommitted EffectAttemptState="committed"; EffectFailed EffectAttemptState="failed"; EffectAmbiguous EffectAttemptState="ambiguous")

type ClaimedAttempt struct { TaskRunID, AttemptID string; PlanVersion, DescriptorVersion int; Step StepDescriptor }
type AttemptFailureInput struct { TaskRunID, AttemptID string; Failure FailureClass; ResultSummary string }
type CommitStepInput struct { TaskRunID, AttemptID, StepKey, WorldOutcomeRef, ResultSummary string }

func DeriveEpisodeID(taskRunID, stepKey string) string
func DeriveIdempotencyKey(taskRunID, effectKey string) string
func CanonicalDigest(v any) (string, error)

func (l *Ledger) CreateDurableRun(ctx context.Context, in CreateDurableRunInput) (*TaskRun, error)
func (l *Ledger) LoadTaskRun(ctx context.Context, id string) (*TaskRun, error)
func (l *Ledger) AmendPlanEffect(ctx context.Context, in AmendEffectInput) (*TaskRun, error)
func (l *Ledger) ListPendingAttempts(ctx context.Context, limit int) ([]StepAttempt, error)
func (l *Ledger) ClaimAttempt(ctx context.Context, attemptID string) (*ClaimedAttempt, error)
func (l *Ledger) DecideRetry(ctx context.Context, tx *sql.Tx, in RetryDecisionInput) (RetryDecision, error)
func (l *Ledger) RecordAttemptFailure(ctx context.Context, in AttemptFailureInput) (RetryDecision, error)
func (l *Ledger) CommitStep(ctx context.Context, in CommitStepInput) (*TaskRun, error)
func (l *Ledger) FailCursorStep(ctx context.Context, taskRunID, attemptID, reason string) error
func (l *Ledger) PrepareEffect(ctx context.Context, in PrepareEffectInput) (*PreparedEffect, error)
func (l *Ledger) ResolveEffect(ctx context.Context, in ResolveEffectInput) error
func (l *Ledger) ResolveUnknownEffect(ctx context.Context, in OperatorDecisionInput) (*TaskRun, error)
func (l *Ledger) CancelDurableRun(ctx context.Context, in CancelRunInput) error
func (l *Ledger) Reconcile(ctx context.Context) (ReconcileReport, error)
```

---

### Task 1: Migration 044 and Schema Invariants

**Files:**
- Create: `internal/store/migrations/044_durable_step_recovery.sql`
- Modify: `internal/store/sqlite_test.go`

**Interfaces:**
- Consumes: existing `task_ledger(id)` and `undo_journal(receipt_id)`; existing `_migrations` lexical runner.
- Produces: the seven normalized durable tables and indexes named in the spec; no Go API.

- [ ] **Step 1: Write the failing fresh/upgrade schema tests**

Add table-driven tests that open a fresh DB and an upgrade DB containing legacy task/checkpoint rows, then query `sqlite_master`, `PRAGMA foreign_key_list`, and check constraints by executing invalid inserts. The key assertions must be literal:

```go
func TestMigration044FreshAndUpgrade(t *testing.T) {
    for _, upgraded := range []bool{false, true} {
        t.Run(fmt.Sprintf("upgrade=%v", upgraded), func(t *testing.T) {
            path := filepath.Join(t.TempDir(), "durable.db")
            if upgraded { seedPre044Database(t, path) }
            db, err := Open(path)
            if err != nil { t.Fatal(err) }
            defer db.Close()
            for _, table := range []string{"task_run_plans", "task_run_steps", "task_plan_effects", "task_step_attempts", "task_effects", "task_effect_attempts", "task_effect_decisions"} {
                var n int
                if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
                    t.Fatalf("table %s count=%d err=%v", table, n, err)
                }
            }
            if upgraded {
                assertScalar(t, db.DB, `SELECT count(*) FROM task_ledger WHERE id='legacy_task'`, 1)
                assertScalar(t, db.DB, `SELECT count(*) FROM task_checkpoints WHERE id='legacy_cp'`, 1)
            }
            assertScalar(t, db.DB, `SELECT count(*) FROM _migrations WHERE name='044_durable_step_recovery.sql'`, 1)
        })
    }
}
```

Also attempt: cursor greater than total, duplicate ordinal/key/episode, `max_attempts=0`, `retry_policy='never'` with two attempts, incomplete idempotency contract, a second live StepAttempt, and a second invoking EffectAttempt. Each must return a SQLite constraint error.

- [ ] **Step 2: Run the migration test and observe RED**

Run: `go test ./internal/store -run 'TestMigration044' -count=1`

Expected: FAIL because the seven tables do not exist and `_migrations` has no `044_durable_step_recovery.sql` row.

- [ ] **Step 3: Create the additive schema**

Create the migration with these columns and checks (retain the named constraints/indexes so failures are diagnosable):

```sql
CREATE TABLE task_run_plans (
    task_run_id TEXT PRIMARY KEY REFERENCES task_ledger(id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    plan_version INTEGER NOT NULL CHECK (plan_version >= 1),
    total_steps INTEGER NOT NULL CHECK (total_steps >= 1),
    cursor_ordinal INTEGER NOT NULL CHECK (cursor_ordinal >= 0 AND cursor_ordinal <= total_steps),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE task_run_steps (
    task_run_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    step_key TEXT NOT NULL CHECK (length(step_key) > 0),
    descriptor_version INTEGER NOT NULL CHECK (descriptor_version >= 1),
    descriptor_json TEXT NOT NULL CHECK (json_valid(descriptor_json)),
    episode_id TEXT NOT NULL CHECK (length(episode_id) = 64),
    max_attempts INTEGER NOT NULL CHECK (max_attempts >= 1),
    retry_policy TEXT NOT NULL CHECK (retry_policy IN ('never','transient')),
    state TEXT NOT NULL DEFAULT 'planned' CHECK (state IN ('planned','running','succeeded','failed','cancelled')),
    result_summary TEXT NOT NULL DEFAULT '',
    world_outcome_ref TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (task_run_id, step_key),
    UNIQUE (task_run_id, ordinal),
    UNIQUE (task_run_id, episode_id),
    CHECK (retry_policy <> 'never' OR max_attempts = 1),
    FOREIGN KEY (task_run_id) REFERENCES task_run_plans(task_run_id) ON DELETE CASCADE
);

CREATE TABLE task_plan_effects (
    task_run_id TEXT NOT NULL,
    step_key TEXT NOT NULL,
    effect_key TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    tool_name TEXT NOT NULL CHECK (length(tool_name) > 0),
    operation TEXT NOT NULL,
    input_template_digest TEXT NOT NULL CHECK (length(input_template_digest) = 64),
    recovery_class TEXT NOT NULL CHECK (recovery_class IN ('read_only','idempotent_write','unknown_non_idempotent')),
    accepts_idempotency_key INTEGER NOT NULL CHECK (accepts_idempotency_key IN (0,1)),
    idempotency_field TEXT NOT NULL DEFAULT '',
    provider_scope TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (task_run_id, effect_key),
    UNIQUE (task_run_id, step_key, ordinal),
    CHECK ((recovery_class='idempotent_write' AND accepts_idempotency_key=1 AND length(idempotency_field)>0 AND length(provider_scope)>0)
        OR (recovery_class<>'idempotent_write' AND accepts_idempotency_key=0 AND idempotency_field='' AND provider_scope='')),
    FOREIGN KEY (task_run_id, step_key) REFERENCES task_run_steps(task_run_id, step_key) ON DELETE CASCADE
);

CREATE TABLE task_step_attempts (
    id TEXT PRIMARY KEY,
    task_run_id TEXT NOT NULL,
    step_key TEXT NOT NULL,
    plan_version INTEGER NOT NULL CHECK (plan_version >= 1),
    descriptor_version INTEGER NOT NULL CHECK (descriptor_version >= 1),
    attempt_no INTEGER NOT NULL CHECK (attempt_no >= 1),
    state TEXT NOT NULL CHECK (state IN ('pending','running','succeeded','failed','interrupted','cancelled')),
    started_at DATETIME,
    finished_at DATETIME,
    interruption_reason TEXT NOT NULL DEFAULT '',
    result_summary TEXT NOT NULL DEFAULT '',
    world_outcome_ref TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (task_run_id, step_key, attempt_no),
    FOREIGN KEY (task_run_id, step_key) REFERENCES task_run_steps(task_run_id, step_key) ON DELETE CASCADE
);
CREATE UNIQUE INDEX ux_task_step_attempt_live ON task_step_attempts(task_run_id, step_key) WHERE state IN ('pending','running');

CREATE TABLE task_effects (
    id TEXT PRIMARY KEY,
    task_run_id TEXT NOT NULL,
    step_key TEXT NOT NULL,
    effect_key TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    operation TEXT NOT NULL,
    redacted_input_digest TEXT NOT NULL CHECK (length(redacted_input_digest)=64),
    recovery_class TEXT NOT NULL CHECK (recovery_class IN ('idempotent_write','unknown_non_idempotent')),
    idempotency_key TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('prepared','retrying','unknown','resolved_committed','resolved_failed','resolved_skipped')),
    resolution_summary TEXT NOT NULL DEFAULT '',
    undo_receipt_id TEXT REFERENCES undo_journal(receipt_id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (task_run_id, effect_key),
    FOREIGN KEY (task_run_id, effect_key) REFERENCES task_plan_effects(task_run_id, effect_key)
);

CREATE TABLE task_effect_attempts (
    id TEXT PRIMARY KEY,
    effect_id TEXT NOT NULL REFERENCES task_effects(id) ON DELETE CASCADE,
    step_attempt_id TEXT NOT NULL REFERENCES task_step_attempts(id),
    invocation_no INTEGER NOT NULL CHECK (invocation_no >= 1),
    state TEXT NOT NULL CHECK (state IN ('invoking','committed','failed','ambiguous')),
    response_summary TEXT NOT NULL DEFAULT '',
    provider_request_ref TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at DATETIME,
    UNIQUE (effect_id, invocation_no)
);
CREATE UNIQUE INDEX ux_task_effect_attempt_invoking ON task_effect_attempts(effect_id) WHERE state='invoking';

CREATE TABLE task_effect_decisions (
    id TEXT PRIMARY KEY,
    task_run_id TEXT NOT NULL REFERENCES task_run_plans(task_run_id),
    effect_id TEXT NOT NULL REFERENCES task_effects(id),
    kind TEXT NOT NULL CHECK (kind IN ('retry','skip','mark_failed','cancel')),
    actor TEXT NOT NULL CHECK (length(actor)>0),
    reason TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_task_run_steps_cursor ON task_run_steps(task_run_id, ordinal, state);
CREATE INDEX idx_task_plan_effects_step ON task_plan_effects(task_run_id, step_key, ordinal);
CREATE INDEX idx_task_step_attempt_live ON task_step_attempts(state, task_run_id);
CREATE INDEX idx_task_effect_attempt_invoking ON task_effect_attempts(state, effect_id);
CREATE INDEX idx_task_effect_unresolved ON task_effects(task_run_id, state);
CREATE INDEX idx_task_effect_confirmation ON task_effects(state, updated_at) WHERE state='unknown';
```

- [ ] **Step 4: Run schema and full store tests for GREEN**

Run: `go test ./internal/store -count=1`

Expected: PASS; reopening the same DB leaves exactly one migration-044 record and preserves legacy rows.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/044_durable_step_recovery.sql internal/store/sqlite_test.go
git commit -m "feat(taskruntime): add durable recovery schema"
```

---

### Task 2: Durable Domain Types, Validation, and Deterministic IDs

**Files:**
- Create: `internal/taskruntime/model.go`
- Create: `internal/taskruntime/model_test.go`

**Interfaces:**
- Consumes: migration enum strings from Task 1.
- Produces: all shared durable types/constants, `Validate()`, `DeriveEpisodeID`, `DeriveIdempotencyKey`, and `CanonicalDigest` used by every later task.

- [ ] **Step 1: Write failing deterministic-ID and validation tests**

Use literal fixtures so later implementations cannot silently change the identity format:

```go
func TestDeriveStableIDs(t *testing.T) {
    if got := DeriveEpisodeID("run_1", "collect"); got != "be4fc5733f832c393c29d5e827ddd1dc78c587f3df423052aeb194a7da9f083b" {
        t.Fatalf("episode id=%s", got)
    }
    if got := DeriveIdempotencyKey("run_1", "send_1"); got != "92f9ae25701ba72ea2fd176eddb935d4eff8b707ed0b0c4e39ec4a6d69e4c7d9" {
        t.Fatalf("idempotency key=%s", got)
    }
}

func TestStepDescriptorValidate(t *testing.T) {
    valid := StepDescriptor{Version: 1, StepKey: "collect", Ordinal: 0, EpisodeID: DeriveEpisodeID("run_1", "collect"), MaxAttempts: 2, RetryPolicy: RetryTransient, InputTemplate: json.RawMessage(`{"query":"weather","token":{"$secret":"WEATHER_TOKEN"}}`)}
    cases := []struct{name string; mutate func(*StepDescriptor)}{
        {"zero max", func(s *StepDescriptor){ s.MaxAttempts=0 }},
        {"never multiple", func(s *StepDescriptor){ s.RetryPolicy=RetryNever; s.MaxAttempts=2 }},
        {"wrong episode", func(s *StepDescriptor){ s.EpisodeID="bad" }},
        {"unsupported version", func(s *StepDescriptor){ s.Version=2 }},
    }
    for _, tc := range cases { t.Run(tc.name, func(t *testing.T) { got:=valid; tc.mutate(&got); if err:=got.Validate("run_1"); err==nil { t.Fatal("expected validation error") } }) }
}
```

Compute and verify the two literal hashes with `printf 'run_1\0collect' | shasum -a 256` and `printf 'run_1\0send_1' | shasum -a 256`; if the displayed values differ, use the command output as the test literal before committing.

- [ ] **Step 2: Run focused tests and observe RED**

Run: `go test ./internal/taskruntime -run 'TestDeriveStableIDs|TestStepDescriptorValidate' -count=1`

Expected: FAIL to compile because durable types and functions do not exist.

- [ ] **Step 3: Implement exact types and validators**

Define the model without database behavior:

```go
type StepDescriptor struct {
    Version int `json:"version"`
    StepKey string `json:"step_key"`
    Ordinal int `json:"ordinal"`
    EpisodeID string `json:"episode_id"`
    MaxAttempts int `json:"max_attempts"`
    RetryPolicy RetryPolicy `json:"retry_policy"`
    InputTemplate json.RawMessage `json:"input_template"`
    Effects []LogicalEffectDescriptor `json:"effects"`
}
type LogicalEffectDescriptor struct {
    EffectKey string `json:"effect_key"`
    ToolName string `json:"tool_name"`
    Operation string `json:"operation"`
    InputTemplateDigest string `json:"input_template_digest"`
    RecoveryClass RecoveryClass `json:"recovery_class"`
    Idempotency IdempotencyContract `json:"idempotency"`
}
type IdempotencyContract struct {
    AcceptsCallerKey bool `json:"accepts_caller_key"`
    Field string `json:"field,omitempty"`
    ProviderScope string `json:"provider_scope,omitempty"`
}
type CreateDurableRunInput struct { CreateInput CreateInput; PlanVersion int; Steps []StepDescriptor }
type AmendEffectInput struct { TaskRunID, StepKey string; ExpectedPlanVersion, ExpectedDescriptorVersion int; Effect LogicalEffectDescriptor; UpdatedTemplate json.RawMessage }
type TaskRun struct { Entry Entry; SchemaVersion, PlanVersion, TotalSteps, CursorOrdinal int; Steps []PlanStep; Attempts []StepAttempt; Effects []Effect }
```

Canonicalize JSON by recursively sorting object keys, preserving array order, encoding numbers with `json.Decoder.UseNumber`, and hashing the compact bytes. `StepDescriptor.Validate(taskRunID)` must reject unsupported version, mismatched ordinal/key/episode ID, invalid retry policy/budget, invalid JSON template, duplicate effect keys/ordinals, wrong digest, and incomplete/extra idempotency fields.

- [ ] **Step 4: Run model tests for GREEN**

Run: `go test ./internal/taskruntime -run 'TestDerive|TestCanonical|TestStepDescriptor|TestLogicalEffect' -count=1`

Expected: PASS, including equal digests for JSON objects with different key order and unequal digests when a secret-reference name changes.

- [ ] **Step 5: Commit**

```bash
git add internal/taskruntime/model.go internal/taskruntime/model_test.go
git commit -m "feat(taskruntime): define durable recovery model"
```

---

### Task 3: Metadata Round-Trip and Strict Marked Legacy Entry Points

**Files:**
- Modify: `internal/taskruntime/ledger.go`
- Create: `internal/taskruntime/metadata_test.go`
- Modify: `internal/taskruntime/ledger_test.go`

**Interfaces:**
- Consumes: `DurableMetadata`, `ErrConflict`, and durable state constants from Task 2.
- Produces: `Metadata.Durable *DurableMetadata`, deep-copy merge semantics, and marked-row CAS delegation while preserving all unmarked SQL paths.

- [ ] **Step 1: Write failing separate-fixture round-trip and compatibility tests**

Create one marked fixture per mutation so terminal-state tests do not depend on execution order. Assert `Create/Get/List/MarkRunning/AddEvidence/Complete/Fail/Cancel` preserve `{1,1}`; assert nil override preserves it and a non-nil override is copied. Keep the existing scheduler test, and add:

```go
func TestLegacyTerminalReopenRemainsCompatible(t *testing.T) {
    l := NewLedger(openTaskRuntimeTestDB(t).DB)
    e, _ := l.Create(context.Background(), CreateInput{ID:"legacy", Title:"legacy"})
    if err:=l.Complete(context.Background(), e.ID, "done"); err!=nil { t.Fatal(err) }
    if err:=l.MarkRunning(context.Background(), e.ID, Metadata{}, "legacy reopen"); err!=nil { t.Fatal(err) }
    got, _ := l.Get(context.Background(), e.ID)
    if got.State != StateRunning { t.Fatalf("legacy state=%s", got.State) }
}

func TestMarkedTerminalCannotReopen(t *testing.T) {
    l, id := createMarkedFixture(t, StateSucceeded)
    err := l.MarkRunning(context.Background(), id, Metadata{}, "stale")
    if !errors.Is(err, ErrConflict) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: Run metadata tests and observe RED**

Run: `go test ./internal/taskruntime -run 'TestMetadataDurable|TestMarked|TestLegacyTerminal' -count=1`

Expected: FAIL because `Metadata` has no typed durable marker and marked rows do not use CAS.

- [ ] **Step 3: Add typed marker and branch only marked calls**

Add:

```go
type Metadata struct {
    // existing fields stay byte-for-byte in their current order
    Durable *DurableMetadata `json:"durable,omitempty"`
}

func cloneDurable(in *DurableMetadata) *DurableMetadata {
    if in == nil { return nil }
    out := *in
    return &out
}
```

`mergeMetadata` must preserve `base.Durable` when override is nil and validate/copy it when non-nil. `scanEntry` must return a JSON decode error instead of silently dropping malformed metadata for a row that appears durable; preserve the current permissive behavior for blank/legacy JSON. Before each generic mutation, load the entry once: when `Durable == nil`, execute the existing SQL unchanged; when marked, call private `markDurableRunningCAS`/`finishDurableCAS` whose allowed sources match the state diagram and whose SQL includes `WHERE id=? AND state=?`. `AddEvidence` uses `WHERE id=? AND metadata=?` for marked rows so concurrent JSON updates cannot lose the marker.

- [ ] **Step 4: Verify marked strictness and legacy compatibility for GREEN**

Run:

```bash
go test ./internal/taskruntime -count=1
go test ./internal/gateway -run 'TestCommands|TestResume|TestScheduler' -count=1
```

Expected: PASS; `/resume` and scheduler tests remain unchanged, marked terminal reopen returns `ErrConflict`, unmarked reopen still succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/taskruntime/ledger.go internal/taskruntime/ledger_test.go internal/taskruntime/metadata_test.go
git commit -m "feat(taskruntime): preserve durable metadata and strict transitions"
```

---

### Task 4: Atomic Plan Creation, Loading, and Additive Amendment

**Files:**
- Create: `internal/taskruntime/plan.go`
- Create: `internal/taskruntime/plan_test.go`

**Interfaces:**
- Consumes: validated Task 2 descriptors, Task 1 tables, and Task 3 metadata marker.
- Produces: `CreateDurableRun`, `LoadTaskRun`, and `AmendPlanEffect` with immutable completed/history fields.

- [ ] **Step 1: Write failing creation/load/amendment tests**

Cover: complete ordered creation; all rows roll back on invalid final step; duplicate ordinal/key/effect; marker/plan mismatch; first attempt captured version; amendment adds exactly one effect, increments plan/descriptor/metadata versions together, updates the live cursor attempt versions, rejects completed steps/existing keys/stale versions, and exposes the amendment after reload.

```go
func TestCreateDurableRunIsAtomic(t *testing.T) {
    l := newLedger(t)
    in := validTwoStepRun("run_atomic")
    in.Steps[1].EpisodeID = "wrong"
    if _, err := l.CreateDurableRun(context.Background(), in); err == nil { t.Fatal("expected validation error") }
    assertCount(t, l.db, `SELECT count(*) FROM task_ledger WHERE id=?`, 0, "run_atomic")
    assertCount(t, l.db, `SELECT count(*) FROM task_run_plans WHERE task_run_id=?`, 0, "run_atomic")
    assertCount(t, l.db, `SELECT count(*) FROM task_step_attempts WHERE task_run_id=?`, 0, "run_atomic")
}
```

- [ ] **Step 2: Run plan tests and observe RED**

Run: `go test ./internal/taskruntime -run 'TestCreateDurableRun|TestLoadTaskRun|TestAmendPlanEffect' -count=1`

Expected: FAIL to compile because plan APIs do not exist.

- [ ] **Step 3: Implement one-transaction creation and strict loading**

`CreateDurableRun` must validate all descriptors before `BeginTx`, set `CreateInput.Metadata.Durable={1, PlanVersion}`, insert `task_ledger`, plan header, every step/effect row, and attempt `1/pending`, then commit. Do not call `Ledger.Create`, which owns its own transaction. Use UUID-prefixed IDs (`attempt_`, later `effect_`, `invoke_`, `decision_`).

`LoadTaskRun` must join the marker/header, load steps ordered by ordinal, effects ordered by step/ordinal, and attempt/effect history ordered by number. It returns `ErrCorrupt` for missing marker/header, version disagreement, cursor bounds, missing ordinal, descriptor JSON/normalized-row disagreement, episode mismatch, or live-attempt contradiction.

- [ ] **Step 4: Implement amendment CAS in one transaction**

Use this transaction order: load plan/step with expected versions; reject `succeeded/failed/cancelled`; verify no effect with the key exists in normalized or descriptor JSON; validate updated canonical template/digest/contract; insert `task_plan_effects`; update descriptor JSON/version with `WHERE descriptor_version=?`; update plan with `WHERE plan_version=?`; update metadata JSON with old marker bytes in `WHERE`; update any `pending/running` cursor attempt captured versions; commit. A zero-row update returns `ErrConflict` and rolls back the inserted normalized descriptor.

- [ ] **Step 5: Run plan tests for GREEN**

Run: `go test ./internal/taskruntime -run 'TestCreateDurableRun|TestLoadTaskRun|TestAmendPlanEffect' -count=1`

Expected: PASS; `PRAGMA foreign_key_check` returns no rows after every success and rejected amendment.

- [ ] **Step 6: Commit**

```bash
git add internal/taskruntime/plan.go internal/taskruntime/plan_test.go
git commit -m "feat(taskruntime): persist versioned durable plans"
```

---

### Task 5: Attempt Claim, Shared Retry Budget, and Atomic Step Commit

**Files:**
- Create: `internal/taskruntime/attempt.go`
- Create: `internal/taskruntime/attempt_test.go`

**Interfaces:**
- Consumes: `TaskRun`, plan state, and attempt rows from Tasks 1–4.
- Produces: `ListPendingAttempts`, `ClaimAttempt`, transaction-scoped `DecideRetry`, `RecordAttemptFailure`, `CommitStep`, and `FailCursorStep`.

- [ ] **Step 1: Write failing CAS, budget, and commit tests**

Run two goroutines against the same attempt; exactly one claim returns a `ClaimedAttempt`, the other returns `ErrConflict`. Table-test `source=ordinary|startup|operator` with identical starting rows and compare the resulting attempt/run/step states. Verify attempt 3 is never created when `max_attempts=2`.

```go
func TestCommitStepAdvancesAtomically(t *testing.T) {
    l, claim := claimedTwoStepRun(t)
    in := CommitStepInput{TaskRunID:claim.TaskRunID, AttemptID:claim.AttemptID, StepKey:claim.Step.StepKey, WorldOutcomeRef:"journal_outcome_"+claim.Step.EpisodeID, ResultSummary:"collected"}
    got, err := l.CommitStep(context.Background(), in)
    if err != nil { t.Fatal(err) }
    if got.CursorOrdinal != 1 || got.Entry.State != StateRunning { t.Fatalf("run=%#v", got) }
    assertCount(t, l.db, `SELECT count(*) FROM task_step_attempts WHERE task_run_id=? AND step_key=? AND state='pending'`, 1, got.Entry.ID, got.Steps[1].StepKey)
}
```

- [ ] **Step 2: Run attempt tests and observe RED**

Run: `go test ./internal/taskruntime -run 'TestClaimAttempt|TestRetryDecision|TestCommitStep|TestAttemptBudget' -count=1`

Expected: FAIL to compile because attempt APIs do not exist.

- [ ] **Step 3: Implement conditional claim and shared retry decision**

Define:

```go
type FailureClass string
const (
    FailureTransientModel FailureClass = "transient_model"
    FailureTransientRead FailureClass = "transient_read"
    FailureInterruptedSafe FailureClass = "interrupted_safe"
    FailureTerminal FailureClass = "terminal"
)
type RetryDecisionInput struct { TaskRunID, StepKey string; Failure FailureClass; Source string; ExpectedRunState State }
type RetryDecision struct { CreatedAttemptID string; AttemptNo int; Exhausted bool }
```

`DecideRetry` is the only function allowed to insert a non-initial StepAttempt. Inside the caller's transaction, count all historical attempts, reject unknown/failed/non-terminal effects, require transient policy plus an allowed failure class, and either insert `next/pending` or atomically mark step/run failed. `ClaimAttempt` updates attempt `pending→running`, step `planned→running` (or keeps `running` on recovery), and run `pending|recovering→running` in the same transaction.

- [ ] **Step 4: Implement atomic step commit**

`CommitStep` must require: attempt `running`, cursor points to its step, the attempt's captured versions match current plan/descriptor, non-empty World reference, and every declared instantiated write Effect is `resolved_committed` or `resolved_skipped`. In one transaction set attempt/step succeeded, store compact references, increment cursor, then either create the next attempt through the same internal insertion primitive or set cursor `total_steps` and run `succeeded`. Inject a transaction callback in tests and prove rollback at each statement leaves all pre-state intact.

- [ ] **Step 5: Run attempt and race tests for GREEN**

Run:

```bash
go test ./internal/taskruntime -run 'TestClaimAttempt|TestRetryDecision|TestCommitStep|TestAttemptBudget' -count=20
go test -race ./internal/taskruntime -run 'TestClaimAttemptExactlyOneWinner' -count=1
```

Expected: PASS; race test reports no data race and exactly one CAS winner.

- [ ] **Step 6: Commit**

```bash
git add internal/taskruntime/attempt.go internal/taskruntime/attempt_test.go
git commit -m "feat(taskruntime): add durable attempt transactions"
```

---

### Task 6: Plan-Bound Effect Validation and Invocation Observations

**Files:**
- Create: `internal/taskruntime/effect.go`
- Create: `internal/taskruntime/effect_test.go`

**Interfaces:**
- Consumes: current cursor/attempt versions from Task 5 and descriptor/canonicalization APIs from Tasks 2/4.
- Produces: `PrepareEffect`, `ResolveEffect`, persisted-resolution lookup, and `EffectCapability` verification input.

- [ ] **Step 1: Write failing validation and invocation-history tests**

For each mismatch—absent key, wrong step, tool, normalized operation, template digest, rendered canonical input, recovery class, key field, provider scope, and unverified capabilities—call `PrepareEffect` and assert the fake final tool counter stays zero and the cursor step becomes failed with `plan_contract_violation`. Add success cases for committed/skipped short-circuit and idempotent retry reusing one logical row/key with invocation numbers 1 and 2.

```go
func TestPrepareEffectPersistsBeforePermission(t *testing.T) {
    l, claim, input := claimedWriteRun(t, RecoveryIdempotentWrite)
    got, err := l.PrepareEffect(context.Background(), PrepareEffectInput{
        TaskRunID:claim.TaskRunID, StepAttemptID:claim.AttemptID, EffectKey:"publish", ToolName:"http", Operation:"POST",
        CanonicalInput:input, RedactedInputDigest:mustDigest(t, input),
        Capability:EffectCapability{IsReadOnly:false, AcceptsCallerKey:true, IdempotencyField:"headers.Idempotency-Key", ProviderScope:"api.example.test"},
    })
    if err != nil { t.Fatal(err) }
    assertCount(t, l.db, `SELECT count(*) FROM task_effect_attempts WHERE effect_id=? AND state='invoking'`, 1, got.EffectID)
    if got.IdempotencyKey != DeriveIdempotencyKey(claim.TaskRunID, "publish") { t.Fatalf("key=%s", got.IdempotencyKey) }
}
```

- [ ] **Step 2: Run effect tests and observe RED**

Run: `go test ./internal/taskruntime -run 'TestPrepareEffect|TestResolveEffect|TestEffectContract|TestEffectHistory' -count=1`

Expected: FAIL to compile because effect APIs do not exist.

- [ ] **Step 3: Implement complete pre-invocation validation**

Define exact inputs/results:

```go
type EffectCapability struct { IsReadOnly, AcceptsCallerKey bool; IdempotencyField, ProviderScope string }
type PrepareEffectInput struct { TaskRunID, StepAttemptID, EffectKey, ToolName, Operation string; CanonicalInput, RenderedTemplate json.RawMessage; RedactedInputDigest string; Capability EffectCapability }
type PreparedEffect struct { EffectID, EffectAttemptID, IdempotencyKey string; InvocationNo int; ExistingResolution *EffectResolution }
type ResolveEffectInput struct { EffectID, EffectAttemptID string; Outcome EffectAttemptState; ExpectedEffectState EffectState; ResponseSummary, ProviderRequestRef, UndoReceiptID string }
```

Load the cursor descriptor at the attempt's captured versions and validate all five plan-bound conditions before opening an invocation transaction. Read-only calls return a marker that creates no Effect rows. A committed/skipped existing Effect returns its stored resolution. An unknown Effect returns `ErrAwaitingEffect`. A new write transaction inserts one logical Effect in `prepared` and attempt 1 in `invoking`; a retry requires `retrying` and inserts the next invocation. Re-render only from an allowlisted map of reconstruction fields plus a secret resolver callback; compute/persist only the redacted digest and never retain the resolver's values.

- [ ] **Step 4: Implement conditional resolution**

For definitive success, CAS invocation `invoking→committed` and Effect `prepared|retrying→resolved_committed`; definitive failure uses `failed/resolved_failed`; ambiguous uses `ambiguous/unknown`. Both updates occur in one transaction. Store summaries truncated to 500 runes and provider references truncated to 200 runes. A stale response after cancellation must update zero rows and return `ErrConflict`.

- [ ] **Step 5: Verify effect tests and resolution race for GREEN**

Run:

```bash
go test ./internal/taskruntime -run 'TestPrepareEffect|TestResolveEffect|TestEffectContract|TestEffectHistory' -count=20
go test -race ./internal/taskruntime -run TestResolveEffectExactlyOneWinner -count=1
```

Expected: PASS; only one terminal resolution wins, logical-effect count remains one, invocation history is ordered, and rejected contracts create no invocation row.

- [ ] **Step 6: Commit**

```bash
git add internal/taskruntime/effect.go internal/taskruntime/effect_test.go
git commit -m "feat(taskruntime): gate and record durable effects"
```

---

### Task 7: Atomic Operator Resolution and Cancellation

**Files:**
- Create: `internal/taskruntime/decision.go`
- Create: `internal/taskruntime/decision_test.go`

**Interfaces:**
- Consumes: Task 5 shared retry decision and Task 6 effect state machine.
- Produces: `ResolveUnknownEffect` and `CancelDurableRun`; immutable decision rows are the operator audit surface.

- [ ] **Step 1: Write failing retry/skip/fail/cancel table tests**

Assert exact rows for one and multiple unknown effects, one failed sibling, exhausted budget, duplicate command, and cancellation racing a late response. The cancellation success assertion is:

```go
assertCount(t, db, `SELECT count(*) FROM task_step_attempts WHERE task_run_id=? AND state IN ('pending','running')`, 0, runID)
assertCount(t, db, `SELECT count(*) FROM task_effect_attempts ea JOIN task_effects e ON e.id=ea.effect_id WHERE e.task_run_id=? AND ea.state='invoking'`, 0, runID)
assertCount(t, db, `SELECT count(*) FROM task_effects WHERE task_run_id=? AND state NOT IN ('resolved_committed','resolved_skipped','resolved_failed')`, 0, runID)
assertCount(t, db, `SELECT count(*) FROM task_effect_decisions WHERE task_run_id=? AND kind='cancel'`, expectedUnresolvedEffects, runID)
```

- [ ] **Step 2: Run decision tests and observe RED**

Run: `go test ./internal/taskruntime -run 'TestOperator|TestCancelDurable' -count=1`

Expected: FAIL to compile because operator APIs do not exist.

- [ ] **Step 3: Implement retry, skip, and mark-failed transactions**

Define:

```go
type DecisionKind string
const (DecisionRetry DecisionKind="retry"; DecisionSkip DecisionKind="skip"; DecisionMarkFailed DecisionKind="mark_failed")
type OperatorDecisionInput struct { TaskRunID, EffectID string; Kind DecisionKind; Actor, RedactedReason string }
type CancelRunInput struct { TaskRunID, Actor, RedactedReason string }
```

Within one transaction insert the decision only after locking/reading an `unknown` Effect, then CAS it to `retrying`, `resolved_skipped`, or `resolved_failed`. After retry/skip, query all cursor-step Effects: preserve awaiting state if any unknown remains; fail if any resolved-failed exists; otherwise call Task 5's transaction-scoped `DecideRetry` and CAS `awaiting_confirmation→recovering`. Mark-failed immediately fails step/run and creates no attempt. Any zero-row CAS rolls back the decision insert.

- [ ] **Step 4: Implement cancellation as one evidence-preserving transaction**

The caller cancels the in-memory executor context first. The ledger transaction then: closes every cursor `invoking→ambiguous`; inserts one cancel decision for every `prepared/retrying/unknown` Effect; CASes each to `resolved_failed` with `cancelled_external_unknown`; cancels live StepAttempts and cursor step; CASes the run from a non-terminal durable state to `cancelled`. `PrepareEffect` already requires run/attempt both running, so an invocation cannot begin after cancellation wins.

- [ ] **Step 5: Verify decisions and races for GREEN**

Run:

```bash
go test ./internal/taskruntime -run 'TestOperator|TestCancelDurable' -count=20
go test -race ./internal/taskruntime -run 'TestOperatorDuplicateCAS|TestCancelResolutionRace' -count=1
```

Expected: PASS; duplicate/stale commands leave no partial decision row, and cancellation leaves no live attempt/invocation/non-terminal Effect.

- [ ] **Step 6: Commit**

```bash
git add internal/taskruntime/decision.go internal/taskruntime/decision_test.go
git commit -m "feat(taskruntime): persist operator effect decisions"
```

---

### Task 8: Full Startup Reconciliation Matrix

**Files:**
- Create: `internal/taskruntime/recovery.go`
- Create: `internal/taskruntime/recovery_test.go`

**Interfaces:**
- Consumes: loaders/validators from Task 4, shared retry helper from Task 5, and effect transitions from Tasks 6–7.
- Produces: `Reconcile(ctx) (ReconcileReport, error)`; it handles only supported marked rows and commits one TaskRun per transaction.

- [ ] **Step 1: Write the failing complete matrix table**

Create a table with one case for every row in the spec, including legacy exclusion, terminal/no-live preservation, terminal/live corruption, pending preservation, pending with unknown/failed corruption, running interruption, pending/running corruption, missing-live reconstruction, unknown→awaiting, failed→run failed, all-steps finalization, awaiting preservation, and every awaiting contradiction. Each case declares exact final run/attempt/effect states and whether `ErrCorrupt` is required.

```go
type recoveryCase struct { name string; seed func(*testing.T,*Ledger) string; wantRun State; wantAttempts map[StepAttemptState]int; wantEffects map[EffectState]int; wantErr error }
```

Add multi-effect cases proving any unknown blocks, any failed fails, safe idempotent work moves to retrying, and exhausted budget resolves remaining prepared/retrying effects as `attempt_budget_exhausted` before failing.

- [ ] **Step 2: Run matrix tests and observe RED**

Run: `go test ./internal/taskruntime -run 'TestReconcileMatrix|TestReconcileMultipleEffects' -count=1`

Expected: FAIL to compile because `Reconcile` does not exist.

- [ ] **Step 3: Implement strict marker scan and per-run transaction**

Scan `task_ledger.metadata` only to identify `durable.schema_version=1`; ignore every unmarked row without joining durable tables. For each marked ID, begin a transaction, load/validate the entire joined TaskRun, then apply exactly one matrix branch. A running attempt is first CASed to interrupted and all invoking observations to ambiguous. Classify each abandoned Effect: verified idempotent becomes/remains retrying; unknown non-idempotent becomes unknown; resolved remains unchanged. Finish with Task 5's shared retry helper when no unknown/failed Effect blocks.

- [ ] **Step 4: Make reconciliation reports explicit and redacted**

```go
type ReconcileReport struct { Scanned, Preserved, Interrupted, Requeued, AwaitingConfirmation, Failed, Finalized int }
```

Return the first corruption/invariant/database error with task ID and state names but no descriptor JSON or tool input. Transaction rollback must preserve that TaskRun's pre-scan state. Already committed earlier TaskRuns remain committed; rerunning reconciliation must be idempotent.

- [ ] **Step 5: Run matrix repeatedly for GREEN**

Run:

```bash
go test ./internal/taskruntime -run 'TestReconcileMatrix|TestReconcileMultipleEffects|TestReconcileIdempotent' -count=20
go test -race ./internal/taskruntime -run TestReconcilePendingClaimRace -count=1
```

Expected: PASS; pending work is never duplicated, terminal runs never reopen, and every stale invocation has a durable disposition.

- [ ] **Step 6: Commit**

```bash
git add internal/taskruntime/recovery.go internal/taskruntime/recovery_test.go
git commit -m "feat(taskruntime): reconcile durable runs at startup"
```

---

### Task 9: Redacted Observability for Durable Transitions

**Files:**
- Create: `internal/taskruntime/observe.go`
- Create: `internal/taskruntime/observe_test.go`
- Modify: `internal/taskruntime/ledger.go`
- Modify: `internal/taskruntime/attempt.go`
- Modify: `internal/taskruntime/effect.go`
- Modify: `internal/taskruntime/decision.go`
- Modify: `internal/taskruntime/recovery.go`

**Interfaces:**
- Consumes: transition points implemented in Tasks 3–8.
- Produces: optional `Observer`, stable event names, and snapshot counters; a nil observer changes no behavior.

- [ ] **Step 1: Write failing event/counter/redaction tests**

Use a recording observer and a secret-bearing rendered input. Assert events include IDs/state/class but do not contain the secret or raw provider response. Assert counters distinguish `recovered_tasks`, `invocation_retries`, `tasks_awaiting_confirmation`, `decisions_retry|skip|mark_failed|cancel`, `duplicate_effects`, and `recovery_failures`.

- [ ] **Step 2: Run observer tests and observe RED**

Run: `go test ./internal/taskruntime -run TestDurableObserver -count=1`

Expected: FAIL because observer APIs do not exist.

- [ ] **Step 3: Implement the small observer boundary**

```go
type DurableEvent struct { Name, TaskRunID, StepKey, AttemptID, EffectID, From, To, RecoveryClass, Decision string }
type Observer interface { ObserveDurable(DurableEvent) }
type Counters struct { RecoveredTasks, InvocationRetries, AwaitingConfirmation, DecisionRetry, DecisionSkip, DecisionMarkFailed, DecisionCancel, DuplicateEffects, RecoveryFailures atomic.Uint64 }
func (l *Ledger) SetObserver(o Observer)
func (l *Ledger) CounterSnapshot() map[string]uint64
```

Emit only after a transaction commits; collect events during the transaction and publish afterward. Use `slog.Info` in the default observer with the same fields. Never include canonical input, template JSON, response text, actor reason, or secret reference resolution.

- [ ] **Step 4: Run observer and taskruntime suites for GREEN**

Run: `go test ./internal/taskruntime -count=1`

Expected: PASS; redaction test finds no fixture secret in captured events/logs.

- [ ] **Step 5: Commit**

```bash
git add internal/taskruntime/observe.go internal/taskruntime/observe_test.go internal/taskruntime/ledger.go internal/taskruntime/attempt.go internal/taskruntime/effect.go internal/taskruntime/decision.go internal/taskruntime/recovery.go
git commit -m "feat(taskruntime): observe durable recovery transitions"
```

---

### Task 10: Durable Episode Envelope and Fresh Agent Execution

**Files:**
- Modify: `internal/agent/cognitive.go`
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/integration_test.go`
- Modify: `internal/episode/episode.go`
- Modify: `internal/episode/episode_test.go`

**Interfaces:**
- Consumes: existing `Agent.invokeTool` and `Runner.Execute`; no taskruntime import is introduced into agent or episode.
- Produces: `DurableEpisodeRequest`, `DurableInvocationEnvelope`, `DurableInvokeFunc`, `GovernedToolExecuteFunc`, and `Agent.RunDurableEpisode` while leaving ordinary `ToolInvokeFunc` unchanged.

- [ ] **Step 1: Write failing envelope and stable-ID tests**

Assert durable tool schemas wrap business input as `{effect_key,input}`, the Runner strips only that wrapper, calls the durable callback, and never sends `effect_key` into the business tool. Assert malformed/missing envelope returns a tool error without calling the durable callback. Execute twice with the same EpisodeID and prove the provider is skipped when World already contains the outcome.

```go
type durableInput struct { EffectKey string `json:"effect_key"`; Input json.RawMessage `json:"input"` }
```

- [ ] **Step 2: Run agent/episode tests and observe RED**

Run: `go test ./internal/agent ./internal/episode -run 'TestDurableEpisode|TestDurableEnvelope' -count=1`

Expected: FAIL to compile because durable request types do not exist.

- [ ] **Step 3: Add non-breaking durable request types**

```go
type DurableInvocationEnvelope struct { EffectKey string; BusinessInput json.RawMessage }
type DurableToolResult struct { Output string; IsError bool; Metadata map[string]string; TransportErr error }
type GovernedToolExecuteFunc func(ctx context.Context, iteration int, call mind.ToolUseBlock) DurableToolResult
var ErrDurableEpisodeSuspended = errors.New("durable episode suspended before world outcome")
type DurableInvokeFunc func(ctx context.Context, iteration int, call mind.ToolUseBlock, envelope DurableInvocationEnvelope, execute GovernedToolExecuteFunc) (string, bool, error)
type DurableEpisodeRequest struct { EpisodeID, Goal, Trigger, ActivityClass string; Memories string; ToolDefs []mind.ToolDefinition; Invoke DurableInvokeFunc }
```

Add optional `DurableInvoke func(context.Context,int,mind.ToolUseBlock,DurableInvocationEnvelope)(string,bool,error)` to `CognitiveRequest`; this is the already-bound callback Runner needs, while the richer `DurableEpisodeRequest.Invoke` stays at the Agent boundary. When non-nil, Runner decodes the wrapper, replaces `call.Input` with compact business JSON, and calls `DurableInvoke`; otherwise it uses existing `Invoke` exactly as today. If the callback returns `ErrDurableEpisodeSuspended`, return a blocked `CognitiveOutcome` immediately without calling `close` or `failEpisode`, so no World outcome is minted for an unknown/failed effect. Wrap durable tool definitions without mutating the registry's original schema.

- [ ] **Step 4: Implement `RunDurableEpisode` through the existing pipeline**

```go
func (a *Agent) RunDurableEpisode(ctx context.Context, in DurableEpisodeRequest) (CognitiveOutcome, error)
```

Create/get the deterministic internal session by concatenating `"durable_"+in.EpisodeID`, construct a fresh `CognitiveRequest` with no restored transcript (only one current trigger message), use supplied fresh memories and the stable EpisodeID. Refactor `invokeTool` into a behavior-identical `invokeToolDetailed` that returns output, error flag, result metadata, and interceptor transport error; keep `invokeTool` as the existing two-value wrapper. Bind `CognitiveRequest.DurableInvoke` so Gateway's callback receives a `GovernedToolExecuteFunc` backed by `invokeToolDetailed`. This lets Gateway prepare the invocation before calling `execute` and persist `receipt_id` afterward while permission/hooks/action/undo behavior stays shared. Ordinary `runKernel` and `RunInternalEpisode` remain unchanged.

- [ ] **Step 5: Verify ordinary and durable Episodes for GREEN**

Run:

```bash
go test ./internal/agent ./internal/episode -count=1
go test ./internal/gateway -run TestChatHeart -count=1
```

Expected: PASS; existing chat/internal tests observe identical calls, durable tests see stable ID and stripped business input.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/cognitive.go internal/agent/agent.go internal/agent/integration_test.go internal/episode/episode.go internal/episode/episode_test.go
git commit -m "feat(agent): execute fresh durable episodes"
```

---

### Task 11: Tool Capability Contract and Operation Normalization

**Files:**
- Modify: `internal/tool/tool.go`
- Create: `internal/tool/recovery_contract_test.go`
- Modify: `internal/action/classifier.go`
- Modify: `internal/action/classifier_test.go`

**Interfaces:**
- Consumes: existing `ToolCapabilities.IsReadOnly` and fail-closed dynamic action classification.
- Produces: explicit idempotency support in `ToolCapabilities`, `IdempotencyAwareTool`, and `NormalizeOperation`; no existing tool is automatically marked idempotent.

- [ ] **Step 1: Write failing capability/normalization tests**

Cover exact HTTP method uppercasing, operation-field trimming without case folding, malformed bash/HTTP/input rejection, missing tool rejection, and the rule that reversibility never implies idempotency. Verify every currently registered mutating tool has the zero-value idempotency contract unless its implementation explicitly injects and verifies a provider key.

- [ ] **Step 2: Run tool/action tests and observe RED**

Run: `go test ./internal/tool ./internal/action -run 'TestRecoveryContract|TestNormalizeOperation' -count=1`

Expected: FAIL because recovery capability fields and normalizer do not exist.

- [ ] **Step 3: Extend capabilities without changing existing defaults**

```go
type IdempotencyCapability struct { AcceptsCallerKey bool; InputField, ProviderScope string }
type IdempotencyAwareTool interface { VerifyIdempotencyInput(input []byte, key, field, providerScope string) error }
type ToolCapabilities struct {
    // retain every existing field
    Idempotency IdempotencyCapability
}

func NormalizeOperation(toolName string, input []byte) (string, error) {
    switch toolName {
    case "http":
        var v struct{ Method string `json:"method"` }
        if err:=json.Unmarshal(input,&v); err!=nil || strings.TrimSpace(v.Method)=="" { return "", fmt.Errorf("normalize http operation") }
        return strings.ToUpper(strings.TrimSpace(v.Method)), nil
    case "bash":
        return "", fmt.Errorf("dynamic shell operation is not declaratively normalizable")
    default:
        var v struct{ Operation string `json:"operation"` }
        if err:=json.Unmarshal(input,&v); err!=nil || strings.TrimSpace(v.Operation)=="" { return "", fmt.Errorf("normalize %s operation", toolName) }
        return strings.TrimSpace(v.Operation), nil
    }
}
```

`GetCapabilities` keeps current fallback semantics and returns a zero idempotency contract for all tools that do not declare one. A capability claiming caller-key support is valid only when the resolved tool also implements `IdempotencyAwareTool`; its verifier must locate the exact final key in the declared field and reject a scope mismatch. `action.Classifier` remains authoritative for governance/reversibility, but durable recovery uses only `IsReadOnly`, normalized operation, and the exact idempotency contract.

- [ ] **Step 4: Verify fail-closed behavior for GREEN**

Run: `go test ./internal/tool ./internal/action -count=1`

Expected: PASS; missing/malformed declarations never become read-only or idempotent.

- [ ] **Step 5: Commit**

```bash
git add internal/tool/tool.go internal/tool/recovery_contract_test.go internal/action/classifier.go internal/action/classifier_test.go
git commit -m "feat(tool): declare durable recovery contracts"
```

---

### Task 12: Gateway Step Executor, Effect Gate, and Dynamic Plan Intent

**Files:**
- Create: `internal/gateway/durable_runtime.go`
- Create: `internal/gateway/durable_runtime_test.go`
- Modify: `internal/gateway/gateway.go`

**Interfaces:**
- Consumes: Task 10 `RunDurableEpisode`, Task 11 resolved capabilities, World `Retrieve/OutcomeExists`, and all Ledger transaction APIs.
- Produces: `durableRuntime.ExecuteAttempt`, `ResolveEffect`, `CancelRun`, and a non-executing `durable_plan_effect` intent path.

- [ ] **Step 1: Write failing fresh-context, gate-order, and World-outcome tests**

Use fakes for World retrieval, provider, secret resolver, and external tool. Assert the trigger contains goal/current descriptor/committed summaries/fresh World hits but not a prior transcript. Assert first and recovered execution send the persisted EpisodeID. Assert an existing World outcome skips the provider and proceeds to `CommitStep` only when effects are resolved. Assert a mismatched write never reaches a permission interceptor spy or external counter.

- [ ] **Step 2: Run runtime tests and observe RED**

Run: `go test ./internal/gateway -run 'TestDurableRuntime' -count=1`

Expected: FAIL because `durableRuntime` does not exist.

- [ ] **Step 3: Implement exact runtime dependencies and prompt reconstruction**

```go
type durableRuntime struct { ledger *taskruntime.Ledger; agent *agent.Agent; world *world.Store; tools *tool.Registry; secrets secretResolver; running sync.Map }
type secretResolver interface { ResolveSecret(context.Context,string) (string,error) }
func newDurableRuntime(l *taskruntime.Ledger, a *agent.Agent, w *world.Store, tools *tool.Registry, s secretResolver) *durableRuntime
func (r *durableRuntime) ExecuteAttempt(ctx context.Context, claim *taskruntime.ClaimedAttempt) error
func (r *durableRuntime) ResolveEffect(ctx context.Context, in taskruntime.OperatorDecisionInput) (*taskruntime.TaskRun,error)
func (r *durableRuntime) CancelRun(ctx context.Context, in taskruntime.CancelRunInput) error
```

Reload the winning plan version before execution. Build trigger JSON with `task_goal`, current `step`, ordered committed `result_summary` values, and fresh `world.Hit` summaries from `world.Retrieve(Query{Text: goal+" "+stepKey, Limit:8})`. Do not load any session transcript/checkpoint. Before provider execution call `OutcomeExists`; when true, verify all effects satisfied and call `CommitStep` with `journal_outcome_<episodeID>`.

- [ ] **Step 4: Put the effect gate before the existing interceptor chain**

The durable callback resolves the tool and capabilities, normalizes its operation, renders the persisted template using allowlisted fields and secret references, and validates the proposed canonical business input. For an idempotent write, derive the key, inject it into the descriptor's declared final input/header field, and require `IdempotencyAwareTool.VerifyIdempotencyInput(finalInput,key,field,scope)` to succeed before calling `Ledger.PrepareEffect`; every retry injects the identical key. Only after `PrepareEffect` commits an invoking row may it call the Agent closure that reaches `invokeTool` with that verified final input. On return, map definite success/failure/ambiguous transport to `ResolveEffect`, capture `result.Metadata["receipt_id"]` when present, and never treat a persistence error after invocation as permission to retry.

Read-only calls require `IsReadOnly=true`, create no effect rows, and run through the existing pipeline. A write with a missing/unknown key or any mismatch calls `FailCursorStep(...,"plan_contract_violation")` and returns without entering permission/action/tool code. A committed/skipped effect returns its compact persisted resolution without invoking.

- [ ] **Step 5: Implement inert dynamic intent then CAS amendment**

Add only to durable Episode tool definitions an internal `durable_plan_effect` schema containing tool, operation, non-secret template, recovery class, and idempotency declaration; it never enters the tool registry or external pipeline. The runtime validates the intent, allocates `effect_<uuid>`, calls `AmendPlanEffect` with expected plan/descriptor versions, reloads the winning descriptor, and returns the allocated key. A CAS loser returns a reload instruction and executes nothing. The path cannot alter/delete an existing descriptor or amend a completed step.

- [ ] **Step 6: Close attempts through shared policy**

After Runner returns, require `world.OutcomeExists(episodeID)` and call `CommitStep`. The durable callback returns `agent.ErrDurableEpisodeSuspended` immediately after an Effect becomes unknown/resolved-failed, after a contract violation, or when post-invocation persistence fails; the Runner therefore cannot commit a World outcome for incomplete effect state. Transient model/read failures call `RecordAttemptFailure` with the classified failure; contract violations, failed/unknown effects, cancellation, and secret reconstruction failures are terminal/non-automatic as defined in the spec. Store/remove the attempt cancel function in `running` so operator cancellation signals it before `CancelDurableRun` begins.

- [ ] **Step 7: Verify runtime behavior for GREEN**

Run:

```bash
go test ./internal/gateway -run 'TestDurableRuntime' -count=20
go test -race ./internal/gateway -run 'TestDurableRuntimeCancelRace|TestDurableRuntimeEffectGate' -count=1
```

Expected: PASS; external invocation count is zero on every mismatch, stable EpisodeID is reused, dynamic intent is inert until amendment commit, and existing World outcomes skip provider calls.

- [ ] **Step 8: Commit**

```bash
git add internal/gateway/durable_runtime.go internal/gateway/durable_runtime_test.go internal/gateway/gateway.go
git commit -m "feat(gateway): execute durable planned steps"
```

---

### Task 13: Reconcile Before Ingress and Start the Bounded Dispatcher

**Files:**
- Create: `internal/gateway/durable_dispatcher.go`
- Create: `internal/gateway/durable_dispatcher_test.go`
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/e2e_lifecycle_test.go`

**Interfaces:**
- Consumes: `Ledger.Reconcile`, `Ledger.ClaimAttempt`, and Task 12 executor.
- Produces: a bounded dispatcher started after successful reconciliation and stopped by Gateway context cancellation.

- [ ] **Step 1: Write failing startup-order and dispatcher tests**

Instrument Admin, health, MCP, hold drain, channel, Heart, and dispatcher starts in a sequence recorder. Assert `reconcile.begin/reconcile.end` precedes all of them. Seed a reconciliation corruption and assert none starts and `Gateway.Start` returns an error. Seed one pending attempt and run two dispatcher ticks; assert exactly one claim and execution.

- [ ] **Step 2: Run lifecycle tests and observe RED**

Run: `go test ./internal/gateway -run 'TestDurableStartupOrder|TestDurableDispatcher' -count=1`

Expected: FAIL because current `gateway.start` calls `admin.Start` first and has no dispatcher.

- [ ] **Step 3: Move synchronous reconciliation to the first startup action**

The beginning of `Gateway.start` must be structurally:

```go
func (gw *Gateway) start(ctx context.Context) error {
    if _, err := gw.taskLedger.Reconcile(ctx); err != nil { return fmt.Errorf("durable reconciliation: %w", err) }
    if err := gw.admin.Start(ctx); err != nil { return fmt.Errorf("admin: %w", err) }
    // existing health/MCP/cleanup/hold/channels/Heart order follows
}
```

Do not move database construction from `Gateway.New`; migration failure still fails construction. Reconciliation failure uses existing `Start` rollback. Legacy rows are ignored by Task 8.

- [ ] **Step 4: Implement bounded conditional dispatch**

```go
type durableDispatcher struct { ledger *taskruntime.Ledger; runtime *durableRuntime; interval time.Duration; limit int; sem chan struct{}; wg sync.WaitGroup }
func newDurableDispatcher(l *taskruntime.Ledger, r *durableRuntime, limit int, interval time.Duration) *durableDispatcher
func (d *durableDispatcher) Start(ctx context.Context)
func (d *durableDispatcher) Stop(ctx context.Context) error
```

Default `limit=2`, `interval=250ms`. Each tick lists at most `limit` pending attempts ordered by creation time, calls `ClaimAttempt`, ignores only `ErrConflict`, and runs each claim under the semaphore. Never execute an unclaimed attempt. Start it after reconciliation and normal tool dependencies are ready but before external channels; its goroutines may overlap ingress only after reconciliation. On shutdown cancel execution contexts and wait; unexpected process exit deliberately leaves running rows for next startup.

- [ ] **Step 5: Verify startup, scheduler, `/resume`, and shutdown for GREEN**

Run:

```bash
go test ./internal/gateway -run 'TestDurableStartupOrder|TestDurableDispatcher|TestGatewayLifecycle|TestResume|TestScheduler' -count=20
go test -race ./internal/gateway -run TestDurableDispatcherClaimsOnce -count=1
```

Expected: PASS; corrupt recovery starts no ingress, preserved pending work executes once, scheduler transitions and `/resume` output remain unchanged, and shutdown leaves recoverable durable state.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/durable_dispatcher.go internal/gateway/durable_dispatcher_test.go internal/gateway/gateway.go internal/gateway/e2e_lifecycle_test.go
git commit -m "feat(gateway): reconcile and dispatch durable work"
```

---

### Task 14: Deterministic Failpoints and Child-Process Crash Matrix

**Files:**
- Create: `internal/taskruntime/failpoint.go`
- Create: `internal/taskruntime/failpoint_test.go`
- Modify: `internal/taskruntime/plan.go`
- Modify: `internal/taskruntime/attempt.go`
- Modify: `internal/taskruntime/effect.go`
- Modify: `internal/taskruntime/decision.go`
- Modify: `internal/gateway/durable_runtime.go`

**Interfaces:**
- Consumes: all durable transaction boundaries from Tasks 4–13.
- Produces: named environment-armed hard-exit boundaries and a subprocess harness that reopens the same SQLite file.

- [ ] **Step 1: Write the failing child-process harness**

Implement `TestDurableCrashRecovery` as parent/child modes. Parent creates a temp DB, invokes the test binary with `DAIMON_DURABLE_CHILD=1`, `DAIMON_DURABLE_DB` set to that temp DB path, and one failpoint name, requires a dedicated exit code, reopens the DB, runs reconciliation/dispatch, and asserts exact run/cursor/step/attempt/effect/invocation/decision rows plus external invocation count stored in a separate append-only fixture file.

```go
cmd := exec.Command(os.Args[0], "-test.run=TestDurableCrashRecovery/child", "-test.v")
cmd.Env = append(os.Environ(), "DAIMON_DURABLE_CHILD=1", "DAIMON_DURABLE_DB="+dbPath, "DAIMON_DURABLE_FAILPOINT="+name, "DAIMON_DURABLE_CALLS="+callsPath)
err := cmd.Run()
var exit *exec.ExitError
if !errors.As(err,&exit) || exit.ExitCode()!=86 { t.Fatalf("child exit=%v", err) }
```

- [ ] **Step 2: Run the crash test and observe RED**

Run: `go test ./internal/taskruntime -run TestDurableCrashRecovery -count=1 -v`

Expected: FAIL because named failpoints are not armed.

- [ ] **Step 3: Implement inert-by-default failpoints**

```go
func durableFailpoint(name string) {
    if os.Getenv("DAIMON_DURABLE_CHILD")=="1" && os.Getenv("DAIMON_DURABLE_FAILPOINT")==name { os.Exit(86) }
}
```

Call it immediately before and after the atomic boundary for: plan creation/first pending attempt; amendment version CAS; pending creation/claim; Effect+invoking transaction; external success/effect resolution; each operator retry/skip/fail/cancel transaction including cancel/response race; World outcome/ledger commit; step success/cursor+next-attempt transaction; final World outcome/run termination. Failpoints must not split statements that are intentionally in one SQLite transaction; “before/after” proves rollback/commit behavior.

- [ ] **Step 4: Assert every crash boundary exactly**

For every case assert: completed steps do not repeat; idempotent calls append multiple invocation observations but one external effect/key; unknown non-idempotent calls do not append automatically; World-committed steps finish without provider calls; no attempt exceeds budget; decision rows exist iff their transaction committed; cancellation leaves no live rows. Run each case twice against a new temp DB to catch leaked global state.

- [ ] **Step 5: Run failpoint and race suites for GREEN**

Run:

```bash
go test ./internal/taskruntime -run TestDurableCrashRecovery -count=2 -v
go test -race ./internal/taskruntime ./internal/gateway -run 'Durable.*Race' -count=1
```

Expected: PASS; all children exit 86 at the named boundary and every reopened DB reaches the conservative expected state.

- [ ] **Step 6: Commit**

```bash
git add internal/taskruntime/failpoint.go internal/taskruntime/failpoint_test.go internal/taskruntime/plan.go internal/taskruntime/attempt.go internal/taskruntime/effect.go internal/taskruntime/decision.go internal/gateway/durable_runtime.go
git commit -m "test(taskruntime): cover durable crash boundaries"
```

---

### Task 15: Deterministic Recovery Eval and Architecture/Operator Documentation

**Files:**
- Create: `evals/durable_recovery_test.go`
- Modify: `docs/architecture/19-data-layer.md`
- Modify: `docs/architecture/04-episode.md`
- Modify: `docs/architecture/14-gateway.md`
- Modify: `docs/architecture/21-cli-reference.md`
- Modify: `docs/SOAK_RUNBOOK.md`

**Interfaces:**
- Consumes: completed runtime and counters from Tasks 9–14.
- Produces: deterministic acceptance eval and exact rollout/rollback/operator guidance; no runtime interface.

- [ ] **Step 1: Write the failing eval fixture**

Create a deterministic fake provider/tool set with: interrupted read-only step, idempotent write interrupted after external success, unknown non-idempotent write, and World outcome committed before ledger commit. Restart runtime from the same DB and assert:

```go
if safeIncomplete != 0 { t.Fatalf("safe incomplete=%d", safeIncomplete) }
if duplicateExternalEffects != 0 { t.Fatalf("duplicate effects=%d", duplicateExternalEffects) }
if unknownAutomaticReplays != 0 { t.Fatalf("unknown replays=%d", unknownAutomaticReplays) }
if unstableEpisodeIDs != 0 { t.Fatalf("unstable episode ids=%d", unstableEpisodeIDs) }
```

- [ ] **Step 2: Run the eval and observe RED**

Run: `go test ./evals -run TestDurableRecoveryEval -count=1`

Expected: FAIL until the fixture is wired through the real reconciliation/dispatcher/effect APIs.

- [ ] **Step 3: Wire the deterministic eval through real components**

Use real SQLite/Ledger/Gateway durable runtime with fake provider and external tool only. Do not duplicate recovery policy inside the eval. Assert counter snapshots include recovered, retry, awaiting-confirmation, decision, duplicate, and recovery-failure keys even when zero.

- [ ] **Step 4: Update architecture and operator docs with exact guarantees**

Document: ledger vs World authority; all seven tables and state enums; plan/effect schemas and deterministic derivations; atomic step boundary; fresh World retrieval/no transcript restore; gate-before-permission ordering; complete startup sequence; confirmation decision semantics; `/resume` remains checkpoint display; rollout disables creation/dispatch only; rollback never deletes evidence or changes unknown to replayable. Add SQL inspection commands:

```bash
sqlite3 "$DAIMON_DB" "SELECT e.task_run_id,e.effect_key,e.tool_name,e.operation,e.recovery_class,e.updated_at FROM task_effects e WHERE e.state='unknown' ORDER BY e.updated_at;"
sqlite3 "$DAIMON_DB" "SELECT task_run_id,state,count(*) FROM task_step_attempts GROUP BY task_run_id,state ORDER BY task_run_id,state;"
sqlite3 "$DAIMON_DB" "PRAGMA foreign_key_check;"
```

The runbook must say to stop creation/dispatcher on rollback, retain migration 044/tables, resolve unknown effects only by persisted retry/skip/mark-failed/cancel decisions, and expand adoption only after the soak shows zero duplicate effects and zero metadata loss.

- [ ] **Step 5: Verify eval and documentation claims for GREEN**

Run:

```bash
go test ./evals -run TestDurableRecoveryEval -count=20
rg -n 'task_checkpoints|/resume|unknown|idempotency|reconciliation|rollback' docs/architecture/04-episode.md docs/architecture/14-gateway.md docs/architecture/19-data-layer.md docs/architecture/21-cli-reference.md docs/SOAK_RUNBOOK.md
```

Expected: eval PASS on all 20 runs; each documented term appears in the relevant authority/startup/operator section and no text claims checkpoint or transcript restoration.

- [ ] **Step 6: Commit**

```bash
git add evals/durable_recovery_test.go docs/architecture/04-episode.md docs/architecture/14-gateway.md docs/architecture/19-data-layer.md docs/architecture/21-cli-reference.md docs/SOAK_RUNBOOK.md
git commit -m "docs: define durable recovery operations"
```

---

### Task 16: Full Compatibility, Race, and Acceptance Verification

**Files:**
- Modify: `docs/SOAK_RUNBOOK.md`

**Interfaces:**
- Consumes: Tasks 1–15.
- Produces: one verified implementation matching all twelve acceptance criteria and a checked release-verification record in the soak runbook.

If a command fails, stop this task, return to the task that owns the failing behavior, make and commit the correction there, then restart Task 16 from Step 1. Task 16 itself changes only the verification record.

- [ ] **Step 1: Run focused durable suites and confirm GREEN**

Run:

```bash
go test ./internal/store ./internal/taskruntime ./internal/agent ./internal/episode ./internal/gateway ./internal/tool ./internal/action ./evals -count=1
```

Expected: PASS with no skipped durable test.

- [ ] **Step 2: Run race-sensitive CAS suites**

Run:

```bash
go test -race ./internal/taskruntime ./internal/gateway -run 'Durable|ClaimAttempt|ResolveEffect|Operator|Reconcile' -count=1
```

Expected: PASS with no race report, duplicate claim, duplicate invocation, or stale resolution winner.

- [ ] **Step 3: Run the crash matrix and deterministic eval**

Run:

```bash
go test ./internal/taskruntime -run TestDurableCrashRecovery -count=2 -v
go test ./evals -run TestDurableRecoveryEval -count=20
```

Expected: PASS; unknown writes wait for decisions, idempotent external effect count is one, and stable EpisodeIDs never change.

- [ ] **Step 4: Run legacy compatibility and full repository tests**

Run:

```bash
go test ./internal/gateway -run 'TestResume|TestScheduler|TestCommands|TestGatewayLifecycle|TestChatHeart' -count=10
go test ./... -count=1
```

Expected: PASS; legacy scheduler state order, unmarked Ledger behavior, checkpoint display, chat, Heart, and existing tool governance remain unchanged.

- [ ] **Step 5: Scan persistence and plan-contract safety**

Run:

```bash
rg -n 'observations_json|plan_json|subtask_index' internal/taskruntime internal/gateway/durable_runtime.go internal/gateway/durable_dispatcher.go
rg -n 'CanonicalInput|RenderedTemplate|ResolveSecret|response_summary' internal/taskruntime internal/gateway/durable_runtime.go
```

Expected: first command reports no durable-runtime recovery read; second command shows secrets only in ephemeral function parameters and persistence writes only digests/redacted summaries.

- [ ] **Step 6: Record and commit the verified gate**

After every preceding command passes, append this exact checklist to `docs/SOAK_RUNBOOK.md`:

```markdown
### Durable step recovery release gate

- [x] Focused durable package tests pass.
- [x] Durable CAS and cancellation race tests pass.
- [x] Child-process crash matrix passes twice.
- [x] Deterministic recovery eval passes twenty consecutive runs.
- [x] Legacy scheduler, `/resume`, chat, Heart, and full repository tests pass.
- [x] Recovery code does not read checkpoint context or persist resolved secrets.
```

Then commit only the verification record:

```bash
git add docs/SOAK_RUNBOOK.md
git commit -m "test(taskruntime): verify durable recovery acceptance"
```

Before committing, `git diff --cached --name-only` must print only `docs/SOAK_RUNBOOK.md`. Record the successful command outputs in the implementation handoff.
