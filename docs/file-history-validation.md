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

After the recorded measurements, a user-requested CLI consolidation moved all
history actions under `afs history`. The recorded source hashes identify the
pre-consolidation snapshot. This command-dispatch and help change leaves the
measured storage/publication engine and HTTP contracts unchanged; the numeric
evidence has not been rerun or rewritten for the new command spelling.

The earlier CLI consolidation passed `go build ./...`, `go vet ./...`, full unit and race
suites with `-p 1 -count=1`, and
`go test -tags=integration -timeout=15m -count=1 ./tests/e2e ./internal/filehistory`.
Process coverage exercises grouped pagination, policy, exact local export,
restore, undelete, lineage, checkpoints and independent writers. Offline help
and rejection of the removed root commands have focused regressions. The original
unchanged drawer passed all four component tests in jsdom against a freshly built
then-available `afs history serve` and disposable Redis, including restored/deleted contents,
activity with capture off, and actor attribution. This records that stage's CLI/component
acceptance, separate from the earlier browser, benchmark and kernel evidence.

The subsequent boundary correction removes that CLI server entry point. The
seven-action history CLI remains, while `NewFileHistoryHandler` stays in the
control-plane package for future server integration and test-only hosts. No
runnable product control plane is delivered. The paragraph above records the
earlier consolidation test; its command spelling is historical, and the existing
benchmark/browser results and source hashes are preserved unchanged.

After removing CLI serving, build, vet, full unit/race and the same isolated
real-Redis process suite passed again. All four unchanged original-drawer tests
passed in jsdom against `NewFileHistoryHandler` hosted by the disposable
`ui_server.go.in` fixture, with exact restored/undeleted contents and ordinals
verified. All fixture listeners closed afterward. Removed-command tests reject
`history serve` before configuration or Redis access; control-plane tests retain
origin enforcement and HTTP contract coverage.

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
| Forty distinct 64 KiB rewrites | 1.546 / 0.828 | 1.770 / 0.934 |
| Forty identical 64 KiB rewrites | 2.978 / 0.961 | 3.191 / 0.998 |
| Forty permission changes | 1.613 / 0.668 | 1.753 / 0.613 |
| Forty 64 KiB rewrites, retain five | 1.159 / 1.181 | 1.422 / 0.811 |
| Sixteen 64-byte appends to a 1 MiB file | 4.769 / 2.245 | 5.989 / 2.771 |

Original/new latency ratios ranged from 0.98 to 3.20; ratios below one mean the new version was slower. The new version was faster in 9 of 10 workload/backend pairs (1.75–3.20 times). Forty 64 KiB rewrites, retain five on standard Redis was 1.9% slower (1.159 to 1.181 ms).
The concurrent-writer scenario is excluded from speed comparisons because the
implementations did not acknowledge the same number of writes.

With five-version retention, summed workspace `MEMORY USAGE` changed from
4,153,440 to 632,448 bytes on standard Redis (84.8% less), and from
4,125,175 to 479,352 bytes on Array (88.4% less). History payload memory fell
87.8% and 90.8%, respectively. The principal
improvement is reclamation: original pruning removed version metadata while
retaining immutable blob bodies. Both implementations suppressed identical
versions and shared one body across the 41 mode-change records.

The new version is not consistently smaller. Unlimited-history standard Redis
workloads used up to 6.5% more workspace memory.
The Array append case used 21,360,982 bytes
versus 19,594,274 bytes
(9.0% more),
and its history payload memory was
9.6% more.
These estimates are retained key memory, not peak server RSS; staging, allocator
behavior and persistence add costs. Full snapshot/hash work remains significant
for small appends to large files. Logical byte limits remain per-version
accounting, separate from physical sharing and Redis overhead.

Both backends used production Go/Lua fingerprint
`acd3be64274c204bb792e8f74c0abd52e30edff8873770042fb80fa566fc519e`. Recorded host: `macOS-15.8-arm64-arm-64bit-Mach-O`;
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
original acknowledged counts ranged from 5 to 22.
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

All local acceptance commands passed. The full suites were rerun after ordinary
sync attribution was completed; after the subsequent narrow sync correction,
build/vet, all affected CLI unit/race tests, complete process acceptance and the
Array history/CLI checks passed again.
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
logs. GitHub core and actual Linux kernel mount results are tracked separately
from these local gates.

