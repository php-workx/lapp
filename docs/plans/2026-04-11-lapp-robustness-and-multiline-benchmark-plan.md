# Lapp Robustness and Multiline Benchmark Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Improve lapp robustness across weaker models by reducing grep/ref parsing ambiguity, then rerun a cleaner multiline benchmark suite with first-class metric tracking.

**Architecture:** Extend lapp in two directions: (1) tool ergonomics and error recovery (`lapp_grep`, ref parsing, diagnostics), and (2) benchmark realism and observability (suite-driven runs, structured result comparison). The key design principle is to reduce model guesswork: whenever the model currently has to parse freeform text or infer the right range, add a machine-oriented affordance or a sharper diagnostic.

**Tech Stack:** Go (`internal/server`, `internal/editor`, `pkg/hashline`), Python benchmark harness (`benchmark/run.py`, `benchmark/v1_report.py`), OpenCode instruction file (`instructions/lapp-tools.md`), Go test, Python CLI scripts.

---

## Scope and success criteria

### In scope
1. Multi-line exact block search support
2. Structured grep output support
3. Full display-line ref acceptance
4. More specific error messages for common model mistakes
5. A/B benchmark support for grep output variants
6. New multiline benchmark reruns on the current preferred models

### Out of scope
- Replacing native file tools in OpenCode/Claude/Codex by default
- Full semantic correctness oracle beyond patch similarity
- New benchmark suites beyond the current V1 single-line and multiline suites

### Success criteria
- At least one machine-friendly multiline locator path exists (`lapp_find_block` or equivalent)
- `lapp_grep` can return either text or structured output
- `lapp_edit` accepts anchors copied from display output without manual cleanup
- Common recovery failures produce explicit, actionable error messages
- The benchmark can compare text grep vs structured grep variants using the four first-class metrics
- A fresh multiline benchmark run exists for the preferred model set using the improved tooling

---

## Task 1: Add multiline exact block search

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Modify: `instructions/lapp-tools.md`
- Optional docs note later: `README.md`

**Step 1: Choose the surface area**

Implement a dedicated tool rather than overloading plain grep:
- Tool name: `lapp_find_block`
- Params:
  - `path` (required)
  - `content` (required)
  - `literal` (optional, default true)
- Return:
  - file path
  - `start` ref
  - `end` ref
  - preview lines (optional)

Rationale: this is easier for models to reason about than `lapp_grep(multiline=true)`.

**Step 2: Write failing tests first**

Add tests in `internal/server/server_test.go` for:
1. exact multi-line block found once → returns correct `start` and `end`
2. repeated first line / repeated last line → exact block still resolves correctly
3. no match → clear error / empty result shape
4. block copied directly from file content with indentation preserved

**Step 3: Run the failing tests**

Run:
```bash
go test ./internal/server -run 'TestFindBlock' -v
```
Expected: FAIL because tool does not exist yet.

**Step 4: Implement minimal server handler**

In `internal/server/server.go`:
- register `lapp_find_block`
- read file through existing file IO path
- split `content` into lines
- exact block scan over file lines
- when found, compute `start` and `end` refs using existing hashline helpers
- return a tool result that is easy to reuse directly in `lapp_edit`

**Step 5: Re-run tests**

Run:
```bash
go test ./internal/server -run 'TestFindBlock' -v
```
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
 git commit -m "feat(lapp): add lapp_find_block for exact multiline range lookup"
```

---

## Task 2: Add structured grep output

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Modify: `instructions/lapp-tools.md`
- Modify later: `benchmark/run.py`

**Step 1: Add output format parameter**

Extend `lapp_grep` with:
- `format`: `"text" | "structured"`
- default: `"text"`

Text mode must stay backward compatible.

Structured mode should return per match:
- `path`
- `anchor`
- `line`
- `line_number`
- `context_before`
- `context_after`

**Step 2: Write failing tests first**

Add tests for:
1. `format="structured"` returns valid JSON
2. anchor matches the same LINE#HASH seen in text mode
3. context arrays are bounded by requested context lines
4. multiple matches in one file return multiple entries

**Step 3: Run failing tests**

```bash
go test ./internal/server -run 'TestGrepStructured' -v
```
Expected: FAIL.

**Step 4: Implement structured response path**

In `handleGrep`:
- keep current text rendering path untouched
- add a structured result builder from the same internal match set
- prefer reusing a shared internal representation so text/structured are parallel views of one search result

**Step 5: Re-run tests**

```bash
go test ./internal/server -run 'TestGrepStructured' -v
```
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
 git commit -m "feat(lapp_grep): add structured output mode"
```

---

## Task 3: Accept full display-line refs

**Files:**
- Modify: `internal/editor/editor.go`
- Modify: `internal/server/server_test.go`

**Step 1: Write failing tests first**

Add regression tests proving these all work:
- `anchor: "245#BS:"`
- `anchor: "245#BS:        full line text"`
- `start/end` with full display-line refs

These tests should mirror what a model would paste directly from `lapp_read` / `lapp_grep` output.

**Step 2: Run failing tests**

```bash
go test ./internal/server -run 'TestRoundTrip_ReadEditAccepts' -v
```
Expected: FAIL for the full-display-line case.

**Step 3: Implement normalization**

In `internal/editor/editor.go`:
- extend `normalizeRef()` so it extracts the `LINE#HASH` prefix even when full line content follows the colon
- preserve `0:` and `EOF:` semantics exactly
- ensure self-correct detection, address parsing, and hash verification all use the same normalization path

**Step 4: Re-run tests**

```bash
go test ./internal/server -run 'TestRoundTrip_ReadEditAccepts' -v
```
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/editor/editor.go internal/server/server_test.go
 git commit -m "feat(lapp_edit): accept full display-line refs copied from read/grep output"
