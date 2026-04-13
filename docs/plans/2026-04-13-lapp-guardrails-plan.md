# Lapp Guardrails Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add deterministic guardrails around risky editing behavior so lapp can prevent or intercept predictable file-editing failures the same way `edit-guard` does for native toolchains.

**Architecture:** Build guardrails as deterministic checks around existing lapp operations, not as a probabilistic planner. The model continues choosing tools, but lapp warns or blocks when the edit pattern is known-dangerous. Keep the core principle: probabilistic strategy selection, deterministic protection.

**Tech Stack:** Go (`internal/server`, `internal/editor`, `internal/fileio`), benchmark harness (`benchmark/run.py`, `benchmark/v1_report.py`, future V2 suites), optional runtime logs/state if needed.

---

## Design goals

### What guardrails should do
- catch risky behavior before silent corruption happens
- return clear, model-actionable warnings/errors
- steer the agent toward safer strategies without replacing the planner
- remain deterministic and cheap

### What guardrails should not do
- silently rewrite broad semantics
- invent new edits the model did not request
- become a second planner inside lapp

---

## Candidate guardrails to implement

### 1. Sequential edit counter
Inspired by `edit-guard`.

Problem:
- repeated edits on the same file increase drift risk
- particularly bad in native toolchains, but also useful for lapp when many retries occur on the same file/region

Possible implementation:
- maintain per-session per-file counters in memory at server level
- if a file sees N consecutive edit-style operations, emit a warning or block

Suggested thresholds for lapp:
- warning at 4
- alert/block at 6

But do **not** start with blocking by default. Begin with warning-only until benchmark evidence justifies harder enforcement.

### 2. Local stale-retry loop detector
Problem:
- model retries the same stale region repeatedly instead of using returned fresh anchors

Implementation:
- if the same file returns `stale_refs` repeatedly with the same changed anchors, emit a stronger message:
  - “You already have fresh anchors for this region. Retry using them directly instead of rereading.”

This is likely more valuable for lapp than a generic sequential edit counter.

### 3. Broad write / line-drop guard
Problem:
- wide writes can accidentally remove content
- especially for future broader helpers or when models fall back to file rewrite paths

Implementation options:
- line-count delta threshold after broad operations
- no-op / major shrink detection
- large middle deletion detection for broad replace/write paths

This is more relevant if lapp later adds more file-rewrite-style helpers.

### 4. Strategy recommendation warnings
This is the most useful immediate guardrail.

Examples:
- many edits on same large file → recommend `lapp_replace_block` or batched `lapp_edit`
- multiline manual choreography detected → recommend `lapp_replace_block`
- repeated stale repair on same region → recommend direct retry using returned anchors

This can remain warning-only and should be deterministic.

---

## Proposed phases

### Phase 1: warning-only guardrails
Implement the lowest-risk, highest-signal guardrails first.

#### A. Stale-retry repetition warning
- detect repeated `stale_refs` on same file/region
- return stronger guidance instead of generic retry language

#### B. Multiline choreography warning
- if the model repeatedly uses `lapp_find_block` + `lapp_edit` on the same file for multiline changes,
  suggest `lapp_replace_block`

#### C. Repeated-search warning
- if a model does many `lapp_grep` / `lapp_read` calls on the same file before one edit,
  suggest a more direct helper

These are model-facing guardrails that do not yet block behavior.

### Phase 2: hard safety checks
After evidence:
- block obviously bad repeated paths
- e.g. too many retries on identical stale anchors
- or dangerous broad write situations if we add more full-file helpers later

### Phase 3: benchmark-driven tuning
Once V2 exists, tune thresholds based on measured outcomes rather than intuition.

---

## Concrete implementation ideas

### A. Server-side session state
Add lightweight in-memory state to `internal/server/server.go`:
- per-file recent operations
- stale-ref count by anchor set
- repeated helper usage

No persistence required initially.

Potential structure:
```go
type fileGuardState struct {
    ConsecutiveOps []string
    LastChangedAnchors []string
    StaleCount int
}
```

### B. Warning result shape
Use structured warnings instead of hidden logs.

Example:
```json
{
  "status": "warning",
  "code": "REPEATED_STALE_RETRY",
  "message": "This file returned stale_refs twice for the same local region. Retry using the fresh anchors below instead of rereading.",
  "anchors": ["742#QS", "754#QW"]
}
```

### C. Hook points
Best insertion points:
- after `handleEdit`
- after `handleReplaceBlock`
- after `handleGrep` / `handleFindBlock` if repeated behavior should be tracked

---

## Benchmark implications

Guardrails should be benchmarked too.

Suggested suites to add later:
- repeated-stale-retry benchmark
- many-edits-same-file benchmark
- multiline helper recommendation benchmark

Important:
- benchmark guardrails separately from core lapp protocol
- avoid mixing “tool quality” and “guardrail intervention” until we can isolate them clearly

---

## First implementation wave

### Task 1: add warning-only stale-retry guardrail
Files:
- `internal/server/server.go`
- `internal/server/server_test.go`

### Task 2: add warning-only multiline-helper recommendation
Files:
- `internal/server/server.go`
- `internal/server/server_test.go`
- `instructions/lapp-tools.md`

### Task 3: benchmark/document guardrail effect
Files:
- `benchmark/run.py` (only if needed for variant metadata)
- `docs/plans/` or README note if we want to surface guidance

---

## Success criteria

This plan is complete when:
1. at least one deterministic warning-only guardrail exists in lapp
2. the warning is machine-readable and model-actionable
3. server tests cover the new guardrail behavior
4. the guardrail does not break existing benchmark suites
5. we can point to a measurable reduction in repeated bad edit patterns in at least one model/harness run

---

## Recommendation

Start with:
1. repeated stale-retry warning
2. multiline-helper recommendation warning

Do **not** start with a hard block.

That gives us the `edit-guard` lesson in a lapp-native form without overreaching or constraining the agent prematurely.
