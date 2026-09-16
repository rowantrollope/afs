# Updated-code acceptance on Sancho and macOS

Follow-up: the unchanged code/workload passes all 12 scenarios on both systems
with the user's replacement database. See
[the replacement-database results](two-system-results-2026-09-16-new-database.md).
The capacity-limited run below is retained as historical evidence.

Run `9cd72d09b414` used the updated code on the actual Sancho Linux host and this
Mac, connecting to the Redis instance configured on each machine. Both reports
agree: **5 passed, 6 capacity-blocked, 1 connection failure**. Both runners exited
1. All 12 scenarios were attempted, but resource failures prevented several from
reaching their workload/validation stages. This is not a full acceptance pass.

## Execution and provenance

- Identical source snapshot, based on `ac31fda` plus the uncommitted fixes.
- Source SHA-256:
  `b630425763c3062a29dd873ba1582608fcbdd87b7d571882a78e35bf6410b1d3`.
- Separate native builds on macOS 15.8 arm64 and Sancho Linux x86-64. The source
  archive and extracted contents were verified before the Sancho build.
- Sancho ran `host`; the Mac ran `join` through a new loopback SSH tunnel.
- Each runner loaded its own existing `~/.config/afs-lite/config.json` Redis URL.
  Both configurations point to the same endpoint and database 0.
- Original default workload: 100 files per writer per bulk round, three rounds,
  8 MiB large fixtures, seed 1, two-second stability window, 120-second phase limit.
- Each scenario used a fresh `two-host-9cd72d09b414-*` workspace and private local
  state. Installed executables, pre-existing workspaces and Redis settings were
  not changed. Some planned workspace names were never successfully created.
- Both runners reported no cleanup errors. A separate process check found no
  remaining AFS processes belonging to this test on either machine. The new
  tunnel was closed; the user's terminal sessions were left alone.

## Results

| Scenario | Result | What happened |
|---|---|---|
| basic | Connection failure | Both writers passed initial/reverse edits, symlinks, modes and checkpoint flush checks. The Mac fresh observer then failed to connect to Redis. |
| mutations | Pass | Recursive rename/delete, identical recreation, append/truncate, file chmod and directory chmod, fresh observers and normal unmount all passed. |
| bulk | Capacity-blocked | Initial concurrent file creation hit repeated Redis OOM errors. It did not converge before 120 seconds; mutation rounds and fresh observers were not reached. |
| large | Capacity-blocked | Redis rejected workspace creation with OOM; large-file operations were not started. |
| ignore | Capacity-blocked | Redis rejected workspace creation with OOM. |
| shared-create | Capacity-blocked | Redis rejected workspace creation with OOM. |
| shared-edit | Capacity-blocked | Redis rejected workspace creation with OOM. |
| symlink-conflict | Capacity-blocked | Redis rejected workspace creation with OOM. |
| delete-edit | Pass | The unpublished edit survived the peer deletion, with correct fresh observers and normal unmount. Sancho encountered a transient OOM but recovered and completed every validation. |
| rename-edit | Pass | Original and edited versions survived the competing rename; final publication and unmount checks passed. |
| partition | Pass | Both file and symlink versions, plus disjoint offline files, recovered after reconnection; fresh observers and normal unmount passed on both OSes. |
| crash | Pass | Warm restart recovered intended edits and symlink targets after SIGKILL; fresh observers and normal unmount passed on both OSes. |

All five passing cases verified exact expected trees, completed checkpoint
flushes, hydrated a fresh observer on each machine, and checked local contents
after normal unmount. A passed case is not just agreement between the writers.

## Failures and their limits

**Redis memory pressure is confirmed.** Bulk logs on both machines contain
`OOM command not allowed when used memory > 'maxmemory'`. The following five
cases failed at workspace creation with the same error. At the bulk deadline,
Sancho lacked 69 files belonging to the Mac writer, and the Mac lacked 45 files
belonging to the Sancho writer. Every reported bulk mismatch was a missing peer
file; the existing entries matched expected content/metadata, and each writer
retained its own intended files. This does not establish permanent loss or prove
that no other issue could coexist. The bulk mutation/queue fix remains unverified
by this actual run because the initial creation stage could not finish.

**Connection capacity strongly explains the basic failure.** Redis reports
`maxclients: 30`. The rejected-connection counter went from 0 at run start to 6
by the next scenario's start, and 9 by the end of mutations. The fresh-observer
failure occurred in this interval. INFO probes also became temporarily
unavailable during that failure. The CLI reports a generic connection error, so
the exact low-level rejection for that particular connection was not retained.
Each AFS process has an eight-connection pool, plus subscription use; the suite
adds fresh observers while writers remain mounted, alongside existing user
connections. This concurrency can exceed a 30-client server limit.

| Server metric | Before run | After run |
|---|---:|---:|
| Used memory, bytes | 30,345,072 | 31,212,696 |
| Rejected connections | 0 | 9 |
| Evicted keys | 370 | 410 |
| Configured client limit | 30 (observed during run) | 30 |
| Eviction policy | volatile-lru | volatile-lru |

The service did not return `maxmemory`; its quota cannot be inferred from the
used/peak values. Counters are server-wide, so the 40 additional evictions cannot
be attributed individually to particular test keys. Test-owned command errors
are the direct evidence for memory failures. No server configuration was changed
and no prior test data was deleted to make the suite pass.

## Assessment

The directory-permission fix is now validated across macOS and Linux. The
symlink save/unmount fix also passes real cross-platform recovery cases:
`partition` and `crash`, which previously failed Linux observer unmount, now
complete normally with their intended targets preserved.

The remaining seven cases are not validated. A full clean rerun needs sufficient
Redis memory and connection headroom. Retained test data contributes to memory
use; cleanup of exact obsolete test workspaces or changing server capacity is a
separate action. The runner can also reduce observer overlap/connection demand
without removing the required cold-observer checks. No resource workaround was
applied during this run.

## Retained evidence

- Mac run and frozen source/build:
  `/var/folders/kg/0cp9s_gx4v5bjkc6bkmcx3f40000gn/T/afs-sancho-e2e-mq4xxn16`.
- Sancho run and frozen source/build: `/tmp/afs-sancho-e2e-mq4xxn16`.
- Mac report: `mac/report.json`; fetched Sancho report:
  `sancho-results/report.json` under the Mac root above.
- Selected Sancho logs, comparisons, oracles and sync baselines were fetched into
  `sancho-results/`, excluding configuration files and local control directories.
- Credential-free report copies and provenance are retained locally at
  `tests/multiwriter/artifacts/two-system-9cd72d09b414-review/` (gitignored).

Full artifact directories contain private configurations; the report copies and
this evaluation are the intended review materials.
