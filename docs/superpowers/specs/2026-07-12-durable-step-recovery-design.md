# Durable Step Recovery Design

## Objective

Make multi-step tasks recoverable after process termination at committed step boundaries. Recovery extends `internal/taskruntime.Ledger`; it does not serialize a Go stack, restore a half-finished model exchange, or revive an Episode transcript.

The design preserves Daimon's primary invariant: persistent semantic state lives outside the transient context, and every new Episode rebuilds context from the World Model. The task ledger is the authority for operational execution facts—run state, completed step boundaries, attempts, and effect receipts—while the World Model remains the authority for identity, memory, commitments, and the semantic material used to continue reasoning.

## Existing boundaries

The implementation builds on these current repository facts:

- `internal/taskruntime/ledger.go` owns `Ledger`, `Entry`, and the `pending → running → succeeded/failed/cancelled` task lifecycle.
- migration `019_task_ledger.sql` creates `task_ledger`; a `TaskRun` is a typed view of one existing row, not a second top-level task table.
- Gateway constructs one ledger in `gateway.New` and currently records scheduler transitions through it.
- Gateway starts external channels in `Gateway.start`; recovery reconciliation must finish before those channel loops or Heart begin accepting work.
- `ToolCapabilities.IsReadOnly` and the action classifier already provide a fail-closed read/write classification boundary.
- the action subsystem may emit an `undo_journal.receipt_id`; durable effect receipts link to it when present rather than duplicating undo data.
- `task_checkpoints` from migration 018 still feeds the `/resume` informational command, but architecture explicitly retires checkpoint deserialization as a continuation mechanism.

`task_checkpoints` remains compatible in this slice so `/resume` does not break. No durable recovery decision reads `observations_json`, `plan_json`, or `subtask_index`, and no new recovery data is added to that table.

## Recovery unit

A recoverable task is a sequence of named steps. The durability boundary is the database commit that marks a `StepAttempt` succeeded and advances the parent `TaskRun` to its next step. A running process may hold model context and tool-local state, but those values are disposable.

Every step has a stable `step_key` within its task and a persisted, versioned descriptor. The descriptor contains the step objective and non-secret reconstruction inputs. Credentials remain environment/configuration references and are resolved again at execution; a step whose required secret input cannot be reconstructed is not automatically retried.

On continuation, Gateway starts a fresh Episode using the task goal, current step descriptor, completed-step summaries, and a fresh World Model retrieval. It never injects a serialized provider request, partial assistant message, Go frame, or tool process into the new Episode.

## Domain model

### TaskRun

`TaskRun` is the recovery-facing Go type backed by the existing `task_ledger` row. It retains the existing identifiers, kind, title, metadata, timestamps, result, and terminal states. It adds two runtime states:

- `recovering`: stale in-flight work has been reconciled and automatic recovery is being prepared;
- `awaiting_confirmation`: at least one effect may have occurred but cannot be safely replayed automatically.

Its recovery metadata records a descriptor version, current `step_key`, and the last committed step summary. These fields are operational pointers, not a replacement for World Model writes. Existing non-recoverable and scheduled entries continue to use their current lifecycle unchanged.

TaskRun state transitions are:

```text
pending ──start──> running ──all steps committed──> succeeded
   │                  ├──terminal error───────────> failed
   │                  ├──cancel───────────────────> cancelled
   │                  └──startup reconciliation───> recovering
   │                                                   ├──safe retry queued──> running
   │                                                   ├──unknown effect────> awaiting_confirmation
   │                                                   └──invalid state─────> failed
   └──cancel────────────────────────────────────────> cancelled

awaiting_confirmation ──confirm retry/skip/resolve──> running
awaiting_confirmation ──cancel──────────────────────> cancelled
```

Terminal states cannot return to a live state. Every update uses a conditional `WHERE state = <expected>` transition and checks the affected row count, preventing two recovery paths from claiming the same run.

### StepAttempt

`StepAttempt` is one execution attempt for one `step_key`. It contains:

- generated attempt ID;
- parent task-run ID;
- stable step key and monotonically increasing attempt number;
- descriptor version and descriptor JSON with secrets excluded;
- state, start/finish timestamps, and interruption reason;
- compact result summary and optional World Model journal reference;
- created/updated timestamps.

