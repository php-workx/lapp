# Technical Specification: lapp Distribution V1

**Version:** 1.0.0
**Status:** Draft
**Goal:** One-command install (`npx -y @lapp-dev/lapp-setup`) that downloads the binary, writes MCP configs, installs skills, and injects instructions — across all supported AI clients.

---

## 1. Problem

Today, installing lapp requires the user to:

1. `go install github.com/lapp-dev/lapp/cmd/lapp@latest` (or download+extract a release binary)
2. Manually edit `~/.claude.json` (or `.cursor/mcp.json`, `.codex/config.toml`, etc.) to add the MCP server entry
3. Manually add a skill or CLAUDE.md instruction so the model knows to prefer lapp tools

This is error-prone and varies per client. Morph solved this with `npx -y @morphllm/morph-setup` — a single CLI that auto-detects clients, writes configs, installs skills, and handles platform differences. We need the same for lapp.

---

## 2. Reference Architecture: `@morphllm/morph-setup`

Analyzed from source (v1.0.8). Key components:

| Component | Purpose |
|-----------|---------|
| `src/source-parser.ts` | Parse `[source]` arg — local path, GitHub repo, GitLab, direct SKILL.md URL |
| `src/git.ts` | Shallow-clone source repo to temp dir |
| `src/skills.ts` | Discover skills by walking dirs for `SKILL.md` (gray-matter frontmatter) |
| `src/agents.ts` | Define 7 agent targets with skillsDir, globalSkillsDir, detection |
| `src/installer.ts` | Install skills: canonical copy → symlink per agent (copy fallback) |
| `src/mcp-install.ts` | Write MCP server entries in each client's config format |
| `src/claude-code-plugin.ts` | Extra Claude Code: plugin install, API key, CLAUDE.md injection |
| `src/opencode-plugin.ts` | Extra OpenCode: npm plugin install, config update |
| `src/telemetry.ts` | Fire-and-forget telemetry |

**Install flow:**

```text
npx -y @morphllm/morph-setup
  → ASCII splash
  → Parse flags (--morph-api-key, --agent, --yes)
  → Get API key (prompt or flag)
  → Detect installed AI clients
  → For each selected client:
      Write MCP config: { command: "npx", args: ["-y", "@morphllm/morphmcp"] }
      If Claude Code: install plugin + inject CLAUDE.md compact instructions
      If OpenCode: install npm plugin + update config
  → Install bundled skills (copy to ~/.agents/skills, symlink to agent dirs)
  → If [source]: clone repo, discover + install extra skills
  → Telemetry → "All done"
```

---

## 3. Key Differences: Morph vs. Lapp

| Aspect | Morph | Lapp |
|--------|-------|------|
| **MCP server runtime** | npm package (`npx -y @morphllm/morphmcp`) | Go binary (needs download) |
| **API key** | Required (`MORPH_API_KEY`) | Not needed (fully local) |
| **Skills** | 4 bundled (code-edit, code-research, explore, feature-research) | 1 bundled (lapp-edit policy) |
| **Instructions** | Compact instructions in CLAUDE.md | lapp-tools.md (editing policy) |
| **Binary management** | None (npm handles it) | Must download binary from GitHub releases |
| **Config entry** | `command: "npx"`, `args: ["-y", "@morphllm/morphmcp"]` | `command: "/path/to/lapp"`, `args: ["--root", "<cwd>"]` |

The biggest difference is **binary distribution**. Morph's MCP server is an npm package — `npx` resolves it. Lapp is a Go binary — the setup CLI must download the correct binary for the user's OS/arch from GitHub Releases.

---

## 4. Package Design

### 4.1 npm Package: `@lapp-dev/lapp-setup`

```text
@lapp-dev/lapp-setup/
├── src/
│   ├── index.ts              # CLI entry, main flow
│   ├── binary.ts             # Binary download, verification, PATH setup
│   ├── agents.ts             # Agent target definitions + detection
│   ├── installer.ts          # Skill installation (symlink/copy)
│   ├── mcp-install.ts        # MCP config entry writing per client
│   ├── claude-code-plugin.ts # Claude Code specific: CLAUDE.md injection, skill install
│   ├── opencode-plugin.ts    # OpenCode specific: instructions copy + config update
│   ├── mcp-client-paths.ts   # Config file paths per client
│   ├── skills.ts             # SKILL.md discovery + parsing
│   ├── source-parser.ts      # Parse [source] argument
│   ├── git.ts                # Shallow clone for source skills
│   └── telemetry.ts          # Fire-and-forget usage tracking
├── dist/                     # Built output (tsup, ESM)
├── .agents/skills/
│   └── lapp-edit/
│       └── SKILL.md          # Bundled skill: lapp editing policy
├── instructions/
│   └── lapp-tools.md         # Bundled instructions file
├── package.json
├── tsconfig.json
└── README.md
```

### 4.2 package.json

```json
{
  "name": "@lapp-dev/lapp-setup",
  "version": "1.0.0",
  "description": "One-command install for lapp MCP server, skills, and instructions",
  "type": "module",
  "bin": {
    "lapp-setup": "./dist/index.js"
  },
  "files": [
    "dist",
    ".agents",
    "instructions",
    "README.md"
  ],
  "publishConfig": {
    "access": "public"
  },
  "scripts": {
    "build": "tsup src/index.ts --format esm --dts --clean",
    "dev": "tsx src/index.ts",
    "prepublishOnly": "npm run build"
  },
  "dependencies": {
    "@clack/prompts": "^0.9.1",
    "chalk": "^5.4.1",
    "commander": "^13.1.0",
    "gray-matter": "^4.0.3",
    "json5": "^2.2.3",
    "simple-git": "^3.27.0"
  },
  "devDependencies": {
    "@types/node": "^22.10.0",
    "tsup": "^8.3.5",
    "tsx": "^4.19.2",
    "typescript": "^5.7.2"
  },
  "engines": {
    "node": ">=18"
  },
  "keywords": [
    "cli",
    "mcp",
    "mcp-server",
    "claude-code",
    "codex",
    "cursor",
    "windsurf",
    "cline",
    "ai-agent",
    "hashline",
    "file-editing"
  ],
  "license": "MIT"
}
```

---

## 5. Binary Download & Management

### 5.1 GitHub Releases as Source

lapp uses GoReleaser, which produces release artifacts at:

