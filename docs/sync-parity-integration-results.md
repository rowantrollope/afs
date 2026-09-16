# Sync command and parity integration validation

Validated 2026-09-16 against main `8a5d706` plus the existing credential/display
changes and parity commit `3fc0689`. This integrates the useful parity work while
retaining main's queue-backpressure, symlink-mode and parallel-save fixes.

## Behavior

- `afs sync --wait <workspace|directory> [--timeout 2m] [--json]` verifies the
  included local tree in Redis and resumes synchronization without a checkpoint.
- `afs sync status [workspace|directory]` observes folder-sync activity. Idle
  queues are not labelled verified. Synchronization remains automatic.
- Read-only folder/FUSE/NFS mounts, FUSE ownership/access options, bounded import
  reuse, and temporary-directory-permission guards are integrated.
- Shared FUSE mounts enable kernel permission checks. Read-only copies lacking
  read bits recover through guarded full reconciliation after live update errors.
  Delayed directory chmod receipts cannot overwrite newer baselines.

## Evidence

| Check | Result |
| --- | --- |
| macOS build, vet, full unit and race suites | Pass |
| Linux build, vet, full unit suite as UID 1000 | Pass |
| Real-Redis CLI integration | Pass, including sync wait/status and read-only live recovery |
| Prior CLI compatibility | Pass |
| Vendored FUSE/NFS dependency race checks | Pass |
| Python workload-harness regressions | 38 pass |
| Four-writer fault/convergence lab | 10/10 pass; cleanup clear |
| Paired-process 12-scenario runner on macOS | 12/12 on both peers; both exit 0; cleanup clear |
| Actual Linux FUSE ownership/access/read-only smoke | Pass, including unrelated-user access denial |
| Mixed Linux FUSE/NFS/folder-sync lab | 9/10; existing NFS reconnect failure remains |

The CLI test independently checks the receipt tree digest, file/entry/byte counts,
remote contents and permissions, no added checkpoint, continued automatic sync,
and timeout failure/request cleanup followed by a successful retry. Unit tests
cover ambiguous/missing/native/read-only/stopped targets and secret-free status.
All three review findings have regressions that failed before their fixes.

The four-writer lab uses 20 files, three rounds and seed 17. The paired-process
run uses 100 files per writer per round, three rounds, 8 MiB fixtures, seed 1,
45-second phase deadlines and a 0.5-second settle window. Both peers ran on this
Mac; this is not a fresh Mac/Sancho test. All Redis servers and client state were
isolated and disposable. The original installation and existing user data were
not used as test targets.

## Remaining native limitation

The mixed Linux suite (four clients: FUSE/NFS/sync/FUSE; four files, two rounds,
seed 17, 90-second case deadlines) passes all scenarios except reconnect. After
reconnection only NFS misses the new `created-offline` directory entry. Existing
file bytes update on that same client. Other clients see the entry. No cleanup
errors or owned processes remain. This reproduces the branch's documented
pre-existing failure; see [NFS follow-up](nfs-reconnect-followup.md).

That full mixed run preceded the final reader-recovery/CLI snapshot; its native
helper bytes are identical to the final build. The permission smoke and nonroot
Linux build/vet/unit suite were repeated on final source. No new macOS kernel
mount test or Array-capable Redis acceptance is claimed.

## Retained provenance

Canonical source fingerprint (479 Go/module files, sorted string paths with NUL
separators around file contents):
`7c8276d1b944de83aa5f0e7100291b7d660cd492dfa4be262e129a3c63ac0a56`.

| Binary | SHA-256 |
| --- | --- |
| macOS CLI | `ec49b0673e57304f008f6facfd496ee7aba084b3c57eaa7c7183f191749f48eb` |
| macOS helper | `23732108dae0f04fe8ed3cb85a265c3a010cd2690e51f0c4278d15e6c7295373` |
| Linux CLI | `7f02887045416186895518e59c59fc718face462c041eb72bd0b4443be65b149` |
| Linux helper | `d7fcce44d62a57f6fc7b59179f7cf6a555394010684407a9b5114ebaf2bfe3fb` |

Logs: `/private/tmp/afs-integration-{unit,race,e2e,compat,native-deps,python}.log`.
Workloads: `/private/tmp/afs-parity-multiwriter-validation`,
`/private/tmp/afs-parity-paired-validation`, and
`/private/tmp/afs-parity-native-validation`. Final native image:
`sha256:2725d3a59f9630b3ff13e664aa98d87b323b3f2ebf22410ea639cfb447628546`.
