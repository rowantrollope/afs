# AFS extraction

## Original file-history capability superset

- [x] Pin the original source and enumerate storage, policy, CLI and web UI contracts.
- [x] Restore content deduplication, equivalent-write suppression, hashes, attribution and metadata-only large-file records while retaining atomic capture.
- [x] Restore original history/content/diff/restore/undelete/checkpoint interfaces and a usable web UI compatibility transport.
- [x] Preserve history, shared-body ownership, lineage and retention through fork/restore/delete and concurrent writers.
- [x] Execute original-versus-new capability and performance comparisons on disposable Redis; record measurable improvements and remaining costs.
- [x] Pass build/vet/unit/race/process gates and review the complete capability matrix.
- [x] Prepare the tested commit and comparison evidence for the authorized GitHub PR.

User requires all original per-file versioning capabilities, including compatible
web UI interfaces unless an evidenced compromise warrants a UI change. Passing
the earlier reduced feature tests does not satisfy this goal. Retain the atomic
publication core and original retained sync/checkpoint code. Tests must use
owned infrastructure and leave the original installation and user data untouched.
Do not claim absolute absence of defects or production superiority from local
tests; measure both implementations with the same workload. Publication is
authorized through a PR, superseding the earlier main-only preference for this work.

Review: paired five-repetition runs on standard Redis and Array confirm
improvements in tested capture/recovery, retention memory and local write latency,
with higher memory in some unlimited-history cases. The exact final-code
measurements cover 240 scenario executions: timed operations were 1.19–3.93 times
faster, and five-version retention used 84.8–88.4% less workspace memory. Costs
include up to 6.5% higher standard Redis memory and about 9% higher Array append
memory. These local measurements do not establish production superiority. Original drawer workflow
passed in a real browser; final component integration uses jsdom because supported
browser automation is unavailable while the desktop is locked. Compatibility
limits, attribution and storage differences are explicit in the report.

Build/vet, full unit/race, all isolated CLI/process acceptance, Array history and
recovery checks, native dependency races and 38 Python harness checks pass.
Regressions reproduced and fixed checkpoint recovery, empty-head diff fallback,
metadata-only pruning and safe test fixture port handling. No production
installation or user Redis has changed. The unchanged original component passed
all three final tests against the benchmarked source. Publication uses
`codex/file-history-superset` and the associated GitHub PR; remote CI supplies
the separate Linux and actual kernel mount gates. See docs/file-history-validation.md
for evidence, source hashes and the explicit production and compatibility limits.

## Initial optional per-file versioning implementation

- [x] Inspect upstream policy, lineage, recovery and retention behavior.
- [x] Capture immutable versions with accepted file, range, rename and delete publication.
- [x] Add root `history`, `versioning` and safe local `recover` commands.
- [x] Preserve independent history through forks, checkpoint restore and deletion.
- [x] Verify focused safety regressions, build/vet/unit/race and disposable-Redis process workflows.
- [x] Document default-off capture, retention, upgrade requirements and snapshot costs.

Design: workspace-wide Redis policy; history records published mutations and stays
separate from checkpoints. Recover into a new local destination without replacing
existing content, retaining exact bytes, modes and symlink targets. Stable lineage
survives rename; recreation gets a new lineage. Upgrade every writer before
enabling. Retention uses bounded batches and history-owned snapshots; logical
byte budgets are not total Redis memory limits. Original installation and user
Redis data remain untouched. See docs/file-history.md.

Review: build/vet and full unit/race suites pass; final unit/race runs use `-p 1`
after parallel acceptance suites exposed a disposable-server port collision.
Full CLI process acceptance, real-Redis storage tests, two independent history
writers with Redis restart, and standard/Array-backed history/fork/restore tests
pass. Regressions cover failed/lost-ack publication, first deletion, exact binary
and symlink recovery, stale writers, retention corruption/budget eviction,
checkpoint retries, rename lineage, source-name reuse and independent forks.
Local whole/range-write benchmarks measure retained memory and Redis script time;
see docs/file-history-validation.md. Actual kernel mounts and remote deployments
were not rerun. History remains off by default; enable only after upgrading all
writers. Source changes are local to this checkout; no installation or push.

## Integrate upstream parity and explicit sync verification

