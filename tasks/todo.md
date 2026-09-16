# AFS extraction

## Friendly Redis connection errors

- [x] Replace raw connection details with a friendly endpoint and --redis example.
- [x] Share the message with sync startup and retain credential redaction.
- [x] Verify exact CLI output in both modes and rebuild the installed binary.

Build, vet, unit/race and focused isolated CLI tests pass, including credential
redaction and the exact friendly error in normal and JSON modes.

## Quiet Redis connection failures

- [x] Reproduce extra Redis retry diagnostics through the installed CLI.
- [x] Disable internal Redis logging at CLI startup; preserve the final AFS error.
- [x] Pass build, vet, unit/race and isolated process checks; rebuild bin/afs.

Regression asserts one stderr line, empty stdout and exit 1 in text and JSON modes.
It fails on the prior binary and passes on the rebuilt installed binary. Build,
vet, unit tests and the complete isolated CLI process suite pass. Race packages
pass; afsfs required a rerun after its disposable Redis failed to become ready.

## Publish lightweight control-plane design

- [x] Verify source, scope and current GitHub main; isolate from other work.
- [x] Write proposed architecture, comparison, failure semantics and acceptance gates.
- [x] Check Markdown, references and documentation-only diff.
- [x] Commit directly to main, push normally and verify the GitHub document.

Spec: publish `docs/lightweight-control-plane.md` as a draft proposal.
Reuse the existing engine, optional `afs serve`, one Redis backend, persistent
access grants and direct Redis file I/O. Document permission/revocation limits;
do not implement features. Use an isolated main checkout and preserve the
shared checkout's branch. No unresolved publication questions; implementation
questions belong in the design.

Review: independent source audit passes against original `c3897ac` and slim
main `09f1146`. GFM parsing validates three tables, 11 relative links/anchors,
13 pinned source links and one Mermaid flowchart. All 11 original source paths
exist in GitHub's pinned tree. Only the draft and task bookkeeping change;
implementation and Redis tests are intentionally deferred. Design commit
`4e70584` was pushed directly to main; GitHub's main ref and exact document
bytes were verified afterward. The shared checkout stayed clean on its existing
`rtwork/single-executable-redis-errors` branch at `96dc5da`.

## PR #4 CI follow-up

- [x] Reproduce the missed startup deletion with a gated subscription regression.
- [x] Recover changes only after confirmed subscription; rescan when no cursor exists.
- [x] Reproduce and fix recycled-inode rename matching on an existing tracked path.
- [x] Isolate the manually queued inbound-read fixture from automatic recovery.
- [x] Preserve large-file chunk metadata when recovery wins the initial upload race.
- [x] Pass final local checks before updating the PR.
- [x] Isolate controlled checkpoint write fixtures from automatic startup recovery; retain conflict and timeout assertions.
- [x] Pass final fixture verification before publishing the PR update.

The original PR CI runs passed all four/eight-client native suites. One unit run
missed a remote deletion before subscription confirmation; the other run's core
smoke preserved an edit as an unexpected conflict copy. A deterministic reused-
inode regression reproduces the same canonical/conflict result and silent-copy
ordering. Historical inode reuse cannot be proven from those artifacts. The fix
keeps existing file/symlink baselines while retaining new-path rename detection.
Deletion and byte-preservation guards and workload deadlines remain unchanged.
Final local verification passes build/vet, 654 unit and 654 race test/subtest
cases with real Array enabled and no skips, 109 CLI cases, 14 dependency
regressions and 16 harness tests. All ten core scenarios also pass with Go 1.22
in isolated Linux processes. Source hashes stayed unchanged and owned cleanup
completed. GitHub PR #4 tracks publication and the merge gate; merge and pull
main only after the updated commit's CI checks pass.
The controlled checkpoint fixtures retain their safety assertions and normal
recovery on restart. Their complete group also passes ten repetitions per mode
(930 normal and 930 race test/subtest passes); final full verification still
passes all 654 unit/race and 109 CLI cases after the fixture-only changes.

