# Lapp Indent Rebasing and Stale Repair Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement two high-value robustness improvements in lapp: (1) shared block indentation rebasing for multiline replacements, and (2) local stale-reference repair payloads in the style of OMP so models can retry with minimal context.

**Architecture:** Extend the existing deterministic multiline path (`lapp_replace_block`) with block-level indentation normalization, and extend the stale-ref mismatch path to return structured, local changed-region repair data instead of only a text-heavy mismatch message. Both changes stay inside lapp’s current truth-preserving edit model — no speculative semantic rewriting, no hidden planning.

**Tech Stack:** Go (`internal/editor`, `internal/server`, `internal/fileio`, `pkg/hashline`), Python benchmark harness (`benchmark/run.py`, `benchmark/v1_report.py`), Go tests, OpenCode benchmark runs.

---

## Scope and non-goals

### In scope
1. Shared block indentation rebasing inside `lapp_replace_block`
2. Region-scoped stale-ref repair payloads for `lapp_edit` / `lapp_replace_block`
3. Regression tests for both behaviors
4. Benchmark reruns for affected multiline models after both changes

### Non-goals
- No automatic semantic patching beyond whitespace/format normalization
- No harness-native tool replacement strategy
- No benchmark redesign beyond using the current official V1 suites

---

## Task 1: Implement shared block indentation rebasing

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Verify: `benchmark/run.py` (no functional change required unless prompt wording needs sync)

**Step 1: Write failing tests first**

Add or extend server tests to prove that `lapp_replace_block`:
- preserves the old block’s shared base indentation
- preserves the new block’s relative internal indentation
- does not flatten nested indentation

Representative test shape:
- file block has base indent 8 spaces
- `old_content` is supplied with base indent 12 spaces
- `new_content` is supplied with base indent 12 spaces and an inner nested line
- result must be rebased onto base indent 8 while keeping the inner nested line relatively deeper

Tests to add/extend:
- `TestReplaceBlockNormalizeWhitespace` (update to assert base + relative indentation)
- add a second case with mixed nested indentation if needed

**Step 2: Run tests to verify failure if not already red**

Run:
```bash
go test ./internal/server -run 'TestReplaceBlock' -v
```
Expected: if the implementation is not yet present, the nested-indent test fails.

**Step 3: Implement minimal code**

In `internal/server/server.go`:
- add a helper that computes shared block indentation from the old block
- normalize the new block by stripping its shared indentation
- reapply the old block’s shared indentation to every non-empty line of the new block
- do this only inside `lapp_replace_block`
- keep blank lines blank
- keep `lapp_edit` unchanged for now (single-line indentation preservation already exists there)

The implementation MUST:
- only affect `lapp_replace_block`
- only affect leading indentation
- preserve relative indentation within the replacement block
- never modify line content beyond leading whitespace adjustment

**Step 4: Re-run tests**

Run:
```bash
go test ./internal/server -run 'TestReplaceBlock' -v
```
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "fix(lapp_replace_block): preserve shared block indentation"
```

---

## Task 2: Implement local stale-ref repair payloads

**Files:**
- Modify: `internal/editor/editor.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Optional helper extraction in: `pkg/hashline` only if it meaningfully reduces duplication

**Step 1: Define the payload shape**

We want an OMP-like local retry payload.

Recommended JSON result shape:

```json
{
  "status": "stale_refs",
  "message": "2 lines changed since last read. Retry with the updated refs below.",
  "count": 2,
  "changed": [
    {"anchor": "742#QS", "line_number": 742, "line": "if err != nil {"},
    {"anchor": "754#QW", "line_number": 754, "line": "normalizeWhitespace := true"}
  ]
}
```

Keep this machine-friendly and small.

### Design rules
- Only include the changed local region(s), not the full file
- Include fresh `anchor` refs the model can paste directly into retry edits
- Include enough surrounding lines to orient the model if needed
- Prefer structured JSON over prose-heavy text

**Step 2: Write failing tests first**

Add server-level tests covering:
1. stale single-line ref on `lapp_edit` returns local changed-region payload
2. stale range/block ref on `lapp_replace_block` returns local changed-region payload
3. payload includes fresh anchors and affected line numbers
4. payload is bounded/local, not a full-file reread

Concrete tests:
- mutate one target line after reading it, then call `lapp_edit` with the stale ref
- mutate one line inside a block after capturing the old block, then call `lapp_replace_block`
- assert JSON shape and changed anchors

**Step 3: Run tests to verify failure**

