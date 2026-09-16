# macOS / Sancho sync run: evaluation

This records the original run before fixes. The follow-up changes and local
validation are recorded at the end; cross-system acceptance still needs a rerun.

Run `bdcfc93bc899` completed all 12 scenarios: **4 passed, 8 failed**. The failed
scenarios group into two confirmed functional problems, one unresolved backlog
or stall, and two scenarios blocked by Redis memory pressure. The SSH setup and
coordination worked. Both runners reported no owned-process cleanup errors.

The passing scenarios are `ignore`, `shared-create`, `delete-edit`, and
`rename-edit`. Each passed exact expected-tree validation, checkpoint flushes,
fresh observers on both systems, and normal unmount.

## Run and evidence

- System A: Sancho, Ubuntu/Linux x86-64; AFS `15ef038`.
- System B: macOS 15.8 arm64; AFS `ac31fda`.
- These commits have identical production Go code. `ac31fda` adds the runner,
  tests, and documentation; the differing version labels do not explain failures.
- Settings: 100 files per writer per bulk round, three rounds, 8 MiB large-file
  fixture, seed 1, two-second stable observation, 120-second per-phase timeout.
- Runtime: approximately ten minutes. All scenarios were attempted, but failed
  scenarios stopped before their later phases; this is not full coverage of every
  planned operation.
- Local source artifacts:
  `/var/folders/kg/0cp9s_gx4v5bjkc6bkmcx3f40000gn/T/afs-two-host-_hmvcg7o`.
- Sancho source artifacts: `/tmp/afs-two-host-tsax4vvc`.
- Credential-free reports, expected trees, comparisons, and follow-up Redis
  metadata are retained locally under
  `tests/multiwriter/artifacts/two-system-bdcfc93bc899-review/` (gitignored).

Evaluation used both reports, both systems' logs and saved state, the source code,
and read-only Redis INFO/HGET/HMGET queries. It did not rerun workloads, change
Redis configuration, delete test workspaces, or change production code.

## Scenario outcomes

| Scenario | Result | Evidence and interpretation |
|---|---|---|
| basic | Fail | All 30 entries matched on both writers and both fresh observers; Sancho's fresh observer then failed normal unmount on a symlink. |
| mutations | Fail | Rename/delete, identical recreation, append and file truncation/mode checks reached expected contents; each peer's renamed directory stayed 0755 instead of 0750 for 120 seconds. |
| bulk | Fail | Initial 200-file creation passed. The first mutation batch left 23 discrepancies on the Mac and unfinished work on Sancho at the deadline. Rounds 1–2 and fresh-observer checks were not reached. |
| large | Fail | Both initial 8 MiB files arrived intact. Chunk-boundary edits were rejected with Redis OOM errors. Peers retained the exact initial file hashes. Shrink, zero-length and regrowth phases were not reached. |
| ignore | Pass | Local ignores survived warm restart; only intended public files appeared in fresh observers. |
| shared-create | Pass | All three rounds preserved both competing versions on both systems, including fresh observers. |
| shared-edit | Fail | The first large-file conflict preserved both versions. The next baseline checkpoint was rejected by Redis with OOM; later rounds were not reached. |
| symlink-conflict | Fail | All six intended symlink versions survived three rounds, flushes and fresh hydration; Sancho's fresh observer failed normal unmount. |
| delete-edit | Pass | Unpublished edited content survived the peer's deletion and all final checks. |
| rename-edit | Pass | Required original and edited versions survived the rename and all final checks. |
| partition | Fail | Offline edits, peer edits, new files and both symlink targets recovered, flushed and hydrated correctly; Sancho's fresh observer failed normal unmount. |
| crash | Fail | Warm recovery preserved all intended file/symlink versions, flushes and fresh hydration matched; Sancho's fresh observer failed normal unmount. |

## 1. Cross-platform symlink save/unmount incompatibility

**Confirmed functional failure; mode handling is the strongly supported cause.**
This explains the common failure pattern in `basic`, `symlink-conflict`,
`partition`, and `crash`.

The normal unmount errors are all on Sancho's fresh observer (`cold-a`):

```text
afs: shutdown failed: save conflict at <symlink>: Redis differs from the stored baseline
```

The targets are correct and identical on both hosts. Retained metadata shows:

| Property | Sancho/Linux | Local/macOS | Redis |
|---|---:|---:|---:|
| Actual symlink mode | 0777 | 0755 | 0755 |
| Fresh observer's saved mode | 0 (interpreted by save as 0777) | 0755 after successful unmount | — |