- [x] Integrate useful parity code/tests; preserve current main and existing edits.
- [x] Add `afs sync --wait <workspace|directory>` and `afs sync status [target]`.
- [x] Fix permission enforcement, unreadable-reader updates and stale chmod receipts.
- [x] Validate build/vet/unit/race, real-Redis CLI, concurrency and native checks.
- [x] Review final diff; record evidence and remaining platform limits.

Design: reuse save's existing authenticated control transport and verified receipt.
Wait requires one active writable folder-sync mount; reject native/read-only/ambiguous
or stopped targets. Status only observes sync activity. Wait resumes sync, creates no checkpoint, and fails clearly
on conflict/timeout/verification errors. JSON must remain machine-readable; human
output follows current REDIS header and timestamp conventions. Preserve main's
queue backpressure, symlink-mode and parallel save-verification fixes. Integrate
on main; test, commit and push after validation. Do not alter installed binaries/configuration during tests.
Tests use disposable Redis and private state. No unresolved questions.

Review: all three review regressions reproduced before fixes and pass after them.
Build/vet/unit/race, real-Redis CLI, original CLI compatibility, native dependency
and 38 Python checks pass. Linux nonroot build/vet/unit and actual FUSE ownership,
other-user permission denial and read-only smoke pass. Four-writer lab: 10/10;
paired Mac processes: 12/12 each, clean exits/cleanup. Mixed Linux native: 9/10;
known pre-existing NFS reconnect misses a newly created directory entry, recorded
without claiming a full pass. See docs/sync-parity-integration-results.md for
source/binary provenance, commands, artifacts and platform limits.

- [x] Prepare tested source for publication to GitHub main.

Publication authorized by the user; final remote verification follows the commit.


## Redis password environment override

- [x] Add AFS_REDIS_PASSWORD at client construction for CLI, sync and native mounts.
- [x] Document both environment variables and preserve URL passwords without new dependencies.
- [x] Verify build, vet, unit/race and isolated Redis authentication/process tests.

Design: --redis > nonempty AFS_REDIS_URL > JSON > default for the URL.
A present AFS_REDIS_PASSWORD, even empty, overrides the password in
the selected URL (JSON or --redis). Never serialize the environment password.
Unset preserves existing URL behavior. No unresolved questions.

Review: build, vet, full unit/race suites and focused real-Redis process test pass.
Tests cover URL/flag/password precedence, explicit empty password, literal special
characters, redaction, saved-password fallback, and authenticated background
sync with unmount/remount persistence. Native NFS export authentication tested
without an OS mount. Human config output suggests the password override; JSON
output is preserved. No dependencies added or user Redis data accessed.
Logs: /tmp/afs-env-{unit,race}.log.

## Redis context in CLI output

- [x] Add one credential-free database header to human database commands.
- [x] Show accurate mount endpoints in status and unmount, including mixed databases.
- [x] Verify build, vet, unit/race and isolated Redis CLI acceptance.

Design: compact Redis URL with explicit effective database; header before
operations/confirmation, after successful connection. Preserve JSON and offline
help/config output. Status stays offline; distinguish configured and mounted
databases. Preserve existing timestamp work. No unresolved questions.

Review: build, vet, full unit/race and isolated real-Redis process suites pass.
Effective DB overrides, credential redaction, offline/empty status, changed mount
configuration and JSON purity are covered. Rebuilt bin/afs (the installed symlink
target) and verified database override/list isolation through that exact binary.
Logs: /tmp/afs-header-{unit,race,e2e}.log. No user Redis data accessed.

## Delete all configured AFS workspaces

- [x] Inventory workspaces and local mounts through the installed CLI.
- [x] Unmount affected local mounts if needed; delete every listed workspace.
- [x] Verify the configured database's workspace list is empty.

Scope: user-requested deletion through `afs` using its current configuration.
Found 12 workspaces and no registered local mounts. No unresolved questions.
Review: the installed `afs` CLI deleted all 12 listed workspaces using
`afs delete <workspace> --yes`; every command succeeded. Final `afs --json list`
returned `[]`. No local unmounts were needed.

## Fix make install over an existing executable

- [x] Reproduce the refusal using an isolated install destination.
- [x] Allow regular-file upgrades while retaining directory/symlink safeguards.
- [x] Verify fresh/repeat/upgrade installs and configuration preservation; install locally.