## Check in native mounts and concurrency fixes

- [x] Verify the final source against retained acceptance evidence and review the diff.
- [x] Fix the close-time FUSE lock-owner regression found during check-in review.
- [x] Record the macOS FUSE approval blocker and run final repository checks.
- [x] Commit the tested implementation on a new branch for pull-request publication.

Scope: check in the tested implementation, harness, regressions and documentation.
The latest live macOS FUSE probe failed before readiness because kernelmanagerd
reported that macFUSE was not approved to load. Both test mounts timed out after
30 seconds; cleanup completed with no remaining owned processes. macOS FUSE
kernel acceptance remains pending user approval in System Settings. No unresolved
design questions remain.

Review: the final check-in source passes build/vet, 645 unit and 645 race
test/subtest cases (real Array enabled, zero skips), 109 CLI process cases,
14 vendored dependency regressions and 16 harness tests. The real Linux FUSE
lock scenario passes both second-descriptor POSIX close and same-session flock
wait/fsync/final-close checks. Cleanup reports no remaining owned processes.
The earlier integrated 68-scenario matrix is retained separately; the final
check-in also fixes the lock owner and sleeping-waiter admission issues found
in review. Source stayed unchanged during final verification. Publication uses
branch rtwork/native-mounts; GitHub CI reruns core and four/eight-client native
acceptance. See docs/multiwriter-results.md for evidence and platform limits.

## Native mount implementation and complete concurrency acceptance

- [x] Restore guarded inode I/O and lock interfaces in the shared Redis client.
- [x] Encapsulate retained FUSE/NFS adapters in an optional native helper.
- [x] Integrate root mount/unmount/status and checkpoint lifecycle across backends.
- [x] Resolve the known eight-writer conflict publication failure with a regression.
- [x] Extend concurrent acceptance to native and mixed mounts; run on available kernels.
- [x] Pass build, vet, unit/race, CLI compatibility and concurrent acceptance; document evidence.

Spec: keep folder sync as the default; add explicit `--backend=fuse|nfs`.
The native helper shares the current Redis client, Array detection, revision and
workspace generation guards. Keep protocol dependencies out of the CLI binary,
control state outside mountpoints, and native mounts visible to checkpoint and
restore/delete safeguards. Reuse original adapters without cloud/search glue.
Use disposable infrastructure only; preserve unrelated work and the original
installation. Completion requires passing tests, not only a successful build.
Review: all required local gates pass on final binaries. Build/vet, 635 unit and
635 race tests (including real Array, zero skips), 109 CLI cases, 12 prior/current
comparisons and 20 paused-writer repetitions pass. Core four/eight-client runs
pass all ten scenarios each on macOS and Linux; mixed FUSE/NFS/sync four/eight
client runs pass all ten each on Linux; macOS NFS passes all eight applicable
scenarios. Total concurrent acceptance: 68/68, with no setup/capture/cleanup
errors. Vendor race checks pass nine; harness checks pass 16; Linux native
package and FUSE cancellation checks pass 16 and five respectively.

Regressions cover directory modes, stale deletion/download results, local edits
during staging, checkpoint conflict moves, native startup/cancellation, parent
identity, generation/session fencing, cache recovery and crash cleanup. The final
eight-writer CREATE failure was false global WATCH contention; a deterministic
four-operation regression proves the narrow parent-guard fix. An occupied-port
RPC fixture now retries only EADDRINUSE; full Go checks run after native macOS
mount cleanup so their lsof scan has a stable mount table. Failed earlier reports
remain retained. Final source and binary hashes were verified after all runs.

Results and exact configurations: docs/multiwriter-results.md. Built binaries
are bin/afs and bin/afsmount. Native drivers stay out of the CLI dependency graph.
Tests used owned Redis and local Linux containers; original installations remain
untouched. macOS FUSE kernel testing needs an active driver; real Linux FUSE and
macOS/Linux NFS passed. Actual customer microVMs and remote CI were not exercised.
No product questions remain.

