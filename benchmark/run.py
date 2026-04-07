#!/usr/bin/env python3
"""
A/B benchmark runner: standard Claude Code tools vs. lapp MCP tools.

For each instance in instances.json:
  1. Clone the repo at base_commit into a temp directory.
  2. Run Config A (standard Read/Edit/Write tools) — capture output + diff.
  3. Reset the repo to base_commit.
  4. Run Config B (lapp_read/lapp_edit/lapp_write/lapp_grep tools) — capture output + diff.
  5. Write results/{instance_id}.json.

Both configs allow Bash so the agent can navigate the repo, but file edits are
forced through the respective tool set via --allowedTools.

Usage:
  python benchmark/run.py                          # run all instances
  BENCHMARK_IDS=id1,id2 python benchmark/run.py   # run a subset
  SKIP_EXISTING=0 python benchmark/run.py         # re-run completed instances
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
RESULTS_DIR = BENCHMARK_DIR / "results"
INSTANCES_FILE = BENCHMARK_DIR / "instances.json"

# Timeout per agent run (seconds). File-editing tasks on small repos should
# complete well within this; increase if you see frequent timeouts.
TIMEOUT = 600

# Maximum agent turns before the run is aborted.
MAX_TURNS = 20

# Built-in tools available in Config A. Bash is included so the agent can
# read directory structure; the edit path goes through Read/Edit/Write.
TOOLS_A = "Read,Edit,Write,Bash"

# MCP tools available in Config B. Bash is included for the same reason.
# NOTE: Claude Code names MCP tools as mcp__<server>__<tool_name>. If your
# version uses a different convention, update these names to match what
# `claude mcp list` shows when lapp is configured.
TOOLS_B = "mcp__lapp__lapp_read,mcp__lapp__lapp_edit,mcp__lapp__lapp_write,mcp__lapp__lapp_grep,Bash"

# Prompt sent to the agent. Tool instructions are appended per-config so we
# measure the tool interface itself, not tool-adoption behaviour.
PROMPT_BASE = dedent("""\
    You are a software engineer. Fix the GitHub issue described below.

    Repository path: {work_dir}
    Issue:
    {problem_statement}

    Rules:
    - Edit only source files; do not modify test files.
    - Do not add new test files.
    - Stop as soon as the fix is complete. Do not explain or summarise.