Final attribution logs are `/private/tmp/afs-history-attribution-{build,vet,unit,race,integration,array}.log`.
The final sync correction logs are
`/private/tmp/afs-history-sync-fix-{build,vet,unit,race,integration,array}.log`;
its unit/race reruns cover the complete `cmd/afs` package. Other production
packages were unchanged after their successful full-suite run. The original
drawer passed four final component tests, including attributed activity.
Initial Linux CI results and the reproduced sync defect are retained below.
Current-head kernel acceptance is tracked separately in the checks attached to
[PR #5](https://github.com/rowantrollope/afs/pull/5), with the final verdict recorded
in its description.

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
- Folder sync retains optional session/agent/user provenance and the original
  `agent_sync` source; activity also carries a display label and client version.
  Native mounts retain the original `mount` default. These labels and explicit
  API action headers do not authenticate identities or open managed sessions.
  The internal control-plane handler retains the history interfaces, with
  loopback hosts used only by tests. AFS ships no server runtime; the original
  application's identity and deployment infrastructure remains outside AFS.
- Local tests and deterministic fault injection are evidence, not proof of zero
  defects. Customer-scale workloads, remote deployments and production soak time
  remain untested for this feature. Native inode operations are covered directly;
  actual kernel mount acceptance is a separate CI gate.

## Linux CI rename investigation

The push run's [eight-client native job failed](https://github.com/rowantrollope/afs/actions/runs/35277223088/job/105390629385)
in `native-rename`: after an NFS rename and a write/fsync through the existing
FUSE handle, one folder-sync client retained `before` instead of `after!` for
90 seconds. The other seven clients had the expected six bytes. The stale
client reported connected, zero queued/tracked uploads and zero conflicts.
Its saved baseline retained the hash of `before`, while local and remote mtimes
both equaled `1789681158188`. Nine other scenarios passed; the supervisor did
not time out and reported no cleanup errors or remaining owned processes.

The [duplicate pull-request run passed all ten scenarios](https://github.com/rowantrollope/afs/actions/runs/35277573040/job/105391757046),
including rename in 7.84 seconds. Both runs used commit
`73caa32aa6f80fb44fb295d2b7fe68f6c40b071f`, identical AFS binary SHA256
`500244ae670b92c1176c160d523aa0d2ceec753fb09613f0b014b67aee9dd77e` and identical
helper SHA256 `f15bbecf3f543444734e49a512cb987d62d9be82367621ad63cc6ba11ace66f5`.
The pass establishes intermittency, not absence of a defect.

Investigation reproduced an unsafe shortcut in the retained sync reconciler:
equal local/remote size and millisecond mtime could override an older acknowledged
remote baseline without checking bytes. Two queued downloads observed before a
local baseline exists can expose this: the first installs old bytes, the second
correctly defers to a full sweep, and that sweep incorrectly accepts coincident
timestamps. These reconciler/downloader files were unchanged by the history
commit. This sequence is consistent with the retained logs and baseline, but
the CI artifacts do not record enough scheduling detail to establish its unique
interleaving or exclude another content/stat race.

`TestSyncRecoveryDoesNotAdoptRemoteMtimeCollision` (with and without a saved
baseline) and `TestSyncSkippedQueuedDownloadRecoversRemoteMtimeCollision` all
failed against the committed pre-fix source using a Go source overlay and passed
after removing only the cross-system timestamp shortcut. Matching each side to
its own acknowledged stable baseline remains a fast path; the regression checks
that the next unchanged warm sweep reads zero file bodies. Files lacking a
baseline now require content verification even when size and timestamps match,
adding first-sync reads while preserving unknown local bytes as conflict copies.
Focused recovery, read-only and startup race tests, vet and diff checks passed.

Retained evidence: [failed artifact](https://github.com/rowantrollope/afs/actions/runs/35277223088/artifacts/10520993227),
particularly `native-rename/client-2/{manifest,differences,status}.json`, its
`state/clients/*/sync/*.json`, `mount-1.log`, and the root `report.json` and
`supervisor.json`. Local copies are under
`/private/tmp/afs-ci-{35277223088,35277573040}-native8`; the full failed job log is
`/private/tmp/afs-ci-35277223088-job.log`, and regression output is retained in
`/private/tmp/afs-ci-mtime-{failed-before,passed-after}.log`.