The attempt states are:

```text
pending ──claim──> running ──commit──> succeeded
                       ├──known failure──> failed
                       └──startup scan───> interrupted

interrupted ──recovery decision──> a new pending attempt, or parent awaiting_confirmation
```

An interrupted attempt is immutable evidence of the abandoned execution. Retrying creates the next attempt number; it never turns the old row back to `running`. At most one `pending` or `running` attempt may exist for a task/step, enforced by transaction logic and a partial unique index.

### EffectReceipt

`EffectReceipt` records the durable intent and observed outcome of a side-effecting tool call within a step attempt. It contains:

- generated receipt ID, parent task-run ID, originating attempt ID, and step key;
- stable `effect_key` derived by the step planner, unique within the task;
- tool name and redacted input digest;
- recovery class: `idempotent_write` or `unknown_non_idempotent`;
- an idempotency key for `idempotent_write`, otherwise empty;
- state: `prepared`, `committed`, `failed`, or `unknown`;
- compact result/error summary;
- optional link to an action `undo_journal` receipt;
- created/updated timestamps.

Before invoking a write, the executor commits a `prepared` receipt. After a definitive tool response it records `committed` or `failed`. SQLite cannot atomically commit an external effect, so a crash or ambiguous transport failure after `prepared` leaves the real-world result unknown. Startup converts that receipt to `unknown` unless the tool supports retry with the same idempotency key.

Read-only calls do not create `EffectReceipt` rows because they have no side effect to deduplicate; their attempt and result summary remain in `StepAttempt`. An Episode-level `Outcome.Receipts` value and an action undo receipt may be referenced by `EffectReceipt`, but neither substitutes for the recovery state.

## Persistence and migration

Add the next embedded migration, `internal/store/migrations/044_durable_step_recovery.sql`. It creates, without renaming or dropping existing tables:

- `task_step_attempts` with a foreign key to `task_ledger(id)` and uniqueness on `(task_run_id, step_key, attempt_no)`;
- `task_effect_receipts` with foreign keys to `task_ledger(id)` and `task_step_attempts(id)`, uniqueness on `(task_run_id, effect_key)`, and nullable `undo_receipt_id`;
- indexes for attempt state, task/step lookup, receipt state, and recovery class;
- a partial unique index that permits only one live (`pending` or `running`) attempt per task/step.

The migration is additive and applies through the existing embedded, lexically ordered migration runner in `internal/store/sqlite.go`. Existing `task_ledger` rows require no backfill and are not considered recoverable unless they carry a supported durable-step descriptor version. Existing `running` scheduler rows without that marker retain their current scheduler behavior and are not swept by the new recovery scan.

Ledger methods that create an attempt, prepare an effect, commit an effect plus step, or reconcile startup state use SQLite transactions. JSON decoding errors, unknown descriptor versions, duplicate live attempts, and impossible state transitions return explicit errors; they are never normalized into a replayable default.

## Declaring replay safety

Recovery uses three classes, selected before tool execution:

### Read-only

A call is read-only only when its resolved `ToolCapabilities.IsReadOnly` is true. An interrupted read-only step may create a new attempt and execute again. Missing capabilities and malformed dynamic operations are not read-only.

### Idempotent write

A write is automatically retryable only when the tool explicitly declares idempotency support and accepts a caller-provided key. The executor derives one stable key from task-run ID plus `effect_key`, persists it before invocation, injects the same value on every retry, and verifies that the tool actually received it. A name-based allowlist or an action's reversibility class is insufficient: undoable does not mean idempotent.

An interrupted `prepared` idempotent write is retried with the same key. A remote success followed by a local crash therefore resolves to the remote system's prior response instead of creating a second effect. If the provider rejects or cannot honor the key, the receipt becomes `unknown` and the task waits for confirmation.

### Unknown non-idempotent

Every other write is classified `unknown_non_idempotent`, including tools with absent/invalid declarations, dynamic shell commands, writes whose secrets cannot be reconstructed, and transports that return an ambiguous result. If interruption occurs after its receipt is prepared, Gateway does not invoke the tool again. It marks the receipt `unknown` and moves the TaskRun to `awaiting_confirmation` with redacted evidence explaining the decision.