## Native mount encapsulation study

- [x] Inspect original driver layout and current retained client boundary.
- [x] Trace NFS/FUSE dependencies, write/flush behavior and lifecycle coupling.
- [x] Verify a minimal extraction boundary in a disposable copy where practical.
- [x] Recommend package/module layout, integration steps and acceptance gates.

Scope: architectural investigation; current production and original installation
stay unchanged. Unresolved questions: none required for the investigation.
Review: recommendation delivered; user subsequently authorized implementation.

## Multi-writer microVM workload lab

- [x] Inspect current CLI, sync guarantees, existing process tests and available runtimes.
- [x] Build configurable isolated clients, disposable Redis, fault injection and retained reports.
- [x] Exercise concurrent hydration/disjoint/shared writes, rename/delete, chunked files, partitions and crash recovery.
- [x] Reproduce findings; fix confirmed bugs with focused regressions where practical.
- [x] Run build, vet, unit/race and real-Redis process checks; document measured results and limits.

Spec: each simulated VM owns a local tree, HOME/config/state and an AFS daemon.
Only a disposable Redis server is shared. Use per-client TCP proxies for network
faults. Synchronize contenders before same-path mutations; check exact candidate
bytes, convergence and cold hydration rather than queue counts. Record seed,
versions, timings, status, logs and manifests; return failure on violated checks.
Provide a Linux container runner. Local process/container runs do not validate
microVM kernels, guest boot, KVM, production latency or customer scale.
Default: four clients; allow scale and seed overrides. Validate an eight-client
run as well. Customer target concurrency remains unspecified; the default is
a test setting, not a deployment capacity claim.

Historical review before native integration: lab reproduced remote-queue overflow dropping recovery requests,
abandoned uploads after EOF, unpublished startup conflict copies, stale renames
erasing peer edits, symlink conflict/recovery failures, unnecessary symlink
replacement on save, and completed inbound/outbound deletion markers suppressing
identical recreations. Focused regressions failed before the fixes. The final
four-client delete/recreate case passed. That eight-client folder-sync run had nine of
ten scenarios pass; one shared-creation candidate remains only as a local
conflict file on its origin while every queue is empty. This publication defect
was retained and documented; the later native integration work added regressions
and fixes for missed conflict-copy publication. See the current review above.
Bulk runs also exposed convergence and checkpoint costs; see
docs/multiwriter-results.md for exact parameters and retained evidence.
Docker Desktop host-bind flock failed an independent probe; the Linux runner
uses native tmpfs for workload execution and exports retained artifacts. That
environment ran, but final Linux validation is blocked by an unresponsive Docker
engine. Final build/vet pass; unit and race each pass 441 cases with three optional
Array skips; real-Redis CLI/process tests pass 109 cases; harness tests pass four.
Docker's graceful stop timed out. One lab-owned client remained in an OS exit
state after SIGKILL; its cleanup remains unconfirmed. No existing user Redis or
installed AFS binary was used or modified.

Customer-readiness follow-ups exposed by the lab:
- [x] Resolve the eight-writer live conflict-copy publication defect.
- [ ] Profile remote verification scans and repeated notification work.
- [x] Rerun final source on a healthy Linux runtime.
- [ ] Validate actual customer microVMs with their deployment environment.

## Remove file commands and flatten workspace actions

- [x] Delete public `fs` commands and their CLI-only helpers.
- [x] Move workspace actions to root; retain `cp`; reject removed `ws`/`fs` forms.
- [x] Update help/docs and adapt process/compatibility tests to mounted files.
- [x] Fix demonstrated parent-directory deletion retry/order issue.
- [x] Verify all retained behavior, readable defaults and explicit JSON.
- [x] Publish through normal CI/PR and update the installed derivative.

Decision: user selected root workspace actions with `cp` retained. File access
uses ordinary mounted directories. Internal storage/sync/Array support remains.
Unresolved questions: none.