```text
https://github.com/lapp-dev/lapp/releases/download/v<VERSION>/lapp_<VERSION>_<OS>_<ARCH>.tar.gz
```

The tarball contains a single `lapp` binary (+ README.md, LICENSE).

### 5.2 Binary Resolution

```text
binary.ts
```

1. **Detect OS/arch**: `process.platform` (`darwin`|`linux`|`win32`) × `process.arch` (`x64`→`amd64`, `arm64`)
2. **Fetch latest version**: `GET https://github.com/lapp-dev/lapp/releases/latest` → parse redirect URL for tag, or query `https://api.github.com/repos/lapp-dev/lapp/releases/latest` → `.tag_name`
3. **Download tarball**: Fetch the matching `lapp_<VERSION>_<OS>_<ARCH>.tar.gz` (`.zip` for Windows)
4. **Extract binary**: Use Node `zlib` + `tar` (or `node:stream` piping) to extract just the `lapp` binary
5. **Install to**: `~/.lapp/bin/lapp` (or `lapp.exe` on Windows)
6. **Verify**: Run `~/.lapp/bin/lapp --version` and confirm it matches the downloaded version
7. **Make executable**: `chmod +x` on non-Windows

### 5.3 Install Location

| OS | Binary path |
|----|-------------|
| macOS / Linux | `~/.lapp/bin/lapp` |
| Windows | `%USERPROFILE%\.lapp\bin\lapp.exe` |

### 5.4 Version Check & Upgrade

Before downloading, check if `~/.lapp/bin/lapp --version` already matches the latest release. If so, skip download (unless `--force`). This avoids redundant downloads on re-runs.

### 5.5 PATH Setup

Append `~/.lapp/bin` to the user's shell profile so `lapp` is available beyond MCP:

| Shell | Profile file |
|-------|-------------|
| bash | `~/.bashrc` (or `~/.bash_profile` on macOS) |
| zsh | `~/.zshrc` |
| fish | `~/.config/fish/config.fish` |

Add only if not already present. Use a marker comment: `# added by lapp-setup`.

The MCP config entries use the **absolute path** (`/Users/.../.lapp/bin/lapp`), so PATH is optional — it's a convenience for manual testing.

### 5.6 Binary Download Fallback

If GitHub API rate-limits or the release tarball is unreachable:

1. Try `go install github.com/lapp-dev/lapp/cmd/lapp@latest` (requires Go toolchain)
2. If Go is not available, print instructions for manual download

---

## 6. Agent Target Definitions

### 6.1 Supported Clients

```typescript
const agents = {
  "claude-code": {
    name: "claude-code",
    displayName: "Claude Code",
    skillsDir: ".claude/skills",
    globalSkillsDir: "~/.claude/skills",
    detectInstalled: () => existsSync("~/.claude"),
  },
  codex: {
    name: "codex",
    displayName: "Codex",
    skillsDir: ".codex/skills",
    globalSkillsDir: "~/.codex/skills",
    detectInstalled: () => existsSync("~/.codex"),
  },
  cursor: {
    name: "cursor",
    displayName: "Cursor",
    skillsDir: ".cursor/skills",
    globalSkillsDir: "~/.cursor/skills",
    detectInstalled: () => existsSync("~/.cursor"),
  },
  windsurf: {
    name: "windsurf",
    displayName: "Windsurf",
    skillsDir: ".windsurf/skills",
    globalSkillsDir: "~/.codeium/windsurf/skills",
    detectInstalled: () => existsSync("~/.codeium/windsurf"),
  },
  opencode: {
    name: "opencode",
    displayName: "OpenCode",
    skillsDir: ".opencode/skills",
    globalSkillsDir: "~/.config/opencode/skills",
    detectInstalled: () => existsSync("~/.config/opencode"),
  },
  amp: {
    name: "amp",
    displayName: "Amp",
    skillsDir: ".agents/skills",
    globalSkillsDir: "~/.config/agents/skills",
    detectInstalled: () => existsSync("~/.config/amp"),
  },
  antigravity: {
    name: "antigravity",
    displayName: "Antigravity",
    skillsDir: ".agent/skills",
    globalSkillsDir: "~/.gemini/antigravity/skills",
    detectInstalled: () => existsSync("~/.gemini/antigravity"),
  },
};
```

### 6.2 MCP Config Paths

| Client | Config path | Format |
|--------|-------------|--------|
| Claude Code | `~/.claude.json` | JSON (`mcpServers`) |
| Codex | `~/.codex/config.toml` | TOML (`[mcp_servers.lapp]`) |
| Cursor | `~/.cursor/mcp.json` | JSON (`mcpServers`) |
| Windsurf | `~/.codeium/windsurf/mcp_config.json` | JSON (`mcpServers`) |
| OpenCode | `~/.config/opencode/opencode.json` | JSON |
| Amp | `~/.config/amp/settings.json` | JSON (`amp.mcpServers`) |
| Antigravity | `~/.gemini/antigravity/mcp_config.json` | JSON (`mcpServers`) |

---

## 7. MCP Config Entry

### 7.1 Entry Shape

For lapp, the MCP server entry points to the installed binary with `--root` set to the project directory:

```json
{
  "lapp": {
    "type": "stdio",
    "command": "/Users/xxx/.lapp/bin/lapp",
    "args": ["--root", "/path/to/project"],
    "env": {}
  }
}
```

**Key decision:** `--root` should default to the **current working directory** at setup time. For global installs, we set `--root` to the cwd. Users can manually edit it later.

