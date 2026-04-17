#!/usr/bin/env python3
"""
Read results/ and print a comparison table.

Metrics reported per instance:
  out-tok   — output tokens (primary lapp claim: this should be lower for B)
  in-tok    — effective input tokens (input + cache_read; lapp adds hashline overhead here)
  turns     — agent turns (proxy for retry count)
  sim       — patch similarity to reference fix (0–100%; higher = more correct)
  cost      — USD cost of the run

Aggregate row shows totals for tokens/cost and mean similarity.

Usage:
  python benchmark/report.py                  # default results dir
  python benchmark/report.py --dir minimax    # results/minimax subdir
  python benchmark/report.py id1 id2          # specific instances
"""

import difflib
import json
import os
import sys
from pathlib import Path

_base = Path(__file__).parent / "results"
RESULTS_DIR = _base / os.environ.get("RESULTS_SUBDIR", "default")
# --dir <name> overrides RESULTS_SUBDIR
if "--dir" in sys.argv:
    idx = sys.argv.index("--dir")
    if idx + 1 >= len(sys.argv):
        print("ERROR: --dir requires an argument", file=sys.stderr)
        sys.exit(1)
    RESULTS_DIR = _base / sys.argv[idx + 1]
    sys.argv = sys.argv[:idx] + sys.argv[idx + 2:]


# ---------------------------------------------------------------------------
# Scoring
# ---------------------------------------------------------------------------

def patch_similarity(applied: str, reference: str) -> float:
    """
    Similarity between the agent's diff and the reference patch, based on
    the set of added/removed lines (ignores line numbers and context lines).

    Returns 0.0-1.0. A score of 1.0 means the agent produced exactly the
    same changes as the reference fix. Low scores don't mean the fix is
    wrong — the agent may have used a different but equivalent approach.
    """
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
        # Neither patch has changes — both are no-ops, counts as a match.
        return 1.0
    if not a or not b:
        return 0.0

    return difflib.SequenceMatcher(None, a, b).ratio()


def made_any_change(diff: str) -> bool:
    return bool(diff.strip())


# ---------------------------------------------------------------------------
# Formatting helpers
# ---------------------------------------------------------------------------

def _tok(n: int | None) -> str:
    if n is None or n == 0:
        return "—"
    return f"{n:,}"


def _pct(f: float) -> str:
    return f"{f * 100:.0f}%"


def _delta(a: int, b: int) -> str:
    if a == 0:
        return "  —  "
    d = b - a
    sign = "+" if d >= 0 else ""
    pct = d / a * 100
    return f"{sign}{pct:.1f}%"


def _cost(usd: float) -> str:
    if usd == 0:
        return "—"
    return f"${usd:.3f}"


def _wall(ms: int) -> str:
    if ms == 0:
        return "—"
    return f"{ms/1000:.0f}s"


def _turn_avg(turns_ms: list) -> str:
    if not turns_ms:
        return "—"
    return f"{sum(turns_ms)/len(turns_ms)/1000:.1f}s"


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def load_results(ids: list[str]) -> list[dict]:
    if ids:
        paths = [RESULTS_DIR / f"{i}.json" for i in ids]
    else:
        paths = sorted(RESULTS_DIR.glob("*.json"))

    results = []
    for p in paths:
        if not p.exists():
            print(f"WARNING: {p} not found — skipping", file=sys.stderr)
            continue
        try:
            results.append(json.loads(p.read_text()))
        except json.JSONDecodeError as e:
            print(f"WARNING: {p} is not valid JSON — skipping: {e}", file=sys.stderr)
            continue
    return results