Human resolution is explicit: confirm retry, confirm the effect occurred and skip, mark failed, or cancel the task. Each decision is persisted as evidence before execution continues. There is no timeout that turns uncertainty into automatic replay.

## Gateway startup recovery

Insert a synchronous recovery stage at the beginning of `Gateway.start`, after the database-backed subsystems exist and before Admin, health, MCP watchers, hold drain, external channels, or Heart start.

The stage performs one transactional reconciliation:

1. Select recoverable TaskRuns with live attempts.
2. Conditionally change each live attempt from `running` to `interrupted`.
3. Inspect its effect receipts and persisted replay-safety declaration.
4. Mark ambiguous non-idempotent receipts `unknown` and the parent `awaiting_confirmation`.
5. Move runs containing only safe read-only or idempotent work to `recovering` and create exactly one new `pending` attempt using the next attempt number.
6. Commit the reconciliation before any recovery execution is dispatched.

After reconciliation, Gateway starts the dependencies required by normal tool execution and then a bounded internal recovery dispatcher claims pending attempts and starts fresh Episodes. Claims are conditional and transactional. External ingress is gated only on durable reconciliation, not on completion of every recovered task, so channels may run concurrently with the dispatcher once no stale attempt is ambiguous. The dispatcher shares Gateway's run context and stops accepting claims during shutdown.

If the reconciliation query, transaction, or schema validation fails, `Gateway.Start` fails and existing startup rollback runs. Starting channels while recovery state is unknown would permit duplicate work and is therefore not a best-effort path. Individual tasks with a recognized but unsafe effect move to `awaiting_confirmation` and do not prevent unrelated tasks from starting.

Daimon remains a single-active-Gateway system. Cross-host leases and active/active recovery are outside this design; conditional claims still defend against duplicate goroutines inside one process.

## Step commit protocol

A normal step follows this order:

1. In a transaction, validate the TaskRun is live, create a `pending` StepAttempt, and set the current step pointer.
2. Conditionally claim the attempt as `running`.
3. Build a fresh Episode request from World Model retrieval plus the persisted step descriptor.
4. For each write, classify it and commit its `prepared` EffectReceipt before calling the tool.
5. Record the definitive effect outcome when available.
6. Apply the Episode Outcome to the World Model using its existing idempotent episode ID.
7. In one ledger transaction, require all effects to be resolved, mark the attempt `succeeded`, store only a compact result/reference, and advance or finish the TaskRun.

If World Model application succeeds but the ledger commit is interrupted, the stable Episode ID makes the repeated outcome application a no-op. Recovery still evaluates tool effects from their receipts before recreating the Episode, so World Model idempotency is never used to justify replaying an unknown external write.

Known tool or model errors mark the attempt failed according to the task's explicit retry budget. Exhaustion fails the TaskRun. Context cancellation during Gateway shutdown leaves a running attempt for the next startup scan; shutdown does not claim that an unfinished step succeeded.

## Error handling and security

- Database or migration error: fail Gateway construction/startup.
- Invalid task descriptor or state transition: fail that TaskRun with a diagnostic safe for operator display; preserve the original rows.
- Unknown recovery class or missing idempotency key: treat as unknown non-idempotent and require confirmation.
- Effect response ambiguity: record `unknown`; never infer success from elapsed time or a missing error.
- Receipt write failure before tool invocation: do not invoke the tool.
- Receipt update failure after invocation: return an error and leave the prepared receipt for conservative startup reconciliation.
- Missing World Model reference: rebuild from current World state and the step objective; do not fall back to checkpoint transcript restoration.
- Secret-bearing input: store a redacted digest and reconstruction reference only. If reconstruction fails, require confirmation or fail the step without executing.
- Operator-facing errors expose task, step, attempt, and receipt IDs but not credentials or raw sensitive tool output.

## Tests

### Ledger and migration

- fresh and upgraded databases apply migration 044 once;
- existing task and checkpoint rows remain readable;
- valid TaskRun and StepAttempt transitions succeed and invalid/terminal reversals fail;
- concurrent claims produce one winner;
- attempt numbering and live-attempt uniqueness hold under contention;
- effect keys are unique per task and raw secret inputs are absent from stored rows.

