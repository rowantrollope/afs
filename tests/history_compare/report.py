#!/usr/bin/env python3
"""Regenerate comparison claims from complete paired runs; never edit raw numbers."""
from __future__ import annotations

import argparse
import json
import pathlib
import shutil

import compare

CASES = (
    ("overwrite", "Forty distinct 64 KiB rewrites"),
    ("identical", "Forty identical 64 KiB rewrites"),
    ("metadata", "Forty permission changes"),
    ("retention", "Forty 64 KiB rewrites, retain five"),
    ("append", "Sixteen 64-byte appends to a 1 MiB file"),
)


def load(path):
    report = json.loads(path.read_text())
    if any("harness_error" in row or row.get("error") for row in report["rows"]):
        raise ValueError("report contains failed executions: " + str(path))
    if report["aggregate"] != compare.aggregate(report["rows"]):
        raise ValueError("recorded aggregates differ from raw rows: " + str(path))
    aggregates = {(row["case"], row["implementation"]): row for row in report["aggregate"]}
    for case, _ in CASES:
        for kind in ("original", "new"):
            group = aggregates[(case, kind)]
            if group["successful_runs"] != report["repetitions"]:
                raise ValueError("incomplete paired workload: " + case + " " + kind)
    return report, aggregates


def between(text, start, end, replacement):
    left = text.index(start)
    right = text.index(end, left)
    return text[:left] + replacement.rstrip() + "\n\n" + text[right:]


def rate(old, new):
    return (1 - new / old) * 100


