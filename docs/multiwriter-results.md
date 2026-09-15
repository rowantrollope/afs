# Multi-writer acceptance

The lab models independent agents sharing a Redis workspace: separate local
trees, configuration, state, foreground AFS processes and TCP connections.
See the [run guide](../tests/multiwriter/README.md) for local and Linux commands.

## Native integration acceptance — passed

The native integration reuses this lab for ordinary folder-sync clients and adds
real FUSE/NFS mounts with the same independent byte oracles and cold observers.
See [native mounts](native-mounts.md) for architecture, commands and semantics.

The earlier eight-writer conflict-publication failure led to regression tests
for preserved copies whose canonical download result is discarded or errors after
the copy is created. Both paths now request publication through recovery. Further
runs reproduced and fixed duplicate deletion markers, directory permissions lost
when a child uploaded before its parent, stale remote-delete observations,
late download acknowledgments removing recreated files, and full recovery
replacing local edits made during download staging.

A final checkpoint drain regression now records only completed sync-owned
conflict moves during the save. It accepts a moved candidate only when its
bytes, permissions and inode identity match, while application renames and
edits still fail the save. The previously failing paused-writer scenario now
passes twenty consecutive runs, including checkpoint creation.

Eight-client native acceptance then exposed false transaction contention:
parent-bound operations watched workspace-wide counters and the journal, so
unrelated writes could exhaust retries and return EIO during file creation.
The deterministic regression reproduces create, write, metadata and remove
failures by publishing an unrelated file before each transaction. The fix
watches only the parent identity chain and lifetime guards; the Lua operation
still checks inode revision, directory entry, generation and session atomically.
Retry limits and error handling are unchanged. Acceptance was repeated after
this change; the failed native run is retained at
`tests/multiwriter/artifacts/native-20260915T074811Z-8df61d6042354272b09b9e8d2f86146b`.

### Integrated baseline acceptance

Before the additional lock regressions found during check-in review, all
required local gates passed on the integrated source. Tests used
disposable Redis servers, including a real Array-enabled build; no optional
tests skipped. The [acceptance summary](../tests/multiwriter/artifacts/final-acceptance-20260915/acceptance-summary.json)
checks all 68 concurrent scenario outcomes, CLI results and final binary hashes.

| Gate | Result |
| --- | --- |
| Build and vet | Pass |
| Unit tests | 635 passes, zero failures/skips |
| Race detector | 635 passes, zero failures/skips |
| Vendored NFS/FUSE cancellation regressions | 3 + 6 race passes |
| Harness oracle, deadline and cleanup tests | 16 passes |
| Linux native package / FUSE cancellation checks | 16 + 5 passes |
| Original/current CLI behavioral comparison | 12 passes, zero failures/skips |
| Paused-writer delete/rename and checkpoint repetitions | 20 passes |
| Full CLI process suite | 109 passes, zero failures/skips |
| Four/eight-client core acceptance, macOS and Linux | 40/40 scenarios pass |
| Native acceptance | Linux four/eight clients 20/20; macOS NFS 8/8 |

The [Go report](../tests/multiwriter/artifacts/final-acceptance-20260915/go-report.json),
[CLI comparison log](../tests/multiwriter/artifacts/final-acceptance-20260915/cli-compatibility.jsonl),
[full CLI report](../tests/multiwriter/artifacts/final-acceptance-20260915/e2e-summary.json),
and [paused-writer log](../tests/multiwriter/artifacts/final-acceptance-20260915/paused-writer-20.jsonl)
retain counts, commands and outcomes. The tested macOS CLI hash is
`aef5e744a9698915cbe53f134dc5a7f8aa11e5ddb4e6f3a25e35869e38f53332`;
the Linux CLI hash is
`405fdd6bc574610fd2cc212df85d1e2118b1f13f69c8a655361d572352f8b1aa`.
The corresponding helper hashes are
`495c018e97869f26d91e4a694484c51ef04001e61ac9f8c04627ce4f48c176b0`
(macOS) and `89a64f84eb5b2ba70b2e72cfd5ffaafff4c11b8ae89a6b66b2957d14ed4ac0af`
(Linux). Go checks used Go 1.26.1 on macOS ARM64; the Linux runs used
LinuxKit 6.12.54 ARM64 and Redis 7.0.15. The real Array build is identified by
executable hash in the Go report. Failed earlier reports remain retained;
none are relabeled as passes.

