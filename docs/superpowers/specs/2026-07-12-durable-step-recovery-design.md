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

## Recovery unit and persisted plan

A recoverable task owns one complete, versioned, ordered step plan. Creation persists the `task_ledger` row, plan header, every step descriptor, cursor at ordinal zero, and the first pending attempt in one transaction. A plan cannot begin execution with missing steps, duplicate ordinals, duplicate step keys, or an unsupported descriptor version.

Each step stores a stable `step_key`, zero-based ordinal, non-secret reconstruction descriptor, and stable `episode_id`. The episode ID is deterministically derived from `task_run_id + NUL + step_key` using the full SHA-256 digest and is also persisted; creation rejects any mismatch between the derived and supplied values. Every initial or recovered execution passes this exact ID in `agent.CognitiveRequest.EpisodeID`.

Credentials remain environment/configuration references and are resolved again at execution. A step whose required secret cannot be reconstructed is not automatically retried. On continuation, Gateway builds a fresh Episode request from the task goal, current persisted step, summaries of committed steps, and a fresh World Model retrieval. It never restores a provider request, partial assistant message, transcript, Go frame, or tool process.

The durability boundary is one SQLite transaction that marks the current step attempt and plan step succeeded, advances the cursor, and either creates the next step's pending attempt or marks the TaskRun succeeded. There is no committed state in which step N succeeded but the cursor still points to N, or in which the cursor points to step N+1 without its first pending attempt.

## Domain model

### TaskRun and durable marker

`TaskRun` is the recovery-facing view of an existing `task_ledger` row joined with its plan. `Metadata` gains a typed optional `Durable` field containing the durable schema version and plan version. Only rows with a supported `Metadata.Durable` marker are processed by strict recovery APIs or startup reconciliation.

The marker is deliberately part of the typed JSON structure. `scanEntry`, `Create`, `mergeMetadata`, `MarkRunning`, `AddEvidence`, and `finish` must round-trip it without loss; a nil override preserves the existing marker, and a non-nil override is validated and deep-copied. Tests exercise Get/List and every legacy metadata update. The ordered plan and cursor live in normalized tables, not inside metadata JSON.

Durable TaskRuns add `recovering` and `awaiting_confirmation` to the existing states:

```text
pending ──claim first attempt──> running ──last step commit──> succeeded
   │                                  ├──terminal error─────> failed
   │                                  ├──cancel─────────────> cancelled
   │                                  └──startup scan───────> recovering
   │                                                               ├──pending retry──> running
   │                                                               └──unknown effect─> awaiting_confirmation
   └──cancel──────────────────────────────────────────────────────> cancelled

awaiting_confirmation ──all effects safely resolved──> recovering
awaiting_confirmation ──mark failed──────────────────> failed
awaiting_confirmation ──cancel───────────────────────> cancelled
```

Every durable transition is a compare-and-swap update with the expected current state in `WHERE`, and zero affected rows is a conflict. Durable terminal states never reopen. The existing `Ledger.MarkRunning`, `Complete`, `Fail`, and `Cancel` entry points branch on the marker: marked rows delegate to strict allowed-state transitions, while unmarked rows retain their current SQL and behavior. Legacy scheduler call order and any existing unmarked terminal-reopen semantics therefore remain unchanged. The recovery executor uses the richer strict methods directly, but a generic API call cannot bypass durable CAS.

### Step plan and cursor

The plan header stores task-run ID, durable schema version, plan version, total step count, and `cursor_ordinal`. Each ordered step stores task-run ID, ordinal, step key, descriptor version and JSON, stable episode ID, state, compact result summary, and optional World Model outcome reference.

Plan step states are `planned`, `running`, `succeeded`, and `failed`. The cursor names the only step eligible to run; `cursor_ordinal == total_steps` means every step is committed. The initial design treats a started plan as immutable. A future replan requires a separately specified version transition and cannot rewrite completed history.

### StepAttempt

`StepAttempt` is one execution try for the cursor step. It stores attempt ID, task-run ID, step key, monotonically increasing attempt number, state, timestamps, interruption reason, compact result, and World outcome reference. Its states are:

```text
pending ──conditional claim──> running ──step transaction──> succeeded
                                      ├──known failure─────> failed
                                      └──startup CAS───────> interrupted
```