def main() -> None:
    ids = sys.argv[1:]
    results = load_results(ids)

    if not results:
        print(f"No results found in {RESULTS_DIR}/")
        print("Run 'python benchmark/run.py' first.")
        return

    # Column widths
    ID_W = max(len(r.get("instance_id", "")) for r in results)
    ID_W = max(ID_W, 12)

    header = (
        f"{'Instance':<{ID_W}}  "
        f"{'A out':>7}  {'B out':>7}  {'Δ out':>7}  "
        f"{'A in':>7}  {'B in':>7}  "
        f"{'A turns':>7}  {'B turns':>7}  "
        f"{'A wall':>7}  {'B wall':>7}  "
        f"{'A t/turn':>8}  {'B t/turn':>8}  "
        f"{'A sim':>6}  {'B sim':>6}  "
        f"{'A cost':>7}  {'B cost':>7}"
    )
    sep = "-" * len(header)

    print()
    print(sep)
    print(header)
    print(sep)

    # Accumulators for totals
    sum_a_out = sum_b_out = 0
    sum_a_in = sum_b_in = 0
    sum_a_cost = sum_b_cost = 0.0
    sim_a_vals: list[float] = []
    sim_b_vals: list[float] = []
    error_count = 0

    for r in results:
        iid = r.get("instance_id", "?")

        if "error" in r and "a" not in r:
            print(f"{'  ERROR':<{ID_W}}  {r['error']}")
            error_count += 1
            continue

        ref = r.get("reference_patch", "")
        a = r.get("a", {})
        b = r.get("b", {})

        err_a = a.get("error", "")
        err_b = b.get("error", "")

        a_out  = a.get("output_tokens", 0)
        b_out  = b.get("output_tokens", 0)
        a_in   = a.get("input_tokens", 0) + a.get("cache_read_tokens", 0)
        b_in   = b.get("input_tokens", 0) + b.get("cache_read_tokens", 0)
        a_cost = a.get("cost_usd", 0.0)
        b_cost = b.get("cost_usd", 0.0)
        a_wall = a.get("wall_ms", 0)
        b_wall = b.get("wall_ms", 0)

        sim_a = patch_similarity(a.get("diff", ""), ref)
        sim_b = patch_similarity(b.get("diff", ""), ref)

        flag = ""
        if err_a:
            flag += f"  [A-ERR: {err_a[:30]}]"
            error_count += 1
        if err_b:
            flag += f"  [B-ERR: {err_b[:30]}]"
            error_count += 1

        print(
            f"{iid:<{ID_W}}  "
            f"{_tok(a_out):>7}  {_tok(b_out):>7}  {_delta(a_out, b_out):>7}  "
            f"{_tok(a_in):>7}  {_tok(b_in):>7}  "
            f"{a.get('num_turns', 0):>7}  {b.get('num_turns', 0):>7}  "
            f"{_wall(a_wall):>7}  {_wall(b_wall):>7}  "
            f"{_turn_avg(a.get('turns_ms',[])):>8}  {_turn_avg(b.get('turns_ms',[])):>8}  "
            f"{_pct(sim_a):>6}  {_pct(sim_b):>6}  "
            f"{_cost(a_cost):>7}  {_cost(b_cost):>7}"
            f"{flag}"
        )

        if not err_a and not err_b:
            sum_a_out += a_out
            sum_b_out += b_out
            sum_a_in += a_in
            sum_b_in += b_in
            sum_a_cost += a_cost
            sum_b_cost += b_cost
            sim_a_vals.append(sim_a)
            sim_b_vals.append(sim_b)

    # Totals / averages row
    print(sep)
    n = len(sim_a_vals)
    if n:
        avg_sim_a = sum(sim_a_vals) / n
        avg_sim_b = sum(sim_b_vals) / n
        print(
            f"{'TOTAL / AVG':<{ID_W}}  "
            f"{_tok(sum_a_out):>7}  {_tok(sum_b_out):>7}  {_delta(sum_a_out, sum_b_out):>7}  "
            f"{_tok(sum_a_in):>7}  {_tok(sum_b_in):>7}  "
            f"{'':>7}  {'':>7}  "
            f"{'':>7}  {'':>7}  "
            f"{'':>8}  {'':>8}  "
            f"{_pct(avg_sim_a):>6}  {_pct(avg_sim_b):>6}  "
            f"{_cost(sum_a_cost):>7}  {_cost(sum_b_cost):>7}"
        )
    print(sep)
    print()
    print("Columns:")
    print("  A = standard tools (Read / Edit / Write)")
    print("  B = lapp tools     (lapp_read / lapp_edit / lapp_write / lapp_grep)")
    print("  out-tok  = output tokens  (lapp's primary claim: B < A)")
    print("  in-tok   = input tokens (input + cache-read; lapp adds hashline overhead)")
    print("  turns    = agent turns (proxy for retries)")
    print("  sim      = patch similarity to reference fix (higher = closer to correct)")
    print("  Δ out    = (B - A) / A  (negative = lapp used fewer output tokens)")
    print()
    print(f"  {n} complete pair(s), {error_count} error(s)")
    print()
    print("Notes:")
    print("  sim measures surface similarity to the reference patch, not correctness.")
    print("  A low sim score may still be a valid fix via a different approach.")
    print("  Bash-based edits (sed, echo >) bypass tool-call token accounting.")
    print("  Re-run with SKIP_EXISTING=0 to rerun individual instances.")


if __name__ == "__main__":
    main()
