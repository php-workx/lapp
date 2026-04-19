# Technical Specification: Project `lapp` (MVP)

**Version:** 2.1.0
**Status:** Ready for Implementation
**Core Philosophy:** Trust the LLM for intent, but verify everything with strict AST logic before touching the disk.

---

## 1. System Architecture & Boundaries

`lapp` is a local process launched on-demand by the MCP client, communicating via Stdio. It is strictly divided into four isolated domains:

1. **Transport Layer:** Handles MCP JSON-RPC routing.
2. **AST Slicer (Read):** Parses files, executes tree queries, extracts byte-exact source hunks.
3. **Semantic Merger (Compute):** Interfaces with Ollama to expand `// ...` markers.
4. **Mutator & Verifier (Write):** Re-parses the merged output, validates safety, and performs atomic disk writes.

### 1.1 Target MVP Scope

- **Languages Supported:** Go (`.go`), Python (`.py`), TypeScript (`.ts`)
- **Local Model Target:** See §6.3 for model selection guidance. Default: `qwen2.5-coder:3b` via local Ollama endpoint (`http://localhost:11434`)

---

## 2. Technical Stack

| Layer | Component | Selection |
| :--- | :--- | :--- |
| **Runtime** | Language | Golang 1.24+ |
| **Interface** | Protocol | MCP (Model Context Protocol) |
| **Parser** | AST Engine | Tree-sitter via `odvcencio/gotreesitter` (pure-Go, no CGO) |
| **Inference** | Local LLM | `qwen2.5-coder:3b` via Ollama (see §6.3 for alternatives) |
| **Communication** | Transport | JSON-RPC over Stdio |

---

## 3. Directory Structure

```
lapp/
├── cmd/
│   └── lapp-mcp/          # Main entrypoint, dependency injection
├── internal/
│   ├── mcp/               # stdio server, tool definitions
│   ├── ast/               # Tree-sitter parsers, queries, offset math
│   ├── llm/               # Ollama client, prompt templates, retry logic
│   └── mutator/           # Atomic file writes, diffing, rollback
├── pkg/
│   └── types/             # Shared domain structs (Hunk, PatchResult)
├── go.mod
└── Makefile
```

---

## 4. Data Contracts

Defining the exact structs ensures no hallucinated data passing between layers.

```go
// pkg/types/types.go

// InputFormat is the detected format of the incoming patch.
type InputFormat int

const (
    FormatLazyPatch  InputFormat = iota // Contains // ... or # ... markers
    FormatUnifiedDiff                    // Standard unified diff
    FormatFullRewrite                    // Complete function, no markers
)

// PatchRequest represents the incoming MCP payload.
type PatchRequest struct {
    Filepath string `json:"filepath"`
    Symbol   string `json:"symbol"`
    Patch    string `json:"patch"` // Lazy patch, unified diff, or full rewrite
}

// SourceHunk represents the extracted AST node.
type SourceHunk struct {
    Filepath  string
    Symbol    string
    Language  string
    Content   string
    StartByte uint32
    EndByte   uint32
}

// ApplyResult is returned to the MCP client on success.
type ApplyResult struct {
    Filepath    string `json:"filepath"`
    Symbol      string `json:"symbol"`
    Format      string `json:"format"`       // "lazy_patch", "unified_diff", or "full_rewrite"
    LinesChanged int   `json:"lines_changed"`
    Diff        string `json:"diff"`          // Unified diff of the full file (old vs new), for agent awareness
}

// ApplyError is returned to the MCP client on failure.
type ApplyError struct {
    Code    string `json:"code"`    // e.g. "ERR_SYNTAX_INVALID"
    Message string `json:"message"` // Human-readable explanation
    Detail  string `json:"detail"`  // Optional: diverged lines, match list, etc.
}
```

---

## 5. The Lazy Patch Format

### 5.1 Design Constraint: We Don't Control the Caller

The calling agent (Claude, GPT, Gemini, etc.) sees only the MCP tool description and input schema. It will not read this spec. It will not follow rigid formatting rules. It will produce code edits the way it naturally does — with `// ...` markers, varying amounts of context, and inconsistent phrasing.

This means the format cannot be a strict contract that we enforce on the caller. Instead it has three layers:

1. **Guidance** — the tool description nudges the agent toward good input (include context lines, use `// ...` for unchanged blocks). This is our only instruction surface.
2. **Tolerance** — the merger (LLM) must handle natural variation in how agents produce lazy patches. This is the core reason we use an LLM instead of a deterministic algorithm.
3. **Rejection boundary** — structural problems that are unresolvable even for an LLM (e.g., the patch is just `// ...` with no context at all). These are caught cheaply before inference.

### 5.2 What Frontier Models Naturally Produce

Based on observed behavior of Claude, GPT-4, and Gemini when editing code:

- They already use `// ...` or `# ...` as shorthand for "the rest stays the same" — this is an ingrained pattern, not something we need to teach.
- They generally include the function signature and closing delimiter.
- They include context lines around changes (enough for a human reader to understand the edit).
- They use variations like `// ... rest of function`, `// ... (unchanged)`, `// ...existing validation...` — all meaning the same thing.
- They sometimes omit context between adjacent unchanged regions (producing back-to-back markers).
- They occasionally produce the full function with no markers at all (no `// ...`, just the complete rewrite).

The merger must handle all of these gracefully. Rigid formatting requirements would cause the tool to reject valid input from well-behaved agents.

### 5.3 Ideal Format (Tool Description Guidance)

The tool description should nudge the agent toward patches that are easiest to resolve. The ideal patch:

1. Includes the function/class signature as the first line (top anchor).
2. Includes the closing delimiter as the last line (bottom anchor).
3. Uses `// ...` or `# ...` to replace contiguous unchanged lines.
4. Includes at least one unchanged context line between markers (anchoring).
5. Writes changed/added lines literally.

**Example — original source (extracted by the AST Slicer):**
```go
func processOrder(order Order) error {
    if order.ID == "" {
        return fmt.Errorf("missing ID")
    }
    if order.Amount <= 0 {
        return fmt.Errorf("invalid amount")
    }
    result, err := db.Save(order)
    if err != nil {
        return err
    }
    notifyService.Send(result)
    return nil
}
```

**Ideal lazy patch:**
```go
func processOrder(order Order) error {
    // ...
    result, err := db.Save(order)
    if err != nil {
        log.Error(err)
        return err
    }
    // ...
}
```

How the apply model resolves this:
- Function signature — top anchor
- First `// ...` — everything between the signature and the context line `result, err := db.Save(order)` (both `if` blocks)
- Changed region — `log.Error(err)` is new; `return err` is context
- Second `// ...` — everything between `}` (closing the if) and `}` (closing the function): `notifyService.Send(result)` and `return nil`
- Closing `}` — bottom anchor

### 5.4 Format Detection and Routing

Before any processing, `lapp` detects the input format and routes it to the appropriate handler. This happens deterministically — no LLM call.

```
lazy_patch input
  │
  ├─ Unified diff?       → deterministic apply → verifier → write
  ├─ Full rewrite?       → skip merger         → verifier → write
  ├─ Lazy patch (markers) → LLM merger         → verifier → write
  └─ Unrecognizable       → ERR_INVALID_PATCH
```

**Detection heuristics (checked in order):**

| Format | Detection | Handler |
| :--- | :--- | :--- |
| **Unified diff** | Contains a unified diff header: a line matching `^@@ -\d+,?\d* \+\d+,?\d* @@`, optionally preceded by `^--- ` and `^\+\+\+ ` lines. Single `+`/`-` prefixes alone are NOT sufficient (they appear in normal code). | Parse diff, apply hunks to original source deterministically. No LLM needed. Use a standard Go patch library. |
| **Full rewrite** | Contains no recognized markers (§5.6) and is not a unified diff. The agent wrote the complete function. | Pass directly to verifier. No LLM needed. |
| **Lazy patch** | Contains one or more recognized markers (§5.6). | Forward to LLM merger (§6.3). |
| **Unrecognizable** | None of the above, and content doesn't parse as valid code for the target language. | Return `ERR_INVALID_PATCH`. |

**Why handle unified diff:** Some models (especially GPT-4) have a strong trained habit of producing unified diffs when asked to edit code. Rejecting these would make the tool fail on valid, well-structured input. Since unified diffs have precise deterministic semantics, we can apply them without an LLM — making this the fastest and most reliable path.