### Integrated concurrent acceptance

All runs use a 90-second assertion deadline. Files/rounds/seed/proxy delay are
recorded below; delay is milliseconds per proxy read in each direction. Times
sum scenario durations, and are not throughput benchmarks. Every passing
scenario checks independent expected data, successful flushes, convergence,
and a fresh observer. Mac NFS additionally verifies complete native sidecars.

| Platform and suite | Clients | Files / rounds / seed / delay | Result | Case total (s) |
| --- | ---: | --- | --- | ---: |
| [macOS core](../tests/multiwriter/artifacts/final-acceptance-20260915/macos-core-four.json) | 4 | 20 / 3 / 1 / 0 | 10/10 pass | 236.42 |
| [macOS core](../tests/multiwriter/artifacts/final-acceptance-20260915/macos-core-eight.json) | 8 | 5 / 2 / 17 / 1 | 10/10 pass | 497.94 |
| [Linux core](../tests/multiwriter/artifacts/final-acceptance-20260915/linux-core-four.json) | 4 | 20 / 3 / 1 / 0 | 10/10 pass | 334.27 |
| [Linux core](../tests/multiwriter/artifacts/final-acceptance-20260915/linux-core-eight.json) | 8 | 5 / 2 / 17 / 1 | 10/10 pass | 647.33 |
| [Linux FUSE/NFS/sync](../tests/multiwriter/artifacts/final-acceptance-20260915/linux-native-four.json) | 4 | 8 / 3 / 17 / 1 | 10/10 pass | 290.18 |
| [Linux FUSE/NFS/sync](../tests/multiwriter/artifacts/final-acceptance-20260915/linux-native-eight.json) | 8 | 8 / 3 / 17 / 1 | 10/10 pass | 903.70 |
| [macOS NFS](../tests/multiwriter/artifacts/final-acceptance-20260915/macos-native-nfs-report.json) | 2 | 2 / 2 / 17 / 0 | 8/8 pass | 417.09 |

The final eight-client native harness overlaps complete-manifest reads across
clients. Every byte/mode/path assertion is preserved; the core lab is unchanged.
Five additional harness tests check concurrent reads and prove that wrong bytes,
modes, missing entries, unexpected native sidecars and read errors still fail.

All final runs have zero setup, capture and cleanup errors. The native
supervisors report no remaining owned processes; both Linux Compose projects
have no remaining containers, networks or volumes. The macOS core audit confirms
all 122 recorded process IDs absent and all 120 client registries empty.
Linked artifacts are retained locally under the ignored test-artifact directory;
CI retains its own run artifacts separately.

The extra Linux FUSE cancellation check initially failed because Docker's
temporary filesystem was mounted `noexec`. An independent script-execution
probe reproduced that restriction. All five tests then passed unchanged with
an executable private tmpfs; the first failed log remains retained.

The first final macOS race run overlapped native NFS mount lifecycle tests.
An unrelated NFS detach made the system-wide `lsof` scan uncertain, so the
existing open-file guard refused the operation. The complete Go gate passed
after native mounts were cleaned up; the guard was not relaxed. A separate
test-only RPC client selected an already-used random source port. Its fixture
now retries only wrapped `EADDRINUSE`, with a fixed attempt limit; deterministic
occupied-port, exhaustion and other-error tests verify the behavior. Production
NFS does not use that test client's dialer.

These are independent processes sharing one host kernel, not customer microVM
acceptance. macOS FUSE kernel behavior remains unverified: a subsequent two-client
probe timed out after 30 seconds, and kernelmanagerd explicitly reported that
the installed macFUSE extension was not approved to load. Cleanup completed
without remaining owned processes. Linux FUSE and macOS/Linux NFS have real
kernel coverage. CI is configured for four/eight-client native runs; the results
above are local acceptance evidence, and remote CI is a separate check-in gate.

### Check-in locking regressions

Review found that the FUSE bridge discarded the lock owner supplied with a
close request. A lock acquired through one descriptor could survive closing
another descriptor for the same inode; closing a shared handle could also
release another owner's record locks. Raw-protocol regressions reproduced both
failures. The bridge now exposes optional owner-aware flush interfaces while
preserving the original interfaces. AFS releases only the supplied owner's
record locks, including when that owner never locked through the closing handle.