### Crash-boundary table tests

Use deterministic failpoints around every durability boundary:

- crash before attempt claim;
- crash after claim but before any tool call;
- crash before and after `prepared` receipt commit;
- external success followed by crash before `committed` receipt update;
- World Model outcome commit followed by crash before ledger step commit;
- ledger step commit followed by process termination.

Each test restarts from the same SQLite file and asserts the exact state and number of tool invocations.

### Recovery policy

- interrupted read-only work runs in one new attempt;
- interrupted idempotent write reuses the identical key and produces one external effect;
- an idempotency provider that cannot honor the key moves the task to confirmation;
- interrupted unknown non-idempotent work never invokes the tool automatically;
- malformed or undeclared capabilities fail closed to confirmation;
- confirm-retry, confirm-skip, fail, and cancel decisions are persisted before transition.

### Gateway and architecture

- stale reconciliation completes before any channel or Heart starts;
- reconciliation failure aborts startup and triggers lifecycle rollback;
- multiple recoverable tasks are reconciled once without duplicate dispatch;
- shutdown leaves unfinished work recoverable on the next start;
- the recovered Episode receives fresh World Model retrieval and no serialized prior transcript;
- existing scheduler ledger behavior and `/resume` checkpoint display remain unchanged.

An end-to-end test kills a child Daimon process at each supported failpoint, restarts it against the same temporary home, and proves: completed steps are not repeated, idempotent effects occur once, and unknown non-idempotent effects wait for confirmation.

## Observability and evaluation

Emit structured transition events for TaskRun state changes, attempt interruption/retry, receipt uncertainty, and confirmation decisions. Logs use IDs and recovery class, never raw inputs. Counters track recovered runs, automatically retried steps by class, tasks awaiting confirmation, duplicate effects detected by the test provider, and recovery failures.

Add a deterministic recovery fixture to the eval gate. Its pass condition is behavioral: after simulated restart, all safe tasks finish, no unknown non-idempotent call is repeated, and the duplicate-effect count is zero. This complements unit tests without reading the operator's live Daimon home.

## Rollout and compatibility

1. Land the additive migration and ledger APIs with legacy callers unchanged.
2. Add the step executor and receipt interception behind an explicit durable-step path; ordinary chat and scheduler tasks remain on their current path.
3. Add startup reconciliation for rows carrying the supported descriptor version.
4. Enable the deterministic restart evaluation and observe confirmation/retry metrics.
5. Expand durable-step adoption only after crash-boundary tests and production soak remain clean.

Rollback disables new durable-step creation and recovery dispatch but leaves the additive tables readable. It must not delete receipts or downgrade `awaiting_confirmation` tasks to replayable states. A forward fix can resume them with their persisted evidence.

## Non-goals

- serializing or resuming a Go call stack, goroutine, subprocess, streaming provider response, or half-finished LLM turn;
- restoring an Episode transcript or prompt as authoritative state;
- reviving `task_checkpoints` as a recovery mechanism;
- exactly-once delivery for external systems that do not support idempotency keys;
- automatically replaying an unknown non-idempotent action;
- replacing World Model outcomes, commitments, or journal entries with task-ledger data;
- active/active Gateways, distributed leases, or cross-host work stealing;
- changing the existing hold, undo, scheduler, or workflow-cache semantics;
- adding new tools or user-facing agent capabilities.

## Acceptance criteria

The design is implemented when:

1. every durable task exposes its current TaskRun, immutable attempt history, and effect receipts from SQLite;
2. Gateway reconciles stale attempts before accepting external work and fails closed on reconciliation errors;
3. a restart re-executes read-only steps safely and retries writes only with an honored stable idempotency key;
4. an ambiguous or unknown non-idempotent effect always waits for explicit confirmation;
5. a committed step and its resolved effects are never repeated after restart;
6. recovered execution creates a fresh Episode from World Model state and the persisted step objective, without checkpoint or transcript deserialization;
7. migration, transition, failpoint, race, Gateway lifecycle, and deterministic eval tests pass;
8. existing scheduler tasks and `/resume` compatibility remain intact;
9. no raw credential or secret-bearing tool input is persisted in recovery tables.
