#!/usr/bin/env python3
"""
Filter SWE-bench Verified to a small, representative set of benchmark instances.

Selection criteria:
  - Patch touches at most MAX_FILES files (1-file changes are highest signal)
  - Patch changes at most MAX_LINES lines (additions + deletions, header lines excluded)
  - At most INSTANCES_PER_REPO instances from any single repository (diversity)
  - Total TARGET_INSTANCES instances

Requires: pip install datasets

Output: instances.json in this directory (committed so benchmark/run.py works
without re-fetching from HuggingFace).
"""

import json
import re
import sys
from collections import defaultdict
from pathlib import Path

# Tuning knobs — adjust before re-running to change the instance set.
MAX_FILES = 2
MAX_LINES = 80
INSTANCES_PER_REPO = 3
TARGET_INSTANCES = 20

OUT_FILE = Path(__file__).parent / "instances.json"


def count_patch_files(patch: str) -> int:
    return len(re.findall(r"^diff --git ", patch, re.MULTILINE))


def count_patch_lines(patch: str) -> int:
    """Count added + deleted lines, excluding diff headers (+++ / ---)."""
    return sum(
        1 for line in patch.splitlines()
        if (line.startswith("+") or line.startswith("-"))
        and not line.startswith("+++")
        and not line.startswith("---")
    )


def main() -> None:
    try:
        from datasets import load_dataset
    except ImportError:
        print("ERROR: run 'pip install datasets' first", file=sys.stderr)
        sys.exit(1)

    print("Loading SWE-bench Verified from HuggingFace (first run may download ~200MB)...")
    ds = load_dataset("princeton-nlp/SWE-bench_Verified", split="test")
    print(f"Loaded {len(ds)} instances. Filtering...")

    selected: list[dict] = []
    per_repo: dict[str, int] = defaultdict(int)
    skipped_files = skipped_lines = skipped_repo = skipped_empty = 0

    for item in ds:
        if len(selected) >= TARGET_INSTANCES:
            break

        patch: str = item.get("patch", "")
        if not patch.strip():
            skipped_empty += 1
            continue

        nfiles = count_patch_files(patch)
        if nfiles > MAX_FILES:
            skipped_files += 1
            continue

        nlines = count_patch_lines(patch)
        if nlines > MAX_LINES:
            skipped_lines += 1
            continue

        repo: str = item["repo"]
        if per_repo[repo] >= INSTANCES_PER_REPO:
            skipped_repo += 1
            continue

        per_repo[repo] += 1
        selected.append({
            "instance_id": item["instance_id"],
            "repo": repo,
            "base_commit": item["base_commit"],
            "problem_statement": item["problem_statement"],
            # Reference patch — the correct fix. Used for scoring only, never shown to the agent.
            "patch": patch,
        })

    print(
        f"Selected {len(selected)}/{len(ds)} instances  "
        f"(skipped: {skipped_files} too-many-files, {skipped_lines} too-many-lines, "
        f"{skipped_repo} repo-cap, {skipped_empty} empty-patch)"
    )

    if len(selected) < TARGET_INSTANCES:
        print(
            f"WARNING: only {len(selected)} instances found (target {TARGET_INSTANCES}). "
            "Consider relaxing MAX_FILES or MAX_LINES.",
            file=sys.stderr,
        )

    OUT_FILE.write_text(json.dumps(selected, indent=2))
    print(f"Written to {OUT_FILE}")
    for repo, count in sorted(per_repo.items()):
        print(f"  {count}x  {repo}")


if __name__ == "__main__":
    main()