An interrupted attempt is immutable history. Recovery creates the next attempt number rather than reopening it. A partial unique index permits at most one `pending` or `running` attempt for a task/step. The dispatcher claims `pending → running` conditionally and changes the TaskRun and plan step to `running` in the same transaction when necessary.

### Logical Effect

`Effect` is one logical side effect, independent of any particular step attempt. It is unique by `(task_run_id, effect_key)` and stores task-run ID, step key, tool name, redacted input digest, recovery class, stable idempotency key when supported, logical state, compact final resolution, optional `undo_journal.receipt_id`, and timestamps.

Its compare-and-swap states are:

```text
prepared ──definite success──────────────> resolved_committed
    │     ├──definite failure────────────> resolved_failed
    │     ├──ambiguous non-idempotent────> unknown
    │     └──safe idempotent recovery────> retrying
    │
unknown ──confirm retry───────────────────> retrying
    │    ├──confirm applied / skip replay─> resolved_skipped
    │    └──mark failed───────────────────> resolved_failed
    │
retrying ──definite success───────────────> resolved_committed
         ├──definite failure─────────────> resolved_failed
         ├──ambiguous non-idempotent─────> unknown
         └──safe idempotent restart──────> retrying (new invocation only)
```

`resolved_skipped` means an operator confirmed that replay must be skipped and the step may treat the logical effect as satisfied. `resolved_failed` means the logical effect was not accepted as satisfied and prevents step success; its resolution detail distinguishes a definitive provider failure from cancellation with external reality still unknown. Resolved states are final. A same-state `retrying` recovery is allowed only while atomically closing the abandoned invocation and creating one new invocation opportunity; it cannot mutate the logical key or idempotency key.

### EffectAttempt

`EffectAttempt` is the observation of one actual tool invocation. It binds one logical Effect to the StepAttempt that caused the call and carries an invocation number, state, start/finish times, redacted response/error summary, and provider request reference. Its states are `invoking`, `committed`, `failed`, and `ambiguous`.

Before the first external write, one transaction creates the logical Effect in `prepared` and its first EffectAttempt in `invoking`. Before a retry, one transaction requires the Effect in `retrying` and creates the next `invoking` EffectAttempt bound to the new StepAttempt. A definitive response conditionally closes the invocation and resolves the logical Effect. A crash leaves the `invoking` row as proof that a call might have escaped; startup closes it as `ambiguous` before deciding whether another invocation is allowed.

Read-only calls create neither Effect nor EffectAttempt because there is no effect to deduplicate. Their execution remains visible in StepAttempt results. Episode `Outcome.Receipts` and action undo receipts may be referenced, but neither is the logical-effect recovery authority.

### Operator decisions

Every confirmation writes an immutable decision row and applies its state changes in the same transaction:

- **retry**: insert `retry` and CAS Effect `unknown → retrying`; if other Effects remain unknown, leave the TaskRun awaiting them, if any Effect is failed fail the step, otherwise create the next pending StepAttempt and move TaskRun `awaiting_confirmation → recovering`;
- **skip replay / confirmed applied**: insert `skip` and CAS `unknown → resolved_skipped`; then leave the TaskRun awaiting other unknowns, fail it if any Effect is failed, or create the pending StepAttempt and move it to `recovering`;
- **mark failed**: insert `mark_failed`, CAS `unknown → resolved_failed`, mark the current plan step and TaskRun failed, and create no attempt;
- **cancel**: insert `cancel`, CAS every current unknown Effect to `resolved_failed` with resolution `cancelled_external_unknown`, CAS the TaskRun to `cancelled`, and create no attempt. This resolves workflow handling without claiming the external operation did or did not occur.

The decision stores effect ID, actor, timestamp, and redacted reason. A duplicate or stale command loses the CAS and makes no partial change. There is no timeout that converts uncertainty into replay permission.

## Persistence and migration

Add `internal/store/migrations/044_durable_step_recovery.sql`. It is additive and creates:

- `task_run_plans`, one row per durable `task_ledger` entry, with schema/plan versions, total steps, and cursor ordinal;
- `task_run_steps`, ordered and versioned descriptors with unique `(task_run_id, ordinal)`, `(task_run_id, step_key)`, and episode ID;
- `task_step_attempts`, unique `(task_run_id, step_key, attempt_no)` with a partial unique index for one live (`pending` or `running`) attempt per step;
- `task_effects`, logical effects unique by `(task_run_id, effect_key)` with indexed state and recovery class;
- `task_effect_attempts`, invocation observations unique by `(effect_id, invocation_no)`, bound to both the logical effect and StepAttempt, with a partial unique index for one `invoking` row per effect;
- `task_effect_decisions`, immutable operator decisions linked to the logical effect and task run.