**Alternative:** Omit `--root` entirely. When lapp starts without `--root`, it defaults to `os.Getwd()` (the MCP client's cwd). This is simpler and works for any project. **Prefer this approach** — it matches the current binary behavior and avoids hardcoding a path.

So the minimal entry is:

```json
{
  "lapp": {
    "type": "stdio",
    "command": "/Users/xxx/.lapp/bin/lapp"
  }
}
```

### 7.2 Claude Code Specifics

Claude Code's config (`~/.claude.json`) uses `type: "stdio"`:

```json
{
  "mcpServers": {
    "lapp": {
      "type": "stdio",
      "command": "/Users/xxx/.lapp/bin/lapp"
    }
  }
}
```

### 7.3 Codex Specifics (TOML)

```toml
[mcp_servers.lapp]
command = "/Users/xxx/.lapp/bin/lapp"
enabled = true
```

### 7.4 Cursor / Windsurf Specifics (JSON)

```json
{
  "mcpServers": {
    "lapp": {
      "command": "/Users/xxx/.lapp/bin/lapp"
    }
  }
}
```

### 7.5 Amp Specifics

```json
{
  "amp.mcpServers": {
    "lapp": {
      "command": "/Users/xxx/.lapp/bin/lapp"
    }
  }
}
```

Also attempt `amp mcp add lapp -- /path/to/lapp` via CLI.

### 7.6 OpenCode Specifics

OpenCode doesn't need MCP config via file — it detects servers differently. Write the binary to PATH and register the skill.

---

## 8. Per-Client Setup Nuances

Morph-setup doesn't just write one MCP config entry and call it done. Each AI client has a different integration surface, and morph-setup handles them differently. This section documents **exactly** what morph-setup does per client, and how lapp-setup should map each step.

### 8.1 Setup Step Matrix

| Step | Claude Code | OpenCode | Codex | Cursor | Windsurf | Amp | Antigravity |
|------|-------------|----------|-------|--------|----------|-----|-------------|
| Write MCP server config | `~/.claude.json` | skip | `~/.codex/config.toml` | `~/.cursor/mcp.json` | `~/.codeium/windsurf/mcp_config.json` | `~/.config/amp/settings.json` + `amp mcp add` CLI | `~/.gemini/antigravity/mcp_config.json` |
| Install plugin package | `claude plugin install morph-compact@morph` | `npm install @morphllm/opencode-morph-plugin` | — | — | — | — | — |
| Register instructions file | — | `opencode.json` → `instructions` array | — | — | — | — | — |
| Inject CLAUDE.md snippet | Yes (compact instructions) | — | — | — | — | — | — |
| Write API key file | `~/.claude/morph/.env` | — | — | — | — | — | — |
| Install skills (symlink) | `~/.claude/skills/` | `~/.config/opencode/skills/` | `~/.codex/skills/` | `~/.cursor/skills/` (copy only) | `~/.codeium/windsurf/skills/` | `~/.config/agents/skills/` | `~/.gemini/antigravity/skills/` |
| Skill install mode | symlink | symlink | symlink | **copy only** | symlink | symlink | symlink |

### 8.2 Claude Code — Three-Layer Integration

Morph does **three** distinct things for Claude Code, not just one:

**Layer 1: MCP server config** — Standard `~/.claude.json` entry under `mcpServers`.

**Layer 2: Plugin install** — Runs `claude plugin marketplace add morphllm/morph-claude-code-plugin --scope user` then `claude plugin install morph-compact@morph --scope user`. This installs a Claude Code plugin that provides a compact/summarization hook. The plugin intercepts compaction and produces a structured summary instead of losing context. This is Morph-specific (their cloud API does the summarization) — **lapp does not need this step**.

**Layer 3: CLAUDE.md injection** — Appends a "compact instructions" block to `~/.claude/CLAUDE.md`:
```markdown
# Compact Instructions

When compacting, if the custom instruction is `morph`, do NOT perform any summarization or analysis. Output ONLY this exact text and nothing else: `Summary provided via SessionStart hook`.
```
This is a control-flow marker so the Morph compact plugin knows when to intercept compaction. **For lapp**, we still inject into CLAUDE.md, but with the **lapp editing policy** instead:
```markdown
# Lapp File Editing Policy

Prefer lapp tools (`lapp_read`, `lapp_edit`, `lapp_grep`, `lapp_write`) over built-in read/edit when available.
See .claude/skills/lapp-edit/SKILL.md for full workflow.
```

**Layer 4: API key file** — Morph writes `MORPH_API_KEY` to `~/.claude/morph/.env` (mode 0600). **Lapp does not need this** (no API key).

**Layer 5: Skills** — Symlink `~/.claude/skills/lapp-edit` → `~/.agents/skills/lapp-edit/`.

**Summary for lapp:** Skip plugin install and API key file. Do MCP config + CLAUDE.md injection + skills.

### 8.3 OpenCode — Plugin Package + Instructions Registration

OpenCode is the most different from other clients. Morph does **three** things:

**Step 1: MCP config is NOT written to a file.** OpenCode discovers MCP servers differently (it returns `{ success: true }` from `installMorphMcpToClient` without writing anything). The MCP server becomes available through the plugin mechanism instead.

**Step 2: Install an npm plugin package.** Morph runs `npm install @morphllm/opencode-morph-plugin` (or `bun install` if bun is detected) inside `~/.config/opencode/`. A `package.json` is created there if it doesn't exist:
```json
{ "private": true }
```
The plugin package (`@morphllm/opencode-morph-plugin`) is a full OpenCode plugin that:
- Imports `@opencode-ai/plugin` and `@morphllm/morphsdk`
- Registers Morph tools (`morph_edit`, `warpgrep_codebase_search`, `warpgrep_github_search`, `morph_compact`) via the OpenCode plugin API
- Provides its own `instructions/morph-tools.md` file inside the npm package

**Step 3: Register the plugin and instructions in `opencode.json`.** Morph updates `~/.config/opencode/opencode.json` to add:
```json
{
  "plugin": ["@morphllm/opencode-morph-plugin"],
  "instructions": ["node_modules/@morphllm/opencode-morph-plugin/instructions/morph-tools.md"]
}
```
The key insight: OpenCode has an **`instructions` array** in its config. Files listed there are **always loaded** by the agent — no skill load step required. This is different from Claude Code (where CLAUDE.md is the always-on instruction surface, and skills are loaded on-demand).

**For lapp**, we need to create a similar plugin or take a simpler approach:

- **Option A (Full plugin):** Create `@lapp-dev/opencode-lapp-plugin` npm package that registers lapp's MCP tools via the OpenCode plugin SDK. The plugin ships `instructions/lapp-tools.md` inside it.
- **Option B (Instructions-only, recommended for V1):** Since lapp's MCP server is a stdio binary (not a JS SDK), OpenCode can discover it via the standard MCP config mechanism. We:
  1. Write the MCP server config to `~/.config/opencode/opencode.json` under the `mcpServers` key (if OpenCode supports it)
  2. Copy `instructions/lapp-tools.md` to `~/.config/opencode/instructions/lapp-tools.md`
  3. Add the path to the `instructions` array in `opencode.json`

**The instructions array is the critical OpenCode-specific mechanism.** It's how OpenCode ensures the agent always knows about lapp's tool selection policy without needing a skill load. This is analogous to CLAUDE.md injection for Claude Code, but uses a different mechanism (config array vs. file append).

### 8.4 Codex — TOML Config Only

Codex is straightforward. The only thing morph does is write a TOML block to `~/.codex/config.toml`:

```toml
[mcp_servers.morph-mcp]
command = "npx"
args = ["-y", "@morphllm/morphmcp", "--api-key", "..."]
enabled = true
startup_timeout_sec = 45
env = { MORPH_API_KEY = "..." }
```

For lapp, simpler — no args, no env:

```toml
[mcp_servers.lapp]
command = "/Users/you/.lapp/bin/lapp"
enabled = true
```

The TOML writer must handle:
- Finding an existing `[mcp_servers.lapp]` block and replacing it in-place
- Appending a new block if none exists
- Preserving all other TOML content

Codex has no instructions mechanism and no plugin system. The MCP config + skills are the full setup.

### 8.5 Cursor — No Symlinks + JSON Config

Cursor is like Claude Code's MCP config but with two differences:

1. **Config path**: `~/.cursor/mcp.json` (not `~/.claude.json`)
2. **No symlinks**: Cursor is in the `AGENTS_THAT_SHOULD_NOT_GET_SYMLINKS` set. Skills must be **copied** directly to `~/.cursor/skills/lapp-edit/` instead of symlinked. This is because Cursor's filesystem watcher or extension loading doesn't handle symlinks reliably.

The MCP config format is the same `mcpServers` JSON as Claude Code (without the `type: "stdio"` field — Cursor omits it).

Cursor has no instructions file mechanism and no plugin system. MCP config + copied skills are the full setup.

### 8.6 Windsurf — JSON Config at Different Path

Identical to Cursor's setup pattern, but config lives at `~/.codeium/windsurf/mcp_config.json`. The JSON structure is the same `mcpServers` format. Skills go to `~/.codeium/windsurf/skills/`. Symlinks are fine for Windsurf.

### 8.7 Amp — Dual Config (CLI + File)

Amp is unique: morph tries **two** config mechanisms:

1. **CLI first**: Run `amp mcp add morph-mcp -- npx -y @morphllm/morphmcp` via `child_process.execFile`. This registers the MCP server through Amp's own CLI. If the CLI fails (not installed), it silently falls through.
2. **File second**: Write to `~/.config/amp/settings.json` under the `amp.mcpServers` key (note: `amp.mcpServers`, not `mcpServers` — Amp uses a prefixed key in their flat settings JSON).

For lapp:
```bash
amp mcp add lapp -- /Users/you/.lapp/bin/lapp
```
Plus the file-based fallback to `~/.config/amp/settings.json`:
```json
{
  "amp.mcpServers": {
    "lapp": {
      "command": "/Users/you/.lapp/bin/lapp"
    }
  }
}
```

Skills go to `~/.config/agents/skills/` (Amp uses `.agents/skills` in the project, but `~/.config/agents/skills` globally).

### 8.8 Antigravity — Standard JSON Config

Same pattern as Cursor/Windsurf. Config at `~/.gemini/antigravity/mcp_config.json` with `mcpServers` key. Skills at `~/.gemini/antigravity/skills/`. No special plugin or instructions mechanism.

### 8.9 Summary: What Lapp-Setup Does Per Client

| Client | MCP config | Instructions injection | Plugin install | Skill install | Skill mode | Extra steps |
|--------|-----------|------------------------|----------------|---------------|------------|-------------|
| **Claude Code** | `~/.claude.json` | `~/.claude/CLAUDE.md` append | — | `~/.claude/skills/` | symlink | — |
| **OpenCode** | skip (or via plugin) | `~/.config/opencode/opencode.json` → `instructions[]` array | npm plugin (V2) | `~/.config/opencode/skills/` | symlink | Copy `instructions/lapp-tools.md` to `~/.config/opencode/instructions/` |
| **Codex** | `~/.codex/config.toml` | — | — | `~/.codex/skills/` | symlink | — |
| **Cursor** | `~/.cursor/mcp.json` | — | — | `~/.cursor/skills/` | **copy** | No symlinks |
| **Windsurf** | `~/.codeium/windsurf/mcp_config.json` | — | — | `~/.codeium/windsurf/skills/` | symlink | — |
| **Amp** | `~/.config/amp/settings.json` + `amp mcp add` CLI | — | — | `~/.config/agents/skills/` | symlink | Dual config (CLI + file) |
| **Antigravity** | `~/.gemini/antigravity/mcp_config.json` | — | — | `~/.gemini/antigravity/skills/` | symlink | — |

### 8.10 The "Always-On Instructions" Pattern

This is the key architectural insight from morph-setup: **each client has a different mechanism for ensuring the agent always knows about the tool policy**.

| Client | Always-on mechanism | What we inject |
|--------|---------------------|---------------|
| Claude Code | `~/.claude/CLAUDE.md` is always loaded | Append lapp editing policy snippet |
| OpenCode | `instructions[]` array in `opencode.json` is always loaded | Add path to `instructions/lapp-tools.md` |
| All others | No always-on mechanism; rely on skill load or MCP tool descriptions | Skills + MCP tool descriptions must be self-descriptive |

This means for Claude Code and OpenCode, we need **both** a skill (for detailed workflow) and an always-on instruction (for tool routing). The skill is loaded on-demand; the always-on instruction ensures the agent routes to lapp tools before loading the skill. For other clients, the MCP tool descriptions themselves must be descriptive enough to guide routing.

---

## 9. Skills & Instructions

### 9.1 Bundled Skill: `lapp-edit`

Ships inside the npm package at `.agents/skills/lapp-edit/SKILL.md`:

```markdown
---
name: lapp-edit
description: Use lapp hashline tools for precise, token-efficient file edits.
metadata:
  author: lapp
  version: "0.1.0"
  argument-hint: <file-or-change>
---

# Lapp Edit

Prefer lapp tools (`lapp_read`, `lapp_edit`, `lapp_grep`, `lapp_write`) over built-in read/edit when available.

## When To Use

- The user requests code changes to existing files.
- You need targeted edits without reproducing unchanged code.
- You want to reduce output token cost per edit.

## Steps

1. Use `lapp_grep "<pattern>" path=<file>` to locate edit targets and get LINE#HASH refs.
2. Use `lapp_edit` with the hash refs to apply precise changes.
3. For full context on small files (<300 lines), use `lapp_read` first.
4. For new files, use `lapp_write`.
5. Re-run relevant checks (tests/build/lint) if available.

## Rules

- Never call `lapp_edit` without first getting LINE#HASH refs from grep or read.
- Never use `lapp_write` on an existing file (use `lapp_edit`).
- Prefer lapp tools over native read/edit whenever available.
- Fall back to native tools only if lapp is not in the tool manifest.
```

### 9.2 Instructions: `lapp-tools.md`

The full editing policy (from `instructions/lapp-tools.md`) is installed alongside the skill. For Claude Code specifically, the setup injects a CLAUDE.md snippet that loads the instructions.

### 9.3 Skill Installation

Follow the morph-setup pattern:

1. **Canonical location**: Copy skill contents to `~/.agents/skills/lapp-edit/`
2. **Per-agent symlink**: Symlink `~/.claude/skills/lapp-edit` → `~/.agents/skills/lapp-edit/`
3. **Fallback**: If symlink fails (Windows, Cursor), copy directly to agent's skills dir

### 9.4 Claude Code: CLAUDE.md Injection

Append to `~/.claude/CLAUDE.md` (create if missing):

```markdown
# Lapp File Editing Policy

Prefer lapp tools (`lapp_read`, `lapp_edit`, `lapp_grep`, `lapp_write`) over built-in read/edit when available.
See .claude/skills/lapp-edit/SKILL.md for full workflow.
```

Only inject if not already present (check for marker text).

---

## 10. CLI Interface

### 10.1 Usage

```text
lapp-setup [options]

Install lapp MCP server, skills, and instructions onto coding agents

Options:
  -g, --global              Install skills globally (default)
  -a, --agent <agents...>   Target specific agents (repeatable)
  -y, --yes                 Skip all confirmation prompts
  --force                   Re-download binary even if already installed
  --version                 Print setup CLI version
  -h, --help                Display help
```

No API key flag — lapp doesn't need one.

### 10.2 Interactive Flow

```console
$ npx -y @lapp-dev/lapp-setup

  ███╗   ███╗ █████╗  ██████╗ ██████╗ ██╗   ██╗
  ████╗ ████║██╔══██╗██╔════╝ ██╔══██╗╚██╗ ██╔╝
  ██╔████╔██║███████║██║  ███╗██████╔╝ ╚████╔╝
  ██║╚██╔╝██║██╔══██║██║   ██║██╔═══╝   ╚██╔╝
  ██║ ╚═╝ ██║██║  ██║╚██████╔╝██║        ██║
  ╚═╝     ╚═╝╚═╝  ╚═╝ ╚═════╝ ╚═╝        ╚═╝

  Lapp MCP Install

  ◇ Detected platforms: Claude Code, Cursor
  ◆ Choose which platforms you want to install into:
    │ ◉ Claude Code (detected)
    │ ◉ Cursor (detected)
    │ ◯ Codex
    │ ◯ Windsurf
    │ ◯ OpenCode
    │ ◯ Amp
    │ ◯ Antigravity

  ◇ Downloading lapp v0.4.2 for darwin/arm64...
  ◇ Installed to ~/.lapp/bin/lapp

  ◇ Installing MCP config for Claude Code → ~/.claude.json
  ◇ Installing MCP config for Cursor → ~/.cursor/mcp.json
  ◇ Installing lapp-edit skill (2 agents)
  ◇ Injecting lapp policy into ~/.claude/CLAUDE.md

  All done. Restart your AI client to activate lapp.
```

### 10.3 Non-Interactive Mode

```bash
npx -y @lapp-dev/lapp-setup -y
```

Auto-detects installed clients, installs to all detected, skips prompts.

### 10.4 Target Specific Agents

```bash
npx -y @lapp-dev/lapp-setup -a claude-code -a cursor
```

---

## 11. Implementation Modules

### 11.1 `src/binary.ts`

```typescript
export interface BinaryInfo {
  version: string;       // e.g. "0.4.2"
  installPath: string;   // e.g. "/Users/xxx/.lapp/bin/lapp"
}

export async function getLatestVersion(): Promise<string>
export async function detectPlatform(): Promise<{ os: string; arch: string }>
export async function isBinaryInstalled(): Promise<BinaryInfo | null>
export async function downloadBinary(version: string, force?: boolean): Promise<BinaryInfo>
export async function addToShellProfile(binDir: string): Promise<void>
```

**Download implementation:**

1. Construct URL: `https://github.com/lapp-dev/lapp/releases/download/v${version}/lapp_${version}_${os}_${arch}.tar.gz`
2. Fetch via `node:https` (or `fetch` in Node 18+)
3. Pipe through `zlib.createGunzip()` → `tar.extract()` (use `node:tar` or a lightweight tar parser)
4. Write to `~/.lapp/bin/lapp`
5. `chmod 0o755` on non-Windows
6. Verify: `execFile(installPath, ['--version'])` matches `version`

**Platform mapping:**

| `process.platform` | OS string | `process.arch` | Arch string |
|-----|------|-----|------|
| `darwin` | `darwin` | `x64` | `amd64` |
| `darwin` | `darwin` | `arm64` | `arm64` |
| `linux` | `linux` | `x64` | `amd64` |
| `linux` | `linux` | `arm64` | `arm64` |
| `win32` | `windows` | `x64` | `amd64` |

### 11.2 `src/agents.ts`

Same structure as morph-setup's `agents.ts`. Define `agents` map with `name`, `displayName`, `skillsDir`, `globalSkillsDir`, `detectInstalled()`. Provide `detectInstalledAgents()` that iterates and returns detected keys.

### 11.3 `src/mcp-client-paths.ts`

Config path resolvers per client. Same structure as morph-setup. Add OS-specific logic for Windows (`APPDATA` env var).

### 11.4 `src/mcp-install.ts`

Per-client config writers. Each function:

1. Reads existing config (JSON5 for JSON files, raw text for TOML)
2. Adds or updates `lapp` entry under `mcpServers` (or `amp.mcpServers` for Amp)
3. Writes back

Key functions:

```typescript
export function listSupportedMcpClients(): ClientInfo[]
export function detectMcpClients(): string[]
export async function installLappMcpToClient(client: string, binaryPath: string): Promise<InstallResult>
```

**Lapp-specific config writer differences from morph-setup:**

- No `args` by default (lapp uses cwd). Optional `--root` if user specifies.
- No `env` block (no API key).
- The `command` field uses the absolute path to the installed binary, not `npx`.

### 11.5 `src/claude-code-plugin.ts`

Claude Code gets extra treatment:

1. Write `lapp` MCP server entry into `~/.claude.json`
2. Install `lapp-edit` skill to `~/.claude/skills/lapp-edit/`
3. Inject editing policy snippet into `~/.claude/CLAUDE.md` (idempotent — check before writing)

**Note:** Morph also installs a `claude plugin install morph-compact@morph` plugin for context compaction. Lapp does NOT need this — lapp has no cloud component. We also skip the `~/.claude/morph/.env` API key file that Morph writes.

### 11.5a `src/opencode-plugin.ts`

OpenCode gets different extra treatment:

1. Copy `instructions/lapp-tools.md` to `~/.config/opencode/instructions/lapp-tools.md`
2. Read `~/.config/opencode/opencode.json` (create if missing)
3. Add `"instructions/lapp-tools.md"` to the `instructions` array (if not already present)
4. Write back `opencode.json`

This is the "always-on instructions" pattern specific to OpenCode — files in the `instructions` array are loaded automatically by the agent without a skill-load step. This is functionally equivalent to CLAUDE.md injection for Claude Code, but uses a config array instead of file appending.

**V2:** Create a full `@lapp-dev/opencode-lapp-plugin` npm package that registers lapp's tools via the OpenCode plugin SDK (`@opencode-ai/plugin`). This would allow richer integration (custom tool metadata, environment setup). For V1, instructions-only is sufficient since lapp is a stdio MCP server that OpenCode discovers via standard config.

### 11.6 `src/installer.ts`

Skill installation logic (symlink + copy fallback). Reuse morph-setup's design verbatim:

1. Copy skill to canonical location (`~/.agents/skills/lapp-edit/`)
2. Symlink from each agent's skills dir to canonical location
3. Fallback to direct copy if symlink fails or agent is in no-symlink set (Cursor)

```typescript
export async function installSkillForAgent(
  skill: Skill, agentType: string, options: InstallOptions
): Promise<InstallResult>

export async function isSkillInstalled(
  skillName: string, agentType: string, options: InstallOptions
): Promise<boolean>
```

### 11.7 `src/skills.ts`

SKILL.md discovery and parsing. Reuse morph-setup's pattern:

- Walk directories looking for `SKILL.md`
- Parse gray-matter frontmatter (`name`, `description`, `metadata`)
- Skip `node_modules`, `.git`, `dist`, `build`, `__pycache__`
- Priority search dirs: `.agents/skills`, `.claude/skills`, `.codex/skills`, etc.

### 11.8 `src/index.ts` — Main Flow

```typescript
async function main() {
  // 1. Parse CLI args (commander)
  // 2. Show ASCII splash
  // 3. Download/verify binary
  const binary = await downloadBinary(version, opts.force);
  // 4. Detect + select agent targets
  const selectedPlatforms = await selectPlatforms(opts);
  // 5. Install MCP config for each selected client
  for (const client of selectedPlatforms) {
    await installLappMcpToClient(client, binary.installPath);
  }
  // 6. Install bundled lapp-edit skill (symlink or copy per agent rules)
  await installBundledSkills({ targetAgents: selectedPlatforms, global: true });
  // 7. Per-client always-on instruction injection:
  //    Claude Code → append to ~/.claude/CLAUDE.md
  //    OpenCode → add to instructions[] in opencode.json + copy lapp-tools.md
  if (selectedPlatforms.includes("claude-code")) {
    await injectClaudeMdPolicy();
  }
  if (selectedPlatforms.includes("opencode")) {
    await installOpenCodeInstructions(binary.installPath);
  }
  // 8. PATH setup (optional, informational)
  await addToShellProfile(path.dirname(binary.installPath));
  // 9. Telemetry
  track({ event: "install", agents: selectedPlatforms.join(",") });
  // 10. Outro
  outro("All done. Restart your AI client to activate lapp.");
}
```

---

## 12. Telemetry

Minimal, opt-out. Same pattern as morph-setup:

- Fire-and-forget `fetch()` to a lightweight endpoint
- Respect `DISABLE_TELEMETRY` and `DO_NOT_TRACK` env vars
- Track: event type, CLI version, target agents, OS/arch
- No PII, no API keys, no file paths

---

## 13. Security Considerations

| Risk | Mitigation |
|------|------------|
| Path traversal in skill names | `sanitizeName()` strips `/`, `\`, `:`, null bytes; `isPathSafe()` checks resolved paths |
| Binary tampering | Verify `--version` output matches expected version after download |
| Config corruption | Read → merge → write (never overwrite). Use JSON5 for lenient parsing |
| Symlink escape | Validate symlink targets stay within expected directories |
| Temp dir cleanup | Only delete paths inside `os.tmpdir()` (same guard as morph-setup) |

---

## 14. Testing Strategy

### Unit Tests

| Module | Test |
|--------|------|
| `binary.ts` | Platform detection, version parsing, install path resolution |
| `agents.ts` | Detection logic (mock `existsSync`) |
| `mcp-install.ts` | Config writing per format (snapshot tests) |
| `installer.ts` | Symlink creation, copy fallback, path traversal rejection |
| `skills.ts` | SKILL.md parsing, directory walking |
| `source-parser.ts` | All source input formats |

### Integration Tests

- Full end-to-end: `lapp-setup -y` on a clean environment → verify binary exists, configs written, skills installed
- Idempotency: Run twice → no duplication in configs/skills/CLAUDE.md
- Upgrade: Install v0.3 → run setup again → binary updated to latest

---

## 15. Release & Publishing

### 15.1 npm Publishing

```bash
cd setup/               # or wherever the package lives
npm run build
npm publish --access public
```

### 15.2 Versioning

The setup CLI version is independent of the lapp binary version. The setup CLI always fetches the **latest** lapp release. Pin with `--version` if needed in the future.

### 15.3 Monorepo vs. Separate Repo

**Recommendation:** Keep the setup package in the lapp monorepo under `setup/`. Benefits:
- Single source of truth for skill/instruction content
- Can reference lapp-tools.md directly
- CI can build + publish in one pipeline

### 15.4 CI Pipeline

1. On push to `main` with changes in `setup/`: build + `npm publish --access public` (if version bumped)
2. On lapp release: no action needed — setup CLI fetches latest binary dynamically

---

## 16. Future Considerations (V2+)

| Feature | Notes |
|---------|-------|
| `--root <dir>` flag | Allow specifying a project root in the MCP config entry |
| `--version <ver>` flag | Pin a specific lapp binary version instead of latest |
| `lapp update` command | Self-update binary to latest |
| `lapp-setup --uninstall` | Remove binary, configs, skills, CLAUDE.md injection |
| Project-local config | Install `.claude/skills/lapp-edit/` in current project (not global) |
| Windows `PATH` via registry | More robust than profile editing on Windows |
| Checksum verification | Verify binary checksums from `checksums.txt` in release |
| Additional skills | `lapp-refactor`, `lapp-batch-edit` — as the tool set grows |

---

## Appendix A: Morph-Setup Source Map

Full file listing of `@morphllm/morph-setup@1.0.8` for reference:

```text
.agents/skills/code-edit/SKILL.md       # 730B
.agents/skills/code-research/SKILL.md   # 1.6kB
.agents/skills/explore/SKILL.md         # 1.0kB
.agents/skills/feature-research/SKILL.md # 1.1kB
dist/a2a-review-GIEEOXF6.js            # 8.7kB (lazy-loaded)
dist/index.d.ts                         # 20B
dist/index.js                           # 42.6kB (main CLI, ~1250 lines)
package.json                            # 2.1kB
README.md                               # 2.8kB
```

The `index.js` compiles from 8 source modules:
`source-parser.ts` → `git.ts` → `skills.ts` → `agents.ts` → `installer.ts` → `mcp-client-paths.ts` → `mcp-install.ts` → `claude-code-plugin.ts` → `opencode-plugin.ts` → `telemetry.ts` → `index.ts`

## Appendix B: MCP Config Examples (All Clients)

### Claude Code (`~/.claude.json`)

```json
{
  "mcpServers": {
    "lapp": {
      "type": "stdio",
      "command": "/Users/you/.lapp/bin/lapp"
    }
  }
}
```

### Codex (`~/.codex/config.toml`)

```toml
[mcp_servers.lapp]
command = "/Users/you/.lapp/bin/lapp"
enabled = true
```

### Cursor (`~/.cursor/mcp.json`)

```json
{
  "mcpServers": {
    "lapp": {
      "command": "/Users/you/.lapp/bin/lapp"
    }
  }
}
```

### Windsurf (`~/.codeium/windsurf/mcp_config.json`)

```json
{
  "mcpServers": {
    "lapp": {
      "command": "/Users/you/.lapp/bin/lapp"
    }
  }
}
```

### Amp (`~/.config/amp/settings.json`)

```json
{
  "amp.mcpServers": {
    "lapp": {
      "command": "/Users/you/.lapp/bin/lapp"
    }
  }
}
```

### Antigravity (`~/.gemini/antigravity/mcp_config.json`)

```json
{
  "mcpServers": {
    "lapp": {
      "command": "/Users/you/.lapp/bin/lapp"
    }
  }
}
```

---

## Appendix C: `add-mcp` Reference Analysis

**Package:** `add-mcp` v1.8.0, by Andre Landgraf / Neon
**Repo:** https://github.com/neondatabase/add-mcp
**License:** Apache-2.0

`add-mcp` is a generic, multi-agent MCP server config writer — a different approach from morph-setup's single-product installer. It's worth studying because it solves the config-writing problem more robustly and covers more clients.

### C.1 Architecture

`add-mcp` is a single-command tool: `npx add-mcp <target>` where `<target>` is:
- **Remote URL**: `https://mcp.sentry.dev/mcp` — classified as `type: "remote"`
- **Command string**: `npx -y @some/mcp-server --arg` — classified as `type: "command"`
- **npm package**: `@some/mcp-server` — classified as `type: "package"` (expanded to `npx -y <pkg>`)

It then:
1. Auto-infers a server name from the input (hostname for URLs, package name for npm)
2. Detects installed AI clients (13 supported)
3. Lets user select which agents to install into
4. Transforms the config into each agent's native format
5. Writes config files preserving existing content

### C.2 Supported Agents (13)

| Agent | Config path | Format | Config key | Project-level | Transform |
|-------|------------|--------|------------|--------------|-----------|
| Antigravity | `~/.gemini/antigravity/mcp_config.json` | JSON | `mcpServers` | No | Custom (remote → `serverUrl`) |
| Cline VSCode | `~/.../saoudrizwan.claude-dev/settings/cline_mcp_settings.json` | JSON | `mcpServers` | No | Custom (remote → `url`+`type`) |
| Cline CLI | `~/.cline/data/settings/cline_mcp_settings.json` | JSON | `mcpServers` | No | Custom |
| Claude Code | `~/.claude.json` | JSONC | `mcpServers` | Yes (`.mcp.json`) | Identity |
| Claude Desktop | `~/.../Claude/claude_desktop_config.json` | JSON | `mcpServers` | No | Identity (stdio only!) |
| Codex | `~/.codex/config.toml` | TOML | `mcp_servers` | Yes (`.codex/config.toml`) | Custom (remote → `type`+`url`) |
| Cursor | `~/.cursor/mcp.json` | JSON | `mcpServers` | Yes (`.cursor/mcp.json`) | Custom (remote → `url`+`headers`) |
| Gemini CLI | `~/.gemini/settings.json` | JSON | `mcpServers` | Yes (`.gemini/settings.json`) | Identity |
| Goose | `~/.config/goose/config.yaml` | YAML | `extensions` | No | Custom (remote → `type`+`uri`) |
| GitHub Copilot CLI | `~/.copilot/mcp-config.json` | JSON | `mcpServers` | Yes (`.vscode/mcp.json`) | Custom (remote → `type`+`url`+`tools:["*"]`) |
| MCPorter | `~/.mcporter/mcporter.json` | JSON/JSONC | `mcpServers` | Yes | Custom (JSONC support) |
| OpenCode | `~/.config/opencode/opencode.json` | JSON | `mcp` | Yes (`opencode.json`) | Custom (remote → `type:"remote"`+`url`) |
| VS Code | `~/.../Code/User/mcp.json` | JSON | `servers` | Yes (`.vscode/mcp.json`) | Identity |
| Zed | OS-specific `settings.json` | JSON | `context_servers` | Yes (`.zed/settings.json`) | Custom (remote → `source:"custom"`+`type`+`url`) |

### C.3 Key Design Decisions

**1. JSONC-aware config writing.** Uses `jsonc-parser`'s `modify()` and `applyEdits()` to preserve comments, trailing commas, and formatting when writing JSON configs. This is critical for `~/.claude.json` which users often annotate.

```typescript
// Instead of JSON.stringify(data, null, 2), add-mcp does:
const edits = jsonc.modify(originalContent, configKeyPath, newValue, {
  formattingOptions: detectIndent(originalContent)
});
const updatedContent = jsonc.applyEdits(originalContent, edits);
```

**2. Project-level vs. global.** Each agent declares whether it supports project config (`localConfigPath`). When `--global` is not set, add-mcp detects project-level config dirs (`.mcp.json`, `.cursor/`, etc.) and offers to install locally. For lapp, this means users can add lapp to a specific project's `.mcp.json` or globally to `~/.claude.json`.

**3. Config key per agent.** Not all agents use `mcpServers`. The config key is declared per agent:
- Claude Code / Cursor / Windsurf: `mcpServers`
- OpenCode: `mcp`
- Codex: `mcp_servers` (TOML)
- Goose: `extensions` (YAML)
- Zed: `context_servers`
- VS Code / GitHub Copilot CLI: `servers`
- Amp: `amp.mcpServers` (morph-setup specific)

**4. `transformConfig` per agent.** Each agent can reshape the same input. For example, a remote URL `{ url, type: "http", headers }` becomes:
- OpenCode: `{ type: "remote", url, enabled: true, headers }`
- Goose (YAML): `{ name, type: "streamable_http", uri, headers, timeout: 300 }`
- Zed: `{ source: "custom", type: "http", url, headers }`
- GitHub Copilot CLI: `{ type: "http", url, tools: ["*"], headers }`

For lapp (always stdio), the transform is simpler — just `{ command, args }` — but the pattern of per-agent transforms is valuable.

**5. Smart agent selection.** add-mcp remembers the last-selected agents in `~/.config/add-mcp/config.json`. On re-runs, it offers "Same as last time" as the first option. This is a UX nicety that morph-setup lacks.

**6. `remove` and `sync` commands.** add-mcp can remove a server from all agents and synchronize server names across agents. These are useful for cleanup.

**7. `find` command with curated registry.** `add-mcp find <keyword>` searches an Anthropic-hosted registry of MCP servers. This is out of scope for lapp-setup, but the registry URL pattern is notable: `https://modelcontextprotocol.io/api/servers`.

**8. Transport validation.** Claude Desktop only supports stdio via config file — add-mcp refuses remote URLs for it with a helpful message pointing to the Settings UI. This kind of per-transport validation is important.

**9. Gitignore support.** `--gitignore` adds project config files (`.mcp.json`, `.codex/config.toml`, etc.) to `.gitignore`. Prevents accidental commits of local config.

### C.4 What lapp-setup Should Adopt from add-mcp

| Feature | Adopt? | Priority |
|---------|--------|----------|
| JSONC-aware config writing | **Yes** | High — preserves user comments in `~/.claude.json` |
| Project-level config (`--local`) | **Yes** | High — `.mcp.json` in project root is the standard |
| Additional agents (Goose, Zed, VS Code, Cline, Gemini CLI) | **Yes** | Medium — expands coverage |
| `transformConfig` per agent | **Yes** | High — clean abstraction for per-client config shapes |
| Config key per agent | **Yes** | High — `mcpServers` vs `mcp` vs `mcp_servers` etc. |
| Config persistence (remember agents) | Nice to have | Low |
| `remove` / `uninstall` command | **Yes** | Medium — cleanup is important |
| `sync` command | Maybe | Low |
| `find` / registry | No | Out of scope |
| Transport validation (stdio vs. remote) | **Yes** | High — lapp is always stdio, but validate |
| `--gitignore` flag | **Yes** | Medium |
| Deep merge (preserve existing config) | **Yes** | High — don't clobber other MCP servers |
| Agent-specific unsupported transport messages | **Yes** | Medium — e.g. Claude Desktop doesn't support remote |

### C.5 What add-mcp Lacks (That lapp-setup Needs)

| Gap | lapp-setup Addition |
|-----|-------------------|
| No binary download | GitHub Releases download + verification |
| No skill installation | Symlink/copy bundled skills to agent dirs |
| No always-on instructions | CLAUDE.md injection + OpenCode `instructions[]` array |
| No Claude Code plugin | Not needed for lapp, but the pattern is instructive |
| No concept of "bundled content" | lapp-setup ships skills + instructions inside the npm package |
| No project-level + global scope awareness | lapp-setup should default to global, support `--local` for project |
| No version pinning | lapp-setup fetches latest release, could add `--version` |

### C.6 Sentry MCP Specifics

The `https://mcp.sentry.dev/mcp` URL is a **remote HTTP MCP server** (not stdio). When add-mcp installs it:

1. **Source parsing**: classifies as `type: "remote"`, infers name `sentry`
2. **Transport**: defaults to `http` (MCP "streamable HTTP")
3. **Per-agent transform**: each agent gets the URL in its native remote-server format
4. **OAuth**: Sentry's endpoint requires authentication. The `www-authenticate` header points to `https://mcp.sentry.dev/.well-known/oauth-protected-resource/mcp`, which returns OAuth metadata including `authorization_endpoint`, `token_endpoint`, `registration_endpoint`, and supported scopes. add-mcp doesn't handle OAuth flows — users must pass `--header "Authorization: Bearer <token>"` manually.

This is fundamentally different from lapp's setup because lapp is always a **local stdio binary**. There's no OAuth, no remote URL, no transport choice. But add-mcp's config-writing infrastructure (JSONC, TOML, YAML, deep merge, per-agent transforms) is directly reusable.