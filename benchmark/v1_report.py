#!/usr/bin/env python3
import difflib
import json
import sys
from pathlib import Path

BASE = Path(__file__).parent
RESULTS = BASE / "results"
SUITES = json.loads((BASE / "v1_suites.json").read_text())


def load_dir(dir_name: str):
    p = RESULTS / dir_name
    rows = []
    for f in sorted(p.glob("*.json")):
        rows.append(json.loads(f.read_text()))
    return rows



def patch_similarity(applied: str, reference: str) -> float:
    def changed_lines(patch: str):
        return [
            line for line in patch.splitlines()
            if (line.startswith('+') or line.startswith('-'))
            and not line.startswith('+++') and not line.startswith('---')
        ]
    a = changed_lines(applied)
    b = changed_lines(reference)
    if not a and not b:
        return 1.0
    if not a or not b:
        return 0.0
    return difflib.SequenceMatcher(None, a, b).ratio()

def summarize(rows):
    complete = []
    for r in rows:
        a = r.get("a", {})
        b = r.get("b", {})
        if not a.get("error") and not b.get("error"):
            complete.append(r)

    if not complete:
        return None

    n = len(complete)

    def corr(side, row):
        if "correctness_similarity" in row.get(side, {}):
            return row[side]["correctness_similarity"]
        return patch_similarity(row[side].get("diff", ""), row.get("reference_patch", ""))

    a_out = sum(r["a"].get("output_tokens", 0) for r in complete)
    b_out = sum(r["b"].get("output_tokens", 0) for r in complete)
    a_in = sum(r["a"].get("input_tokens", 0) + r["a"].get("cache_read_tokens", 0) for r in complete)
    b_in = sum(r["b"].get("input_tokens", 0) + r["b"].get("cache_read_tokens", 0) for r in complete)
    a_wall = sum(r["a"].get("wall_ms", 0) for r in complete)
    b_wall = sum(r["b"].get("wall_ms", 0) for r in complete)
    a_turns = sum(r["a"].get("num_turns", 0) for r in complete)
    b_turns = sum(r["b"].get("num_turns", 0) for r in complete)
    a_corr = sum(corr("a", r) for r in complete) / n
    b_corr = sum(corr("b", r) for r in complete) / n
    def delta(a, b):
        if a == 0:
            return None
        return (b - a) / a * 100

    return {
        "pairs": n,
        "a_out": a_out,
        "b_out": b_out,
        "d_out": delta(a_out, b_out),
        "a_in": a_in,
        "b_in": b_in,
        "d_in": delta(a_in, b_in),
        "a_wall": a_wall,
        "b_wall": b_wall,
        "d_wall": delta(a_wall, b_wall),
        "a_turns": a_turns,
        "b_turns": b_turns,
        "d_turns": delta(a_turns, b_turns),
        "a_corr": a_corr,
        "b_corr": b_corr,
        "d_corr": delta(a_corr, b_corr),
    }


def fmt_pct(v):
    if v is None:
        return "—"
    return f"{v:+.1f}%"


def fmt_ms(ms):
    return f"{ms/1000:.0f}s"


def fmt_corr(v):
    return f"{v*100:.0f}%"


def main():
    if len(sys.argv) > 1:
        dirs = sys.argv[1:]
    else:
        dirs = [
            str(p.relative_to(RESULTS))
            for p in RESULTS.rglob('*')
            if p.is_dir() and any(p.glob('*.json')) and '_legacy' not in p.parts
        ]
    print("V1 benchmark summary")
    print("suite, model_dir, pairs, corr(A→B), wall(A→B), turns(A→B), out(A→B), in(A→B)")
    for suite_name in SUITES["suites"]:
        for d in sorted(dirs):
            rows = load_dir(d)
            if not rows:
                continue
            row_suite = rows[0].get("suite")
            if row_suite and row_suite != suite_name:
                continue
            expected = set(SUITES["suites"][suite_name]["instances"])
            actual = {r["instance_id"] for r in rows}
            if not actual.intersection(expected):
                continue
            rows = [r for r in rows if r["instance_id"] in expected]
            s = summarize(rows)
            model_label = rows[0].get("model", d)
            if not s:
                print(f"{suite_name}, {model_label}, 0/{len(expected)}, no complete pairs")
                continue
            print(
                f"{suite_name}, {model_label}, {s['pairs']}/{len(expected)}, "
                f"{fmt_corr(s['a_corr'])}->{fmt_corr(s['b_corr'])} ({fmt_pct(s['d_corr'])}), "
                f"{fmt_ms(s['a_wall'])}->{fmt_ms(s['b_wall'])} ({fmt_pct(s['d_wall'])}), "
                f"{s['a_turns']}->{s['b_turns']} ({fmt_pct(s['d_turns'])}), "
                f"{s['a_out']}->{s['b_out']} ({fmt_pct(s['d_out'])}), "
                f"{s['a_in']}->{s['b_in']} ({fmt_pct(s['d_in'])})"
            )


if __name__ == "__main__":
    main()
