# File history validation and objective comparison

Acceptance on 17 September 2026 uses disposable Redis processes, private state
and freshly built binaries. The original checkout, installed binaries and user
Redis data are unchanged. The reference is redis/agent-filesystem commit
`1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e`.

## Assessment

The new implementation improves tested publication consistency, recoverability,
retention memory and local write latency. It does not establish unconditional
production superiority. The original has production experience that a local test
suite cannot replace. This implementation has more complex synchronous history
handling, sometimes uses more memory, and has interface and storage differences.

The per-file versioning engine is an adaptation built around the retained AFS
filesystem/checkpoint code, not an unchanged copy of the original observer and
storage engine. Original policy concepts, lineage semantics, response structures
and history workflows are retained. Capture moves into the existing conditional
Redis publication scripts, and history owns reference-counted SHA256 bodies.
Original stored histories use different Redis keys and records; there is no
migration or mixed-writer guarantee. All writers must be upgraded before enabling
new history. See [compatibility limits](file-history-compatibility.md).

## Paired measurements

5 repetitions ran on each of standard Redis 7.2.5 and the experimental Array
build `255.255.255` (`ead90ea0`). Each implementation/repetition/scenario used a
fresh owned server with persistence disabled, identical payloads and matching
enabled-history workloads. There were 240 scenario executions without
harness failures. These are local Apple arm64 microbenchmarks, not remote,
production, fsync or large-workspace capacity measurements. The table reports the
median of the per-run median operation times, including history handling.

| Workload | Original / new, standard Redis (ms) | Original / new, Array (ms) |
| --- | ---: | ---: |
| Forty distinct 64 KiB rewrites | 1.679 / 0.914 | 1.868 / 0.755 |
| Forty identical 64 KiB rewrites | 2.925 / 0.914 | 3.167 / 0.805 |
| Forty permission changes | 1.631 / 0.668 | 1.723 / 0.646 |
| Forty 64 KiB rewrites, retain five | 1.180 / 0.988 | 1.353 / 0.924 |
| Sixteen 64-byte appends to a 1 MiB file | 4.731 / 2.303 | 6.060 / 2.759 |

Across these timed scenarios, the new version was 1.19–3.93 times faster.
The concurrent-writer scenario is excluded from speed comparisons because the
implementations did not acknowledge the same number of writes.

With five-version retention, summed workspace `MEMORY USAGE` changed from
4,153,440 to 632,448 bytes on standard Redis (84.8% less), and from
4,125,164 to 479,356 bytes on Array (88.4% less). History payload memory fell
87.8% and 90.8%, respectively. The principal
improvement is reclamation: original pruning removed version metadata while
retaining immutable blob bodies. Both implementations suppressed identical
versions and shared one body across the 41 mode-change records.

The new version is not consistently smaller. Unlimited-history standard Redis
workloads used up to 6.5% more workspace memory.
The Array append case used 21,360,982 bytes
versus 19,594,306 bytes
(9.0% more),
and its history payload memory was
9.6% more.
These estimates are retained key memory, not peak server RSS; staging, allocator
behavior and persistence add costs. Full snapshot/hash work remains significant
for small appends to large files. Logical byte limits remain per-version
accounting, separate from physical sharing and Redis overhead.

Both backends used production Go/Lua fingerprint
`049f82f3231c87136e85e5e8511c467bf3892c785618a7aa0ad5d7aa646da0c5`. Recorded host: `macOS-15.8-arm64-arm-64bit-Mach-O`;
toolchain: `go version go1.26.1 darwin/arm64`. Both servers used the libc allocator. Implementation order alternated
by repetition, and heavy validation paused during measurement. Raw timing rows,
tail latencies, source/harness hashes and API fields are retained in
[standard Redis evidence](evidence/file-history-redis7.json) and
[Array evidence](evidence/file-history-array.json). The
[runner](../tests/history_compare/README.md) reproduces the method. These local
samples do not establish statistical significance or universal superiority.

## Correctness and compatibility evidence

| Recoverability / contract check | Original passing runs | New passing runs |
| --- | ---: | ---: |
| First deletion after enabling history | 0 / 10 | 10 / 10 |
| Deleted contents with a one-version limit | 0 / 10 | 10 / 10 |
| Process exit at publication/observer boundary | 0 / 10 | 10 / 10 |
| Every acknowledged concurrent-write payload retained | 6 / 10 | 10 / 10 |
| Final live contents recoverable after concurrent writes | 0 / 10 | 10 / 10 |
| Eight content/pagination/diff/restore/undelete API checks | 10 / 10 | 10 / 10 |

