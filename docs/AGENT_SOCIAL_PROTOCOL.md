# Agent Social Protocol — Design Note (Non-Normative)

> Status: **design note only**. Nothing in this document is implemented, and
> nothing in it should be implemented until the preconditions in §5 hold.
> This file exists to record the shape of the idea and — more importantly —
> the explicit reasons we are *not* building it yet.

## 1. What this is

Daimon agents are sovereign: each one owns a local world (journal, identity,
values, trust ledger) and can be moved wholesale between machines via the
soul archive (`daimon soul export/import`). The natural next question is:
what happens when two sovereign agents need to cooperate?

The "social protocol" is the hypothetical answer: a minimal, verifiable
message layer through which one daimon can ask another to do work, with the
same accountability guarantees each agent already enforces internally.

## 2. Design sketch (if it were ever built)

Three primitives, deliberately mirroring the internal episode contract:

1. **Request** — a goal + constraints + a value budget, signed by the
   requesting agent's identity. Structurally identical to an internal
   episode goal; a remote request is just an episode whose principal is
   another agent instead of the local operator.
2. **Outcome** — the existing `Outcome` accounting record (status, summary,
   cost, value_created), signed and returned. No new schema: the journal
   entry *is* the protocol message.
3. **Receipt chain** — each side appends the exchange to its own journal.
   Disputes are resolved by comparing journals, not by a central ledger.

Trust between agents reuses the internal trust ladder (`ask_every` →
`ask_first` → `hold_then_auto` → `full_auto`): a remote agent is just
another action source that earns autonomy through verified outcomes, and
promotions notify the operator exactly like local trust promotions do.

Transport is deliberately unspecified. Anything that moves signed bytes
works (file drop, HTTP, a message queue); the protocol is the payload
contract, not the pipe.

## 3. What makes this different from existing multi-agent frameworks

Every mainstream multi-agent system (orchestrator/worker swarms, crew-style
role graphs) assumes a shared runtime and a single principal. The social
protocol assumes the opposite: **no shared runtime, no shared owner, no
shared trust root**. Each agent answers to its own operator; cooperation is
a treaty between sovereigns, not a function call inside one process.

That inversion is the only genuinely novel part — and it is exactly the part
that has no user yet.

## 4. Why we are not building it (YAGNI, explicitly)

- **There is one daimon.** The protocol's minimum viable population is two
  independently-operated agents with a real need to transact. Today there is
  a population of one. Building coordination machinery for a society that
  does not exist is speculative abstraction — the precise failure mode the
  coding constitution's YAGNI clause exists to prevent.
- **Every internal primitive it would reuse is still maturing.** Outcome
  accounting, trust promotion, hold execution and undo all shipped recently;
  their semantics should harden under single-agent production load before
  being frozen into a cross-agent wire contract. A protocol ossifies
  whatever it touches.
- **Identity signing does not exist.** The sketch assumes signed requests
  and outcomes. Daimon has no keypair identity today, and adding one *only*
  to serve a hypothetical protocol would be an inverted dependency: the
  protocol should be a consumer of an identity layer that earns its way in
  on other merits (e.g. soul-archive authenticity), never the reason for it.
- **The cost of waiting is near zero.** Because the protocol reuses the
  journal/Outcome/trust primitives rather than inventing parallel ones,
  nothing built today needs to change shape to enable it later. Deferral
  loses no optionality — which is the strongest possible YAGNI signal.

## 5. Preconditions for revisiting

Revisit this note only when **all** of the following are true:

1. Two or more independently-operated daimon instances exist with a concrete
   recurring task that requires them to transact (not a demo).
2. Outcome accounting and the trust ladder have survived meaningful
   production soak without schema churn.
3. An identity/signing layer exists for reasons independent of this
   protocol.

Until then: this document is the implementation.