Scope: Makefile installation and documentation only, on main. Preserve unrelated
branch-review notes and runtime data. No unresolved questions.

Review: the previous Makefile rejects a copy of the installed AFS executable.
The corrected rule recognizes its Go command identity and replaces it with a
staged checkout symlink, preserving unrelated files, directories and links.
Nine isolated checks pass, including repeat installs, paths with spaces,
configuration preservation and installing into the build directory itself.
`make install` now succeeds on the real installation; PATH resolves the correct
link and offline help passes. Configuration and the original `/usr/local/bin/afs`
link are unchanged. Build and diff checks pass. Evidence and prior binary:
`/private/tmp/afs-install-checks-1t73f4wc/`. Changes are local on main; not pushed.

## Review all branches

- [x] Inventory local/remote branches, worktrees and GitHub pull requests.
- [x] Compare every branch with live main; identify unique and superseded work.
- [x] Report cleanup candidates and preservation requirements.

Scope: branch review and record the user's main-only preference. Preserve
uncommitted work and the runtime Redis dump. No new branches.

Review against fetched main `8ad12fc`: seven extra branch names, six local and
six remote. `rtwork/slim-afs`, `rtwork/readable-cli-output`,
`rtwork/workspace-cli` and `rtwork/native-mounts` are fully merged (PRs 1–4).
Local-only `rtwork/control-plane-design` has no unique commits; its linked
worktree contains the superseded draft and task bookkeeping, not product code.
Main contains the improved final design.

Two branches contain substantive unmerged work: `rtwork/single-executable-redis-errors`
has two commits, including single-binary native mounting missing from main;
preserve main's newer Redis error wording when integrating it.
Remote-only `codex/upstream-parity-fixes` has one unique commit restoring bounded
import reuse, read-only mounts, native ownership options and directory-mode
safety, plus regressions/audits. Main also has its own newer sync/checkpoint
fixes, so preserve both sets rather than replacing main with either branch.
The parity branch's retained report records an unresolved NFS reconnect failure;
no tests were rerun for this ancestry/source review. No branches/worktrees were
deleted and no commits pushed. Main-only preference is recorded in lessons.

## Rerun on the user-selected replacement Redis database

- [x] Verify the same Mac/Sancho source and binaries from run 9cd72d09b414.
- [x] Prepare private per-run connection overrides without changing saved configuration.
- [x] Complete the unchanged 12-scenario workload on both actual systems.
- [x] Evaluate both reports, capacity counters and cleanup; retain review evidence.

User explicitly selected a different database for this rerun. Preflight reports
256 maximum clients, no rejected connections and no evictions. Keep the workload,
timeouts and tested code identical so the results can be compared with the prior
capacity-limited run. Use fresh workspaces; preserve prior artifacts and data.

Run a7145c3da397: all 12 scenarios pass on both actual systems; both runners exit
0. All 76 Mac and 80 Sancho saved tree comparisons are clear, including complete
bulk mutation, checkpoint, fresh hydration and normal unmount checks. No OOM
evidence, rejected connections, evictions or Redis error replies were recorded.
Cleanup reports are empty; owned processes and the new tunnel are gone. Saved
configuration and installed binaries are unchanged. Evaluation and provenance:
docs/two-system-results-2026-09-16-new-database.md.

Publication scope: the user requested committing the completed fixes, regressions,
runner diagnostics and result documentation directly to main. Private runtime
configuration and ignored test artifacts remain outside version control.

## Run fixed build on Sancho and macOS

- [x] Transfer and verify the same source snapshot; build separately on both hosts.
- [x] Run all 12 scenarios against the Redis configured on both machines.
- [x] Collect both reports and diagnose every failed/incomplete scenario.

User explicitly authorized this real two-host run against their existing Redis.
Use fresh named test workspaces and separate binaries/state; preserve existing
workspaces, installed binaries, server settings and original-run artifacts.

Run 9cd72d09b414: five complete passes (mutations, delete-edit, rename-edit,
partition, crash), six OOM-blocked cases, and a basic fresh-observer connection
failure. Redis exposes a 30-client limit and rejected connections rose from 0
to 9. Both runners exited 1 with no cleanup errors; owned processes and tunnel
are gone. The chmod and recovery symlink-unmount fixes pass across OSes; bulk
mutations remain unvalidated because OOM stopped initial creation. Full details:
docs/two-system-results-2026-09-16-fixed.md. Further acceptance needs memory and
connection headroom; no data cleanup or server settings change was performed.