**Unified diff scope:** The diff is applied to the `SourceHunk` extracted by the slicer (not the full file). The slicer still runs first to locate the symbol and extract its byte range. The diff applier treats the hunk as the base text. If the diff references lines outside the hunk (e.g., the agent generated a diff of the full file), the applier returns `ERR_DIFF_APPLY_FAILED`. This keeps all three paths at hunk scope.

**Why handle full rewrites:** If the calling agent simply rewrites the whole function, we should accept it. Rejecting correct output because the agent didn't use markers would be hostile UX. Note that this is a **degraded mode** — `lapp` adds only syntax checking and atomic writes, not the merge intelligence. The value of `lapp` is strongest on the lazy patch path.

### 5.5 Tolerance: Variations the Merger Must Handle

For input routed to the LLM merger (lazy patch format), these variations must be handled:

| Variation | Example | How to handle |
| :--- | :--- | :--- |
| Marker with trailing description | `// ... validation logic` | Strip to `// ...` during marker normalization (see below) |
| Marker with different phrasing | `// rest of function unchanged` | Recognize via regex (§5.6), normalize to `// ...` |
| Missing function signature | Patch starts mid-body | Prepend the original signature from the `SourceHunk` |
| Missing closing delimiter | Patch ends before the last `}` | Append the original closing from the `SourceHunk` |
| Adjacent markers (no context between) | `// ...\n// ...` | Forward to the LLM (it may resolve from broader context) but accept higher risk of incorrect merge |
| Indentation mismatch | Tabs vs spaces, 2-space vs 4-space | Delegate to the LLM — it receives both the original (with correct indentation) and the patch. The prompt says "preserve original indentation exactly." Deterministic normalization is deferred to post-MVP. |

**Marker normalization order:** Before the LLM is called, all recognized markers (§5.6) are normalized to the canonical form (`// ...` for Go/TS, `# ...` for Python). This means the LLM prompt only needs to handle one marker syntax, not the full variation set.

### 5.6 Marker Recognition

A line is recognized as a marker if it matches (after trimming leading whitespace):

- `// ...` followed by anything (Go, TypeScript)
- `# ...` followed by anything (Python)
- `// rest of` ... , `// remaining` ... , `// unchanged` ... (common agent phrasings)
- `# rest of` ... , `# remaining` ... , `# unchanged` ... (Python equivalents)

The recognizer should be a simple regex, not an LLM call.

### 5.7 Pre-flight Rejection (Before LLM)

Reject with `ERR_AMBIGUOUS_PATCH` before invoking the LLM if:
- The patch is *only* markers (no changed lines at all — the agent sent a no-op)

Note: "no markers AND byte-identical to original" is NOT checked here — that case is already classified as "full rewrite" by the format detector (§5.4) and handled on the full rewrite path.

All other cases — including adjacent markers, missing anchors, weird phrasing — should be forwarded to the LLM merger. The LLM is the tolerance layer; over-aggressive pre-flight rejection defeats the purpose.

---

## 6. Component Specifications

### 6.1 The MCP Interface (`internal/mcp`)

The server exposes a single tool: `lapp_apply`.

**Inputs:**
- `filepath` (string, required) — absolute path to the target file
- `symbol` (string, required) — the function/class name to modify
- `patch` (string, required) — the edit: a lazy patch (§5), unified diff, or full rewrite

**Tool description (shown to the calling agent):**
> "Surgically patches a specific function or class by name. Provide the edit as a lazy patch with `// ...` for unchanged blocks, a unified diff, or a full rewrite. Preferred format — lazy patch with context anchors:
> ```
> func foo(x int) int {
>     // ...
>     result := x * 2  // changed
>     // ...
> }
> ```
> This is faster and cheaper than rewriting the whole file."

**Input schema:** JSON schema enforcing `filepath`, `symbol`, and `patch` as required strings.

**Error codes returned to the calling agent:**
- `ERR_SYMBOL_NOT_FOUND` — no AST node matches `symbol`; agent should re-examine the file
- `ERR_SYMBOL_AMBIGUOUS` — multiple matches for `symbol` with similar confidence; response includes the match list so the agent can refine
- `ERR_SYNTAX_INVALID` — merged result has parse errors; agent should rewrite the patch
- `ERR_MERGE_DRIFT` — verifier detected the merger altered unchanged regions; includes which lines diverged
- `ERR_MERGE_AMBIGUOUS` — the LLM could not resolve one or more markers (insufficient context anchors)
- `ERR_DIFF_APPLY_FAILED` — unified diff hunk(s) failed to apply (context mismatch)
- `ERR_AMBIGUOUS_PATCH` — patch is only markers with no changes (no-op); agent should include actual edits
- `ERR_INVALID_PATCH` — input is not recognizable as a lazy patch, unified diff, or full rewrite
- `ERR_OLLAMA_UNAVAILABLE` — Ollama is not running or model not pulled; response includes setup instructions

