#!/usr/bin/env python3
"""
A/B benchmark: standard Read/Edit vs. lapp_read/lapp_edit.

For each instance, the agent is given a pre-fetched source file and asked to:
  1. Read the file using its tool set.
  2. Apply a specific change (shown as a unified diff in the prompt).
  3. Save the result.

This measures tool-interface token cost in isolation — not bug-finding ability.
Config A uses the built-in Read + Edit tools.
Config B uses lapp_read + lapp_edit.

Bash is excluded from both configs so file edits must go through the
respective tool set (prevents sed/echo workarounds from polluting the signal).

Usage:
    python benchmark/run.py
    BENCHMARK_IDS=id1,id2 python benchmark/run.py     # subset
    SKIP_EXISTING=0 python benchmark/run.py           # re-run all
"""

import copy
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
FILES_DIR     = BENCHMARK_DIR / "files"
RESULTS_BASE  = BENCHMARK_DIR / "results"
INSTANCES_FILE = BENCHMARK_DIR / "instances.json"
SUITES_FILE    = BENCHMARK_DIR / "v1_suites.json"

TIMEOUT   = 120   # seconds per config; >2m for a single-file targeted edit is benchmark-fail
MAX_TURNS = 20    # upper bound; keeps loops visible while allowing a few retries

# Agent selection: AGENT=claude (default) or AGENT=opencode
AGENT = os.environ.get("AGENT", "claude")

# OpenCode model to use (any model string opencode accepts)
OPENCODE_MODEL  = os.environ.get("OPENCODE_MODEL", "opencode/gpt-5-nano")
OPENCODE_CONFIG = Path.home() / ".config/opencode/opencode.json"
LAPP_GREP_FORMAT = os.environ.get("LAPP_GREP_FORMAT", "text")