Read-only Redis checks confirmed mode 493 (0755) for `a/directory-link`,
`shared-0`, and both recovery scenarios' `pointer` entries. The four error paths
include the same kinds of links, including a recovered conflict symlink.

[`planSyncSave`](../cmd/afs/sync_save_engine.go) compares the remote mode with both
local intent and the saved baseline. The symlink fallback baseline is 0777 when
its saved mode is zero. In this state, remote 0755 differs from both Linux's local
0777 and its interpreted baseline, producing the observed error despite correct
targets. The Mac's completed saves preserve its local 0755 mode in Redis.

This is a portability/baseline bug, with no missing target demonstrated in these
four scenarios. The partition/crash recovery stages themselves passed. Their
complete scenarios still fail because a normal detach must succeed safely.
Forced cleanup succeeded afterward; that does not convert unmount into a pass.

Next: establish portable symlink-mode semantics and retain the relevant remote
baseline. Reproduce with a focused mixed-mode save regression and then run an
actual Linux/macOS observer pair. Keep strict mode checks for regular files and
directories.

## 2. Live directory chmod does not propagate

**Confirmed functional failure in both directions.** `a/moved` remained 0755 on
the Mac while Sancho had changed it to 0750; `b/moved` showed the reciprocal
mismatch. The final comparison contained exactly one directory-mode mismatch per
host. File bytes, file modes, and the other paths matched.

The local-only acceptance runs reproduced the same issue before this cross-host
run. Source review identifies a direct cause in
[`handleLocalDir`](../cmd/afs/sync_event_reconciler.go): it returns immediately for
an already tracked directory without comparing its stored and current mode.
The existing uploader already has a chmod operation, but this event path does
not enqueue it. The defect may extend to any existing directory chmod; the
measured fixture changes permissions after a rename.

Next: add focused existing-directory and renamed-directory chmod regressions,
then route mode changes through guarded publication while preserving echo
suppression and recovery behavior.

## 3. Bulk mutation backlog or stall

**Confirmed failure to converge within 120 seconds; root cause unresolved.**
Initial creation of 100 files per host passed in about 18 seconds. After the
first concurrent mutation batch, Sancho's local tree matched the oracle, while
the Mac had:

- 7 files still containing their exact initial bytes;
- 7 missing rename destinations;
- 9 obsolete paths, including old rename sources and files intended for deletion.

Sancho's final status reported **775 queued events and 77 tracked uploads**.
The source log had a roughly 95-second gap near the end before a final upload
and cancellation during cleanup. The Mac logged one full-reconcile `already
exists` failure. Neither host's bulk log showed OOM errors.

The evidence supports unfinished or stalled propagation under real network
conditions. It does not establish permanent event loss, byte corruption, or
that merely increasing the timeout would solve it. Sancho retained the intended
local result when the test stopped. Forced cleanup means publication was not
certified for this failed scenario.

Next: reproduce `bulk` alone with memory headroom and periodic per-host queue,
progress and latency samples. Diagnose queue growth and stalls before changing
the acceptance deadline. Preserve the original 120-second failure as evidence.

## 4. Redis memory pressure blocks large writes and checkpoints

**Confirmed environmental capacity failure affecting `large` and `shared-edit`.**
Both hosts repeatedly logged:

```text
OOM command not allowed when used memory > 'maxmemory'
```

The large-file peers retained the exact initial hashes, while intended edited
bytes remained on their originating writers. This is a rejected-publication
result; this run does not demonstrate mixed/corrupted chunk contents. Status
later reported `staged file expired before publication`, and recovery retries
also encountered OOM/EXECABORT errors.

A read-only server snapshot after the run reported:

| Metric | Observation |
|---|---|
| Current used memory | 28.93 MiB |
| Peak used memory | 39.33 MiB |
| Eviction policy | volatile-lru |
| Evicted keys | 370 |
| Rejected connections | 0 |

The service did not return `maxmemory` in this INFO response, so these values do
not establish the configured capacity. The eviction count is cumulative, with
no pre-run baseline; it cannot all be attributed to this test. `errorstats` did
not provide a usable per-error breakdown. The recorded command errors remain
the evidence for OOM.

AFS stages publication in temporary keys with a one-hour TTL. The observed
volatile eviction policy makes those keys eligible for memory-pressure eviction;
that is a plausible explanation for missing staging keys within a two-minute
phase, but this run did not trace specific eviction events.

