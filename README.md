# lapp

An MCP server that gives AI coding agents a better way to read and edit files using **hashline addressing** — every line is tagged with a line number and content hash, so the model references those tags to specify edits instead of reproducing unchanged code.

This reduces model output tokens per edit. All operations are deterministic (no LLM inference). Written in Go — single static binary.

Matches the hashline format of [oh-my-pi](https://github.com/can1357/oh-my-pi), so models trained on that format transfer immediately.

## Installation

```bash
go install github.com/lapp-dev/lapp/cmd/lapp@latest
```

## MCP Configuration

### Claude Code (`~/.claude.json`)

```json
{
  "mcpServers": {
    "lapp": {
      "command": "lapp",
      "args": ["--root", "/path/to/project"]
    }
  }
}
```

### Codex (`.codex/mcp.json`)

```json
{
  "mcpServers": {
    "lapp": {
      "command": "lapp",
      "args": ["--root", "/path/to/project"]
    }
  }
}
```

## Recommended CLAUDE.md Entry

Add this to your project's `CLAUDE.md` to guide the model toward lapp tools:

```
This project uses lapp for file editing. Prefer lapp_read / lapp_edit / lapp_write / lapp_grep over the built-in Read / Edit / Write / Grep tools.
```

lapp emits this hint to stderr on startup as a reminder.

## Design Principles

> Inspired by what editing-focused systems such as Cursor's "Instant Apply" make explicit:
> planning and applying are different problems.

lapp is deliberately optimized for the **apply** stage of coding agents:
- **Planning stays outside lapp.** A frontier model or chat loop decides *what* to change.
- **Applying stays deterministic.** lapp reads, locates, and edits with hash-verified refs.
- **Higher-order edit helpers matter.** `lapp_find_block` and `lapp_replace_block` reduce fragile multi-step choreography that weaker models struggle with.
- **Optimize for accuracy and latency together.** The benchmark treats correctness, wall time, turn count, and token count as first-class metrics.
- **Prefer local retries over broad rereads.** Stale refs return small structured repair payloads so the model can retry with fresh local anchors instead of starting over.

This is why the benchmark does **not** ask models to debug issues from scratch. It gives them real files and concrete changes so we can measure the apply system itself, not general coding ability.

## Benchmark

Multi-hunk edit on a large file (seaborn, 1800+ lines, 3-5 simultaneous changes). Correctness measured by diff similarity against the reference patch. Native edit timed out on several models; lapp strategies achieved up to 100% correctness where native edit failed.

| Model | Strategy | Correctness | Wall Time |
|-------|----------|-------------|-----------|
| GLM-5.1 | native edit | 12% | 84s |
| GLM-5.1 | lapp replace-block | **94%** | 59s |
| DeepSeek v3.1 | native edit | 3% (timeout) | — |
| DeepSeek v3.1 | lapp structured-grep | **94%** | 102s |
| MiniMax M2.7 | native edit | 10% | 93s |
| MiniMax M2.7 | lapp structured-grep | **100%** | 72s |
| Kimi K2 | native edit | 11% | 116s |
| Kimi K2 | lapp structured-grep | **94%** | 37s |

Full results and methodology in [`benchmark/`](benchmark/).


## Tools

### `lapp_read`

Read a file with `LINE#HASH:content` prefixes. Supports pagination via `offset`/`limit`. Hash references are valid for the entire file and can be combined across pages.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | yes | Absolute path |
| `offset` | integer | no | Start line, 1-indexed (default: 1) |
| `limit` | integer | no | Max lines (default: 2000) |

### `lapp_edit`

Edit a file using `LINE#HASH` references from `lapp_read`. All edits validated atomically — if any reference is stale, nothing is written.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | yes | Absolute path |
| `edits` | array | yes | Edit operations (max 100) |

Edit operations:

```json
// Replace single line
{"type": "replace", "anchor": "5#SN", "content": "new content"}

// Replace range
{"type": "replace", "start": "2#KT", "end": "4#QW", "content": "new\ncontent"}

// Insert after (special anchors: "0:" = beginning, "EOF:" = end)
{"type": "insert_after", "anchor": "6#PM", "content": "inserted line"}

// Insert before
{"type": "insert_before", "anchor": "5#SN", "content": "inserted line"}

// Delete
{"type": "delete", "anchor": "3#ZP"}
```

### `lapp_write`

Create a new file. Returns an error if the file already exists.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | yes | Absolute path |
| `content` | string | yes | Complete file content |

### `lapp_grep`

Search files and return matches with `LINE#HASH` references usable directly in `lapp_edit`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `pattern` | string | yes | Pattern to search for (regex unless `literal=true`) |
| `literal` | boolean | no | Treat `pattern` as a fixed string (no regex). Use when searching for code with special characters |
| `format` | string | no | Output format: `text` (default) or `structured` (machine-readable JSON) |
| `path` | string | no | File or directory (default: root) |
| `context` | integer | no | Context lines (default: 2) |

## Server Flags

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--root <dir>` | `LAPP_ROOT` | CWD | Restrict operations to this directory |
| `--limit <n>` | `LAPP_LIMIT` | 2000 | Default max lines for lapp_read |
| `--block <glob>` | `LAPP_BLOCK` (colon-separated) | see below | Add blocked path pattern |
| `--allow <glob>` | `LAPP_ALLOW` (colon-separated) | — | Remove blocked path pattern |
| `--only-tools <list>` | `LAPP_ONLY_TOOLS` | — | Expose only selected tools |
| `--log-file <path>` | `LAPP_LOG_FILE` | stderr | Log destination |
| `--version` | — | — | Print version and exit |

## Security

All file operations are restricted to `--root`. The default block list covers sensitive files:
`**/.env`, `**/.env.*`, `**/secrets.*`, `**/credentials.*`, `**/*.pem`, `**/*.key`, `**/*.p12`, `**/*.pfx`, `**/.aws/credentials`, `**/.aws/config`.

Use `--allow '**/.env.local'` to unblock a specific pattern. Use `--block` to add patterns.

## License

MIT