Foreign keys reference `task_ledger`, plan steps, attempts, and effects as appropriate. State columns use explicit checks for the enums in this design. Indexes support cursor lookup, live-attempt scan, invoking-effect scan, unresolved logical effects, and confirmation queues.

The migration applies through the existing embedded lexical migration runner. Existing rows need no backfill. A row is durable only when its typed metadata marker and joined plan versions agree; disagreement is corruption and fails reconciliation. Existing checkpoint rows and unmarked scheduler rows remain untouched.

## Declaring replay safety

Recovery selects one of three policies before execution:

### Read-only

A call is read-only only when resolved `ToolCapabilities.IsReadOnly` is true. An interrupted read-only step can create a new StepAttempt and run again. Missing capabilities and malformed dynamic operations are not read-only.

### Idempotent write

A write is automatically retryable only when the tool explicitly declares that it accepts and honors a caller-provided idempotency key. The executor deterministically derives the key from task-run ID and `effect_key`, persists it before invocation, injects the identical value on every EffectAttempt, and verifies it reached the provider. A tool-name allowlist or reversibility class is insufficient.

At startup, abandoned `prepared` or `retrying` idempotent effects may be CASed to `retrying` and scheduled with the same key. If support cannot be verified, the invocation is `ambiguous`, the Effect becomes `unknown`, and the TaskRun waits for confirmation.

### Unknown non-idempotent

Every other write is `unknown_non_idempotent`, including undeclared tools, dynamic shell commands, unreconstructable secret inputs, and ambiguous transports. An abandoned invocation becomes `ambiguous`, its Effect becomes `unknown`, and no new invocation is created without an operator retry decision. Explicit retry authorizes exactly one new invocation; if that invocation is interrupted, the Effect returns to `unknown` rather than recursively retrying.

## Gateway startup reconciliation

Run synchronous reconciliation at the beginning of `Gateway.start`, after database construction and before Admin, health, MCP watchers, hold drain, channels, or Heart. Here, “live” means `pending`, `running`, or `recovering`; combinations not admitted by the matrix are corruption. The scanner selects only supported durable markers and uses this complete matrix:

| TaskRun / attempt state | Startup action |
|---|---|
| legacy row without durable marker | ignore completely |
| durable terminal TaskRun, no live attempt | preserve; never reopen |
| durable terminal TaskRun with live attempt | fail reconciliation as corrupt |
| live TaskRun with `pending` attempt and no unknown/failed Effect | preserve it; dispatcher later claims it with CAS |
| live TaskRun with `pending` attempt plus unknown/failed Effect | treat as contradictory state and fail reconciliation; execute nothing |
| `running`/`recovering` TaskRun with `running` attempt | CAS it to `interrupted`, close every `invoking` EffectAttempt as `ambiguous`, then apply effect policy |
| `pending` TaskRun with `running` attempt | fail reconciliation as corrupt because claim must change both states atomically |
| live TaskRun with no `pending`/`running` attempt and cursor on an unfinished step | reconstruct exactly one `pending` attempt from the persisted plan/cursor if no Effect is unknown or failed |
| live TaskRun with no live attempt and an unknown Effect | move to `awaiting_confirmation`; create no attempt |
| live TaskRun with no live attempt and a `resolved_failed` Effect | mark the cursor step and TaskRun failed; create no attempt |
| live TaskRun with cursor equal to total steps and every step succeeded | CAS TaskRun to `succeeded`; create no attempt |
| `awaiting_confirmation` with no live attempt, unfinished cursor, and unknown Effect | preserve; await an operator decision |
| `awaiting_confirmation` with a live attempt, invalid cursor, missing step, version mismatch, or contradictory step state | fail reconciliation as corrupt; execute nothing |

For interrupted work, resolved effects remain resolved. Abandoned idempotent effects become/remain `retrying` and permit a new attempt with the same key. Abandoned non-idempotent effects become `unknown`. Multiple effects are assessed together: any `unknown` blocks automatic execution; any `resolved_failed` fails the step; otherwise reconciliation creates one pending StepAttempt from the cursor.