Concurrent runs attempted 40 writes without retrying conflicts.
The new implementation acknowledged 20 writes in every run;
original acknowledged counts ranged from 2 to 26.
These are consistency stress results, not an equal-success-count throughput
comparison. All rejected counts and error text remain in the raw reports.
The deterministic crash experiment targets the original observer gap; it does
not estimate random crash frequency or Redis server-persistence durability.

The unchanged original drawer, hooks, HTTP client and styles passed an earlier
actual Chrome workflow using a small provider host: 55-version pagination,
content selection, checkpoint diff, restore and undelete. Later component
follow-up runs use jsdom plus real HTTP/Redis and are explicitly distinct from
that browser run. This does not cover the original application's cloud, account
or catalog screens. Exact source and scope are recorded in the
[UI evidence](evidence/file-history-ui.json).

Focused regressions cover atomic whole/range/chunk writes, binary and empty files,
symlinks, modes, rename and replacement, identical-write suppression, shared
ownership, physical reclamation, metadata-only records, byte quotas, corruption
preflight, lost acknowledgements, stale revisions and generation fencing. They
also cover policy races and original glob grammar, independent forks/source
deletion, checkpoint restore, multiple interruptions, retained restore-version
links and protection of the safety baseline checkpoint. The final review's three
checkpoint recovery regressions reproduced before their fixes and passed after.

## Final acceptance gates

All final local acceptance commands passed on the frozen implementation.
Commands use owned fixtures, never existing user Redis:

```sh
go build ./...
go vet ./...
go test -p 1 -count=1 ./...
go test -race -p 1 -count=1 ./...
go test -tags=integration -timeout=15m -count=1 ./tests/e2e ./internal/filehistory
make native-deps-test
python3 -m unittest discover -s tests/multiwriter -p 'test_*.py'
```

Package execution is serialized for full Go suites. The first final race run
stopped before an affected test because an owned Redis child could not bind its
selected ephemeral port. Startup now verifies the child PID and retries only
confirmed bind collisions; regressions check that another owned server is never
used and unrelated startup errors remain visible. The clean full unit and race
reruns passed. A metadata-only lineage without a recoverable head also exposed
a real pruning error in process acceptance. The regression failed before the
fix and passed afterward; the complete process suite then passed. The Python harness
requires localhost listeners and process inspection; the sandbox-restricted run
failed on those permissions, and the normal-permission rerun passed all 38 tests.
Those permission failures are not counted as product failures or silently omitted.

Final Array checks used an owned server with verified `INFO process_id`, the
Array executable on PATH for every child fixture, and explicit native Array
settings pointing only at that owned endpoint. Native history/restore/Array race
checks and all history CLI/process/storage integration checks passed. Servers
were stopped after completion. Native dependency race tests and all 38 Python
harness checks also passed.

Local logs: `/private/tmp/afs-history-superset-{unit,race}-final-rerun.log`,
`/private/tmp/afs-history-superset-integration-final-rerun.log`,
`/private/tmp/afs-history-superset-array-final.log`,
`/private/tmp/afs-history-superset-native-deps-final.log`, and
`/private/tmp/afs-history-superset-python-final.log`. Initial failed runs retain
separate log names. The committed tests reproduce the checks without those local
logs. GitHub core and actual Linux kernel mount CI will run after publication.

## Costs and limits

- History capture is synchronous with publication. A history quota, preparation
  or integrity failure can reject the live write. The original observer can
  publish content first and then fail to record it. This is a consistency versus
  write-availability tradeoff, not a free improvement.
- New distinct content retains a full snapshot. Hashing reads the full staged
  file for range writes; there are no deltas or close/time-window coalescing.
  Recovery loads the full selected body into client memory. Both designs have
  full-snapshot costs, and bounded history does not impose a peak memory ceiling.
- Shared-body ownership, preflight checks and atomic Lua add implementation
  complexity. Fork/history copying and checkpoint restore include bulk work
  proportional to retained history or tree size and can block Redis. The local
  measurements do not validate large-workspace tail latency.
- Default pagination is bounded, invalid/oversized glob input is rejected,
  off/exclusions stop already tracked lineages, and a deletion may retain a
  recoverable record plus tombstone under a one-version limit. These intentional
  differences are documented rather than described as exact equivalence.
- Ordinary mounts provide client origin/source, not the original application's
  authenticated session/agent/user context. Explicit API actions accept optional
  provenance labels. The optional server provides history interfaces on loopback,
  not the original application's identity or deployment infrastructure.
- Local tests and deterministic fault injection are evidence, not proof of zero
  defects. Customer-scale workloads, remote deployments and production soak time
  remain untested for this feature. Native inode operations are covered directly;
  actual kernel mount acceptance is a separate CI gate.
