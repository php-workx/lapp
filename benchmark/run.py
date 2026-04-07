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

import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from textwrap import dedent

BENCHMARK_DIR = Path(__file__).parent
FILES_DIR     = BENCHMARK_DIR / "files"
RESULTS_DIR   = BENCHMARK_DIR / "results"
INSTANCES_FILE = BENCHMARK_DIR / "instances.json"

TIMEOUT   = 300   # seconds per agent run; read+edit of a small file is fast
MAX_TURNS = 20    # upper bound; lapp may need extra turns on first read+edit attempts

# Tools available in each config. Bash excluded intentionally — we want the
# agent to go through the tool interface, not shell out to sed/awk.
TOOLS_A = "Read,Edit,Write"
TOOLS_B = "lapp_read,lapp_edit,lapp_write,lapp_grep"

PROMPT_A = dedent("""\
    Read the file shown below, apply the change, and save the result.

    Use ONLY the Read and Edit tools. Do not use any shell commands.

    File: {filepath}

    Change to apply (unified diff — lines starting with - are removed, + are added):
    {diff}
""")

PROMPT_B = dedent("""\
    Apply the change shown below to the file at the given path.

    Preferred workflow for large files: use lapp_grep to locate the exact
    LINE#HASH reference for the line(s) being changed, then lapp_edit to apply
    the change. Only use lapp_read if you need broader context.
    Do not use any shell commands.

    File: {filepath}

    Change to apply (unified diff — lines starting with - are removed, + are added):
    {diff}
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

def build_prompt(template: str, work_dir: Path, instance_id: str) -> str:
    diff_path = FILES_DIR / instance_id / "_patch.diff"
    diff = diff_path.read_text() if diff_path.exists() else "(diff unavailable)"

    # Find the primary changed file in the work dir. For multi-file patches
    # both files are present; the diff already names them.
    # We give the agent the work_dir root so it can resolve any path in the diff.
    return template.format(filepath=str(work_dir), diff=diff)


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
        "stop_reason":         raw.get("stop_reason", ""),
        "error":               "",
    }


def _err(msg: str) -> dict:
    return {
        "output_tokens": 0, "input_tokens": 0,
        "cache_read_tokens": 0, "cache_create_tokens": 0,
        "num_turns": 0, "cost_usd": 0.0,
        "stop_reason": "", "error": msg,
    }


# ---------------------------------------------------------------------------
# Per-instance orchestration
# ---------------------------------------------------------------------------

def run_instance(instance: dict, lapp_binary: str) -> dict:
    iid = instance["instance_id"]

    with tempfile.TemporaryDirectory(prefix=f"lapp-bench-") as tmp:
        tmp_path = Path(tmp)
        work_dir = (tmp_path / "work").resolve()
        work_dir.mkdir(parents=True, exist_ok=True)

        try:
            setup_work_dir(iid, work_dir)
        except FileNotFoundError as exc:
            return {"instance_id": iid, "error": str(exc)}

        empty_mcp = tmp_path / "empty.json"
        write_empty_mcp(empty_mcp)

        lapp_mcp = tmp_path / "lapp.json"
        write_lapp_mcp(lapp_binary, work_dir, lapp_mcp)

        # Config A — standard tools
        print(f"    [A] Read → Edit", flush=True)
        prompt_a = build_prompt(PROMPT_A, work_dir, iid)
        result_a = run_claude(prompt_a, TOOLS_A, empty_mcp, work_dir)
        diff_a = capture_diff(work_dir, iid)

        reset_work_dir(iid, work_dir)
        # lapp MCP config points at work_dir; re-write after reset so the path
        # is still valid (same path, same dir, so this is a no-op in practice).
        write_lapp_mcp(lapp_binary, work_dir, lapp_mcp)

        # Config B — lapp tools
        print(f"    [B] lapp_read → lapp_edit", flush=True)
        prompt_b = build_prompt(PROMPT_B, work_dir, iid)
        result_b = run_claude(prompt_b, TOOLS_B, lapp_mcp, work_dir)
        diff_b = capture_diff(work_dir, iid)

    return {
        "instance_id":     iid,
        "repo":            instance["repo"],
        "reference_patch": instance["patch"],
        "a": {**result_a, "diff": diff_a},
        "b": {**result_b, "diff": diff_b},
    }


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

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

    lapp_binary = shutil.which("lapp")
    if not lapp_binary:
        print("ERROR: lapp not in PATH — run: go install ./cmd/lapp",
              file=sys.stderr)
        sys.exit(1)

    filter_ids = {s for s in os.environ.get("BENCHMARK_IDS", "").split(",") if s}
    if filter_ids:
        instances = [i for i in instances if i["instance_id"] in filter_ids]
        print(f"Subset: {len(instances)} instance(s)")
    else:
        print(f"Running {len(instances)} instance(s)")

    skip_existing = os.environ.get("SKIP_EXISTING", "1") != "0"
    RESULTS_DIR.mkdir(exist_ok=True)

    for idx, instance in enumerate(instances, 1):
        iid = instance["instance_id"]
        out_path = RESULTS_DIR / f"{iid}.json"

        if skip_existing and out_path.exists():
            print(f"  [{idx}/{len(instances)}] {iid}  (skipped)")
            continue

        print(f"  [{idx}/{len(instances)}] {iid}")
        result = run_instance(instance, lapp_binary)
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

    print(f"\nResults in {RESULTS_DIR}/")
    print("Run 'python benchmark/report.py' to see the table.")


if __name__ == "__main__":
    main()