A blocking lock acquisition also counted its sleeping retry loop as an active
storage request. This could prevent flush, fsync or flock close from draining.
The session now admits each actual acquisition attempt separately. Sleeping
waiters do not hold up a barrier; resumed attempts still check admission,
workspace generation and the session lease. Regressions cover barrier progress,
paused admission, cancellation, session close, restore fencing and flock release.
The real-kernel lock scenario checks both closing an untouched descriptor and
fsync/last-descriptor close while a flock waiter is blocked.

The [final check-in report](../tests/multiwriter/artifacts/checkin-20260915-final/report.json)
records passing build/vet, 645 unit and 645 race test/subtest cases with real
Array enabled and zero failures/skips, 109 CLI process cases, 14 vendored
regressions and 16 harness tests. Source hashes remained unchanged throughout
that verification. The subsequent two-client
[Linux FUSE lock run](../tests/multiwriter/artifacts/checkin-20260915-final/native-20260915T195646Z-9afe7473c3204717b747d69c343038f2/report.json)
passed the strengthened lock scenario on rebuilt binaries, including cold
observation. Its supervisor recorded no timeout, cleanup errors or remaining
owned processes. This check-in evidence supplements the earlier full matrix;
remote CI runs the full core and four/eight-client native workloads again.

## Earlier lab findings — before native integration

The eight-writer run described below had a conflict-publication failure: a
candidate survived only as a local conflict file on its originating client,
with empty queues and no sync-state entry. These results describe that earlier
snapshot and explain the subsequent regressions.

## What the tests found

Every fix below has a focused regression that reproduced the previous failure.
The lab exercises the public CLI and ordinary mounted-file operations in
addition to these narrower tests.

| Failure | Customer impact | Change |
| --- | --- | --- |
| Remote notification queue filled; fallback used the same full queue. | A client could miss files permanently while reporting an empty queue. | Send recovery through the separate full-scan/root-replacement request channels. |
| An upload failed with EOF and no later event woke it. | A file created while disconnected could remain only on that microVM. | Request the existing recovery loop; failed scans use its delayed retries. |
| Warm recovery created conflict copies before watcher installation. | The recovered microVM retained the version, but peers and Redis never received it. | Request a follow-up scan when recovery creates a conflict copy. |
| A stale rename accepted a newer remote source. | Recovery could overwrite a peer's published source edit with stale renamed bytes. | Compare source contents with the saved baseline before a conditional rename. |
| Concurrent symlink create/retarget and warm recovery lacked conflict preservation. | Competing targets could disappear. | Compare the baseline, conditionally remove only the observed inode, and use the staged conflict-preserving downloader. |
| Saving a symlink with only a permission difference removed/recreated it. | Other clients could observe and act on a false deletion. | Keep the inode and apply the existing permission update. |
| Symlink recovery omitted mode/size from scans and rejected a checked missing destination. | Safe delete/retarget recovery repeatedly rejected its own plan or left the local link missing. | Retain comparison metadata and honor the plan's expected absence during download. |
| Completed deletion retained an outbound deletion marker. | An identical file or symlink recreated by a peer could be ignored and subsequently deleted from Redis. | Clear completed deletion baselines, with version and root guards; keep delayed results from touching newer edits. Inbound and outbound completion require separate coverage. |

The focused regressions are in `cmd/afs/sync_remote_overflow_test.go`,
`sync_recovery_publication_test.go`, `sync_rename_source_conflict_test.go`,
`sync_symlink_multiwriter_test.go`, `sync_save_symlink_mode_test.go`, and
`sync_symlink_delete_recovery_test.go`, and `sync_remote_recreate_test.go`.

## Test interpretation

The oracle comes from intended bytes and symlink targets, not from rereading a
path after another writer might have changed it. A run requires expected files
and every competing candidate to survive on every client, after successful
flushes, and on a newly mounted observer. Identical missing data on all clients
does not count as convergence. Disjoint workloads also reject unexpected files.

Initial experiments used short 15-second deadlines. Focused regressions showed
that the abandoned-upload and warm-conflict failures had no pending recovery;
waiting longer alone could not fix them. Four-client reruns of disjoint bursts,
partition recovery and crash recovery passed after the changes.

Docker Desktop's Mac bind mount failed an independent exclusive-lock probe:
a second process acquired an already-held lock. The Linux runner therefore uses
native tmpfs for all workload files and copies evidence to the host afterward.
This is an environment correction, not a change to AFS locking.