Review: build/vet pass; unit and race each pass 403 test/subtest cases with three
optional Array skips. Process acceptance passes 109 cases; paired original/current
CLI comparison passes 12, with no skips in either. File operations now use real
mounted folders, preserving all retained sync scenarios. A parent-first directory
delete failure reproduced on original and derivative; the existing reconciliation
planner now retries it safely, including protection for a newer peer edit.
Focused race cases pass 30 repetitions. An initial disposable Redis startup
timeout did not recur in 30 focused repetitions or the full race rerun; captured
server logs showed no startup errors. Original checkout remains unchanged.
Published in PR #3. Linux build/vet/unit/race/process checks passed on `826b052`
in run `34919890029`. The installed slim AFS binary now uses that implementation
and passed all 109 process cases directly through its installed path.

## Default output correction

- [x] Reproduce JSON-default regression and inspect prior readable output.
- [x] Restore text confirmations, tables and details; preserve explicit JSON.
- [x] Verify both output modes through CLI contract and prior/current tests.
- [x] Run build, vet, unit/race and process checks.
- [x] Publish through normal CI/PR.

Scope: presentation only; `fs cat` keeps exact bytes. Unresolved questions: none.

Review: default-output regressions fail on the preserved pre-fix binary; original
CLI passes. Fresh derivative: 170 process passes and 12 prior/current comparison
passes, zero skips. Explicit JSON shapes and binary/empty `cat` are preserved.
Build/vet pass; unit/race each pass 401 cases with three optional Array skips.
Published in PR #2. Linux build/vet/unit/race/process checks passed on `ed59c1e`
in run `34917706289`; JSON schemas and sync/lifecycle behavior remain unchanged.

Baseline: redis/agent-filesystem `c3897ac05265444568a3819c21728a38a4ed254b`.
Target: rowantrollope/afs, private; existing initial commit `a91598c` preserved.

## Plan
- [x] Read brief, original guidance, inspect target and source provenance.
- [x] Inventory retained implementation and run baseline tests in a disposable copy.
- [x] Extract Go storage, checkpoints, filesystem client, and folder sync.
- [x] Remove cloud, composition/volumes, native mount drivers, search, MCP, and packaging.
- [x] Adapt CLI, isolated configuration/namespace/state, lifecycle and flush wiring.
- [x] Map prior CLI to reduced CLI and run black-box behavioral comparisons.
- [x] Execute every documented reduced command and README workflow through a built binary.
- [x] Verify final regressions; real Redis and independent-process acceptance; focused performance checks.
- [x] Update guarantees, limitations, provenance, additions, and final results.
- [x] Build, vet, test, race; commit, push and verify Linux CI.

## Decisions
- Original checkout and installed processes/configuration/data remain untouched.
- Baseline runs in `/private/tmp/afs-baseline-c3897ac`; only tracked source is copied.
- Retain private inode/tree storage and manifest checkpoint machinery. No sync rewrite.
- Independent agents inspect/extract storage and sync; root owns CLI/lifecycle and integration.
- Unresolved questions: none; investigate guarantees through tests before claims.

## Completion gate
The user explicitly evaluates completion through old-versus-new CLI behavior.
Reduced instructions must run successfully and preserve applicable prior behavior.
Package tests alone do not satisfy this gate.

## Review
All local acceptance gates pass on final production code:
- Build, vet and Linux cross-build.
- Unit and race: 396 test/subtest passes each; three optional Array skips each.
- Fresh-binary CLI/process suite: 135 passes, no failures or skips.
- Actual prior/current CLI comparison: nine passes, no failures or skips.
- Tests reproduce and fix ignored-file hydration, pending remote deletion during
  full reconciliation, and empty-file replay after a peer deletion.
- The pending-delete regression also fails on the original baseline; four
  focused sync cases pass 50 race-detector repetitions after the narrow fix.
- Production source is about 80% smaller. Original checkout remains unchanged.
- Linux CI passed build, vet, unit, race and real-Redis process acceptance on
  production commit `e983f4d`: run `34916337513`. Published through PR #1;
  visibility and protections remain unchanged.