def direction(percent):
    return f"{abs(percent):.1f}% {'less' if percent >= 0 else 'more'}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--redis7", required=True, type=pathlib.Path)
    parser.add_argument("--array", required=True, type=pathlib.Path)
    parser.add_argument("--write", action="store_true", help="replace generated report sections and checked-in raw evidence")
    args = parser.parse_args()
    standard, std = load(args.redis7)
    array, arr = load(args.array)
    for field in ("original_revision", "original_production_sha256", "new_production_sha256", "repetitions"):
        if standard[field] != array[field]:
            raise ValueError("paired backends disagree on " + field)
    reps = standard["repetitions"]
    runs = reps * 2
    rows = standard["rows"] + array["rows"]
    groups = (std, arr)
    ratios = [group[(case, "original")]["median_us"] / group[(case, "new")]["median_us"] for group in groups for case, _ in CASES]
    table = ["| Workload | Original / new, standard Redis (ms) | Original / new, Array (ms) |", "| --- | ---: | ---: |"]
    for case, label in CASES:
        values = [f'{group[(case,"original")]["median_us"]/1000:.3f} / {group[(case,"new")]["median_us"]/1000:.3f}' for group in groups]
        table.append(f"| {label} | {values[0]} | {values[1]} |")
    retention = []
    payload_savings = []
    for label, group in (("standard Redis", std), ("Array", arr)):
        old, new = group[("retention", "original")], group[("retention", "new")]
        retention.append(f'{old["workspace_memory_usage_bytes"]:,.0f} to {new["workspace_memory_usage_bytes"]:,.0f} bytes on {label} ({direction(rate(old["workspace_memory_usage_bytes"],new["workspace_memory_usage_bytes"]))})')
        payload_savings.append(rate(old["historical_payload_memory_usage_bytes"], new["historical_payload_memory_usage_bytes"]))
    standard_memory_changes = [-rate(std[(case, "original")]["workspace_memory_usage_bytes"], std[(case, "new")]["workspace_memory_usage_bytes"]) for case in ("overwrite", "identical", "metadata", "append")]
    old_append, new_append = arr[("append", "original")], arr[("append", "new")]
    speed = f"Across these timed scenarios, the new version was {min(ratios):.2f}–{max(ratios):.2f} times faster." if min(ratios) >= 1 else f"Original/new latency ratios ranged from {min(ratios):.2f} to {max(ratios):.2f}; ratios below one mean the new version was slower."
    measurements = f"""## Paired measurements

{reps} repetitions ran on each of standard Redis 7.2.5 and the experimental Array
build `255.255.255` (`ead90ea0`). Each implementation/repetition/scenario used a
fresh owned server with persistence disabled, identical payloads and matching
enabled-history workloads. There were {len(rows)} scenario executions without
harness failures. These are local Apple arm64 microbenchmarks, not remote,
production, fsync or large-workspace capacity measurements. The table reports the
median of the per-run median operation times, including history handling.

{chr(10).join(table)}

{speed}
The concurrent-writer scenario is excluded from speed comparisons because the
implementations did not acknowledge the same number of writes.

With five-version retention, summed workspace `MEMORY USAGE` changed from
{retention[0]}, and from
{retention[1]}. History payload memory fell
{payload_savings[0]:.1f}% and {payload_savings[1]:.1f}%, respectively. The principal
improvement is reclamation: original pruning removed version metadata while
retaining immutable blob bodies. Both implementations suppressed identical
versions and shared one body across the 41 mode-change records.

The new version is not consistently smaller. Unlimited-history standard Redis
workloads used up to {max(standard_memory_changes):.1f}% more workspace memory.
The Array append case used {new_append['workspace_memory_usage_bytes']:,.0f} bytes
versus {old_append['workspace_memory_usage_bytes']:,.0f} bytes
({direction(rate(old_append['workspace_memory_usage_bytes'],new_append['workspace_memory_usage_bytes']))}),
and its history payload memory was
{direction(rate(old_append['historical_payload_memory_usage_bytes'],new_append['historical_payload_memory_usage_bytes']))}.
These estimates are retained key memory, not peak server RSS; staging, allocator
behavior and persistence add costs. Full snapshot/hash work remains significant
for small appends to large files. Logical byte limits remain per-version
accounting, separate from physical sharing and Redis overhead.

Both backends used production Go/Lua fingerprint
`{standard['new_production_sha256']}`. Recorded host: `{standard['host']}`;
toolchain: `{standard['go']}`. Both servers used the libc allocator. Implementation order alternated
by repetition, and heavy validation paused during measurement. Raw timing rows,
tail latencies, source/harness hashes and API fields are retained in
[standard Redis evidence](evidence/file-history-redis7.json) and
[Array evidence](evidence/file-history-array.json). The
[runner](../tests/history_compare/README.md) reproduces the method. These local
samples do not establish statistical significance or universal superiority.
"""
    def success(case, kind, check=None):
        return sum(group[(case, kind)]["recoverable_runs"] if check is None else group[(case, kind)].get("check_passing_runs", {}).get(check, 0) for group in groups)
    api_pass = {kind: sum(all(row.get("checks", {}).values()) and len(row.get("checks", {})) == 8 for row in rows if row["case"] == "api" and row["implementation"] == kind) for kind in ("original", "new")}
    assertions = [("First deletion after enabling history", "first_delete", None), ("Deleted contents with a one-version limit", "delete_retention", None), ("Process exit at publication/observer boundary", "crash", None), ("Every acknowledged concurrent-write payload retained", "multiwriter", "all_accepted_writes_recoverable"), ("Final live contents recoverable after concurrent writes", "multiwriter", None)]
    correctness = ["## Correctness and compatibility evidence", "", "| Recoverability / contract check | Original passing runs | New passing runs |", "| --- | ---: | ---: |"]
    for label, case, check in assertions:
        correctness.append(f"| {label} | {success(case,'original',check)} / {runs} | {success(case,'new',check)} / {runs} |")
    correctness.append(f"| Eight content/pagination/diff/restore/undelete API checks | {api_pass['original']} / {runs} | {api_pass['new']} / {runs} |")
    counts = {kind: [row["accepted_writes"] for row in rows if row["case"] == "multiwriter" and row["implementation"] == kind] for kind in ("original", "new")}
    correctness += ["", "Concurrent runs attempted 40 writes without retrying conflicts.", (f"The new implementation acknowledged {min(counts['new'])} writes in every run;" if min(counts['new']) == max(counts['new']) else f"New acknowledged counts ranged from {min(counts['new'])} to {max(counts['new'])};"), f"original acknowledged counts ranged from {min(counts['original'])} to {max(counts['original'])}.", "These are consistency stress results, not an equal-success-count throughput", "comparison. All rejected counts and error text remain in the raw reports.", "The deterministic crash experiment targets the original observer gap; it does", "not estimate random crash frequency or Redis server-persistence durability.", "", "The unchanged original drawer, hooks, HTTP client and styles passed an earlier", "actual Chrome workflow using a small provider host: 55-version pagination,", "content selection, checkpoint diff, restore and undelete. Later component", "follow-up runs use jsdom plus real HTTP/Redis and are explicitly distinct from", "that browser run. This does not cover the original application's cloud, account", "or catalog screens. Exact source and scope are recorded in the", "[UI evidence](evidence/file-history-ui.json)."]
    correctness = "\n".join(correctness)
    summary = {"production_sha256": standard["new_production_sha256"], "scenario_executions": len(rows), "speedup_min": min(ratios), "speedup_max": max(ratios), "standard_retention_workspace_saved_percent": rate(std[("retention","original")]["workspace_memory_usage_bytes"],std[("retention","new")]["workspace_memory_usage_bytes"]), "array_retention_workspace_saved_percent": rate(arr[("retention","original")]["workspace_memory_usage_bytes"],arr[("retention","new")]["workspace_memory_usage_bytes"]), "standard_unlimited_workspace_increase_max_percent": max(standard_memory_changes), "array_append_workspace_increase_percent": -rate(old_append["workspace_memory_usage_bytes"],new_append["workspace_memory_usage_bytes"])}
    print(json.dumps(summary, indent=2))
    if args.write:
        docs = compare.ROOT / "docs"
        validation = docs / "file-history-validation.md"
        text = between(validation.read_text(), "## Paired measurements", "## Correctness and compatibility evidence", measurements)
        text = between(text, "## Correctness and compatibility evidence", "Focused regressions cover", correctness)
        validation.write_text(text)
        parity = docs / "file-history-parity.md"
        text = between(parity.read_text(), "The completed comparison used", "## Browser compatibility gate", measurements + "\n\n" + correctness) if "The completed comparison used" in parity.read_text() else between(parity.read_text(), "## Paired measurements", "## Browser compatibility gate", measurements + "\n\n" + correctness)
        parity.write_text(text)
        evidence = docs / "evidence"
        shutil.copy2(args.redis7, evidence / "file-history-redis7.json")
        shutil.copy2(args.array, evidence / "file-history-array.json")
        (evidence / "file-history-summary.json").write_text(json.dumps(summary, indent=2) + "\n")


if __name__ == "__main__":
    main()