## Fix failures from the first two-system run

- [x] Normalize folder-sync symlink modes without relaxing target conflicts.
- [x] Schedule guarded reconciliation for live directory chmods, including echoes.
- [x] Prevent saturated worker queues from blocking result consumption; recover deferred work.
- [x] Record Redis memory/error telemetry and distinguish capacity-blocked scenarios.
- [x] Parallelize bounded small-file save verification after reproducing the delayed checkpoint timeout.
- [x] Run focused regressions, build/vet/unit/race and disposable Redis process validation.

The subsequent actual Mac/Sancho run is recorded above. Local validation used
separate binaries and disposable Redis; the authorized real run used the existing
configured Redis with fresh test workspaces.

Validation: six new focused concurrency/mode/save regressions pass (the initial
three reproduced symlink-save, swallowed-chmod and full-queue failures before
fixes); build, vet, unit/race, native dependency checks, isolated CLI integration
and 38 Python regressions pass. All 12 paired scenarios pass on both local
processes with default workload and 45-second phases; the existing four-writer
lab passes all 10 scenarios, including its formerly intermittent rename/delete
case. A 12 MiB disposable Redis produces capacity-blocked large-file outcomes
and exit 1 on both peers. Owned cleanup succeeds, and pre-existing fixture
workspace metadata/cold-hydrated bytes remain unchanged. Credential-free local
validation reports are in tests/multiwriter/artifacts/two-system-fixes-2026-09-16.

The added 5 ms proxy-delay bulk run completed every mutation oracle, then hit
its fixed 120-second checkpoint save deadline. Bounded parallel verification
of files <=1 MiB fixes the serial read bottleneck without increasing that
limit. A focused 402-file delayed regression passes checkpoint (34.72s), cold
hydrate, normal cold/writer unmount (30.99/31.63s), exact retained trees and
cleanup. Full unit/race, CLI integration and all 12 normal paired scenarios
pass again on the final implementation. The full delayed mutation workload
itself has not been repeated after the save optimization.



## Evaluate actual macOS / Sancho acceptance results

- [x] Read completed reports, comparisons, daemon logs and saved baselines on both hosts.
- [x] Separate functional failures, unresolved backlog and Redis capacity failures.
- [x] Verify relevant test-owned Redis metadata and memory/eviction metrics read-only.
- [x] Preserve credential-free evidence and write docs/two-system-results-2026-09-16.md.

Run bdcfc93bc899: four passes, eight failures. Four failures share Linux fresh
observer symlink-unmount conflicts (0777 locally versus 0755 in Redis/macOS, with
zero saved modes interpreted as 0777). Directory chmod fails bidirectionally;
the existing-directory event handler returns without checking mode changes.
Bulk has 23 stale/missing/obsolete path discrepancies on the Mac at 120 seconds,
with 775 queued events and 77 tracked uploads on Sancho; root cause is unresolved.
Large-file edits and a shared-edit checkpoint hit repeated Redis OOM errors.
Recovery contents and fresh hydration pass before symlink unmount failures.
All owned-process cleanup reports are empty. Version labels differ, but the
tested commits have identical production Go code. No data was deleted, server
settings changed, workloads rerun, or production code modified during evaluation.

- [x] Fix and regress mixed-platform symlink baseline/mode handling at save/unmount.
- [x] Reproduce and fix bounded worker-queue deadlock; validate delayed bulk propagation.
- [x] Confirm the fixes in a new actual Mac/Sancho run with Redis headroom.
- [x] Add harness memory preflight/failure telemetry and capacity-blocked reporting.

The follow-up fixes directory chmod and a reproduced queue deadlock. The complete
replacement-database run a7145c3da397 above confirms all 12 scenarios across the
actual Mac and Sancho, including the previously incomplete bulk workload.

## Two-system synchronization acceptance

- [x] Add a coordinated macOS/Linux runner with isolated state and directories.
- [x] Check independent content/metadata oracles, bidirectional mutations, conflicts,
      partitions, warm crash recovery, ignores, flushes, and cold hydration.
- [x] Retain per-host diagnostics; reject missing peers, false convergence, and failures.
- [x] Document two-host SSH setup and validate the harness plus repository gates.

