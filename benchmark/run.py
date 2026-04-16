#!/usr/bin/env python3
"""
A/B benchmark: standard Read/Edit vs. lapp_read/lapp_edit.

For each instance, the agent is given a pre-fetched source file and asked to:
  1. Read the file using its tool set.
  2. Apply a specific change (shown as a unified diff in the prompt).
  3. Save the result.

This measures tool-interface token cost in isolation — not bug-finding ability.
Config A uses the built-in Read + Edit tools.
Config B uses either native Read + Edit or a lapp strategy, depending on LAPP_STRATEGY.

Bash is excluded from both configs so file edits must go through the
respective tool set (prevents sed/echo workarounds from polluting the signal).

Usage:
    python benchmark/run.py
    BENCHMARK_IDS=id1,id2 python benchmark/run.py     # subset
    SKIP_EXISTING=0 python benchmark/run.py           # re-run all
"""

import difflib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from textwrap import dedent

BENCHMARK_DIR = Path(__file__).parent
PROJECT_DIR   = BENCHMARK_DIR.parent
FILES_DIR     = BENCHMARK_DIR / "files"
RESULTS_BASE  = BENCHMARK_DIR / "results"
INSTANCES_FILE = BENCHMARK_DIR / "instances.json"
SUITES_FILE    = BENCHMARK_DIR / "v1_suites.json"
INSTRUCTIONS_FILE = PROJECT_DIR / "instructions" / "lapp-tools.md"

TIMEOUT   = 120   # seconds per config; >2m for a single-file targeted edit is benchmark-fail
MAX_TURNS = 20    # upper bound; keeps loops visible while allowing a few retries

# Agent selection: AGENT=claude (default) or AGENT=opencode
AGENT = os.environ.get("AGENT", "claude")

# OpenCode model to use (any model string opencode accepts)
OPENCODE_MODEL  = os.environ.get("OPENCODE_MODEL", "opencode/gpt-5-nano")
OPENCODE_CONFIG = Path.home() / ".config/opencode/opencode.json"
LAPP_GREP_FORMAT = os.environ.get("LAPP_GREP_FORMAT", "text")
LAPP_STRATEGY = os.environ.get("LAPP_STRATEGY")

V2_STRATEGIES = {
    "native-edit",
    "lapp-text-grep",
    "lapp-structured-grep",
    "lapp-replace-block",
}

# Claude tool names (--allowedTools values)
TOOLS_A = "Read,Edit,Write"
TOOLS_B = "lapp_read,lapp_edit,lapp_write,lapp_grep,lapp_insert_block,lapp_apply_patch"
TOOLS_B_REPLACE_BLOCK = (
    "lapp_read,lapp_edit,lapp_write,lapp_grep,lapp_insert_block,lapp_apply_patch,lapp_replace_block,lapp_find_block"
 )

# ---- Claude prompts ----
PROMPT_A = dedent("""\
    Apply the following change to the file. Read the file first to locate the
    exact content, make the replacement, save it.
    Use ONLY the Read and Edit tools. No shell commands.
    Do not explain your steps.

    Repository root: {filepath}

    {changes}
""")

PROMPT_B = dedent("""\
    Apply the following change to the file.
    {strategy_hint}
    If a full unified diff is provided below for one file, call lapp_apply_patch immediately before doing any manual grep/read/edit work.
    If lapp returns a stale_refs payload, retry using the returned local anchors
    instead of rereading the whole file.
    No shell commands. Do not explain your steps.

    Repository root: {filepath}

    {changes}
""")

# ---- OpenCode prompts (lowercase tool names, lapp exposed as lapp_lapp_*) ----
PROMPT_A_OC = dedent("""\
    Apply the following change to the file. Read the file first to locate the
    exact content, make the replacement, save it.
    Use ONLY the read and edit tools. No shell commands.
    Do not explain your steps.

    Repository root: {filepath}

    {changes}
""")

PROMPT_B_OC = dedent("""\
    Apply the following change to the file.
    {strategy_hint}
    If a full unified diff is provided below for one file, call lapp_lapp_apply_patch immediately before doing any manual grep/read/edit work.
    If lapp returns a stale_refs payload, retry using the returned local anchors
    instead of rereading the whole file.
    No shell commands. Do not explain your steps.

    Repository root: {filepath}

    {changes}
""")


# ---------------------------------------------------------------------------
# Work directory
# ---------------------------------------------------------------------------

def setup_work_dir(instance_id: str, work_dir: Path) -> None:
    """Copy pre-fetched files into work_dir, excluding the stored diff."""
    src = FILES_DIR / instance_id
    if not src.exists():
        raise FileNotFoundError(
            f"No files for {instance_id} — run prepare.py first"
        )
    for item in src.iterdir():
        if item.name == "_patch.diff":
            continue
        dst = work_dir / item.name
        if item.is_dir():
            shutil.copytree(item, dst)
        else:
            shutil.copy2(item, dst)