# Claude tool names (--allowedTools values)
TOOLS_A = "Read,Edit,Write"
TOOLS_B = "lapp_read,lapp_edit,lapp_write,lapp_grep"

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
    Apply the following change to the file. Read the file first to locate the
    exact content, make the replacement, save it.
    {grep_hint}
    If a change spans multiple lines, prefer lapp_find_block with the exact old
    block, then use the returned start/end refs in lapp_edit.
    Do not use a single-anchor replace for a multi-line block.
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
    Apply the following change to the file. Read the file first to locate the
    exact content, make the replacement, save it.
    {grep_hint}
    If a change spans multiple lines, prefer lapp_lapp_find_block with the exact
    old block, then use the returned start/end refs in lapp_lapp_edit.
    Do not use a single-anchor replace for a multi-line block.
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
    for orig in src.rglob("*"):
        if orig.name.startswith("_") or orig.is_dir():
            continue
        rel = orig.relative_to(src)
        current = work_dir / rel
        if not current.exists():
            continue
        result = subprocess.run(
            ["diff", "-u",
             "--label", f"a/{rel}",
             "--label", f"b/{rel}",
             str(orig), str(current)],
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


def build_prompt(template: str, work_dir: Path, instance_id: str) -> str:
    diff_path = FILES_DIR / instance_id / "_patch.diff"
    diff = diff_path.read_text() if diff_path.exists() else ""
    changes = parse_patch_change(diff)

    if not changes:
        # Fallback — should not happen for well-formed instances.
        return template.format(filepath=str(work_dir), changes="(no changes parsed)", grep_hint=grep_hint())

    blocks = []
    for i, ch in enumerate(changes, 1):
        full_path = str(work_dir / ch["file"])
        label = f"Change {i}" if len(changes) > 1 else "Change"
        old_lines = ch['old'].splitlines()
        new_lines = ch['new'].splitlines()

        if not old_lines or not new_lines:
            old_block = _indent(ch['old']) if ch['old'] else '    (no old lines in this hunk)'
            new_block = _indent(ch['new']) if ch['new'] else '    (delete the matched range)'
            blocks.append(
                f"{label}\n"
                f"  File: {full_path}\n"
                f"  This hunk includes insertion-only or deletion-only lines.\n"
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

    return template.format(filepath=str(work_dir), changes="\n\n".join(blocks), grep_hint=grep_hint())


def grep_hint() -> str:
    if AGENT == "opencode":
        tool = "lapp_lapp_grep"
        edit = "lapp_lapp_edit"
    else:
        tool = "lapp_grep"
        edit = "lapp_edit"
    if LAPP_GREP_FORMAT == "structured":
        return f"Preferred: {tool} with literal=true and format=structured to get a machine-readable anchor, then use that anchor directly in {edit}."
    return f"Preferred: {tool} with literal=true to find the line → {edit}."


def _indent(text: str) -> str:  # prefix every line with 4 spaces for readability
    return "\n".join(f"    {l}" for l in text.splitlines())


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


def load_v1_suite(name: str) -> list[str]:
    data = json.loads(SUITES_FILE.read_text())
    suites = data.get("suites", {})
    if name not in suites:
        raise SystemExit(f"unknown suite {name!r}; available: {', '.join(sorted(suites))}")
    return suites[name]["instances"]


def benchmark_version() -> str:
    if SUITES_FILE.exists():
        return json.loads(SUITES_FILE.read_text()).get("version", "ad-hoc")
    return "ad-hoc"


# ---------------------------------------------------------------------------
# OpenCode config helpers
# ---------------------------------------------------------------------------

def _oc_config(lapp_binary: str | None, work_dir: Path | None) -> str:
    """
    Return opencode config JSON, merging lapp MCP into the user's existing config.
    If lapp_binary is None, returns the base config unchanged (Config A).
    """
    base = json.loads(OPENCODE_CONFIG.read_text()) if OPENCODE_CONFIG.exists() else {}
    if lapp_binary is None:
        return json.dumps(base)
    cfg = copy.deepcopy(base)
    cfg.setdefault("mcp", {})["lapp"] = {
        "type": "local",
        "command": [lapp_binary, "--root", str(work_dir)],
        "enabled": True,
    }
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

    if proc.returncode != 0 and output_tokens == 0:
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

def run_instance(instance: dict, lapp_binary: str) -> dict:
    iid = instance["instance_id"]

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
            # Config A = base config (no lapp).  Config B = base + lapp merged in.
            print(f"    [A] read → edit  (opencode/{OPENCODE_MODEL})", flush=True)
            prompt_a = build_prompt(PROMPT_A_OC, work_dir, iid)
            result_a = run_opencode(prompt_a, None, work_dir)
            diff_a   = capture_diff(work_dir, iid)

            reset_work_dir(iid, work_dir)

            print(f"    [B] lapp_lapp_grep → lapp_lapp_edit  (opencode/{OPENCODE_MODEL})", flush=True)
            prompt_b = build_prompt(PROMPT_B_OC, work_dir, iid)
            result_b = run_opencode(prompt_b, lapp_binary, work_dir)
            diff_b   = capture_diff(work_dir, iid)

        else:
            # Claude Code: --allowedTools + --strict-mcp-config for clean isolation.
            empty_mcp = tmp_path / "empty.json"
            write_empty_mcp(empty_mcp)
            lapp_mcp  = tmp_path / "lapp.json"
            write_lapp_mcp(lapp_binary, work_dir, lapp_mcp)

            print(f"    [A] Read → Edit  (claude)", flush=True)
            prompt_a = build_prompt(PROMPT_A, work_dir, iid)
            result_a = run_claude(prompt_a, TOOLS_A, empty_mcp, work_dir)
            diff_a   = capture_diff(work_dir, iid)

            reset_work_dir(iid, work_dir)
            write_lapp_mcp(lapp_binary, work_dir, lapp_mcp)

            print(f"    [B] lapp_read → lapp_edit  (claude)", flush=True)
            prompt_b = build_prompt(PROMPT_B, work_dir, iid)
            result_b = run_claude(prompt_b, TOOLS_B, lapp_mcp, work_dir)
            diff_b   = capture_diff(work_dir, iid)

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


def canonical_results_dir(suite_name: str | None) -> Path:
    override = os.environ.get("RESULTS_SUBDIR")
    if override:
        return RESULTS_BASE / override
    suite_part = suite_name or 'ad-hoc'
    model_part = f"{AGENT}__{slug_model_name(current_model_name())}"
    if current_grep_variant() != "text":
        model_part += f"__grep-{slug_model_name(current_grep_variant())}"
    return RESULTS_BASE / suite_part / model_part


def main() -> None:
    if not INSTANCES_FILE.exists():
        print(f"ERROR: instances.json not found — run fetch_instances.py first.",
              file=sys.stderr)
        sys.exit(1)

    instances: list[dict] = json.loads(INSTANCES_FILE.read_text())
    if not instances:
        print("ERROR: instances.json is empty — run fetch_instances.py first.",
              file=sys.stderr)
        sys.exit(1)

    suite_name = None
    if "--suite" in sys.argv:
        idx = sys.argv.index("--suite")
        suite_name = sys.argv[idx + 1]
        suite_ids = set(load_v1_suite(suite_name))
        instances = [i for i in instances if i["instance_id"] in suite_ids]

    lapp_binary = shutil.which("lapp")
    if not lapp_binary:
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
    results_dir = canonical_results_dir(suite_name)
    results_dir.mkdir(parents=True, exist_ok=True)

    for idx, instance in enumerate(instances, 1):
        iid = instance["instance_id"]
        out_path = results_dir / f"{iid}.json"

        if skip_existing and out_path.exists():
            print(f"  [{idx}/{len(instances)}] {iid}  (skipped)")
            continue

        print(f"  [{idx}/{len(instances)}] {iid}")
        result = run_instance(instance, lapp_binary)
        result.setdefault("benchmark_version", benchmark_version())
        result.setdefault("suite", suite_name or "ad-hoc")
        result.setdefault("agent", AGENT)
        result.setdefault("model", current_model_name())
        result.setdefault("grep_format", current_grep_variant())
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