## Verification

Retained JSON/Markdown reports record the exact binary hash, platform, Redis
version, settings, phase timings, operation ledger, expected versions, manifests,
status and cleanup outcomes.

### Earlier repository checks

These checks passed on the earlier pre-native snapshot:

- `go build ./...` and `go vet ./...`.
- Unit: 441 test/subtest passes, zero failures, three optional Array skips.
- Race detector: 441 passes, zero failures, the same three skips.
- Existing real-Redis CLI/process suite: 109 passes, zero failures or skips.
- Harness oracle/cleanup self-tests: four passes.
- Python compilation and `git diff --check`.

Logs: `/private/tmp/afs-final-{build,vet,unit,race,e2e,harness}.log`.
These checks do not turn the separate eight-writer workload failure into a pass.

### Earlier eight-writer run: nine passes, one publication failure

Artifacts: `/private/tmp/afs-lab-final-eight`. Native ARM Python 3.9.6,
Redis 8.6.2, eight initial writers, five files per writer, two rounds, seed 17,
1 ms delay per proxy read in each direction, and a 90-second assertion deadline.
Final binary SHA-256:
`9b1d02c0aa462daa0f94c52305ce6681d1ffcad579383dbf9b0f2c5b77aebb9c`.

| Scenario | Result | Total seconds |
| --- | --- | ---: |
| Populated workspace and edits | Pass | 222.71 |
| Disjoint create/delete/rename/edit | Pass | 164.84 |
| Shared creation | **Fail: unpublished conflict copy** | 110.62 |
| Shared edits | Pass | 55.97 |
| Chunked shared files | Pass | 163.80 |
| Identical delete/recreate | Pass | 21.93 |
| Network partition | Pass | 17.24 |
| Process crash and warm recovery | Pass | 18.62 |
| Rename/delete races | Pass | 37.82 |
| Symlink races and warm recovery | Pass | 32.62 |

All eight candidates survived the first shared-creation round. In the second,
client 0 retained all eight versions, but clients 1–7 retained only seven. The
missing 256-byte version exists only at
`shared-create/client-0/workspace/shared-1.bin.conflict-MacBook-Air-4.local-20260915T031243.361-1`.
Its SHA-256 matches the independent oracle in `shared-create/candidates-1.json`:
`0e7ecda7436d0d10c7fabf8492bce1cf695d52c6898a821cdc4fdcb6dca73bc8`.
It is absent from every sync-state. All clients reported connected with zero
queued operations and zero tracked uploads; activity stopped well before timeout.

**Finding at that snapshot:** concurrent conflict handling preserved this version without
reliably arranging its publication. The client log records a rejected concurrent
upload shortly before the conflict copy appeared. Logs cannot identify which
conflict-handling route created it or the exact missed scheduling point. Whether the failure predated that patch was not isolated.
The failed case stopped before its final checkpoint/cold-observer checks, so the
evidence establishes missing peer publication, not loss of the local bytes.

Rerun this workload independently (interleavings can differ):

```sh
python3 scripts/multiwriter_lab.py --clients 8 --files 5 --rounds 2 \
  --seed 17 --latency-ms 1 --timeout 90 --scenario shared-create
```

The two disjoint creation batches took 8.51 and 9.64 seconds. Total scenario times
include one serialized checkpoint per client: those calls consumed 126.46 seconds
of hydration and 93.25 seconds of disjoint testing. In the same run, median
checkpoint time rose from 0.52 seconds for one file to about 11.4–11.7 seconds
for 41–48 files plus directories. Save verification performs two uncached remote
scans with serial metadata/content reads. There is no fixed ten-second drain
delay in the code. Exact scan-versus-drain timings and the longer outliers remain
unmeasured. These totals must not be presented as file-propagation latency.

The runner returned failure as intended and retained all evidence. No capture
errors were reported. Shared-create client 0 exceeded the five-second process
wait after SIGKILL. A later process check still showed that owned PID 46700 in an
OS exit state, reparented to PID 1; cleanup of that process is unconfirmed.
The remaining cases reported no cleanup errors.

### Four writers, 20 files per writer, three rounds