def reset_work_dir(instance_id: str, work_dir: Path) -> None:
    """Restore work_dir to original state between Config A and Config B."""
    shutil.rmtree(work_dir)
    work_dir.mkdir()
    setup_work_dir(instance_id, work_dir)


def capture_diff(work_dir: Path, instance_id: str) -> str:
    """Return a unified diff of every changed file in work_dir."""
    src = FILES_DIR / instance_id
    parts = []

    # Build union of relative paths from both source and work dirs.
    rels = set()
    for orig in src.rglob("*"):
        if orig.name.startswith("_") or orig.is_dir():
            continue
        rels.add(orig.relative_to(src))
    for cur in work_dir.rglob("*"):
        if cur.is_dir():
            continue
        rels.add(cur.relative_to(work_dir))

    for rel in sorted(rels):
        orig = src / rel
        current = work_dir / rel
        a_path = str(orig) if orig.exists() else "/dev/null"
        b_path = str(current) if current.exists() else "/dev/null"
        result = subprocess.run(
            ["diff", "-u",
             "--label", f"a/{rel}",
             "--label", f"b/{rel}",
             a_path, b_path],
            capture_output=True, text=True,
        )
        if result.stdout:
            parts.append(result.stdout)
    return "\n".join(parts)


# ---------------------------------------------------------------------------
# MCP config
# ---------------------------------------------------------------------------

def write_empty_mcp(path: Path) -> None:
    path.write_text('{"mcpServers": {}}')


def write_lapp_mcp(lapp_binary: str, work_dir: Path, path: Path) -> None:
    path.write_text(json.dumps({
        "mcpServers": {
            "lapp": {"command": lapp_binary, "args": ["--root", str(work_dir)]}
        }
    }))


# ---------------------------------------------------------------------------
# Prompt construction
# ---------------------------------------------------------------------------

def parse_patch_change(diff: str) -> list[dict]:
    """
    Parse a unified diff into a list of {file, old, new} dicts.
    Each entry describes one hunk: the exact lines to find and replace.
    Strips leading +/- markers; preserves indentation.
    """
    changes = []
    current_file = None
    old_lines: list[str] = []
    new_lines: list[str] = []

    def flush():
        if current_file and (old_lines or new_lines):
            changes.append({
                "file": current_file,
                "old": "\n".join(old_lines),
                "new": "\n".join(new_lines),
            })

    for line in diff.splitlines():
        if line.startswith("+++ b/"):
            flush()
            current_file = line[6:]
            old_lines, new_lines = [], []
        elif line.startswith("@@ "):
            flush()
            old_lines, new_lines = [], []
        elif line.startswith("-") and not line.startswith("---"):
            old_lines.append(line[1:])
        elif line.startswith("+") and not line.startswith("+++"):
            new_lines.append(line[1:])

    flush()
    return changes


def build_prompt(
    template: str,
    work_dir: Path,
    instance_id: str,
    strategy_hint: str = "",
    strategy: str | None = None,
 ) -> str:
    diff_path = FILES_DIR / instance_id / "_patch.diff"
    diff = diff_path.read_text() if diff_path.exists() else ""
    changes = parse_patch_change(diff)

    if not changes:
        # Fallback — should not happen for well-formed instances.
        return template.format(
            filepath=str(work_dir),
            changes="(no changes parsed)",
            strategy_hint=strategy_hint,
        )

    blocks = []
    for i, ch in enumerate(changes, 1):
        full_path = str(work_dir / ch["file"])
        label = f"Change {i}" if len(changes) > 1 else "Change"
        old_lines = ch['old'].splitlines()
        new_lines = ch['new'].splitlines()

        if not old_lines or not new_lines:
            old_block = _indent(ch['old']) if ch['old'] else '    (no old lines in this hunk)'
            new_block = _indent(ch['new']) if ch['new'] else '    (delete the matched range)'
            insert_tool_name = "lapp_lapp_insert_block" if AGENT == "opencode" else "lapp_insert_block"
            if not old_lines and new_lines:
                hunk_note = "This is an INSERTION-ONLY hunk."
                if strategy != "native-edit":
                    hunk_note += (
                        f" Find the exact anchor line or block immediately before this insertion, then use {insert_tool_name} instead of piecing together multiple edits."
                    )
            else:
                hunk_note = "This is a DELETION-ONLY hunk."
                if strategy != "native-edit":
                    hunk_note += " Locate the exact stale block and apply one delete edit; do not force replace_block."
            blocks.append(
                f"{label}\n"
                f"  File: {full_path}\n"
                f"  {hunk_note}\n"
                f"  Old block (if any):\n{old_block}\n"
                f"  New block (if any):\n{new_block}"
            )
        elif len(old_lines) > 1 or len(new_lines) > 1:
            blocks.append(
                f"{label}\n"
                f"  File: {full_path}\n"
                f"  This is a MULTI-LINE range replacement.\n"
                f"  First line to replace:\n{_indent(old_lines[0])}\n"
                f"  Last line to replace:\n{_indent(old_lines[-1])}\n"
                f"  Replace the entire range containing this exact old block:\n{_indent(ch['old'])}\n"
                f"  With this new block:\n{_indent(ch['new'])}"
            )
        else:
            blocks.append(
                f"{label}\n"
                f"  File: {full_path}\n"
                f"  Find this exact content:\n{_indent(ch['old'])}\n"
                f"  Replace with:\n{_indent(ch['new'])}"
            )

    if strategy and strategy != "native-edit" and len(changes) > 1:
        rendered = (
            "This prompt contains multiple changes in one file. FIRST ACTION: use lapp_apply_patch with the unified diff below. "
            "Only if apply_patch fails may you use the fallback per-hunk instructions after it.\n\n"
            "Unified diff for this file:\n\n" + diff + "\n\nFallback per-hunk instructions (only if apply_patch fails):\n\n" + "\n\n".join(blocks)
        )
    else:
        rendered = "\n\n".join(blocks)
    return template.format(
        filepath=str(work_dir),
        changes=rendered,
        strategy_hint=strategy_hint,
    )