Scope: folder sync on two actual systems; orchestration travels outside the synced
tree. User clarified that both hosts must use their existing remote Redis server;
create fresh uniquely named test workspaces there, preserve existing workspaces,
and never restart/flush Redis or use installed AFS state. Only coordination needs
an SSH tunnel. Our local validation uses disposable Redis. Local paired-process
validation does not substitute for running on the user's two systems.

Validation: build, vet, unit/race and isolated CLI process suites pass. Harness
regressions cover independent oracles, authenticated coordination, per-case failure
recovery, remote proxy partitions and actual TLS hostname/trust verification.
Two complete paired-process runs finish 11/12 scenarios; both correctly return 1
for directory chmod after rename (expected 0750, peer remains 0755). A third,
earlier run independently reproduced that assertion before continuation existed.
Final workload: 100 files/writer/round, three rounds, 8 MiB large fixtures,
seed 1, 45-second phase deadline and 0.5-second stability window. All owned
process cleanup completes; a pre-existing fixture workspace retains its metadata
and cold-hydrated bytes. Production code/installation and remote Redis stay untouched.
The existing ten-scenario lab passed nine; its rename/delete candidate-preservation
check timed out once and passed a focused rerun. Retain that intermittent result.
See docs/two-system-sync.md for run instructions and local findings.

- [x] Investigate/fix live directory chmod propagation after directory rename.
- [ ] Investigate intermittent existing-lab rename/delete candidate loss.

## Align root help columns

- [x] Align every command and option description to the same column.
- [x] Rebuild and verify displayed help and repository checks.

All 15 command/option descriptions start in column 34 in the built help.
Build, vet, unit/race and isolated real-Redis process checks pass.

## Discoverable user configuration

- [x] Add config.example.json with every supported setting and its default.
- [x] Seed missing user configuration from make install without overwriting files.
- [x] Add offline config set with dotted keys, validation, private atomic writes,
      preservation of unrelated settings, and alternate --config paths.
- [x] Validate CLI behavior, installation preservation, build, vet, unit/race,
      and isolated real-Redis process checks.

User explicitly requested the config command family; workspace actions remain
at the root. Existing mounts retain their startup settings.

Validation: build, vet, unit/race and the isolated real-Redis process suite pass.
Regressions cover offline setup, saved connection use, alternate files and
one-command overrides, invalid-edit preservation, secret-free output, concurrent
updates, permissions, symlinks, and unknown-field preservation. make install
creates the full default file privately and preserves existing configurations.
The local binary was rebuilt and missing user configuration initialized.

## Per-user CLI installation

- [x] Link the current checkout's bin/afs into ~/.local/bin directly in make install.
- [x] Add make install, custom destination support, and setup/removal documentation.
- [x] Validate repeat installs, conflicting paths, PATH guidance, and CLI execution.
- [x] Run repository build, vet, unit/race and isolated process checks.

The Makefile recipe preserves existing unrelated commands and requires no sudo.
User clarified that installation must use make install without a separate script. The
existing local AFS link already points to this checkout.

Validation: make install and installed CLI help pass. Temporary-directory checks
cover default/custom destinations, spaces, repeat installs, PATH guidance, and
preservation of conflicting files, directories, and dangling symlinks. Build,
vet, unit/race and the isolated real-Redis process suite pass.

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
## Human-readable display timestamps

- [x] Audit date/time display paths.
- [x] Use dd/mm/yyyy hh:mm:ss AM/PM (12-hour time) in the system local timezone for human output.
- [x] Verify build, vet, unit/race and isolated Redis output acceptance.

Scope: CLI tables, details and sync logs; preserve JSON/storage formats.
Unresolved questions: none.

Review: also covered version build dates and standard native-driver logs with
one shared formatter. Build, vet, unit and race suites passed. Four isolated
Redis output/CLI acceptance tests passed. Fixed build timestamp verified in UTC
and Asia/Kolkata (including next-day rollover); native helper error output verified.
Checkpoint IDs and conflict filenames retain their filename-safe timestamps.

## Capitalize Redis labels

- [x] Use REDIS in headers and detail labels; update existing assertions.
- [x] Rebuild and verify focused CLI output checks.

Review: focused unit and disposable-Redis output tests pass through rebuilt bin/afs.
