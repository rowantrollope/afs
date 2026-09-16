# AFS extraction

## Publish single executable and quiet Redis errors

- [x] Review the combined changes and compare source with passing acceptance evidence.
- [x] Exclude the local Redis database dump from source control.
- [ ] Commit all intended source, tests and documentation and push to GitHub.

Scope: publish the combined changes on `rtwork/single-executable-redis-errors`.
Keep the local runtime database intact. No unresolved questions.

## Single executable distribution

- [x] Move native daemon implementation/tests into an internal package; dispatch through `afs`.
- [x] Launch native mounts and recovery through the current executable; retain lifecycle safeguards.
- [x] Remove separate-helper build/install configuration and update docs and process labs.
- [x] Verify build/vet, unit/race, isolated CLI and real native mount acceptance; record sizes/results.
- [x] Remove the obsolete `bin/afsmount` after verification.

Spec: distribute only `afs`, with sync as the default and FUSE/NFS available via
the existing backend flags. Reuse the separate native child process, bootstrap,
authenticated control, readiness, flush and recovery behavior. No public daemon
command or helper-path setting. Preserve unrelated connection-presentation work,
existing installations and all user Redis data. Unresolved questions: none.

Review: `afs` now dispatches native mounts and stale-mount recovery through its
own private daemon entrypoint. The native implementation and tests moved to
`internal/nativedaemon`; authenticated control, bootstrap, ownership, readiness
and flush safeguards are retained. Builds, Docker and the native lab use one
executable. The renamed-binary regression proves recovery without a companion
binary, PATH entry, driver or Redis connection.

Final verification passes build/vet, 655 unit and 655 race test/subtest cases
(real Array enabled, zero skips), 120 CLI cases, vendored regressions and all
16 harness tests. All ten four-client sync workloads pass; all ten four-client
Linux FUSE/NFS/sync scenarios pass; macOS NFS file/checkpoint/crash scenarios pass
with two clients. Source hashes stayed unchanged through verification. Native
supervisors report no timeout, cleanup errors or remaining owned PIDs. macOS FUSE
kernel acceptance was not rerun; Linux provides the real FUSE coverage.

The verified macOS ARM64 binary is 12,472,738 bytes, SHA-256
`692bca82ca908f671749bccc4955ff46978732010d15b377553d61c6b8b0790e`.
Installed identical bytes at `~/.local/bin/afs`; verified PATH resolution,
offline help and unavailable-Redis presentation against a reserved local port.
Deleted `bin/afsmount`; preserved the original `/usr/local/bin/afs` symlink.

Evidence: `tests/multiwriter/artifacts/single-binary-go-20260916/report.json`,
`single-binary-core-20260916/report.json`,
`single-binary-linux-20260916/acceptance-summary.json`,
`single-binary-macos-nfs-20260916/supervisor.json`, and
`single-binary-install-20260916.json` under the same artifacts directory.

## Redis connection error presentation

- [x] Apply the user correction: remove connection progress in every output mode.
- [x] Verify error-only output and refresh the installed CLI.
- [x] Reproduce unavailable Redis output and add CLI regression coverage.
- [x] Show one endpoint-specific error and suppress repeated dial logs.
- [x] Verify success/JSON/offline behavior, build/vet, unit/race and isolated CLI tests.

Spec: connection attempts are silent; failures produce one clean stderr error.
Errors name the host and port without credentials or raw driver diagnostics.
Keep JSON stdout, offline commands, retry behavior and daemon lifecycle intact.
No unresolved product questions.

Verification note: focused old/new binary regressions pass for the Redis fix.
The first full run was interrupted when concurrent native packaging changes
introduced recursive startup test children; all attributed processes and the
owned Redis were cleaned up. Final Redis verification uses an isolated snapshot
of merged main plus only this fix. The native packaging task owns validation
and installation of the combined executable.
Review: the isolated fix passes build/vet, 654 unit and 654 race test/subtest
cases with real Array and no skips, 117 CLI cases, and 16 harness tests.
Dependency checks pass after one retained, confirmed NFS fixture source-port
collision and a scoped retry. All owned processes are gone and source hashes
are unchanged. Evidence: tests/multiwriter/artifacts/redis-connection-ux-isolated-20260915/acceptance-summary.json.
The combined executable has also passed its integration gates and is installed
at ~/.local/bin/afs. Its hash matches the tested bin/afs; an unavailable local
endpoint gets a friendly endpoint/timeout error. The earlier connecting banner
has been removed following the user's correction.
The legacy /usr/local/bin/afs installation remains unchanged.

Correction verification: the error-only executable passes build/vet, 655 unit,
655 race and 120 CLI cases with real Array and zero skips. All 455 source hashes
match the frozen snapshot, and owned processes exited. The tested binary was
installed at bin/afs and ~/.local/bin/afs; a reserved unavailable endpoint now
produces exactly one stderr error line, empty stdout and exit 1. Offline help
remains silent on stderr. Evidence: tests/multiwriter/artifacts/quiet-redis-20260916/.

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

## Upstream validation audit

- [x] Compare upstream tests, benchmarks and validation scripts with retained coverage.
- [x] Verify useful candidates against current CLI, storage and mount contracts.
- [x] Rank additions by value and adaptation effort; record evidence and limits.

Scope: inspect the local upstream checkout at `c3897ac` and this derivative.
Recommend imports without modifying production or running upstream workloads
against existing Redis data. Unresolved questions: none.

Review: all nine upstream Go benchmarks are already retained (six manifest,
three native-client). Highest-value additions are `tests/bench/main.go` (19
local-versus-mounted filesystem operations), the workloads in
`scripts/bench_claude_fs.sh`, and selected corpus/reporting components from
`tests/bench_md_workloads/main.go`. Port benchmarks with failing error/content
oracles and separate local operation, publication and fresh-observer timings.
The original runners can print failures or mismatches without failing the run.
Useful test gaps are assembled streaming-import deduplication/readback
(`internal/controlplane/import_pipeline_test.go`) and explicit Array selection
during manifest materialization (`internal/controlplane/workspace_root_test.go`).
Adapt both to current service APIs and owned disposable Redis; the upstream Array
fixture flushes an externally selected DB. A trimmed `scripts/test_harness.py`
could provide discovery, but must recognize our integration/compatibility tags
and exclude retained artifact directories. Its upstream catalog discovery ran
successfully; this was a source audit, not a benchmark or acceptance rerun.
Cloud/search/UI/SDK validation and obsolete lifecycle scripts do not fit the
retained product. No test imports or production changes made by this audit.

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
