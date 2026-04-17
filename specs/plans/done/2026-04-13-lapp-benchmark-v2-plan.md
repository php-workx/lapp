# Lapp Benchmark V2 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a Benchmark V2 family that measures lapp across file-size buckets, change-count buckets, and strategy families while preserving the same first-class metrics used in V1.

**Architecture:** Keep V1 as the stable baseline. Add V2 as a separate benchmark family, not a replacement. For Milestone 1, implement exactly one concrete V2 suite with one concrete real-world case, then expand only after the framework is stable.

**Tech Stack:** Python benchmark harness (`benchmark/run.py`, `benchmark/v1_report.py` plus a V2 report path), suite manifests (`benchmark/v1_suites.json`, `benchmark/v2_suites.json`), prepared fixtures under `benchmark/files/`, ignored result directories under `benchmark/results/`.

---

## Milestone 1 Scope Freeze

**Implement exactly one first V2 suite:** `v2-bulk-edits-same-file`

**Concrete initial suite membership:**
- `mwaskom__seaborn-3069`
- file: `seaborn/_core/plot.py`
- file-size bucket: `large` (>1000 lines, current fixture ~1649 lines)
- change-count bucket: `three-to-five-changes`
- one file, multiple same-file regions

**Initial supported strategy set for Milestone 1:**
- `native-edit`
- `lapp-text-grep`
- `lapp-structured-grep`
- `lapp-replace-block`

**Explicitly deferred from Milestone 1:**
- `native-bottom-up-edit`
- `script-generation`
- `v2-large-file-targeted`
- `v2-sequential-edit-drift`
- `v2-post-write-integrity`

This keeps the first V2 slice mechanically comparable and small enough to validate rigorously.

---

## Objectives

### Benchmark goals
1. Measure lapp on repeated edits in the same file (not just one change)
2. Add explicit metadata for file-size bucket, change-count bucket, and strategy family
3. Preserve the current first-class metrics without collapsing to one score
4. Keep V1 intact so prior runs stay comparable

### Non-goals
- Do not delete or redefine V1 suites
- Do not mix V1 and V2 summaries by default
- Do not benchmark issue-debugging/problem-analysis tasks in Milestone 1
- Do not add semantic correctness or test-execution grading yet

---

## V2 benchmark design

### New benchmark dimensions

#### 1. File size bucket
- `small` — <300 lines
- `medium` — 300-1000 lines
- `large` — >1000 lines

#### 2. Change count bucket
- `one-change`
- `three-to-five-changes`
- `six-to-ten-changes`

#### 3. Strategy family
For Milestone 1 only:
- `native-edit`
- `lapp-text-grep`
- `lapp-structured-grep`
- `lapp-replace-block`

---

## Proposed V2 suites

### Suite A: `v2-bulk-edits-same-file`
Purpose:
- measure repeated edits in the same file
- this is where lapp batching / local helpers should matter most

Milestone 1 exact case:
- `mwaskom__seaborn-3069`
- one file
- 4 changes in the same file
- large-file bucket
- 3-5 change bucket

Requirements:
- no issue analysis required
- prompt tells model exactly what changes to make
- same fixture is used for all four in-scope strategies

### Future suites (deferred)
- `v2-large-file-targeted`
- `v2-sequential-edit-drift`
- `v2-post-write-integrity`

These stay documented but out of scope for Milestone 1.

---

## Data and fixture plan

### Step 1: create a V2 manifest
Create:
- `benchmark/v2_suites.json`

It must include:
- `version: "v2"`
- the same first-class metrics as V1
- dimensions:
  - `file_size_bucket`
  - `change_count_bucket`
  - `strategy`
- one concrete suite:
  - `v2-bulk-edits-same-file`
  - exact instance: `mwaskom__seaborn-3069`

### Step 2: fixture preparation
Use existing `benchmark/prepare.py` to prepare the `mwaskom__seaborn-3069` fixture.
If extra metadata is needed for grouped same-file edits, add it explicitly rather than overloading V1 fixture semantics.

### Step 3: optional candidate curation helper
A small helper like `benchmark/select_v2_candidates.py` is allowed, but not required for Milestone 1 if the single exact case is already chosen.

---

## Harness changes

### 1. Add V2 suite support
Current runner already supports `--suite`, but only against `v1_suites.json`.

Implement:
- `benchmark/run.py --suite-file <path>`
- default remains V1
- V2 suites load from `benchmark/v2_suites.json`

### 2. Add strategy metadata
Every V2 result JSON should store:
- `strategy`
- `file_size_bucket`
- `change_count_bucket`

### 3. Add V2 report support
Create either:
- `benchmark/v2_report.py`
or generalize the reporting path without breaking V1.

Milestone 1 report requirements:
- per-suite table
- per-strategy comparison table
- the same first-class metrics visible side by side

### 4. Preserve canonical result layout
Use:
- `benchmark/results/<suite>/<agent__model>[__variant]/`

Do not reintroduce ad-hoc `-v2` dir naming.

---

## Milestone 1 strategy comparison rules

Only compare these four strategy variants in the first implementation:
- `native-edit`
- `lapp-text-grep`
- `lapp-structured-grep`
- `lapp-replace-block`

Do not implement bottom-up or script-generation strategy support in this milestone.
Those remain future work.

---

## Validation requirements

Milestone 1 is complete only when:
1. `benchmark/v2_suites.json` exists and validates
2. the runner can execute suites from `--suite-file`
3. result JSON includes strategy + bucket metadata
4. V2 reporting renders the suite
5. the `v2-bulk-edits-same-file` suite has been run on at least 3 models

Validation commands:
```bash
go test ./...
python3 -m py_compile benchmark/run.py benchmark/prepare.py benchmark/v1_report.py [new v2 scripts]
```

And at least one smoke benchmark run must complete successfully.

---

## Phased implementation order

### Wave 1
1. create `benchmark/v2_suites.json` with the concrete suite
2. add `--suite-file` support to the runner
3. add strategy + bucket metadata to results

### Wave 2
4. add V2 report support
5. prepare the `mwaskom__seaborn-3069` fixture and any metadata needed for grouped edits

### Wave 3
6. run the first V2 suite on:
   - DeepSeek
   - Kimi
   - Gemma4
   - Devstral
   - Minimax

---

## Success criteria

This plan is complete when:
1. V2 exists as a real benchmark family, not just notes
2. the first V2 suite is concrete and reproducible
3. the first V2 report compares the four in-scope strategies on one real same-file bulk-edit case
4. V1 remains untouched and comparable

---

## Suggested first implementation ticket set
1. Add `benchmark/v2_suites.json` with exact suite membership
2. Add suite-file support to runner
3. Add strategy metadata to result JSON
4. Create `v2-bulk-edits-same-file` using `mwaskom__seaborn-3069`
5. Run DeepSeek / Kimi / Gemma4 / Devstral / Minimax on that suite