**Success response:** Returns an `ApplyResult` (§4) containing the filepath, symbol, detected format, number of lines changed, and a unified diff of the actual changes. The diff gives the calling agent visibility into what `lapp` did — critical for agents that need to verify or report their edits.

**Logic:** Coordinates the Slicer → Merger → Verifier → Writer pipeline.

---

### 6.2 The AST Slicer (`internal/ast`)

Instead of processing the whole file, the Slicer isolates only the target symbol.

#### 6.2.1 Language Registry

Use `github.com/odvcencio/gotreesitter` — a ground-up pure-Go reimplementation of the tree-sitter runtime with 206 bundled grammars. No CGO, no C toolchain required. Grammar tables are extracted from upstream `parser.c` files and compiled into Go-native binary blobs embedded in the binary.

| Extension | Grammar | Auto-detected by |
| :--- | :--- | :--- |
| `.go` | Go | `grammars.DetectLanguage("file.go")` |
| `.py` | Python | `grammars.DetectLanguage("file.py")` |
| `.ts` | TypeScript | `grammars.DetectLanguage("file.ts")` |

**Performance:** Full parse is ~2.4x slower than native C tree-sitter (e.g., 4.2ms vs 1.76ms for a 500-function Go file). This is a non-issue for an MCP tool processing single files.

**Build portability:** `go install` works on every `GOOS`/`GOARCH` target — macOS (ARM/Intel), Linux, Windows — without a C compiler. This is critical for adoption.

**Risk:** The library is young (created Feb 2026, v0.13.2). API may evolve. However, velocity is high, architecture is sound (not a transpilation), and Go/Python/TypeScript grammars are confirmed present and tested.

#### 6.2.2 Symbol Lookup (Language-Generic)

The slicer uses a **single, mostly language-agnostic algorithm** rather than per-language S-expression queries. Tree-sitter grammars across languages share a structural convention: declaration/definition nodes have a child with field name `name` that is some form of identifier.

**Algorithm:**

1. Parse the file with the appropriate language grammar.
2. Walk the full AST. For every node that is a **declaration-like construct** (see filter below), check whether it has a child with field name `name` whose text equals `symbol_name`.
3. Return the matching node's byte range as the `SourceHunk`.

**Declaration-like node filter:** A node is a candidate if its type matches any of:
- `*_declaration` (Go `function_declaration`, TS `function_declaration`, `class_declaration`)
- `*_definition` (Python `function_definition`, `class_definition`)
- `method_declaration`, `method_definition`
- `variable_declarator` where the value is a function/arrow expression (TS `const foo = () => {}`)

This is a whitelist based on the three MVP languages. Adding a new language may require adding its declaration node types. This is a one-line addition per type, not a full query.

**Why the previous approach was wrong:** Searching for all identifier nodes matching the name would hit call sites, variable references, type annotations, and import paths — then walking up from those would find the *enclosing* function, not the *target* function. Filtering at the declaration level eliminates these false positives.

**Go-specific: receiver type matching.** For Go methods, `method_declaration` doesn't have a `name` field for the receiver type — the receiver is in a separate `parameter_list` child. To support qualified names like `OrderProcessor.Validate` (see §6.2.3), the Go path must inspect the receiver's type. This is the one piece of language-specific logic in the slicer.

**Byte offsets only** — never line numbers, as LLMs frequently miscount lines. `StartByte` and `EndByte` from the tree-sitter node are absolute.

#### 6.2.3 Qualified Symbol Names

The `symbol` parameter supports an optional parent qualifier using dot notation:

- `validate` — matches any function/method named `validate`
- `OrderProcessor.validate` — matches `validate` only when it is nested inside an `OrderProcessor` class/struct