All changes for one TaskRun occur in one transaction with conditional updates. Reconciliation commits every stale attempt to a durable disposition before ingress starts. Gateway then starts normal tool dependencies and a bounded dispatcher; external ingress may run concurrently only after reconciliation. Database, schema, or invariant failure aborts `Gateway.Start` and triggers existing rollback.

Daimon remains single-active-Gateway. CAS protects against duplicate goroutines in one process; distributed leases and active/active operation are outside this design.

## Execution and commit protocol

1. Create the TaskRun, complete ordered plan, cursor, stable per-step episode IDs, and first pending StepAttempt atomically.
2. Claim the pending attempt with CAS; set its plan step and durable TaskRun running in the same transaction.
3. Before model execution, check the current step's persisted episode ID through the existing World outcome idempotency path. If the outcome already exists and every Effect is resolved as committed/skipped, skip the provider and proceed directly to ledger commit.
4. Otherwise build a fresh request and pass the persisted ID in `agent.CognitiveRequest.EpisodeID` for both first execution and recovery.
5. For each write, resolve the stable `effect_key`. A previously committed/skipped logical Effect returns its persisted resolution without invoking the tool. A new or authorized retry creates its EffectAttempt transaction before invocation.
6. Persist each definitive invocation observation and logical Effect resolution with CAS. Ambiguous results become `unknown` and stop the step.
7. Apply the Episode Outcome to the World Model. Its existing episode-ID claim makes a repeated apply a no-op.
8. In one transaction, require the attempt running, cursor on this step, World outcome present, and every Effect resolved committed/skipped; mark attempt and step succeeded, store compact references, and advance the cursor. Create the next step's pending attempt in this transaction, or, for the last step, set cursor to total steps and TaskRun succeeded.

If the World outcome commits before the ledger transaction and the process dies, recovery passes the same episode ID; the existing outcome is detected without a provider call, and the ledger transaction completes. World idempotency never authorizes replay of an Effect that remains unknown.

Known model or read-only errors close the attempt failed and may create another attempt under the task's retry budget. A write that reaches `resolved_failed` fails the step and TaskRun rather than replaying that Effect. Budget exhaustion fails the durable TaskRun through strict CAS. Shutdown cancellation leaves a running attempt for startup reconciliation and never claims success.

## Error handling and security

- Database, migration, or reconciliation invariant error: fail Gateway construction/startup.
- Missing/invalid durable marker, plan version, step descriptor, cursor, or stable episode ID: execute nothing and fail reconciliation.
- Unknown recovery class or absent idempotency key: classify as unknown non-idempotent.
- Effect response ambiguity: close the EffectAttempt ambiguous and set the logical Effect unknown; never infer success.
- Persistence failure before creating an `invoking` row: do not invoke the tool.
- Persistence failure after invocation: return an error and leave the logical state for conservative startup reconciliation.
- Missing World reference: retrieve current World state; never deserialize checkpoint context.
- Secret input: persist only a redacted digest and reconstruction reference. Failed reconstruction does not execute.
- Operator errors expose IDs and classes, not raw sensitive inputs or outputs.

## Tests

### Migration, metadata, and CAS

- fresh and upgraded databases apply migration 044 exactly once and preserve task/checkpoint rows;
- complete plan ordering, uniqueness, cursor bounds, version agreement, and deterministic episode IDs are enforced;
- Metadata.Durable survives Create/Get/List/MarkRunning/AddEvidence/Complete/Fail round trips on separate marked fixtures, and each generic mutation enforces the marked row's strict allowed states;
- durable strict transitions reject stale writers and terminal reopen while unmarked scheduler behavior remains byte-for-byte compatible;
- concurrent attempt claims and effect resolutions have exactly one winner;
- partial indexes prevent duplicate live attempts and duplicate invoking observations;
- logical Effect history remains one row while repeated invocations create ordered EffectAttempt rows.

### Startup matrix and crash boundaries

Table tests cover every startup-matrix row, including pending preservation, running interruption, missing-live-attempt reconstruction from cursor, all-steps finalization, awaiting-confirmation preservation, legacy exclusion, and corrupt terminal/live combinations.

Deterministic failpoints restart from the same SQLite file after:

- plan creation before and after the first pending attempt transaction;
- pending attempt creation and claim;
- Effect plus invoking observation transaction;
- external success before invocation/effect resolution;
- operator retry/skip/fail/cancel decision transactions;
- World outcome commit before ledger commit;
- step success before cursor advance/next-attempt creation transaction;
- final step World outcome before TaskRun termination.