The run in `/private/tmp/afs-lab-verified-four` used seed 1, no added proxy delay,
a 90-second deadline per assertion, Redis 8.6.2, and binary SHA-256
`e1617ed5307982650fa4ff9a8b6dc0d70e413d88972471c5d389ef7c7fb4c908`.
This was after the inbound-delete fix and before the outbound-delete fix.

| Scenario | Result | Total seconds |
| --- | --- | ---: |
| Populated workspace and edits | Timed out during edits | 107.56 |
| Disjoint create/delete/rename/edit | Timed out after first creation batch | 138.78 |
| Shared creation | Pass | 18.26 |
| Shared edits | Pass | 21.46 |
| Chunked shared files | Pass | 65.73 |
| Identical delete/recreate | Failed; exposed remaining outbound deletion bug | 95.43 |
| Network partition | Pass | 9.08 |
| Process crash and warm recovery | Pass | 7.62 |
| Rename/delete races | Pass | 17.26 |
| Symlink races and warm recovery | Pass | 14.75 |

Initial hydration passed: all four mounts took 6.47 seconds. After the first
80 edits, clients still had 16–22 queued operations at the deadline; each owner's
edits were correct, but peers retained 4–5 old versions. Logs showed 225–232
downloads per client for 60 remote edits, suggesting duplicate invalidation work
is worth profiling. This does not yet isolate its performance cost.

The first disjoint batch published 80 files in 41.24 seconds (1.94 files/second,
one sample). At the later timeout, all 52 required remaining files matched on all
four clients; obsolete rename-source paths remained, with uploads and queued
operations still active. These are unresolved convergence-performance failures.
Eventual completion was not verified. They must not be reported as passes or as
demonstrated permanent data loss.

The default Python was x86_64 under Rosetta on an ARM Mac. Python verification,
proxy scheduling, local persistence, and host load all affect these numbers.
Docker also stopped responding during this session. No controlled experiment
established which factor dominated the slowdown.

A follow-up using native ARM Python, the same binary and a single 80-edit
hydration round passed in 68.52 seconds (`/private/tmp/afs-lab-native-python`).
The edit-convergence assertion took 30.01 seconds; a cold observer matched after
flush. This establishes completion for that rerun, not for the failed run or all
three rounds. Interpreter and host conditions were not independently controlled.

### Delete/recreate after the final correction

The four-client public case passed in 9.53 seconds with native ARM Python:
`/private/tmp/afs-lab-outbound-recreate-fixed-native`. It uses final binary SHA-256
`9b1d02c0aa462daa0f94c52305ce6681d1ffcad579383dbf9b0f2c5b77aebb9c`.
The new outbound file/symlink regression matrix failed in all four combinations
before the correction and passed afterward. Race checks also cover an
acknowledgment arriving after remote recreation, a local write, or a newer delete.

### Linux environment verification

The corrected native-tmpfs Docker runner executed earlier in this session and
exported reports even when the test returned failure. Five cases passed and
three reproduced recovery/symlink failures in the intermediate binary. Artifacts:
`/private/tmp/afs-lab-linux/run-20260915T022610Z-2a561d6eb91f400c978f65e8f202f243`.

The final image build stalled and Docker Engine stopped responding. No Linux
workload ran against the final source. A bounded graceful Docker Desktop stop
also timed out with processes remaining; no forced shutdown or data deletion was
performed. The lab-owned build client exited. Logs are retained in
`/private/tmp/afs-multiwriter-docker-approved-build.log` and
`/private/tmp/afs-multiwriter-docker-stop.log`. The Linux runner is available;
validation of the final fixes on Linux remains outstanding.

## Limits

These tests run separate processes on one kernel; the Linux variant uses one
container. They do not measure guest boot or resource isolation, loss of an
ephemeral guest disk, production network behavior, Redis failover, or the
customer's actual workload. Application saves use atomic replacement. Timing
includes Python verification and a local Redis with `appendfsync always`; Linux
tmpfs results are not disk-durability or production-capacity measurements.

Inbound deletion holds the sync-state mutex while checking a subtree and
removing it. This prevents a concurrent scan from turning that removal into a
new outgoing delete. Large subtree deletion latency and contention need separate
profiling; these runs do not establish a bound for that operation.

Same-path writes intentionally create conflict versions for the application to
resolve. They do not merge documents or provide a distributed filesystem
transaction. Repeat these invariants in actual microVMs before making a customer
deployment or capacity claim.