```

---

## Task 4: Make error messages precise and model-oriented

**Files:**
- Modify: `internal/editor/editor.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Modify: `instructions/lapp-tools.md`

**Step 1: Enumerate concrete error classes**

Implement specific diagnostics for:
1. trailing-colon refs
2. full display-line refs
3. no-match literal grep with regex-escaped punctuation
4. multiline replacement attempted with single anchor
5. stale refs with remapping already available

**Step 2: Write failing tests first**

Add tests that assert the message text contains the exact correction, for example:
- `Malformed anchor "245#BS:" — use "245#BS"`
- `This looks like a multi-line replacement; use start and end refs`
- `No literal match found; if the term contains \[ or \. copied from regex syntax, retry with the exact file text`

**Step 3: Run failing tests**

```bash
go test ./internal/server -run 'Test(Edit|Grep)Error' -v
```
Expected: FAIL.

**Step 4: Implement message improvements**

Keep messages short, imperative, and directly reusable by the model.

**Step 5: Re-run tests**

```bash
go test ./internal/server -run 'Test(Edit|Grep)Error' -v
```
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/editor/editor.go internal/server/server.go internal/server/server_test.go instructions/lapp-tools.md
 git commit -m "improve(lapp): add explicit model-recovery error messages"
```

---

## Task 5: Add benchmark A/B support for grep output variants

**Files:**
- Modify: `benchmark/run.py`
- Modify: `benchmark/v1_suites.json`
- Modify: `benchmark/v1_report.py`

**Step 1: Extend benchmark metadata**

Add benchmark variant metadata:
- `grep_format`: `text` or `structured`
- default current V1: `text`

**Step 2: Add runner support**

Support an env var or CLI flag such as:
- `LAPP_GREP_FORMAT=structured`

Prompt B-side should change accordingly:
- text mode: “use lapp_grep with literal=true to find the line”
- structured mode: “use lapp_grep with format=structured and use the returned anchor directly”

**Step 3: Add report support**

`benchmark/v1_report.py` should be able to group by:
- suite
- model
- grep format variant

**Step 4: Run side-by-side benchmark**

For the current strongest single-line models:
- DeepSeek
- Gemma4
- Kimi
- Devstral

Run both:
- text grep mode
- structured grep mode

Compare all four first-class metrics equally.

**Step 5: Commit**

```bash
git add benchmark/run.py benchmark/v1_report.py benchmark/v1_suites.json
 git commit -m "feat(benchmark): add grep output variant benchmarking"
```

---

## Task 6: Refresh multiline V1 benchmark suite

**Files:**
- Modify if needed: `benchmark/v1_suites.json`
- Use existing: `benchmark/prepare.py`, `benchmark/run.py`, `benchmark/v1_report.py`

**Step 1: Confirm suite purity**

The multiline V1 suite must contain only:
- pure replacement hunks
- no insertion-only or deletion-only edge cases unless we explicitly add a third suite for those

If needed, adjust `targeted-multiline-pure-replacement` instance membership.

**Step 2: Re-fetch fixtures if suite changes**

```bash
BENCHMARK_IDS=<comma-separated-ids> python benchmark/prepare.py
```

**Step 3: Run the suite on the preferred model set**

Use the currently strongest single-line models first:
- `ollama-cloud/deepseek-v3.1:671b`
- `ollama-cloud/gemma4:31b`
- `ollama-cloud/kimi-k2:1t`
- `ollama-cloud/devstral-2:123b`
- `ollama-cloud/glm-5.1`

Run:
```bash
AGENT=opencode OPENCODE_MODEL='<model>' python benchmark/run.py --suite targeted-multiline-pure-replacement
```

**Step 4: Verify benchmark output**

```bash
python benchmark/v1_report.py
```
Expected:
- both suites appear
- multiline rows are complete or clearly show incomplete pairs

**Step 5: Commit if suite membership changed**

```bash
git add benchmark/v1_suites.json benchmark/files/
 git commit -m "chore(benchmark): refresh V1 multiline suite fixtures"
```

---

## Task 7: Final validation

**Files:**
- Verify all changed files

**Step 1: Run Go tests**

```bash
go test ./...
```
Expected: PASS.

**Step 2: Run benchmark script sanity checks**

```bash
python3 -m py_compile benchmark/run.py benchmark/v1_report.py benchmark/prepare.py
```
Expected: PASS.

**Step 3: Verify V1 report renders both suites**

```bash
python benchmark/v1_report.py
```
Expected:
- one section for `targeted-singleline`
- one section for `targeted-multiline-pure-replacement`
- four first-class metrics visible for each model

**Step 4: Commit final integration**

```bash
git add internal/editor internal/server benchmark instructions
 git commit -m "feat(lapp): improve grep/ref robustness and refresh V1 multiline benchmark"
```

---

## Recommended execution order

1. `lapp_find_block`
2. structured grep output
3. full display-line ref acceptance
4. specific error messages
5. benchmark A/B for grep output formats
6. rerun multiline V1 suite
7. final validation

---

## Expected impact by improvement

| Improvement | Main metric(s) expected to improve |
|---|---|
| `lapp_find_block` / multiline exact block locator | correctness, turns, wall time |
| structured grep output | correctness, turns, maybe input tokens |
| full display-line ref acceptance | correctness, turns |
| better specific error messages | correctness, turns, wall time |
| grep output variant benchmark | measurement quality, future optimization guidance |

---

Plan complete and saved to `docs/plans/2026-04-11-lapp-robustness-and-multiline-benchmark-plan.md`.

Two execution options:

1. Subagent-Driven (this session) — implement task-by-task here
2. Parallel Session (separate) — open a new session and execute from the saved plan