def strategy_hint(strategy: str, agent: str) -> str:
    if strategy == "native-edit":
        if agent == "opencode":
            return "Use the read and edit tools (no lapp tools)."
        return "Use the Read and Edit tools (no lapp tools)."

    if agent == "opencode":
        grep_tool = "lapp_lapp_grep"
        edit_tool = "lapp_lapp_edit"
        insert_tool = "lapp_lapp_insert_block"
        apply_tool = "lapp_lapp_apply_patch"
        replace_tool = "lapp_lapp_replace_block"
        find_tool = "lapp_lapp_find_block"
        read_tool = "lapp_lapp_read"
    else:
        grep_tool = "lapp_grep"
        edit_tool = "lapp_edit"
        insert_tool = "lapp_insert_block"
        apply_tool = "lapp_apply_patch"
        replace_tool = "lapp_replace_block"
        find_tool = "lapp_find_block"
        read_tool = "lapp_read"

    if strategy == "lapp-text-grep":
        return (
            f"Use only {grep_tool}, {edit_tool}, {read_tool}, {insert_tool}, and {apply_tool}. "
            f"Do not use {replace_tool} or {find_tool}. If the prompt contains multiple changes in the same file, your FIRST attempt must be {apply_tool} with the full diff from the prompt. "
            f"Only if {apply_tool} fails should you fall back to manual {grep_tool}/{read_tool}/{edit_tool} steps. For insertion-only multi-line changes, locate the exact anchor line or block with {grep_tool}/{read_tool} and use {insert_tool}."
        )

    if strategy == "lapp-structured-grep":
        return (
            f"Use only {grep_tool}, {edit_tool}, {read_tool}, {insert_tool}, and {apply_tool}. "
            f"Do not use {replace_tool} or {find_tool}. If the prompt contains multiple changes in the same file, your FIRST attempt must be {apply_tool} with the full diff from the prompt. "
            f"Only if {apply_tool} fails should you use {grep_tool} with literal=true and format=structured to get machine-readable anchors, confirm the exact range with {read_tool}, then apply one {edit_tool} range replacement. For insertion-only multi-line changes, use {insert_tool} after locating the exact anchor block."
        )

    if strategy == "lapp-replace-block":
        return (
            f"For repeated edits in one file, your FIRST attempt must be {apply_tool} if the full unified diff is provided. Only if {apply_tool} fails should you use {replace_tool} for multi-line replacements. "
            f"For insertion-only multi-line changes, use {insert_tool} after locating the exact anchor block. Only if {replace_tool} fails with multiple matches or stale refs, use {find_tool} once to recover the exact range, then {edit_tool}."
        )


    return ""


def _indent(text: str) -> str:  # prefix every line with 4 spaces for readability
    return "\n".join(f"    {line}" for line in text.splitlines())


def patch_similarity(applied: str, reference: str) -> float:
    def changed_lines(patch: str) -> list[str]:
        return [
            line
            for line in patch.splitlines()
            if (line.startswith("+") or line.startswith("-"))
            and not line.startswith("+++")
            and not line.startswith("---")
        ]

    a = changed_lines(applied)
    b = changed_lines(reference)
    if not a and not b:
        return 1.0
    if not a or not b:
        return 0.0
    return difflib.SequenceMatcher(None, a, b).ratio()


V2_METADATA_FIELDS = ["file_size_bucket", "change_count_bucket"]