Run:
```bash
go test ./internal/server -run 'Test.*Stale' -v
```
Expected: FAIL because stale recovery currently returns generic hash-mismatch text only.

**Step 4: Implement minimal code**

### In `internal/editor/editor.go`
- extend the mismatch formatting path to build structured stale-region data
- use current `RefMismatch` data to identify affected lines
- produce a compact local set of changed lines with fresh refs
- keep existing human-readable mismatch information only if needed for fallback/debugging

### In `internal/server/server.go`
- when `editor.ApplyEdits` returns stale hash mismatch / line-out-of-range in a recoverable local case,
  return the structured tool result text (JSON) instead of only `mcp.NewToolResultError(...)`
- apply the same behavior to `lapp_replace_block`, since it internally builds a range edit and can suffer stale refs too

Important:
- preserve backward compatibility where possible
- do not suppress real errors
- only use the local stale-ref payload when the issue is genuinely a stale ref / local drift

**Step 5: Re-run stale tests**

Run:
```bash
go test ./internal/server -run 'Test.*Stale' -v
```
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/editor/editor.go internal/server/server.go internal/server/server_test.go
git commit -m "feat(lapp): return local stale-ref repair payloads"
```

---

## Task 3: Sync prompts/instructions with new behavior

**Files:**
- Modify: `benchmark/run.py`
- Modify: `instructions/lapp-tools.md`

**Step 1: Update benchmark prompts**

For multiline B-side prompts in `benchmark/run.py`:
- prefer `lapp_replace_block`
- mention that if a block edit fails because lines changed, lapp will return fresh local anchors to retry

Do not overconstrain models. Keep the prompt simple and operational.

**Step 2: Update instruction file**

In `instructions/lapp-tools.md`:
- add one concise line under workflows or rules:
  - on stale refs, use the returned local changed-region anchors directly rather than re-reading the whole file

Keep the instruction compact.

**Step 3: Verify syntax only**

Run:
```bash
python3 -m py_compile benchmark/run.py
```
Expected: PASS.

**Step 4: Commit**

```bash
git add benchmark/run.py instructions/lapp-tools.md
git commit -m "docs(benchmark): mention replace_block and local stale retries"
```

---

## Task 4: Rerun affected benchmarks

**Files:**
- No new code; use benchmark runner

### A. Multiline reruns for the three target models
Run official V1 multiline suite on:
- `ollama-cloud/minimax-m2.7`
- `ollama-cloud/gemma4:31b`
- `ollama-cloud/glm-5.1`

Command pattern:
```bash
AGENT=opencode OPENCODE_MODEL='<model>' SKIP_EXISTING=0 python3 benchmark/run.py --suite targeted-multiline-pure-replacement
```

### B. Evaluate before/after
For each of the three, compare:
- correctness_similarity
- wall_ms
- num_turns
- output_tokens
- input_tokens

Use:
```bash
python3 benchmark/v1_report.py
```

### C. Optional targeted stale-ref regression run
If stale-ref payloads are wired into benchmark-visible behavior, reproduce one stale multiline path manually to ensure the returned payload is local and actionable.

**Step 5: Commit benchmark state only if code changed**

No need to commit result artifacts if they remain ignored. If benchmark metadata or prompts changed, commit those code files only.

---

## Task 5: Final validation

**Files:**
- Verify all touched files

**Step 1: Run Go tests**

```bash
go test ./...
```
Expected: PASS.

**Step 2: Run Python syntax checks**

```bash
python3 -m py_compile benchmark/run.py benchmark/v1_report.py benchmark/prepare.py
```
Expected: PASS.

**Step 3: Render benchmark summary**

```bash
python3 benchmark/v1_report.py
```
Expected:
- both official V1 suites render
- Minimax / Gemma / GLM rows reflect new multiline results

**Step 4: Final integration commit**

```bash
git add internal/editor internal/server benchmark instructions
git commit -m "feat(lapp): improve multiline stability and stale-ref retries"
```

---

## Recommended execution order

1. shared block indentation rebasing
2. stale-ref repair payloads
3. prompt/instruction sync
4. benchmark reruns
5. final validation

---

## Expected impact

| Improvement | Expected metrics impact |
|---|---|
| shared block indentation rebasing | correctness, turns, wall time |
| local stale-ref repair payloads | turns, wall time, correctness |
| prompt/instruction sync | turns, wall time |

---

Plan complete and saved to `docs/plans/2026-04-12-lapp-indent-rebase-and-stale-repair-plan.md`.

Two execution options:

1. Subagent-Driven (this session) — implement task-by-task here
2. Parallel Session (separate) — open a new session and execute from the saved plan
