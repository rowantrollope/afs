# Upstream audit follow-up implementation

Historical branch validation below predates integration into main. The integration
adds `afs sync --wait` / `afs sync status` and fixes the three review findings.
Current integration results are recorded in `tasks/todo.md`.

Implemented 2026-09-16 against AFS `ac31fda`, following the
[upstream completeness audit](upstream-completeness-audit.md). The user authorized
findings 1–3 and live directory chmod; standalone save and file history remain
analysis only. Changes are in the workspace; no installed binary, original
checkout or existing user Redis data was changed.

## Implemented behavior

| Area | Result |
| --- | --- |
| Import | Reuse up to 8 MiB / 2,048 entries of immutable blob payloads during fresh root initialization, skip empty-namespace scans, release memory on success/failure and retain Redis fallback over the cap. |
| Read-only mounts | `afs mount ... --readonly` reaches sync, FUSE and NFS. Sync receives changes without publishing local edits, deletions, chmods or automatic conflict checkpoints. Checkpoints skip reader-local contents and reader unmount returns no save receipt. |
| FUSE ownership/access | Restore `--uid`, `--gid` and `--allow-other`, including explicit zero ownership and backend validation. Fix ownership in SETATTR replies after chmod/truncate; the kernel previously cached root ownership despite overrides. |
| Live directory chmod | Publish tracked mode changes after rename; apply conditional remote updates and preserve peer-winning changes. Mode/inode-aware echoes distinguish application edits from downloaded changes. |
| Recovery permissions | Suppress temporary directory permissions through repeated events and queued uploads. Failed restorations retain protection and retry before a scan; replacements retire stale markers. |

Read-only regular files retain their read/execute bits while losing write bits,
including mode `0000`. Directory access remains usable by folder sync. A local
owner can still edit the local copy; receive-only mode is enforced by the sync
engine and is not Redis authorization. Warm remount rejects changing the access
mode of an existing sync directory; use a new local directory for that change.

## Focused regression evidence

The actual streaming-create service performs zero blob GETs and zero SCANs for
the repeated-content cache fixture. Overflow tests prove correct bytes through
the Redis fallback, bounded payload/entry counts, source-buffer immutability and
release after build, flush and materialization failures.

Isolated reader tests cover live/offline local edits, file types, checkpoint/fork
exclusion, remote changes, normal unmount while Redis is paused, remount and mode
masking across cold, full, live and chunked downloads. Writable controls still
create safety checkpoints and preserve equal-byte/different-mode conflicts.

Independent counterfactual probes reproduce the readonly-checkpoint leak and
temporary-permission publication when their guards are removed. Directory tests
cover queued work before/after remote observation, delayed peer chmod and file
replacement, repeated parent events, failed restoration and ancestor replacement.
The process test alternates chmods between two mounted clients after a rename;
no checkpoint, save or remount can conceal missing live propagation.

The Linux FUSE smoke starts its own Redis and three mounts, drops to UID/GID 1000,
checks write/read/chmod ownership, rejects mutations on the reader, rejects
another user on a private mount, and verifies normal unmount and cleanup. The
ownership regression failed before the adapter response fix and passes after it.
NFS read-only protocol tests accept reads/barriers and reject mutation RPCs.

## Final verification

| Check | Result |
| --- | --- |
| `go build ./...`, both executables, `go vet ./...` | Pass |
| Unit tests | 722 test/subtest passes; four optional Array skips |
| Race tests | 722 test/subtest passes; four optional Array skips |
| Isolated real-Redis CLI process suite | 116 passes |
| Prior/current CLI compatibility | 12 passes |
| Vendored native dependency race checks | Pass |
| Python acceptance harness regressions | 34 passes |
| Linux FUSE non-root/read-only/access smoke | Pass, including owned cleanup |
| Coordinated sync workload, two independent macOS processes | 12/12 scenarios pass on the final binary; both processes exit 0 |
| Mixed Linux FUSE/NFS/sync, four clients | 9/10; identical NFS reconnect failure reproduced on untouched `ac31fda` |

The coordinated workload uses 100 files per writer per round, three rounds,
8 MiB large files, seed 1, a 45-second phase deadline and a 0.5-second stability
window. It now passes the unchanged directory-chmod-after-rename assertion that
previously failed. A pre-existing fixture workspace retains its metadata and
cold-hydrated bytes; owned cleanup completes. This is two processes on one host,
not a new cross-system claim.

The mixed-native suite uses four clients, FUSE/NFS/sync/FUSE backends, four
files, two rounds, seed 17 and an unchanged 90-second case deadline. Nine cases
pass: file/permission operations, disjoint ranges, overlapping writes, append,
open-handle rename, checkpoints, helper crash recovery, generation fencing and
locks. The reconnect case fails after 90 seconds: only the NFS client cannot
discover `created-offline`; both FUSE clients and the sync client have its exact
25-byte content. A focused rerun repeats the failure.

A pristine `git archive` of pre-change `ac31fda`, built separately in Docker,
fails the same case on the same kernel with the same client and missing path.
That establishes this as a pre-existing NFS directory-discovery issue, not a
regression from these changes. All three runs have no capture/cleanup errors and
no remaining owned processes. The NFS issue remains open in `tasks/todo.md`;
the result must not be summarized as a fully passing mixed-native suite.
The [NFS follow-up](nfs-reconnect-followup.md) separates verified enumeration
failure from unproven cache hypotheses and defines the next bounded investigation.

Baseline CLI SHA-256:
`c1e85f905f7ccc2f866e49cabadd5673559c6dfac52d7710ea54365bebe1b043`;
baseline helper:
`0fba64246c73e12d7b0c41f00fb2706cc6b16c5bf98adf165df9b5adedaafbd9`.
Baseline diagnostics are in `/private/tmp/afs-followups-native-baseline-artifacts`;
the focused current diagnostics are in
`/private/tmp/afs-followups-native-reconnect-artifacts`.

The Go source/module fingerprint stayed unchanged across the full final gates:
`2f3fdd65f84b9e8c2fc4d4eeffe0dae4d32b300ebdc478da41e9841f4811668a`.

Final binary SHA-256 values:

| Platform | CLI | Native helper |
| --- | --- | --- |
| macOS ARM64 | `0289ea963711e0d87c54e42dd96d3f35d65ce2cd3fa7aa49eff294e010a2c331` | `348ce230de2bb73fd9589f661f4f76377acdd5b4502a1c681fe56f020543ed4d` |
| Linux ARM64 | `28d596a5fdab336d54baa6f26d784fd4ec42837e3a1d7c79950a51e748521aa1` | `496185ef24d042afe0cf336bdd3414f18a8b43304646bc75687bdfced392acca` |

Linux kernel checks use LinuxKit `5.15.49` and Redis `7.0.15` in an isolated
container. The primary logs are under `/private/tmp/afs-followups-validation`,
`/private/tmp/afs-followups-native-artifacts`, and
`/private/tmp/afs-followups-native-options-final2.log`. Earlier failed probes and
the first passing gate run are retained separately.

## Boundaries

No new macOS FUSE kernel acceptance, actual two-host/WAN run, microVM benchmark,
or Array-capable server acceptance is claimed. The import cache's command-count
improvement is measured; it is not a WAN throughput benchmark.

Temporary directory restoration guards are in memory. Persisting a recovery
journal across abrupt process death would be separate hardening; this inherited
limitation is not resolved by the live-mode fix.

Standalone-save value and proposed scope are in
[verified-save assessment](verified-save-value.md). The implementation stages,
publication guarantees, native-write costs, fork/restore compatibility and
retention decisions for optional history are in the
[file-history analysis](file-history-design.md).