def _load_suite_file(path: Path) -> dict:
    try:
        return json.loads(path.read_text())
    except FileNotFoundError as exc:
        raise SystemExit(f"ERROR: suite file not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise SystemExit(f"ERROR: suite file {path} is not valid JSON: {exc}") from exc


def _validate_suite_file(path: Path) -> dict:
    data = _load_suite_file(path)
    suites = data.get("suites")
    if not isinstance(suites, dict):
        raise SystemExit(f"ERROR: suite file {path} missing 'suites' object")
    return data


def _extract_suite_metadata(raw: dict[str, object]) -> dict[str, str]:
    metadata = raw.get("metadata")
    if not isinstance(metadata, dict):
        return {}

    return {
        field: value
        for field in V2_METADATA_FIELDS
        if (value := metadata.get(field)) is not None
    }


def _extract_suite_strategies(raw: dict[str, object], suite_name: str) -> list[str]:
    value = raw.get("strategies")
    if value is None:
        return []
    if not isinstance(value, list):
        raise SystemExit(
            f"ERROR: suite {suite_name!r} must define 'strategies' as a list"
        )

    strategies: list[str] = []
    for item in value:
        if not isinstance(item, str):
            raise SystemExit(
                f"ERROR: suite {suite_name!r} strategy entries must be strings in {value!r}"
            )
        strategies.append(item)

    return strategies


def _validate_suite_strategies(strategies: list[str], suite_name: str) -> None:
    unknown = [s for s in strategies if s not in V2_STRATEGIES]
    if unknown:
        raise SystemExit(
            "ERROR: suite "
            f"{suite_name!r} includes unsupported strategies: {', '.join(unknown)}; "
            f"supported: {', '.join(sorted(V2_STRATEGIES))}"
        )


def load_suite(name: str, suite_file: Path) -> tuple[
    list[str],
    dict[str, dict[str, str]],
    dict[str, str],
    list[str],
]:
    data = _validate_suite_file(suite_file)
    suites = data.get("suites")

    if name not in suites:
        raise SystemExit(
            f"unknown suite {name!r}; available: {', '.join(sorted(suites))}"
        )

    suite = suites[name]
    if not isinstance(suite, dict):
        raise SystemExit(f"ERROR: suite {name!r} entry must be an object in {suite_file}")

    instances = suite.get("instances")
    if not isinstance(instances, list):
        raise SystemExit(
            f"ERROR: suite {name!r} must define 'instances' as a list in {suite_file}"
        )

    suite_defaults: dict[str, str] = _extract_suite_metadata(suite)
    suite_strategies = _extract_suite_strategies(suite, name)
    _validate_suite_strategies(suite_strategies, name)

    suite_ids: list[str] = []
    per_instance_metadata: dict[str, dict[str, str]] = {}

    for entry in instances:
        if isinstance(entry, str):
            suite_ids.append(entry)
            continue

        if isinstance(entry, dict):
            instance_id = entry.get("instance_id") or entry.get("id")
            if not isinstance(instance_id, str) or not instance_id:
                raise SystemExit(
                    f"ERROR: invalid instance entry in suite {name!r}: {entry}"
                )
            suite_ids.append(instance_id)
            per_instance = _extract_suite_metadata(entry)
            if per_instance:
                per_instance_metadata[instance_id] = per_instance
            continue

        raise SystemExit(
            f"ERROR: invalid instance entry in suite {name!r}: {entry}"
        )

    return suite_ids, per_instance_metadata, suite_defaults, suite_strategies


def benchmark_version(suite_file: Path) -> str:
    if not suite_file.exists():
        return "ad-hoc"
    return _load_suite_file(suite_file).get("version", "ad-hoc")


# ---------------------------------------------------------------------------
# OpenCode config helpers
# ---------------------------------------------------------------------------

def _oc_config(lapp_binary: str | None, work_dir: Path | None) -> str:
    """
    Return opencode config JSON for benchmark reproducibility.

    Constructs a minimal, deterministic config so that Config A/B are
    reproducible across machines. When lapp_binary is None (Config A),
    returns a bare config with no MCP servers. When lapp_binary is set
    (Config B), adds the lapp MCP server and instructions file.
    """
    cfg: dict = {}
    if lapp_binary is None:
        return json.dumps(cfg)
    command = [lapp_binary, "--root", str(work_dir)]
    only_tools = os.environ.get("LAPP_ONLY_TOOLS", "").strip()
    if only_tools:
        command.extend(["--only-tools", only_tools])
    cfg.setdefault("mcp", {})["lapp"] = {
        "type": "local",
        "command": command,
        "enabled": True,
    }
    # Always-on instructions: match lapp-setup's OpenCode integration.
    if INSTRUCTIONS_FILE.exists():
        instructions_path = str(INSTRUCTIONS_FILE)
        instructions = cfg.setdefault("instructions", [])
        if instructions_path not in instructions:
            instructions.append(instructions_path)
    return json.dumps(cfg)


# ---------------------------------------------------------------------------
# OpenCode runner
# ---------------------------------------------------------------------------

def run_opencode(
    prompt: str,
    lapp_binary: str | None,   # None = Config A (no lapp)
    work_dir: Path,
) -> dict:
    """
    Run opencode run --format json with the prompt on stdin.

    Config A: base opencode config (no lapp).
    Config B: base config merged with lapp MCP.
    Tool restriction is prompt-driven (no --allowedTools equivalent in opencode).
    """
    env = {**os.environ, "OPENCODE_CONFIG_CONTENT": _oc_config(lapp_binary, work_dir)}
    cmd = [
        "opencode", "run",
        "--format", "json",
        "--model", OPENCODE_MODEL,
    ]

    wall_start = time.monotonic()
    try:
        proc = subprocess.run(
            cmd,
            input=prompt,
            capture_output=True,
            text=True,
            timeout=TIMEOUT,
            cwd=work_dir,
            env=env,
        )
    except subprocess.TimeoutExpired:
        return _err(f"timeout after {TIMEOUT}s")
    wall_ms = int((time.monotonic() - wall_start) * 1000)


    # Parse the event stream. Each line is a JSON event.
    output_tokens = input_tokens = cache_read = cache_write = num_turns = 0
    cost = 0.0
    tools_used: list[str] = []
    last_error = ""
    turns_ms: list[int] = []
    step_start_ts: int | None = None

    for line in proc.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
            t = obj.get("type", "")
            part = obj.get("part", {})
            if t == "step_start":
                step_start_ts = obj.get("timestamp", 0)
            elif t == "tool_use":
                tools_used.append(part.get("tool", "?"))
            elif t == "step_finish":
                tok = part.get("tokens", {})
                output_tokens += tok.get("output", 0)
                input_tokens  += tok.get("input", 0)
                cache_read    += tok.get("cache", {}).get("read", 0)
                cache_write   += tok.get("cache", {}).get("write", 0)
                cost          += part.get("cost", 0.0)
                num_turns     += 1
                if step_start_ts:
                    turns_ms.append(obj.get("timestamp", 0) - step_start_ts)
                    step_start_ts = None
            elif t == "error":
                err_data = obj.get("error", {})
                last_error = str(err_data.get("data", {}).get("message", err_data))[:200]
        except json.JSONDecodeError:
            pass
            pass

    if last_error and output_tokens == 0:
        return _err(last_error)

    if proc.returncode != 0:
        detail = (proc.stderr[:300] or proc.stdout[:300]).strip()
        return _err(detail or f"exit {proc.returncode}")

    return {
        "output_tokens":       output_tokens,
        "input_tokens":        input_tokens,
        "cache_read_tokens":   cache_read,
        "cache_create_tokens": cache_write,
        "num_turns":           num_turns,
        "cost_usd":            cost,
        "wall_ms":             wall_ms,
        "turns_ms":            turns_ms,
        "stop_reason":         "end_turn",
        "tools_used":          tools_used,
        "error":               "",
    }

# ---------------------------------------------------------------------------
# Agent runner
# ---------------------------------------------------------------------------

def run_claude(
    prompt: str,
    allowed_tools: str,
    mcp_config_path: Path,
    work_dir: Path,
) -> dict:
    cmd = [
        "claude",
        "--print",
        "--output-format", "json",
        "--max-turns", str(MAX_TURNS),
        "--permission-mode", "bypassPermissions",
        "--no-session-persistence",
        "--strict-mcp-config",
        "--mcp-config", str(mcp_config_path),
        "--allowedTools", allowed_tools,
    ]

    wall_start = time.monotonic()
    try:
        proc = subprocess.run(
            cmd,
            input=prompt,
            capture_output=True,
            text=True,
            timeout=TIMEOUT,
            cwd=work_dir,
        )
    except subprocess.TimeoutExpired:
        return _err(f"timeout after {TIMEOUT}s")
    wall_ms = int((time.monotonic() - wall_start) * 1000)


    if proc.returncode != 0:
        detail = (proc.stderr[:300] or proc.stdout[:300]).strip()
        return _err(detail or f"exit {proc.returncode}")

    try:
        raw = json.loads(proc.stdout)
    except json.JSONDecodeError:
        return _err(f"non-JSON output: {proc.stdout[:200]}")

    if raw.get("is_error"):
        subtype = raw.get("subtype", "")
        # error_max_turns: preserve partial token data so the run appears in the report.
        if subtype == "error_max_turns":
            usage = raw.get("usage", {})
            return {
                "output_tokens":       usage.get("output_tokens", 0),
                "input_tokens":        usage.get("input_tokens", 0),
                "cache_read_tokens":   usage.get("cache_read_input_tokens", 0),
                "cache_create_tokens": usage.get("cache_creation_input_tokens", 0),
                "num_turns":           raw.get("num_turns", 0),
                "cost_usd":            raw.get("total_cost_usd", 0.0),
                "wall_ms":             wall_ms,
                "turns_ms":            [],
                "stop_reason":         subtype,
                "error":               "max_turns",
            }
        return _err(raw.get("result", "unknown")[:200])

    usage = raw.get("usage", {})
    return {
        "output_tokens":       usage.get("output_tokens", 0),
        "input_tokens":        usage.get("input_tokens", 0),
        "cache_read_tokens":   usage.get("cache_read_input_tokens", 0),
        "cache_create_tokens": usage.get("cache_creation_input_tokens", 0),
        "num_turns":           raw.get("num_turns", 0),
        "cost_usd":            raw.get("total_cost_usd", 0.0),
        "wall_ms":             raw.get("duration_ms", wall_ms),
        "turns_ms":            [],  # claude --output-format json has no per-turn timestamps
        "stop_reason":         raw.get("stop_reason", ""),
        "error":               "",
    }


def _err(msg: str) -> dict:
    return {
        "output_tokens": 0, "input_tokens": 0,
        "cache_read_tokens": 0, "cache_create_tokens": 0,
        "num_turns": 0, "cost_usd": 0.0,
        "wall_ms": 0, "turns_ms": [],
        "stop_reason": "", "error": msg,
    }


# ---------------------------------------------------------------------------
# Per-instance orchestration
# ---------------------------------------------------------------------------

def run_instance(instance: dict, lapp_binary: str, strategy: str | None = None) -> dict:
    iid = instance["instance_id"]
    run_strategy = strategy or (
        "lapp-structured-grep" if AGENT == "opencode" and LAPP_GREP_FORMAT == "structured"
        else "lapp-text-grep"
    )

    with tempfile.TemporaryDirectory(prefix="lapp-bench-") as tmp:
        tmp_path = Path(tmp)
        work_dir = (tmp_path / "work").resolve()
        work_dir.mkdir(parents=True, exist_ok=True)

        try:
            setup_work_dir(iid, work_dir)
        except FileNotFoundError as exc:
            return {"instance_id": iid, "error": str(exc)}

        if AGENT == "opencode":
            # OpenCode: config injection via OPENCODE_CONFIG_CONTENT; no --allowedTools.
            # Config A = base config (no lapp).  Config B = base + lapp MCP.
            print(f"    [A] read → edit  (opencode/{OPENCODE_MODEL})", flush=True)
            prompt_a = build_prompt(PROMPT_A_OC, work_dir, iid, strategy="native-edit")
            result_a = run_opencode(prompt_a, None, work_dir)
            diff_a   = capture_diff(work_dir, iid)

            reset_work_dir(iid, work_dir)

            if run_strategy == "native-edit":
                print(f"    [B] read → edit  (opencode/{OPENCODE_MODEL})", flush=True)
                prompt_b = build_prompt(PROMPT_A_OC, work_dir, iid, strategy="native-edit")
                result_b = run_opencode(prompt_b, None, work_dir)
            else:
                b_tool_desc = "lapp_lapp_replace_block" if run_strategy == "lapp-replace-block" else "lapp_lapp_grep"
                print(
                    f"    [B] {b_tool_desc} → lapp_lapp_edit  (opencode/{OPENCODE_MODEL})",
                    flush=True,
                )
                prompt_b = build_prompt(
                    PROMPT_B_OC,
                    work_dir,
                    iid,
                    strategy_hint=strategy_hint(run_strategy, AGENT),
                    strategy=run_strategy,
                )
                result_b = run_opencode(prompt_b, lapp_binary, work_dir)

            diff_b = capture_diff(work_dir, iid)

        else:
            # Claude Code: --allowedTools + --strict-mcp-config for clean isolation.
            empty_mcp = tmp_path / "empty.json"
            write_empty_mcp(empty_mcp)
            lapp_mcp  = tmp_path / "lapp.json"
            write_lapp_mcp(lapp_binary, work_dir, lapp_mcp)

            print("    [A] Read → Edit  (claude)", flush=True)
            prompt_a = build_prompt(PROMPT_A, work_dir, iid, strategy="native-edit")
            result_a = run_claude(prompt_a, TOOLS_A, empty_mcp, work_dir)
            diff_a   = capture_diff(work_dir, iid)

            reset_work_dir(iid, work_dir)
            write_lapp_mcp(lapp_binary, work_dir, lapp_mcp)

            if run_strategy == "native-edit":
                print("    [B] Read → Edit  (claude)", flush=True)
                prompt_b = build_prompt(PROMPT_A, work_dir, iid, strategy="native-edit")
                result_b = run_claude(prompt_b, TOOLS_A, empty_mcp, work_dir)
            else:
                tools_b = TOOLS_B_REPLACE_BLOCK if run_strategy == "lapp-replace-block" else TOOLS_B
                b_label = "lapp_replace_block → lapp_edit" if run_strategy == "lapp-replace-block" else "lapp_read → lapp_edit"
                print(f"    [B] {b_label}  (claude)", flush=True)
                prompt_b = build_prompt(
                    PROMPT_B,
                    work_dir,
                    iid,
                    strategy_hint=strategy_hint(run_strategy, AGENT),
                    strategy=run_strategy,
                )
                result_b = run_claude(prompt_b, tools_b, lapp_mcp, work_dir)

            diff_b = capture_diff(work_dir, iid)

    return {
        "instance_id":     iid,
        "repo":            instance["repo"],
        "reference_patch": instance["patch"],
        "a": {**result_a, "diff": diff_a, "correctness_similarity": patch_similarity(diff_a, instance["patch"])},
        "b": {**result_b, "diff": diff_b, "correctness_similarity": patch_similarity(diff_b, instance["patch"])},
    }


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def current_model_name() -> str:
    return OPENCODE_MODEL if AGENT == "opencode" else os.environ.get("CLAUDE_MODEL", "claude")


def current_grep_variant() -> str:
    return LAPP_GREP_FORMAT if AGENT == "opencode" else "text"


def slug_model_name(name: str) -> str:
    out = []
    for ch in name:
        if ch.isalnum() or ch in {'.', '-'}:
            out.append(ch)
        else:
            out.append('_')
    slug = ''.join(out).strip('_')
    while '__' in slug:
        slug = slug.replace('__', '_')
    return slug or 'unknown-model'

def resolve_v2_strategy(suite_strategies: list[str]) -> str | None:
    strategy = LAPP_STRATEGY
    if strategy is not None and strategy not in V2_STRATEGIES:
        raise SystemExit(
            f"ERROR: invalid LAPP_STRATEGY {strategy!r}; "
            f"expected one of: {', '.join(sorted(V2_STRATEGIES))}"
        )

    if strategy is not None:
        if suite_strategies and strategy not in suite_strategies:
            raise SystemExit(
                f"ERROR: LAPP_STRATEGY {strategy!r} is not configured for this suite; "
                f"available: {', '.join(suite_strategies)}"
            )
        return strategy

    # Preserve V1-style grep defaults when no V2 override is set.
    if "lapp-structured-grep" in suite_strategies and AGENT == "opencode" and LAPP_GREP_FORMAT == "structured":
        return "lapp-structured-grep"
    if "lapp-text-grep" in suite_strategies:
        return "lapp-text-grep"
    if suite_strategies:
        return suite_strategies[0]
    return None


def canonical_results_dir(suite_name: str | None, variant: str | None = None) -> Path:
    override = os.environ.get("RESULTS_SUBDIR")
    if override:
        return RESULTS_BASE / override
    suite_part = suite_name or 'ad-hoc'
    model_part = f"{AGENT}__{slug_model_name(current_model_name())}"
    if variant:
        model_part += f"__{slug_model_name(variant)}"
    elif current_grep_variant() != "text":
        model_part += f"__grep-{slug_model_name(current_grep_variant())}"
    return RESULTS_BASE / suite_part / model_part


def _parse_args() -> tuple[str | None, Path]:
    suite_name: str | None = None
    suite_file = SUITES_FILE
    args = sys.argv[1:]
    idx = 0

    while idx < len(args):
        arg = args[idx]
        if arg == "--suite":
            if idx + 1 >= len(args):
                print("ERROR: --suite requires a suite name", file=sys.stderr)
                sys.exit(1)
            suite_name = args[idx + 1]
            idx += 2
        elif arg == "--suite-file":
            if idx + 1 >= len(args):
                print("ERROR: --suite-file requires a file path", file=sys.stderr)
                sys.exit(1)
            suite_file = Path(args[idx + 1]).expanduser()
            idx += 2
        else:
            print(f"ERROR: unknown argument: {arg}", file=sys.stderr)
            print("Supported arguments: --suite, --suite-file", file=sys.stderr)
            sys.exit(1)

    return suite_name, suite_file


def _get_suite_instance_metadata(
    suite_default: dict[str, str],
    per_instance: dict[str, dict[str, str]],
    instance_id: str,
) -> dict[str, str]:
    merged = dict(suite_default)
    merged.update(per_instance.get(instance_id, {}))
    return merged


def main() -> None:
    suite_name, suite_file = _parse_args()

    if not INSTANCES_FILE.exists():
        print("ERROR: instances.json not found — run fetch_instances.py first.",
              file=sys.stderr)
        sys.exit(1)

    instances: list[dict] = json.loads(INSTANCES_FILE.read_text())
    if not instances:
        print("ERROR: instances.json is empty — run fetch_instances.py first.",
              file=sys.stderr)
        sys.exit(1)

    suite_defaults: dict[str, str] = {}
    per_instance_metadata: dict[str, dict[str, str]] = {}
    suite_strategies: list[str] = []
    suite_version = benchmark_version(suite_file)

    if suite_name:
        suite_ids, per_instance_metadata, suite_defaults, suite_strategies = load_suite(
            suite_name, suite_file
        )
        suite_id_set = set(suite_ids)
        instances = [
            {
                **i,
                **_get_suite_instance_metadata(suite_defaults, per_instance_metadata, i["instance_id"]),
            }
            for i in instances
            if i["instance_id"] in suite_id_set
        ]
    elif suite_file != SUITES_FILE:
        # Validate only: suite file is explicitly requested.
        _validate_suite_file(suite_file)
        print(f"Validated suite file: {suite_file}")
        return

    if suite_version == "v2" and not suite_strategies:
        raise SystemExit(
            "ERROR: v2 suite metadata missing 'strategies'; add a suite-level strategy list"
        )

    strategy = None
    if suite_version == "v2":
        strategy = resolve_v2_strategy(suite_strategies)
        if strategy is None:
            raise SystemExit(
                "ERROR: unable to determine V2 strategy; set LAPP_STRATEGY to one of: "
                f"{', '.join(sorted(V2_STRATEGIES))}"
            )
    elif LAPP_STRATEGY:
        if LAPP_STRATEGY not in V2_STRATEGIES:
            raise SystemExit(
                f"ERROR: invalid LAPP_STRATEGY {LAPP_STRATEGY!r}; "
                f"expected one of: {', '.join(sorted(V2_STRATEGIES))}"
            )
        strategy = LAPP_STRATEGY

    lapp_binary = shutil.which("lapp")
    needs_lapp = strategy != "native-edit"
    if not lapp_binary and needs_lapp:
        print("ERROR: lapp not in PATH — run: go install ./cmd/lapp",
              file=sys.stderr)
        sys.exit(1)

    filter_ids = {s for s in os.environ.get("BENCHMARK_IDS", "").split(",") if s}
    if filter_ids:
        instances = [i for i in instances if i["instance_id"] in filter_ids]
        print(f"Subset: {len(instances)} instance(s)")
    elif suite_name:
        print(f"Running suite {suite_name}: {len(instances)} instance(s)")
    else:
        print(f"Running {len(instances)} instance(s)")

    skip_existing = os.environ.get("SKIP_EXISTING", "1") != "0"
    results_dir = canonical_results_dir(
        suite_name, strategy if suite_version == "v2" else None
    )
    results_dir.mkdir(parents=True, exist_ok=True)

    for idx, instance in enumerate(instances, 1):
        iid = instance["instance_id"]
        out_path = results_dir / f"{iid}.json"

        if skip_existing and out_path.exists():
            print(f"  [{idx}/{len(instances)}] {iid}  (skipped)")
            continue

        print(f"  [{idx}/{len(instances)}] {iid}")
        result = run_instance(
            instance,
            lapp_binary,
            strategy=strategy,
        )
        result.setdefault("benchmark_version", suite_version)
        result.setdefault("suite", suite_name or "ad-hoc")
        result.setdefault("agent", AGENT)
        result.setdefault("model", current_model_name())
        result.setdefault("grep_format", current_grep_variant())
        for field in V2_METADATA_FIELDS:
            if field in instance:
                result.setdefault(field, instance[field])
        if suite_version == "v2":
            if any(not instance.get(field) for field in V2_METADATA_FIELDS):
                raise SystemExit(
                    f"ERROR: missing V2 metadata for {iid}; expected fields "
                    f"{', '.join(V2_METADATA_FIELDS)}"
                )
            result["strategy"] = strategy
        out_path.write_text(json.dumps(result, indent=2))

        if "error" in result and "a" not in result:
            print(f"           ERROR: {result['error']}")
            continue

        a, b = result.get("a", {}), result.get("b", {})
        err = a.get("error") or b.get("error")
        if err:
            print(f"           ERROR: {err}")
        else:
            delta = b["output_tokens"] - a["output_tokens"]
            sign = "+" if delta >= 0 else ""
            print(
                f"           A={a['output_tokens']} out-tok  "
                f"B={b['output_tokens']} out-tok  "
                f"Δ={sign}{delta}"
            )

    print(f"\nResults in {results_dir}/")
    print("Run 'python benchmark/report.py --dir <suite/model> or benchmark/v1_report.py' to see the table.")


if __name__ == "__main__":
    main()