The harness retains workspace/checkpoint data across scenarios and adds staging
and snapshot allocations beyond the visible file sizes. It currently has no
capacity preflight or resource samples during a scenario. That is a harness
limitation worth fixing; all eight failures should not be classified as eight
independent sync defects.

Next: establish sufficient memory headroom before rerunning these cases, review
the database eviction policy for persistent workspace storage, and record memory
and error counters at run start and on failure. A smaller selected workload can
help diagnosis. Any deletion of retained test data or server configuration change
is a separate action; neither was performed during this evaluation.

## Priority and acceptance recommendation

1. Fix and prove the symlink save/unmount and live directory chmod behavior.
2. Diagnose the bulk queue growth/stall with the recorded discrepancies intact.
3. Address Redis capacity and add resource-aware harness reporting, then rerun
   the capacity-blocked cases and the complete two-host suite.

The run provides positive evidence for conflict-version preservation, ignored
file isolation, delete/edit and rename/edit handling, and content recovery after
partition/crash. It does **not** meet full two-host acceptance. No corruption or
lost candidate was demonstrated in the completed recovery validations; the
failed/unfinished publication phases still require investigation and retesting.

## Follow-up fixes

Folder-sync saves now normalize symlink modes to zero, consistent with live sync,
and leave native Redis symlink permissions unchanged. Target/type conflicts and
regular-file/directory permission checks remain enforced. Regressions exercise
legacy baselines with modes 0, 0755 and 0777 against a differing remote mode,
and reject a subsequent peer target change.

Tracked-directory chmod now schedules the existing guarded reconciliation path.
Directory echoes compare permissions, so they cannot consume an application's
later chmod. Recovery also rejects a plan if the local directory mode changed
after its scan.

A focused saturation regression reproduced a deadlock: the event worker could
block sending to a full work channel while the upload/download workers blocked
sending to full result channels that only that event worker consumes. Enqueue
is now nonblocking; overflow requests recovery, clears pending-upload counts and
removes an unpublished rename's provisional baseline. Duplicate local events
coalesce while an upload is pending, with a rescan after its acknowledgment.
Regressions verify the newest edited bytes, renamed bytes and deferred inbound
deletions survive. This is consistent with the original stall; an actual remote
rerun is still needed to establish that it resolves that observed failure.

The runner now records allowlisted INFO memory/error snapshots, counter deltas,
and case-owned OOM evidence. `capacity-blocked` remains a failing/incomplete
outcome, never a pass. Hidden/denied metrics remain unknown, and server-wide
counters alone cannot classify a test failure.

Local validation with disposable Redis passed all 12 paired-process scenarios
on both sides (100 files per writer per round, three rounds, 8 MiB fixtures,
45-second phase limit, 0.5-second stability window). Both processes exited 0 and
reported no cleanup errors. A separate 12 MiB/noeviction instance produced the
expected `capacity-blocked` large-file result and exit 1 on both sides. In both
runs, a pre-existing fixture workspace retained its metadata and cold-hydrated
bytes. These tests used a separate executable and did not alter the installed
binary or the user's remote Redis.


A follow-up stress run added 5 ms delay per proxy read on each host and allowed
180 seconds per phase. All three 100-file-per-writer creation/mutation rounds
converged (the slowest mutation took 132 seconds), then the final checkpoint hit
the CLI's separate fixed 120-second save deadline. This is retained as a failure,
not converted to a pass. `--timeout` controls the runner, not that CLI deadline.

Save verification previously serialized per-file Redis reads. It now verifies
files up to 1 MiB with the existing eight-worker limit, leaves larger files
serial, and joins all readers before applying changes or resuming sync. It still
reads and hashes every file, checks chunk metadata, and runs both preflight and
post-publication verification. The focused worker regression checks concurrency,
verified hashes, bounded reader count, error propagation and joining on failure.
A separate 402-file process regression with the same 5 ms delay completed its
checkpoint in 34.72 seconds under the unchanged CLI deadline, and fresh hydration
matched the independent source tree. Cold/writer normal unmount completed in
30.99/31.63 seconds, with exact retained local trees and no cleanup errors.
The complete delayed bulk workload has not been rerun after this save
optimization. The final normal paired run again passed all 12 scenarios on
both sides, with exit 0, no cleanup errors and unchanged pre-existing fixture
metadata and cold-hydrated bytes.
