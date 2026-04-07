import os

#!/usr/bin/env python3
"""
Fetch the source files changed by each benchmark instance.

For each instance in instances.json, parses the reference patch to find which
files are modified, then downloads each file at the base_commit from GitHub's
raw content API. Files are saved to benchmark/files/<instance_id>/<filepath>.

No git, no cloning. Each instance typically touches 1-2 Python files.

Usage:
    python benchmark/prepare.py
"""

import json
import re
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

BENCHMARK_DIR = Path(__file__).parent
FILES_DIR = BENCHMARK_DIR / "files"
INSTANCES_FILE = BENCHMARK_DIR / "instances.json"

RAW_URL = "https://raw.githubusercontent.com/{repo}/{commit}/{filepath}"


# ---------------------------------------------------------------------------
# Patch parsing
# ---------------------------------------------------------------------------

def changed_files(patch: str) -> list[tuple[str | None, str | None]]:
    """
    Parse a unified diff and return (src_path, dst_path) pairs.

    src_path is None for newly created files (--- /dev/null).
    dst_path is None for deleted files (+++ /dev/null).
    Paths have the leading a/ or b/ git prefix stripped.
    """
    results = []
    src = dst = None
    for line in patch.splitlines():
        if line.startswith("--- "):
            raw = line[4:]
            src = None if raw == "/dev/null" else re.sub(r"^a/", "", raw)
        elif line.startswith("+++ ") and src is not None or (
            line.startswith("+++ ") and dst is None
        ):
            raw = line[4:]
            dst = None if raw == "/dev/null" else re.sub(r"^b/", "", raw)
            results.append((src, dst))
            src = dst = None
    return results


# ---------------------------------------------------------------------------
# File fetching
# ---------------------------------------------------------------------------

def fetch_file(repo: str, commit: str, filepath: str) -> bytes:
    url = RAW_URL.format(repo=repo, commit=commit, filepath=filepath)
    try:
        with urllib.request.urlopen(url, timeout=30) as resp:
            return resp.read()
    except urllib.error.HTTPError as exc:
        raise RuntimeError(f"HTTP {exc.code} fetching {url}") from exc


def fetch_instance_files(instance: dict) -> None:
    iid = instance["instance_id"]
    repo = instance["repo"]
    commit = instance["base_commit"]
    patch = instance["patch"]

    dest_dir = FILES_DIR / iid
    if dest_dir.exists() and any(dest_dir.iterdir()):
        print(f"  {iid}  (already fetched)")
        return

    dest_dir.mkdir(parents=True, exist_ok=True)

    files = changed_files(patch)
    for src_path, dst_path in files:
        if src_path is None:
            # New file — will be created from scratch by the agent. Write an
            # empty placeholder so run.py knows to include it in the work dir.
            target = dest_dir / dst_path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(b"")
            print(f"    (new file) {dst_path}")
            continue

        print(f"    fetching {src_path} @ {commit[:8]}…", flush=True)
        try:
            content = fetch_file(repo, commit, src_path)
        except RuntimeError as exc:
            print(f"    WARNING: {exc}", file=sys.stderr)
            continue

        target = dest_dir / src_path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)
        time.sleep(0.1)   # be a polite guest to GitHub's CDN


# ---------------------------------------------------------------------------
# Prompt fragment stored alongside files
# ---------------------------------------------------------------------------

def hunk_summary(patch: str) -> str:
    """
    Return the patch as a clean unified diff, stripping git metadata lines.
    Used verbatim in the run-time prompt so the agent knows what to change.
    """
    skip_prefixes = ("diff --git", "index ", "old mode", "new mode",
                     "Binary files", "similarity index", "rename from",
                     "rename to")
    lines = [
        line for line in patch.splitlines()
        if not any(line.startswith(p) for p in skip_prefixes)
    ]
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> None:
    if not INSTANCES_FILE.exists():
        print(f"ERROR: {INSTANCES_FILE} not found — run fetch_instances.py first.",
              file=sys.stderr)
        sys.exit(1)

    instances: list[dict] = json.loads(INSTANCES_FILE.read_text())
    if not instances:
        print("ERROR: instances.json is empty — run fetch_instances.py first.",
              file=sys.stderr)
        sys.exit(1)


    filter_ids = {s for s in os.environ.get("BENCHMARK_IDS", "").split(",") if s}
    if filter_ids:
        instances = [i for i in instances if i["instance_id"] in filter_ids]
    FILES_DIR.mkdir(exist_ok=True)

    print(f"Fetching source files for {len(instances)} instance(s)…\n")

    failed = []
    for inst in instances:
        iid = inst["instance_id"]
        print(f"  {iid}")
        try:
            fetch_instance_files(inst)

            # Write the clean diff alongside the files so run.py can embed it
            # in the prompt without re-parsing instances.json at runtime.
            diff_path = FILES_DIR / iid / "_patch.diff"
            diff_path.write_text(hunk_summary(inst["patch"]))
        except Exception as exc:
            print(f"    ERROR: {exc}", file=sys.stderr)
            failed.append(iid)

    print(f"\nDone. Files saved to {FILES_DIR}/")
    if failed:
        print(f"Failed: {', '.join(failed)}", file=sys.stderr)
    else:
        print("Run 'python benchmark/run.py' to start benchmarking.")


if __name__ == "__main__":
    main()