Assertions include exact TaskRun, cursor, step, StepAttempt, Effect, EffectAttempt, and decision rows plus external invocation count.

### Recovery policy and stable Episode

- read-only interruption creates one new StepAttempt;
- idempotent retries reuse one logical Effect, the identical key, and multiple invocation observations while producing one external effect;
- an explicit non-idempotent retry authorizes one invocation and returns to unknown if interrupted;
- unknown non-idempotent work never invokes automatically;
- committed/skipped effects are not reinvoked when a recovered Episode requests the same effect key;
- retry, skip, mark-failed, and cancel decisions are atomic, durable, and CAS-protected;
- first execution and every recovery pass the same persisted `CognitiveRequest.EpisodeID`;
- an existing World outcome bypasses the provider and completes the pending ledger transaction.

### Gateway and architecture

- reconciliation finishes before channel or Heart start and failure triggers lifecycle rollback;
- the dispatcher conditionally claims preserved/reconstructed pending attempts once;
- shutdown leaves unfinished work recoverable;
- resumed Episodes use fresh World retrieval and no prior transcript;
- legacy scheduler state changes and `/resume` checkpoint display remain unchanged.

An end-to-end child-process test kills Daimon at each failpoint and proves completed steps do not repeat, idempotent effects occur once, unknown effects wait for confirmation, and a World-committed step finishes without another provider call.

## Observability and evaluation

Emit structured events for durable TaskRun transitions, cursor advancement, attempt interruption/claim, logical Effect state, invocation observations, and operator decisions. Logs contain IDs and redacted summaries only. Counters distinguish recovered tasks, invocation retries, tasks awaiting confirmation, decisions by kind, duplicate effects, and recovery failures.

The deterministic eval fixture restarts the runtime and requires all safe tasks to finish, no unknown non-idempotent invocation to repeat, the same episode ID on every step attempt, and zero duplicate external effects.

## Rollout and compatibility

1. Add migration, typed Metadata.Durable round-trip, and strict durable APIs without changing legacy callers.
2. Add plan creation, execution, logical effects, invocation observations, and confirmation transactions behind the durable marker.
3. Add startup reconciliation for supported markers only.
4. Enable crash-boundary eval and observe recovery/confirmation metrics.
5. Expand adoption only after soak confirms no duplicate effects or metadata loss.

Rollback disables creation and dispatch of new durable runs but preserves all plan, effect, invocation, and decision rows. It never deletes evidence or converts unknown effects into replayable work.

## Non-goals

- serializing a Go stack, goroutine, subprocess, provider stream, or half-finished LLM turn;
- restoring an Episode transcript or prompt as authoritative state;
- reviving `task_checkpoints` as recovery state;
- exactly-once delivery without provider idempotency support;
- automatic replay of unknown non-idempotent effects;
- replacing World Model outcomes, commitments, or journal entries with task data;
- mutable/replanned step plans in this first protocol;
- active/active Gateways, distributed leases, or work stealing;
- changing hold, undo, scheduler, workflow-cache, or legacy Ledger semantics;
- adding tools or user-facing agent features.

## Acceptance criteria

The design is implemented when:

1. every marked TaskRun has a complete versioned plan, valid cursor, stable persisted step episode IDs, immutable attempt history, logical effects, and per-invocation observations;
2. step success, cursor advance, next pending attempt creation, and final TaskRun completion are atomic;
3. Gateway implements the full startup matrix before ingress and reconstructs missing live work only from the persisted plan/cursor;
4. pending attempts are preserved and conditionally claimed, running attempts are interrupted, and legacy entries are excluded;
5. idempotent retries reuse one logical effect and key while unknown non-idempotent effects require an atomic persisted decision;
6. retry, skip, mark-failed, and cancel decisions cannot partially apply or race successfully twice;
7. initial and recovered Episodes use the same persisted `CognitiveRequest.EpisodeID`, and an existing World outcome completes ledger state without a provider call;
8. strict CAS and terminal rules apply only to durable markers; legacy scheduler and metadata behavior remain compatible;
9. migration, metadata round-trip, state matrix, failpoint, race, lifecycle, end-to-end, and eval tests pass;
10. no raw credential or secret-bearing tool input is persisted.