**Resolution:** If the symbol contains a `.`, split on the last dot. The suffix is the target identifier; the prefix is the expected parent. After step 3 of the lookup algorithm (§6.2.2), check whether the enclosing declaration's own name matches the prefix. Discard matches where it doesn't.

This handles the common case (method inside a class) without inventing a full namespace syntax. Go receiver types are a special case: `(o *OrderProcessor).Validate` is awkward, so for Go, `OrderProcessor.Validate` matches a method declaration whose receiver type is `OrderProcessor`.

#### 6.2.4 Multiple Symbol Matches

After applying the qualified name filter (if any), multiple nodes may still match:

1. If only one match exists, use it.
2. If multiple matches exist, compare each match's source text against the `lazy_patch` using **line-level longest common subsequence (LCS)**. Select the match with the highest LCS ratio (shared lines / total lines). This is a cheap, deterministic heuristic.
3. If the top two matches have LCS ratios within 10% of each other (genuinely ambiguous), return `ERR_SYMBOL_AMBIGUOUS` with the list of matches (including their byte offsets and first line of source) so the calling agent can refine its request using a qualified name.

This avoids both the vagueness of "highest similarity" and the cost of an LLM call for disambiguation.

---

### 6.3 The Semantic Merger (`internal/llm`)

The LLM is treated as a highly unreliable text generator and is constrained accordingly.

This component implements the "planning/applying" decomposition described by Cursor's Fast Apply research: the calling agent (Claude, etc.) *plans* the edit as a lazy patch with `// ...` markers; `lapp`'s merger *applies* it by resolving those markers against the original hunk. The LLM is needed here because marker resolution is context-dependent and not reliably solvable with deterministic string matching when markers are ambiguous or span non-contiguous blocks.

#### 6.3.1 Model Selection

The model is configurable via the `LAPP_MODEL` environment variable (default: `qwen2.5-coder:3b`).

| Tier | Model | Size | Apple Silicon | x86 CPU-only | Use case |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Development default** | `qwen2.5-coder:3b` | ~2GB | ~150 t/s | ~30 t/s | Fast iteration, any machine |
| **Production default** | `qwen2.5-coder:7b` | ~4.7GB | ~50 t/s | ~10 t/s | Better quality, still CPU-safe |
| **High quality (opt-in)** | `qwen3-coder-next:q4_K_M` | 52GB | GPU/64GB+ RAM only | Not viable | Machines with serious hardware |

Start with `qwen2.5-coder:3b`. If the verifier's rejection rate is unacceptably high in testing, step up to `qwen2.5-coder:7b`. The model swap requires no code changes.

**Realistic latency targets** (hunk-level merge, 50-200 token output):
- Apple Silicon (Metal): 1-5 seconds
- x86 CPU-only: 5-20 seconds

#### 6.3.2 Ollama Client

- **Endpoint:** `POST http://localhost:11434/api/generate`
- **Timeout:** 30-second hard context timeout
- **Parameters:** `temperature: 0.0` (near-deterministic — floating-point and scheduling variance may cause minor differences across runs, but output is maximally constrained)

#### 6.3.3 Retry Policy

If the LLM returns output that fails verification (syntax error, merge drift, or ambiguous marker), `lapp` retries **once** with the same prompt. LLM non-determinism at temperature 0 means a second attempt may succeed where the first failed. If the retry also fails, return the error to the calling agent. No further retries — the agent is better positioned to adjust its patch than `lapp` is to adjust its prompt.

Transient Ollama errors (HTTP 500, timeout) are retried up to 2 times with 1-second backoff before returning `ERR_OLLAMA_UNAVAILABLE`.

#### 6.3.4 Merge Prompt

The prompt uses separated, labeled blocks (inspired by the LLM-optimized diff format from the codereview project). Separating original and patch into distinct sections avoids interleaving confusion and lets the model process each independently.

```
SYSTEM: You are a code-merging engine. You receive an [ORIGINAL] block and a [PATCH] block.

The [PATCH] contains `// ...` or `# ...` markers that represent unchanged code from [ORIGINAL].
Lines in [PATCH] that are NOT markers are either context lines (identical to [ORIGINAL]) or changed/added lines.
Context lines are anchors — they tell you where each marker's boundaries are.

PROCEDURE:
1. Walk through [PATCH] top to bottom.
2. For each `// ...` marker, find the region in [ORIGINAL] between the nearest context lines above and below. Copy that region verbatim.
3. For all other lines in [PATCH], output them as-is.

