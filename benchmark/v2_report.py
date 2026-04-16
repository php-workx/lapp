#!/usr/bin/env python3
"""Render Benchmark V2 milestone reports.

Current milestone scope:
- Suite: v2-bulk-edits-same-file
- Case: mwaskom__seaborn-3069
- Strategies: native-edit, lapp-text-grep, lapp-structured-grep, lapp-replace-block

The report is intentionally narrow: it compares V2 runs for the same suite/case,
keeps baseline A-side metrics as the native reference, and keeps V1 reporting
untouched.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

BASE = Path(__file__).parent
RESULTS = BASE / "results"
SUITES = json.loads((BASE / "v2_suites.json").read_text())

TARGET_SUITE = "v2-bulk-edits-same-file"
FALLBACK_STRATEGIES = [
    "native-edit",
    "lapp-text-grep",
    "lapp-structured-grep",
    "lapp-replace-block",
]
METRICS = [
    "correctness_similarity",
    "wall_ms",
    "num_turns",
    "output_tokens",
    "input_tokens",
]


def dedupe(values: list[str]) -> list[str]:
    out: list[str] = []
    for value in values:
        if value not in out:
            out.append(value)
    return out


def load_dirs(path_args: list[str]) -> list[str]:
    if path_args:
        return [p for p in path_args]

    suite_dir = RESULTS / TARGET_SUITE
    if not suite_dir.exists():
        return []

    return [
        str(p.relative_to(RESULTS))
        for p in sorted(suite_dir.iterdir())
        if p.is_dir() and any(q.suffix == ".json" for q in p.iterdir()) and "_legacy" not in p.parts
    ]


def load_rows(dir_name: str) -> list[dict]:
    rows: list[dict] = []
    p = Path(dir_name)
    if not p.is_absolute():
        p = RESULTS / p
    if not p.exists() or not p.is_dir():
        return rows

    for path in sorted(p.glob("*.json")):
        try:
            rows.append(json.loads(path.read_text()))
        except json.JSONDecodeError:
            print(f"WARNING: malformed JSON in {path}", file=sys.stderr)
    return rows


def parse_suite() -> dict[str, object]:
    suite = SUITES.get("suites", {}).get(TARGET_SUITE)
    if not isinstance(suite, dict):
        print(
            f"ERROR: suite {TARGET_SUITE!r} not found in v2_suites.json",
            file=sys.stderr,
        )
        sys.exit(1)
    return suite


def supported_strategies(suite: dict[str, object]) -> list[str]:
    """Resolve supported strategies from the manifest when present."""

    raw = suite.get("strategies")
    if isinstance(raw, list):
        clean = [
            s
            for s in raw
            if isinstance(s, str)
            and s.strip()
            and s.strip() in FALLBACK_STRATEGIES
        ]
        if clean:
            return dedupe(clean)

    return FALLBACK_STRATEGIES


def model_bucket(strategy_list: list[str]) -> dict[str, list[dict[str, float | int | None]]]:
    return {s: [] for s in strategy_list}


def parse_strategy(
    value: object,
    grep_format: object,
    strategy_list: list[str],
    model_dir: str,
 ) -> str | None:
    if isinstance(value, str):
        candidate = value.strip()
        if candidate in strategy_list:
            return candidate

    if isinstance(value, (list, tuple)) and len(value) == 1 and isinstance(value[0], str):
        candidate = value[0].strip()
        if candidate in strategy_list:
            return candidate

    if "__grep-" in model_dir:
        suffix = model_dir.split("__grep-", 1)[1]
        if suffix == "structured" and "lapp-structured-grep" in strategy_list:
            return "lapp-structured-grep"
        if suffix == "text" and "lapp-text-grep" in strategy_list:
            return "lapp-text-grep"

    if isinstance(grep_format, str) and model_dir.endswith("__lapp-structured-grep") and "lapp-structured-grep" in strategy_list:
        return "lapp-structured-grep"
    if isinstance(grep_format, str) and model_dir.endswith("__lapp-text-grep") and "lapp-text-grep" in strategy_list:
        return "lapp-text-grep"
    if isinstance(grep_format, str) and model_dir.endswith("__lapp-replace-block") and "lapp-replace-block" in strategy_list:
        return "lapp-replace-block"

    return None


def from_result(block: dict) -> tuple[dict[str, float | int | None], str | None]:
    corr = block.get("correctness_similarity")
    wall = block.get("wall_ms")
    turns = block.get("num_turns")
    out = block.get("output_tokens")
    inp = block.get("input_tokens", 0)
    cache_read = block.get("cache_read_tokens", 0)
    err = block.get("error")

    for value in (corr, wall, turns, out):
        if value is None:
            return {
                "correctness_similarity": None,
                "wall_ms": None,
                "num_turns": None,
                "output_tokens": None,
                "input_tokens": None,
            }, err or "missing-metrics"

    try:
        return {
            "correctness_similarity": float(corr),
            "wall_ms": int(wall),
            "num_turns": int(turns),
            "output_tokens": int(out),
            "input_tokens": int(inp) + int(cache_read),
        }, err
    except (TypeError, ValueError):
        return {
            "correctness_similarity": None,
            "wall_ms": None,
            "num_turns": None,
            "output_tokens": None,
            "input_tokens": None,
        }, err or "invalid-metrics"


def summarize_strategy(entries: list[dict[str, float | int | None]]) -> dict[str, float | int | None]:
    result: dict[str, float | int | None] = {
        "correctness_similarity": None,
        "wall_ms": None,
        "num_turns": None,
        "output_tokens": None,
        "input_tokens": None,
    }
    if not entries:
        return result

    for metric in METRICS:
        vals = [e[metric] for e in entries if isinstance(e.get(metric), (int, float))]
        if not vals:
            continue
        if metric == "correctness_similarity":
            result[metric] = sum(vals) / len(vals)
        else:
            result[metric] = int(sum(vals) / len(vals))
    return result


def fmt_pct(v: float | None) -> str:
    if v is None:
        return "—"
    return f"{v * 100:.0f}%"


def fmt_ms(ms: int | float | None) -> str:
    if ms is None:
        return "—"
    return f"{ms / 1000:.1f}s"


def fmt_num(n: int | float | None) -> str:
    if n is None:
        return "—"
    return f"{n:,}"


def fmt_delta(base: float | int | None, value: float | int | None) -> str:
    if base is None or value is None or base == 0:
        return "—"
    delta = (value - base) / base
    sign = "+" if delta >= 0 else ""
    return f"{sign}{delta * 100:.1f}%"


def truncate(text: str | None, n: int = 80) -> str:
    if not text:
        return ""
    if len(text) <= n:
        return text
    return text[: n - 1] + "…"


def main() -> None:
    suite = parse_suite()
    expected_instances = set(suite.get("instances", []))
    if not expected_instances:
        print(f"ERROR: suite {TARGET_SUITE!r} has no instances in v2_suites.json", file=sys.stderr)
        sys.exit(1)

    strategies = supported_strategies(suite)
    if not strategies:
        strategies = FALLBACK_STRATEGIES

    dirs = load_dirs(sys.argv[1:])
    results_by_model: dict[str, dict[str, list[dict[str, float | int | None]]]] = {}
    status_by_model: dict[str, dict[str, list[str]]] = {}
    unsupported_variants: dict[str, int] = {}

    for d in sorted(dirs):
        for row in load_rows(d):
            if (row.get("benchmark_version") not in {"v2", None}):
                continue
            if row.get("suite") != TARGET_SUITE:
                continue
            if row.get("instance_id") not in expected_instances:
                continue

            model = row.get("model") or Path(d).name
            model_results = results_by_model.setdefault(model, model_bucket(strategies))
            model_status = status_by_model.setdefault(model, model_bucket(strategies))

            a_metrics = None
            a_err = None
            b_metrics = None
            b_err = None

            if isinstance(row.get("a"), dict):
                a_metrics, a_err = from_result(row["a"])
            if isinstance(row.get("b"), dict):
                b_metrics, b_err = from_result(row["b"])

            # Use the native A-side once per case/model. Variant directories contain the same A-side baseline,
            # so guard by instance_id to avoid duplicating the baseline when multiple strategy dirs exist.
            if a_metrics is not None:
                seen_native = model_status.setdefault("__seen_native__", [])
                native_key = row.get("instance_id")
                if native_key not in seen_native:
                    model_results["native-edit"].append(a_metrics)
                    seen_native.append(native_key)
                    if a_err:
                        model_status["native-edit"].append(f"A-ERR: {truncate(a_err)}")

            # Legacy/partial compatibility: if row is side-less but still contains metrics,
            # treat it as native (best-effort; this should not happen for milestone data).
            if a_metrics is None and b_metrics is None and any(k in row for k in METRICS):
                fallback_metrics, fallback_err = from_result(row)
                model_results["native-edit"].append(fallback_metrics)
                if fallback_err:
                    model_status["native-edit"].append(f"ERR: {truncate(fallback_err)}")

            strategy = parse_strategy(
                row.get("strategy"),
                row.get("grep_format"),
                strategies,
                Path(d).name,
            )

            if b_metrics is not None:
                if strategy in strategies and strategy != "native-edit":
                    model_results[strategy].append(b_metrics)
                    if b_err:
                        model_status[strategy].append(f"B-ERR: {truncate(b_err)}")
                elif strategy is None:
                    unsupported_variants[model] = unsupported_variants.get(model, 0) + 1
                elif strategy == "native-edit":
                    if b_metrics is not None:
                        # Keep explicit: native strategy rows use A-side as baseline.
                        if b_err:
                            model_status[strategy].append(f"B-ERR: {truncate(b_err)}")

    if not results_by_model:
        print("No V2 milestone 1 results found.")
        print("Expected suite:", TARGET_SUITE)
        print("Expected case:", ", ".join(sorted(expected_instances)))
        print("Expected strategy set:", ", ".join(strategies))
        return

    print("V2 benchmark summary")
    print("Suite:", TARGET_SUITE)
    print("Case:", ", ".join(sorted(expected_instances)))
    print("Target metrics:", ", ".join(METRICS))
    print()

    for model in sorted(results_by_model):
        entries = results_by_model[model]
        status = status_by_model[model]
        baseline = summarize_strategy(entries["native-edit"])

        print(f"Model: {model}")
        header = [
            "Strategy",
            "Pairs",
            "Corr",
            "Wall",
            "Turns",
            "Out",
            "In",
            "ΔCorr",
            "ΔWall",
            "ΔTurns",
            "ΔOut",
            "ΔIn",
            "Status",
        ]

        rows = []
        for strategy in strategies:
            metrics = summarize_strategy(entries[strategy])
            samples = len(entries[strategy])
            pair_text = f"{samples}/{len(expected_instances)}"
            status_text = ""
            if status[strategy]:
                status_text = status[strategy][0]

            if samples == 0 and strategy != "native-edit":
                row = [
                    strategy,
                    pair_text,
                    "—", "—", "—", "—", "—",
                    "—", "—", "—", "—", "—",
                    "missing",
                ]
            else:
                row = [
                    strategy,
                    pair_text,
                    fmt_pct(metrics["correctness_similarity"]),
                    fmt_ms(metrics["wall_ms"]),
                    fmt_num(metrics["num_turns"]),
                    fmt_num(metrics["output_tokens"]),
                    fmt_num(metrics["input_tokens"]),
                    fmt_delta(baseline["correctness_similarity"], metrics["correctness_similarity"]),
                    fmt_delta(baseline["wall_ms"], metrics["wall_ms"]),
                    fmt_delta(baseline["num_turns"], metrics["num_turns"]),
                    fmt_delta(baseline["output_tokens"], metrics["output_tokens"]),
                    fmt_delta(baseline["input_tokens"], metrics["input_tokens"]),
                    status_text or "ok",
                ]
            rows.append(row)

        widths = [len(h) for h in header]
        for row in rows:
            for i, cell in enumerate(row):
                widths[i] = max(widths[i], len(str(cell)))

        def fmt(row: list[str]) -> str:
            return "  ".join(str(cell).ljust(widths[i]) for i, cell in enumerate(row))

        print(fmt(header))
        print("  ".join("-" * w for w in widths))
        for row in rows:
            print(fmt(row))

        missing = [
            s
            for s in strategies
            if s != "native-edit" and len(entries[s]) == 0
        ]
        if missing:
            print("Missing strategy variants:", ", ".join(missing))

        if unsupported_variants.get(model):
            print(f"Unclassified variant runs: {unsupported_variants[model]}")

        if any(status[s] for s in strategies):
            print("Warnings:")
            for s in strategies:
                for note in status[s]:
                    print(f"  [{s}] {note}")

        print()


if __name__ == "__main__":
    main()