""")

PROMPT_SUFFIX_A = "Use the Read, Edit, and Write tools to modify files."
PROMPT_SUFFIX_B = "Use lapp_read, lapp_edit, lapp_write, and lapp_grep to modify files."


# ---------------------------------------------------------------------------
# Git helpers
# ---------------------------------------------------------------------------

def _run(cmd: list[str], cwd: Path | None = None, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, capture_output=True, text=True, cwd=cwd, check=check)


def clone_repo(repo: str, commit: str, work_dir: Path) -> None:
    url = f"https://github.com/{repo}.git"
    _run(["git", "clone", "--quiet", "--depth", "100", url, str(work_dir)])
    _run(["git", "checkout", "--quiet", commit], cwd=work_dir)


def reset_repo(work_dir: Path) -> None:
    """Restore the working tree to the last commit, remove untracked files."""
    _run(["git", "restore", "."], cwd=work_dir)
    _run(["git", "clean", "-fd", "--quiet"], cwd=work_dir)


def capture_diff(work_dir: Path) -> str:
    result = _run(["git", "diff"], cwd=work_dir, check=False)
    return result.stdout


# ---------------------------------------------------------------------------
# MCP config
# ---------------------------------------------------------------------------

def write_empty_mcp_config(path: Path) -> None:
    """Empty MCP config used with --strict-mcp-config to block any globally
    configured MCP servers from leaking into Config A runs."""
    path.write_text('{"mcpServers": {}}')


def write_lapp_mcp_config(lapp_binary: str, work_dir: Path, path: Path) -> None:
    config = {
        "mcpServers": {
            "lapp": {
                "command": lapp_binary,
                "args": ["--root", str(work_dir)],
            }
        }
    }
    path.write_text(json.dumps(config))


# ---------------------------------------------------------------------------
# Agent runner
# ---------------------------------------------------------------------------

def run_claude(
    prompt: str,
    allowed_tools: str,
    mcp_config_path: Path,
    work_dir: Path,
) -> dict:
    """
    Run `claude --print` with the given prompt and tool restrictions.

    Returns a dict with at minimum:
      output_tokens, input_tokens, cache_read_tokens, num_turns,
      cost_usd, error (empty string on success).
    """
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
        prompt,
    ]

    try:
        proc = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=TIMEOUT,
            cwd=work_dir,
        )
    except subprocess.TimeoutExpired:
        return _error_record(f"timeout after {TIMEOUT}s")

    if proc.returncode != 0:
        # Claude exits non-zero on hard errors; stderr has details.
        return _error_record(proc.stderr[:400].strip() or f"exit {proc.returncode}")

    try:
        raw = json.loads(proc.stdout)
    except json.JSONDecodeError:
        return _error_record(f"non-JSON output: {proc.stdout[:200]}")

    if raw.get("is_error") or raw.get("subtype") == "error":
        return _error_record(raw.get("result", "unknown error")[:200])

    usage = raw.get("usage", {})
    return {
        "output_tokens":      usage.get("output_tokens", 0),
        "input_tokens":       usage.get("input_tokens", 0),
        "cache_read_tokens":  usage.get("cache_read_input_tokens", 0),
        "cache_create_tokens": usage.get("cache_creation_input_tokens", 0),
        "num_turns":          raw.get("num_turns", 0),
        "cost_usd":           raw.get("total_cost_usd", 0.0),
        "stop_reason":        raw.get("stop_reason", ""),
        "error":              "",
    }


def _error_record(msg: str) -> dict:
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

    with tempfile.TemporaryDirectory(prefix=f"lapp-bench-{iid}-") as tmp:
        tmp_path = Path(tmp)
        work_dir = tmp_path / "repo"

        print(f"    cloning {instance['repo']} @ {instance['base_commit'][:8]}")
        try:
            clone_repo(instance["repo"], instance["base_commit"], work_dir)
        except subprocess.CalledProcessError as exc:
            return {"instance_id": iid, "error": f"clone failed: {exc.stderr[:200]}"}

        base_prompt = PROMPT_BASE.format(
            work_dir=work_dir,
            problem_statement=instance["problem_statement"],
        )

        # Config A — standard tools
        empty_mcp = tmp_path / "empty_mcp.json"
        write_empty_mcp_config(empty_mcp)

        print(f"    [A] standard tools (Read/Edit/Write)")
        result_a = run_claude(
            prompt=base_prompt + "\n" + PROMPT_SUFFIX_A,
            allowed_tools=TOOLS_A,
            mcp_config_path=empty_mcp,
            work_dir=work_dir,
        )
        diff_a = capture_diff(work_dir)

        reset_repo(work_dir)

        # Config B — lapp tools
        lapp_mcp = tmp_path / "lapp_mcp.json"
        write_lapp_mcp_config(lapp_binary, work_dir, lapp_mcp)

        print(f"    [B] lapp tools (lapp_read/lapp_edit)")
        result_b = run_claude(
            prompt=base_prompt + "\n" + PROMPT_SUFFIX_B,
            allowed_tools=TOOLS_B,
            mcp_config_path=lapp_mcp,
            work_dir=work_dir,
        )
        diff_b = capture_diff(work_dir)

    return {
        "instance_id": iid,
        "repo": instance["repo"],
        "reference_patch": instance["patch"],
        "a": {**result_a, "diff": diff_a},
        "b": {**result_b, "diff": diff_b},
    }


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> None:
    if not INSTANCES_FILE.exists():
        print(
            f"ERROR: {INSTANCES_FILE} not found.\n"
            "Run 'python benchmark/select.py' first to populate it.",
            file=sys.stderr,
        )
        sys.exit(1)

    instances: list[dict] = json.loads(INSTANCES_FILE.read_text())
    if not instances:
        print("ERROR: instances.json is empty — run select.py first.", file=sys.stderr)
        sys.exit(1)

    lapp_binary = shutil.which("lapp")
    if not lapp_binary:
        print(
            "ERROR: lapp binary not found in PATH.\n"
            "Run: go install ./cmd/lapp",
            file=sys.stderr,
        )
        sys.exit(1)

    # Optional: run a subset by setting BENCHMARK_IDS=id1,id2
    filter_ids = {s for s in os.environ.get("BENCHMARK_IDS", "").split(",") if s}
    if filter_ids:
        instances = [i for i in instances if i["instance_id"] in filter_ids]
        print(f"Subset mode: {len(instances)} instance(s)")
    else:
        print(f"Running {len(instances)} instance(s)")

    skip_existing = os.environ.get("SKIP_EXISTING", "1") != "0"

    RESULTS_DIR.mkdir(exist_ok=True)

    for idx, instance in enumerate(instances, 1):
        iid = instance["instance_id"]
        out_path = RESULTS_DIR / f"{iid}.json"

        if skip_existing and out_path.exists():
            print(f"  [{idx}/{len(instances)}] {iid}  (skipped — already done)")
            continue

        print(f"  [{idx}/{len(instances)}] {iid}")
        result = run_instance(instance, lapp_binary)
        out_path.write_text(json.dumps(result, indent=2))

        # Print a one-line summary so the user can follow along.
        a, b = result.get("a", {}), result.get("b", {})
        if a.get("error") or b.get("error"):
            err = a.get("error") or b.get("error")
            print(f"           ERROR: {err}")
        else:
            delta = b["output_tokens"] - a["output_tokens"]
            sign = "+" if delta >= 0 else ""
            print(
                f"           A={a['output_tokens']} out-tok  "
                f"B={b['output_tokens']} out-tok  "
                f"delta={sign}{delta}"
            )

    print(f"\nResults written to {RESULTS_DIR}/")
    print("Run 'python benchmark/report.py' to see the comparison table.")


if __name__ == "__main__":
    main()