RULES:
- Output ONLY the merged code. No markdown fences. No explanations.
- Preserve original indentation exactly.
- If a marker cannot be resolved (no anchoring context lines), output the single line: ERROR_AMBIGUOUS_MARKER

[ORIGINAL]
{SOURCE_CONTENT}

[PATCH]
{LAZY_PATCH_CONTENT}
```

**Why this works better than the previous prompt:** The explicit PROCEDURE gives the model a mechanical algorithm (walk, match anchors, copy) rather than a vague "replace markers with corresponding lines." The separated blocks avoid the model confusing source with patch content. The anchor concept is named explicitly so the model knows *how* to match.

**Sentinel handling:** Before passing the LLM output to the verifier, check if it contains the string `ERROR_AMBIGUOUS_MARKER`. If found, do NOT run the verifier (it would report a misleading syntax error). Instead, return `ERR_MERGE_AMBIGUOUS` directly to the calling agent with a message indicating which marker(s) could not be resolved.

#### 6.3.5 Quantization

Use 4-bit quantization (Q4_K_M). All recommended models above are distributed in this format by default on Ollama.

---

### 6.4 The Mutator & Verifier (`internal/mutator`)

This is the firewall. If the LLM generates bad code, this layer catches it before the file is corrupted.

**What the verifier CAN catch:** syntax errors, missing symbols, structural drift.
**What the verifier CANNOT catch:** semantic errors (e.g., the LLM swapped two blocks of code — both blocks are syntactically valid but in the wrong positions). This is an inherent limitation. The mitigation is the context-line anchoring in the lazy patch format (§5.3), which reduces the chance of block-swap errors in the merger, and the diff safety check (§6.4.3) which catches gross deviations. True semantic verification would require running the code, which is out of scope for MVP.

#### 6.4.1 Syntax Re-Parsing

After splicing the merged text into the full file (step 2 of §6.4.4), parse the **entire resulting file** using Tree-sitter — not the hunk in isolation. Parsing the hunk alone would produce spurious errors for nested constructs (Python methods indented inside classes, TypeScript class methods, etc.).

- Query the new tree for `(ERROR)` or `(MISSING)` nodes within the byte range of the modified hunk.
- If syntax errors exist in that range, **abort the operation** and return the parser error to the MCP client.
- Errors outside the hunk range are pre-existing and should be ignored.

#### 6.4.2 Symbol Integrity Check

Parse the merged text and verify that a node matching `symbol_name` still exists. This prevents the LLM from accidentally renaming or deleting the function.

#### 6.4.3 Diff Safety Check

The checks applied depend on which format path produced the merged result:

**Lazy patch path (LLM merger):** The highest-risk path. Two complementary heuristics:

- **Context line preservation:** Extract the non-marker lines from the lazy patch (the context lines and changed lines the agent explicitly wrote). Verify that each appears in the merged result at the expected relative position. If context lines are missing or reordered, the merger likely hallucinated.
- **Unchanged region integrity:** For each `// ...` marker in the patch, identify the corresponding region in the original source (using anchor context lines). Verify that those lines appear verbatim in the merged result. If the merger altered lines that should have been copied unchanged, flag it.

If either check fails, abort with `ERR_MERGE_DRIFT` and include a summary of which lines diverged.

**Unified diff path (deterministic apply):** The diff applier is deterministic, so hallucination isn't a concern. The only check needed is that the diff applied cleanly (no failed hunks). If a hunk fails to apply (context lines don't match the source), abort with `ERR_DIFF_APPLY_FAILED`.

**Full rewrite path:** No merge occurred, so no merge-drift check applies. The syntax check (§6.4.1) and symbol integrity check (§6.4.2) are sufficient. The calling agent takes full responsibility for the content.

#### 6.4.4 Atomic File Writing

Never overwrite the source file directly:

1. Read the original file into a byte array.
2. Splice: `file[:StartByte] + mergedText + file[EndByte:]`
3. Write to a temp file: `<filepath>.lapp.tmp`
4. Atomic rename: `os.Rename(tempFile, originalFile)`

#### 6.4.5 File Locking (Concurrency Safety)

