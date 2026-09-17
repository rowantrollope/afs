# Original/new file-history comparison

This runner builds two isolated source snapshots, starts a fresh Redis process
for every scenario/implementation/repetition, and drives equivalent filesystem
client operations with history enabled on both implementations. It never uses an
installed AFS binary, saved AFS configuration, or existing Redis endpoint.

```sh
python3 tests/history_compare/compare.py \
  --original /path/to/redis-agent-filesystem \
  --original-ref 1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e \
  --output /tmp/afs-history-comparison-final \
  --repetitions 5
```

The output directory must not exist. Dependencies are read from the existing Go
module cache; build products, a private HOME, original/new source snapshots,
server logs, and results.json stay in the output directory. The original
checkout is read with `git archive` at the recorded commit; new source includes
the current working changes. Both source hashes, Go/Redis versions and host
platform are recorded. Use `--redis-server /path/to/redis-server` to run the
same scenarios against another disposable Redis implementation.

Scenarios:

- 40 distinct 64 KiB rewrites, with unlimited retention and with five versions.
- 40 identical rewrites and 40 mode changes to the same 64 KiB file.
- Sixteen 64-byte appends to a 1 MiB file.
- First deletion of a file created before history was enabled.
- Deletion with a one-version retention policy.
- Explicit four-byte capture cutoff, and disabling capture on a tracked file.
- Matching service calls for ID/ordinal content, descending cursor pagination,
  checkpoint/live diffs, in-place restore and undelete. Normalized response
  fields and boolean assertions are retained in `results.json`.
- Two concurrent clients issuing 40 distinct rewrites to one file. This is a
  correctness stress case: the report retains accepted/rejected counts and
  error text, and checks every acknowledged payload plus final live content.
  Do not compare its latency as equivalent throughput when acceptance differs.
- A process exit at the original implementation's gap between accepted live
  publication and history observation; new exits immediately after its atomic
  publication returns. This is a deterministic fault-boundary test, not a claim
  about random crash probability or recovery from a Redis server crash.

Median and p95 latencies cover each write/chmod/append call, including its
synchronous history handling. Total milliseconds for timed scenarios is the sum
of those operation durations; setup and correctness reads are excluded.
Redis `MEMORY USAGE` is summed over workspace keys after operations. Payload
memory separately sums original immutable blob bodies or new history-owned
bodies. It is a Redis key-memory estimate, not peak RSS or a network/storage
capacity estimate. Persistence is disabled in both servers. Run deployments
with representative payloads, latency and persistence settings before drawing
capacity conclusions.

Do not compare an original-enabled run against a new-disabled run. Report
slower or larger scenarios alongside improvements. The harness retains failures
in its JSON output instead of silently discarding them. During implementation,
`--implementations original` or `new` can run one side, but a final comparative
claim needs a complete two-implementation run.

Regenerate the checked-in numeric tables from complete paired runs with:

```sh
python3 tests/history_compare/report.py \
  --redis7 /tmp/afs-history-comparison-redis7/results.json \
  --array /tmp/afs-history-comparison-array/results.json --write
```

The report generator rejects failed/incomplete runs, recomputes their
aggregates, and requires matching original/new production fingerprints across
backends. It updates both comparison documents and the raw evidence together.
Without `--write`, it prints derived metrics without changing files.

## Original browser component

```sh
python3 tests/history_compare/ui_compare.py \
  --original /path/to/redis-agent-filesystem \
  --output /tmp/afs-original-history-drawer \
  --duration 1800
```

The runner copies the pinned original source, builds current AFS and a seed
fixture in a separate source snapshot, starts owned Redis/API/Vite processes,
and prints two drawer URLs. It imports the **unchanged original drawer, hooks,
HTTP client, shared components and CSS**. The new host only supplies React Query
and theme providers plus workspace/path/editability props. Original UI
dependencies are read from `ORIGINAL/ui/node_modules`, or `--node-modules` can
point to another compatible existing installation. Nothing is installed or
written in the original checkout: Vite uses native config loading and a cache in
the disposable copy. The printed source/component hashes and seed metadata are
saved in `session.json`. Ctrl-C or the duration limit stops only owned processes.

Use a real browser on the printed live drawer URL:

1. Verify latest `v55` and `draft revision 55` load with recent path activity.
2. Click **Load more history**; verify versions 5 through 1 appear.
3. Select `v2`; verify `draft revision 2` appears.
4. Click **Diff against head**; verify `-draft revision 2` and `+checkpoint text`.
5. Click **Restore**; verify completion and a new `v56` / `version_restore` event.
6. Open the printed deleted drawer URL, click **Undelete**, then select `v3`;
   verify `recover this deleted content`, a live lineage and `version_undelete`.
7. Open the same host with `?path=/activity-only.txt`; verify a `put` path event
   exists even though that file was created while capture was off.
8. Open the same host with `?path=/attributed.txt`; verify the activity actor is
   `Named sync agent`, links to `v1`, and the version source is `agent_sync`.

Inspect screenshots at the loaded, diff and undeleted states, and check browser
console errors. This is component integration evidence, not a reintroduction or
test of the original cloud/catalog/authentication application.

For an unattended supplementary integration run, add `--component-test`. It
renders the unchanged component and hooks in jsdom, drives the same actions
against real disposable HTTP/Redis, and writes `component-results.json` plus
backend `verification.json` before stopping all processes. This exercises the
component's behavior and API mappings; it does not replace visual or actual
browser verification.
