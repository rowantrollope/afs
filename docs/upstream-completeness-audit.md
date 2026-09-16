# Upstream completeness audit

This report records the original `ac31fda` audit. A subsequent user-authorized
change restores findings 1–3 and fixes live directory chmod. See the
[implementation and validation results](audit-followup-results.md), current
[README](../README.md), [verified-save assessment](verified-save-value.md) and
[file-history implementation analysis](file-history-design.md). Historical
reproductions and results below describe the audited revision.

Reviewed 2026-09-16. AFS preserves the principal filesystem, synchronization and
checkpoint machinery, and adds several meaningful safety improvements. However,
it is not yet a complete subset of the original's applicable capabilities: one
import optimization was lost, read-only mounting is no longer reachable, and
FUSE ownership/access options from an upstream fix were not carried through.
Standalone save and optional per-file history also deserve explicit scope decisions.

This is a source and behavioral audit, not a production change. No installed
binary, original checkout, existing Redis dataset or OS mount was changed.

## Comparison boundary

| Repository | Audited revision | Published HEAD at audit start |
| --- | --- | --- |
| redis/agent-filesystem | [`c3897ac05265444568a3819c21728a38a4ed254b`](https://github.com/redis/agent-filesystem/commit/c3897ac05265444568a3819c21728a38a4ed254b) | Same |
| rowantrollope/afs | [`ac31fda403bcc1bfd0733968954dc1679f603d76`](https://github.com/rowantrollope/afs/commit/ac31fda403bcc1bfd0733968954dc1679f603d76) | Same |

Upstream HEAD is still the original extraction baseline. There are **no newer
upstream commits to catch up with** at this boundary. The question is what the
extraction and subsequent native integration omitted or changed.

Four parallel reviews inspected CLI/lifecycle, folder sync, storage/checkpoints,
and native adapters/dependencies. Comparisons normalized module names and traced
production callers, rather than assuming a retained function remained usable.
The extraction brief, later decisions and lessons define intentional exclusions.

## Findings and recommended action

### 1. Restore the import optimization or document a measured replacement — P2

**Confirmed extraction regression in efficiency, not a demonstrated corruption bug.**

Original import retains file buffers and initializes the live tree with
`BlobProvider: sink.Get` and `SkipNamespaceReset: true`. AFS retains these options
but its actual `create --from` path never supplies them. After uploading the blobs,
it downloads each non-inline file again, serially, to initialize the live tree.
Repeated content is re-read per path despite deduplicated upload. It also scans an
empty namespace even though the workspace ID was newly allocated.

Sources: [original import wiring](https://github.com/redis/agent-filesystem/blob/c3897ac05265444568a3819c21728a38a4ed254b/cmd/afs/afs_commands.go#L325),
[AFS caller](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/internal/controlplane/service.go#L100),
[retained provider/fallback](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/internal/controlplane/workspace_root.go#L382).

A disposable command-count probe imported **64 identical 64-KiB files**:

| Path exercised | Blob GETs | Namespace SCANs |
| --- | ---: | ---: |
| Actual AFS streaming-create path | 64 | 5 |
| Retained materializer with upstream options | 0 | 0 |

That is 4 MiB of avoidable blob downloads in this small fixture. The probe measures
commands, not WAN performance. Extra metadata lookups also occur while resolving
each blob. The cost grows with file count and Redis latency.

The original cache held the unique blob corpus in memory. Restoring it verbatim
has a memory cost; bounded caching/prefetch is another option. Keep that tradeoff
explicit. Add an import-path regression: the surviving option unit tests pass
while the production caller remains disconnected. The upstream composed import
test in `internal/controlplane/import_pipeline_test.go` was removed.

### 2. Restore read-only mount wiring across sync, FUSE and NFS — P2

**Confirmed lost filesystem capability; no explicit scope exclusion found.**

Upstream supports `vol mount --readonly`. The engine and native adapters still
support read-only operation in AFS, but the public parser accepts only backend and
foreground options. The sync daemon constructor and native bootstrap leave their
read-only fields false. There is no equivalent config setting.

Sources: [original flag](https://github.com/redis/agent-filesystem/blob/c3897ac05265444568a3819c21728a38a4ed254b/cmd/afs/mount_commands.go#L95),
[AFS parser](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/cmd/afs/sync_lifecycle.go#L50),
[sync constructor](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/cmd/afs/sync_lifecycle.go#L304),
[native bootstrap](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/cmd/afs/native_lifecycle.go#L218).

A fresh AFS binary rejects `mount sample DIR --readonly` with
`unknown option --readonly`, before connecting to Redis. An observer or reader
agent therefore cannot request this behavior through the supported CLI.

Restore the thin flag/config/registry/bootstrap wiring and test the full route.
Handle checkpoint/unmount explicitly: the inherited sync save engine rejects
saving a read-only mount. Native read-only denies filesystem writes; sync read-only
blocks publication and adjusts local modes. Neither should be described as a
replacement for server-side authorization.

### 3. Carry through FUSE ownership and access options — P2

**An upstream fix's capability survives in the adapter but is unavailable to users.**

Upstream [PR #25 / commit 7da1215](https://github.com/redis/agent-filesystem/commit/7da121505954727b2890e055e44fb07826ca5ba7)
added `--uid` and `--gid` so a privileged mount process can present files as the
non-root workload user. Its helper also supports `--allow-other`.

AFS always calls `GetOwnership()` and leaves `AllowOther` false. The lower FUSE
adapter still has these options, but the public command/helper bootstrap cannot
select them. Fresh-binary probes reject all three flags before Redis access.
This matters for agents or containers running as another local user even when
the original CSI/product framework is deliberately excluded.

Sources: [upstream helper options](https://github.com/redis/agent-filesystem/blob/c3897ac05265444568a3819c21728a38a4ed254b/mount/cmd/agent-filesystem-mount/main.go#L35),
[AFS fixed ownership](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/mount/native/session.go#L119).

Restore explicit FUSE options and propagate them through authenticated startup
state. Verify a privileged helper serving a non-root workload in isolated Linux
kernel acceptance; CLI/adapter tests alone cannot establish that behavior.

### 4. Decide whether to restore standalone save and its timeout/receipt — P2/P3

**Documented CLI reduction with a real operational capability cost.**

Upstream `vol save --timeout 10m --json DIR` verifies publication while leaving
the mount running and without creating a checkpoint. AFS preserves the engine,
but exposes it only through checkpoint creation and unmount. It fixes each flush
at two minutes and discards the save receipt at those public call sites.

Sources: [upstream parser and result](https://github.com/redis/agent-filesystem/blob/c3897ac05265444568a3819c21728a38a4ed254b/cmd/afs/sync_save_command.go#L22),
[AFS fixed timeout](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/cmd/afs/sync_save_command.go#L11),
[flush caller](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/cmd/afs/sync_lifecycle.go#L600).

Fresh-binary probes confirm `save` and `cp create ... --timeout 10m` reject.
Automations must create checkpoint history or disconnect to obtain a public
completion boundary, and cannot extend the flush deadline for large/slow trees.
Status queue counts are not equivalent to a verification receipt.

A root-level `save DIR [--timeout ...]` could reuse the existing engine without
restoring the removed `vol` or `fs` groups. This is a product recommendation,
not evidence that checkpoint/unmount lost their flush safety.

### 5. Explicitly decide the scope of automatic per-file recovery — P3 decision

**Substantial opt-in feature removed; not a regression in upstream default safety.**

Upstream offers automatic file history, rename lineage, historical restore,
undelete, and retention by count, age and bytes. Policy can track all or selected
paths. Its default is **off**. Those implementations, policies and their focused
tests are absent from AFS; a retained observer interface has no production recorder.

Sources: [upstream default/policy](https://github.com/redis/agent-filesystem/blob/c3897ac05265444568a3819c21728a38a4ed254b/internal/controlplane/versioning_policy.go#L10),
[restore/undelete](https://github.com/redis/agent-filesystem/blob/c3897ac05265444568a3819c21728a38a4ed254b/internal/controlplane/file_version_actions.go),
[AFS constructors with nil observer](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/mount/internal/client/client.go#L167).

Five unchanged upstream tests passed during this audit: default-off policy,
undelete, historical restore across rename, count retention, and independent
fork history/policy. The functionality was operational, not merely declared.

AFS preserves explicit checkpoints, safety checkpoints and conflict copies.
Those do not retain an ordinary sequential overwrite or deletion between
checkpoints. Removing public rich file commands does not automatically settle
whether that recovery layer should be removed. Either restore a small opt-in
history capability or document its exclusion explicitly in the product scope.

## Existing robustness issues, separated from omissions

**Live directory chmod is broken in both versions.** A deterministic probe
settled a directory rename, changed mode from `0755` to `0750`, and delivered the
watcher event. Neither version queued an upload; Redis remained `0755`. A forced
warm reconciliation repaired it to `0750` in both versions.

Both handlers return immediately for an already-tracked directory without
checking its mode: [AFS](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/cmd/afs/sync_event_reconciler.go#L730),
[upstream](https://github.com/redis/agent-filesystem/blob/c3897ac05265444568a3819c21728a38a4ed254b/cmd/afs/sync_event_reconciler.go#L998).
This explains the existing two-system harness finding and merits a focused fix,
but it is **inherited**, not something extraction left out. A fix must cover
both event scheduling and mode-sensitive echo suppression.

The intermittent rename/delete candidate-preservation failure already recorded
in `tasks/todo.md` remains unresolved. This audit did not establish its cause or
classify it as an extraction regression. A passing standard suite does not close it.

## What was retained

| Area | Audit result |
| --- | --- |
| Recent upstream correctness changes | Chunk upload fix #28, watcher-overflow/recovery fixes #29, and save/drain/verification core from #30 are retained. History-specific parts of #30 belong to finding 5. |
| Folder watcher and recovery | Recursive watches, bounded/configurable queues, separate overflow signaling, warm baselines, tombstones, conflict copies and worker draining remain. Several files are identical after import-name normalization. |
| File types and metadata | Regular/binary/empty files, symlinks, permission modes and directory-mode restoration remain. The live chmod defect above is inherited. |
| Scanning and chunking | Parallel local hashing, breadth-first pipelined Redis traversal and chunk-delta machinery remain. Import caller optimization is the specific exception in finding 1. |
| Checkpoints and forks | Immutable manifests/blobs, initial checkpoint, independent fork ownership and pre-restore safety checkpoint remain. AFS adds guarded checkpoint deletion. |
| Redis content | String/range/chunk and optional Array implementations remain. Array search probing was removed with search. Auth, database selection, TLS and redaction remain. |
| Native filesystems | FUSE/NFS adapters, cache, range I/O and NFS vendor optimizations remain. Session leases, inode identity, lock handling and reconnect invalidation are strengthened. |
| Publication safety | AFS adds staged atomic publication, conditional writes, atomic content/journal commits, lost-ack protection and generation fencing across restore/delete. These improve on the reviewed upstream baseline. |
| Local lifecycle | Private per-mount state, authenticated control, startup readiness, root identity/ownership checks, honest failed-flush behavior and stale-client rejection remain or improve. |

No other demonstrated extraction-related data-corruption defect was found in
the inspected implementation. This is an audit result, not a proof of all
possible concurrent workloads.

## Intentional cuts and smaller gaps

- Cloud/account services, web UI, MCP, SDK/framework integrations, search, public
  volumes, workspace composition and rich remote file commands match explicit
  exclusions. They should not be counted as accidental regressions.
- First-mount preview/approved union of unrelated populated trees is removed;
  AFS intentionally rejects those cases. The planner survives, but that workflow
  is narrower. Internal create-exclusive/undelete control operations disappear
  with the rich file surface; local filesystem calls on independent sync copies
  do not provide a cross-client exclusive-create operation.
- Native NFS uses private loopback exports intentionally. AppleDouble sidecars
  now persist as ordinary Redis files instead of RAM-only shadows, as documented
  in `native-mounts.md`; restoring the old shadow would reduce durability.
- FUSE cache/debug tuning and NFS operation statistics were removed. NFS startup
  cache prewarming was also dropped. The shorter metadata TTL is a documented
  stale-cache safety tradeoff. No WAN/large-tree regression benchmark was run;
  these are performance/diagnostic follow-ups, not proven corruption findings.
- The original Array root-materialization test was removed. General materializer
  and Array client tests remain, but that exact combined coverage should return
  if Array import/restore completeness is a supported promise. Do not copy the
  old fixture's destructive Redis setup against an existing server.
- Full-remote-file reads for some chunk comparisons, in-memory checkpoint
  construction, lack of distributed snapshots and several POSIX limitations
  already exist upstream. They are not newly lost optimizations or guarantees.

## Fresh verification and limits

Platform: macOS arm64, Go 1.26.1, disposable Redis 7.2.5 and miniredis fixtures.
All compared CLI binaries were built from the audited source, with private test
configuration/state and test-owned Redis processes.

| Check | Result |
| --- | --- |
| `go build ./...`, `go vet ./...` | Pass |
| `go test -count=1 -json ./...` | 665 test/subtest passes; 4 optional Array test skips |
| `go test -race -count=1 -json ./...` | Final full run: 665 passes; same 4 skips; no race report |
| `go test -tags=integration -timeout=15m -count=1 -json ./tests/e2e` | 113 passes, no skips; 30.894 seconds |
| Original/current `tests/compat`, with freshly built upstream binary | 12 passes, no skips; 5.394 seconds |
| Native adapter/helper/mountcontrol race tests | Pass |
| Vendored NFS race tests and portable FUSE mount-context race regressions | Pass |
| Upstream history/policy behavioral checks | 5 focused tests pass |
| Import command-count probe | Confirms lost production optimization |
| Directory chmod regression | Expected failure on both versions; warm scan repairs both |

The first full race run failed because its own Redis process could not bind a
selected port (`Address already in use`). It did not reach the affected assertion
and did not report a data race. The subsequent complete race run passed. The
initial failure is retained in the evidence, not counted as a pass.

The four Array tests were skipped because no dedicated disposable Array-capable
server was configured. No new Linux/macOS kernel mount acceptance, customer
microVM run, two-physical-system run or remote-latency benchmark was performed.
Existing checked-in reports describe earlier kernel/lab results; those are
separate from this audit's fresh evidence.

Local raw logs and binaries: `/private/tmp/afs-audit-results-20260916`.
Focused reports: `/private/tmp/afs-sync-audit.md`,
`/private/tmp/afs-storage-audit.md`, `/private/tmp/afs-native-audit.md`.
Reproduction sources/overlays: `/private/tmp/afs-storage-audit-probe_test.go`,
`/private/tmp/afs-storage-audit-overlay.json`,
`/private/tmp/afs-directory-chmod-audit_test.go`,
`/private/tmp/afs-chmod-slim-overlay.json`,
`/private/tmp/afs-chmod-upstream-overlay.json`.
These temporary artifacts are local evidence, not a permanent CI archive.

Recommended sequence: resolve the import regression and missing mount options;
fix the inherited live directory-mode defect; decide standalone save and automatic
history scope; then add acceptance that exercises those production paths and
continue the already-open multi-system reliability work.