The full pipeline (read → slice → merge → verify → write) is not atomic. If two concurrent `lapp_apply` calls target the same file, the second call could read stale content, merge against it, and silently overwrite the first call's changes.

**Mitigation:** Acquire a per-file advisory lock (`<filepath>.lapp.lock`) before reading the original file. Hold it through the entire pipeline. Release after the atomic rename (or on error). Use `flock(2)` on Unix and `LockFileEx` on Windows.

This serializes concurrent operations on the same file. Operations on different files are fully parallel.

The lock file is advisory — it only protects against concurrent `lapp` processes, not against external editors. This is acceptable: if the user is simultaneously editing the same function in their IDE while an agent is patching it via `lapp`, the result is inherently unpredictable regardless of locking.

**Crash safety:** `flock(2)` is process-scoped — the kernel automatically releases the lock when the process dies (panic, SIGKILL). The `.lapp.lock` file itself persists on disk but is inert (it's just a handle, not the lock). No manual cleanup is needed.

---

## 7. Edge Cases & Risk Mitigation

| Risk | Mitigation |
| :--- | :--- |
| **Marker Hallucination** | The merger might replace `// ...` with invented content. The diff safety check (§6.4.3) verifies that unchanged regions are preserved verbatim and context lines are intact. |
| **Semantic Block Swap** | The merger might copy the right lines but in the wrong order. This is NOT catchable by syntax or symbol checks. Mitigation: context-line anchoring (§5.3) constrains where each block can go. This is an accepted MVP limitation — true semantic checks require execution. |
| **Indentation Mismatch** | Python and Go are indentation-sensitive. Delegated to the LLM for MVP (the prompt says "preserve original indentation exactly" and receives the correctly-indented original). Deterministic normalization is post-MVP. |
| **Inference Latency** | If Ollama is cold, the first request will lag. The MCP server sends a `progress` notification to the agent during the "Merging" phase. |
| **Ollama Not Running** | On startup, `lapp` pings `GET http://localhost:11434/api/tags` to verify Ollama is reachable and the configured model is available. If not, it returns `ERR_OLLAMA_UNAVAILABLE` with a human-readable message: `"Ollama is not running or model '{model}' is not pulled. Run: ollama pull {model}"`. This check runs once at server init, not on every request. |
| **Build Portability** | Use `odvcencio/gotreesitter` (§6.2.1) — pure-Go, no CGO. `go install` works on all platforms without a C toolchain. Risk: young library (Feb 2026). Mitigation: the API surface we use is small (parse, walk, query node fields). |

---

## 8. Implementation Plan

Execute phases sequentially. Do not proceed to the next phase until unit tests for the current phase pass.

### Phase 1 — Bootstrapping & Types

- [ ] Initialize `go.mod`, install dependencies:
  - `github.com/odvcencio/gotreesitter` (pure-Go tree-sitter)
  - `github.com/mark3labs/mcp-go` (MCP SDK)
- [ ] Validate tree-sitter library: parse a Go, Python, and TypeScript file; walk the AST; confirm declaration nodes have `name` children with expected field names. This is the C1 risk gate — if the library doesn't support the operations we need, stop and evaluate alternatives.
- [ ] Create directory structure (`cmd/lapp-mcp/`, `internal/{mcp,ast,llm,mutator}/`, `pkg/types/`)
- [ ] Implement all types from §4 (`PatchRequest`, `SourceHunk`, `ApplyResult`, `ApplyError`, `InputFormat`)

### Phase 2 — AST Slicer (`internal/ast`)

- [ ] Implement language registry (§6.2.1): extension → grammar via `grammars.DetectLanguage()`
- [ ] Implement symbol lookup (§6.2.2): walk declaration-like nodes, match `name` field children
- [ ] Implement Go receiver type matching (§6.2.2, Go-specific logic)
- [ ] Implement qualified name resolution (§6.2.3): `Class.method` dot notation
- [ ] Implement LCS-based disambiguation for multiple matches (§6.2.4)
- [ ] **Unit tests:**
  - Extract a Go function by name; verify byte offsets are exact
  - Extract a Go method by qualified name (`OrderProcessor.Validate`); verify receiver matching
  - Extract a Python method inside a class using qualified name
  - Verify call-site identifiers are NOT matched (a function `main` calling `validate` should not return `main`)
  - Provide an ambiguous symbol name; verify `ERR_SYMBOL_AMBIGUOUS` is returned

### Phase 3 — Format Detection & Pre-flight (`internal/detect`)

- [ ] Implement format detector (§5.4): unified diff (full header regex), full rewrite, lazy patch, unrecognizable
- [ ] Implement marker recognizer regex (§5.6)
- [ ] Implement marker normalization (§5.5): all recognized variants → canonical `// ...` or `# ...`
- [ ] Implement pre-flight rejection (§5.7): markers-only no-op detection
- [ ] Implement unified diff applier (deterministic path — no LLM)
- [ ] **Unit tests:**
  - Detect each format correctly (unified diff, full rewrite, lazy patch)
  - Do NOT false-positive on code containing `+`, `-`, or `@@` (e.g., math, Java annotations)
  - Recognize and normalize marker variations (`// ...`, `// ... validation logic`, `// rest of function` → `// ...`)
  - Apply a unified diff to a source hunk; verify correctness
  - Return `ERR_DIFF_APPLY_FAILED` when diff context doesn't match the hunk
  - Reject a patch that is only markers

### Phase 4 — LLM Merger (`internal/llm`)

- [ ] Implement Ollama HTTP client with 30s timeout (§6.3.2)
- [ ] Implement retry policy (§6.3.3): 1 retry on verification failure, 2 retries on transient HTTP errors
- [ ] Implement startup health check: `GET /api/tags` (§7, Ollama Not Running)
- [ ] Implement merge prompt template (§6.3.4)
- [ ] Implement sentinel detection: check LLM output for `ERROR_AMBIGUOUS_MARKER` before verification
- [ ] Support `LAPP_MODEL` environment variable (§6.3.1)
- [ ] **Unit tests:**
  - Mock Ollama endpoint; verify prompt is correctly assembled with normalized markers
  - Mock Ollama returning `ERROR_AMBIGUOUS_MARKER`; verify `ERR_MERGE_AMBIGUOUS` (not `ERR_SYNTAX_INVALID`)
  - Mock Ollama HTTP 500; verify retry + eventual `ERR_OLLAMA_UNAVAILABLE`
  - **Integration test** (requires running Ollama): merge 5 known lazy patches against known sources. **Acceptance: all 5 must pass verification.** If <5 pass, the model is insufficient — escalate to `qwen2.5-coder:7b` and re-run.

### Phase 5 — Mutator & Verifier (`internal/mutator`)

- [ ] Implement syntax re-parsing on the full spliced file (§6.4.1) — errors scoped to modified byte range only
- [ ] Implement symbol integrity check (§6.4.2)
- [ ] Implement diff safety check per format path (§6.4.3): context preservation + unchanged integrity for lazy patches; clean-apply check for diffs; syntax + symbol only for full rewrites
- [ ] Implement atomic file writing with temp file + rename in same directory (§6.4.4)
- [ ] Implement per-file advisory locking via `flock`/`LockFileEx` (§6.4.5)
- [ ] **Unit tests:**
  - Reject syntactically broken merged code (missing closing brace)
  - Reject merge where symbol was renamed/deleted
  - Reject merge where unchanged regions were altered (`ERR_MERGE_DRIFT`)
  - Accept a Python method parsed in the context of its full class file (no spurious indent errors)
  - Verify atomic write doesn't corrupt on simulated crash (write temp, don't rename)
  - Verify concurrent writes to the same file are serialized

### Phase 6 — MCP Wiring & End-to-End

- [ ] Implement MCP stdio server with `lapp_apply` tool (§6.1)
- [ ] Wire the full pipeline: detect → route → (slicer → merger/diff-apply → verifier) → writer
- [ ] Return `ApplyResult` on success, `ApplyError` on failure
- [ ] **End-to-end tests:**
  - Lazy patch path: agent-style patch with `// ...` → merged and written correctly
  - Unified diff path: standard diff → applied and written correctly
  - Full rewrite path: complete function → verified and written correctly (degraded mode)
  - Error paths: bad symbol → `ERR_SYMBOL_NOT_FOUND`; broken merge → `ERR_SYNTAX_INVALID`; ambiguous marker → `ERR_MERGE_AMBIGUOUS`; failed diff → `ERR_DIFF_APPLY_FAILED`
- [ ] Build binary and test inside Claude Desktop / Claude Code
