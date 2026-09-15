# Simplification report

## Provenance

AFS is extracted from `redis/agent-filesystem` commit
`c3897ac05265444568a3819c21728a38a4ed254b` (tracked source only). The original
checkout, untracked work, binaries, configuration, processes and Redis data were
not changed. `rowantrollope/afs` began at `a91598c` with a `.gitkeep`; that history
is preserved. The upstream AGPL-3.0 license is retained in LICENSE.

## Inventory before extraction

| Component | Original location | Action and reason |
| --- | --- | --- |
| CLI dispatch and composed workspaces | `cmd/afs/main.go`, `workspace_composition_commands.go`, `volume_commands.go` | Replace dispatch with six command groups; remove composition and exposed volumes |
| Folder sync | `cmd/afs/sync_daemon.go`, `sync_watcher.go`, `sync_event_reconciler.go`, `sync_{uploader,downloader,full_reconciler}.go` | Retain native events, streaming changes, chunk deltas, bounded queues and warm reconciliation |
| Recovery and conflicts | `cmd/afs/sync_{recovery_plan,conflict,directory_modes,state}.go` | Retain baselines, tombstones, conflict copies and mode restoration; add demonstrated safety regressions |
| Flush | `cmd/afs/sync_save_{engine,mutations,service}.go`, `sync_control.go` | Retain worker-generation draining, actual-byte verification, save receipts and failure recovery; wire checkpoint/unmount |
| Daemon and registry | `cmd/afs/sync_lifecycle.go`, `mount_state.go` | Retain re-exec, private bootstrap, readiness and atomic registry writes; remove managed sessions and native modes |
| Redis filesystem client | `mount/client/`, `mount/internal/client/` | Retain shared storage, invalidation and chunk interfaces; remove native drivers and search projections |
| Workspace/checkpoint storage | `internal/controlplane/{store,checkpoint_commit,blob_writer,import_lock,workspace_root,workspace_root_manifest}.go` | Retain manifest/blob engine and import coordination; extract core service methods |
| Scan/materialization | `internal/worktree/` and `cmd/afs/workspace_mount_bridge.go` | Retain parallel hashing, pipelined breadth-first reads, metadata and symlink support |
| Native mounts | `mount/cmd/`, `mount/internal/{afsfs,nfsfs}`, `third_party/go-nfs/` | Remove FUSE/NFS and their dependencies |
| Product services/integrations | `cmd/afs-control-plane`, UI, SDKs, MCP, sandbox, provisioning, query packages, templates/plugins/examples | Exclude from derivative |

## Remaining private machinery

The package named `internal/controlplane` now implements only local Go workspace
and checkpoint operations against Redis. There is no HTTP server, tenancy,
account management or control-plane process. The `mount` directory retains only
the shared Go Redis client; it implements no operating-system mount. The nested
Go modules were collapsed into the root module so standard root commands test
the retained implementation.

The original opaque workspace storage ID owns one inode tree, its manifests and
blob references. This is the former one-tree storage primitive, used directly;
there is no volume catalog or workspace composition subsystem. The small
`afsStore` adapter preserves the sync engine's existing interface.

All Redis keys are separated under `afs-lite:`. Local configuration defaults to
`~/.config/afs-lite/config.json`, state to `~/.afs-lite`, and control files to
`.afs-lite-sync` inside a mount. No migration or compatibility access to the
original installation is attempted.

## Additions and their reasons

- Thin CLI parsing/wiring: the old CLI routed through composed workspaces,
  cloud sessions and rich file tools. Underlying file, import, checkpoint and sync
  operations are reused. Recursive deletion adds a postorder traversal over the
  existing conditional delete operation; the original primitive removes only
  empty directories.
- Authenticated status/shutdown/detach operations extend the existing file-based
  save control transport. This is needed for flush-before-unmount and safe daemon
  identification. Registry and root file locks prevent competing local owners.
- A per-client state directory separates different roots, Redis databases and
  workspace IDs. Tokens and connection credentials are never printed in status.
- Checkpoint deletion is new because upstream had no delete operation. It uses
  the retained blob-reference machinery and refuses deletion of live heads.
- Lifecycle generations fence deleted/restored workspaces, including client
  restarts. A changed saved generation requires a fresh local directory so old
  baselines cannot silently republish old content.
- Publication staging and conditional commit address reproduced interrupted
  chunk writes and concurrent publication. They retain the stored inode/content
  format and chunk delta transfer; temporary staging keys expire.

## Demonstrated baseline safety findings

Baseline tests passed before deletion. Focused fault injection then reproduced:

1. A missing local root could be scanned as an empty tree and delete remote files.
2. Interrupted chunk publication could expose mixed old and new file bytes.
3. A concurrent same-path create without a baseline could overwrite a competing
   remote file.
4. Warm reconciliation of an edited local file against a remote deletion could
   remove the edited local bytes.
5. A crash between file commit and separate journal append could leave no durable
   reconnect event for published bytes.
6. Journal catch-up could wait indefinitely for a new event after reaching its end.
7. A full reconciliation between observing a remote deletion and applying it
   locally could mistake the pending local copy for a new file and re-upload it.
   A deterministic test reproduced this on both the original and derivative.
   The live baseline is now retained until local deletion completes.

Regression tests and narrow changes address these cases using the existing
conflict-copy and recovery policy. Historical upstream notes were used to find
relevant tests, not as evidence of unfixed defects.

## CLI acceptance corrections

Testing the extracted wiring found and corrected several issues before delivery:

- Cold materialization replaced the local owner lock. It now preserves the
  reserved control directory and restores supported root permissions.
- An established mount with remote changes was rejected on restart. Successful
  mount state now records generation and root identity so warm recovery is
  allowed only for the established directory. A substituted populated directory
  is rejected before Redis access.
- `fs rm --recursive` needed a traversal around the original empty-directory
  remove primitive. It now observes the tree first, removes children before
  parents, and rejects competing updates through the retained conditional API.
- Local checkpoint flush matching treated different Redis usernames as separate
  databases. It now matches normalized endpoint/database/workspace identity.
- Initial hydration could classify `.afsignore` and excluded local files as an
  empty directory and remove them. The CLI regression reproduced this loss.
  Bulk hydration now requires a root containing only control metadata; the
  retained warm path preserves other local entries and applies ignore rules.
- Managed mounts reject authoritative bulk root replacement; restore cannot
  erase their pending local edits while a manifest is being read.
- Linux CI exposed Redis 7 serializing a zero-byte decrement as `-0`, which
  aborted empty-file deletion before its change event. A regression reproduced
  the failure on Redis 7.0.15; skipping that zero counter adjustment fixes it.
  Full unit/race and both CLI suites then passed on Redis 7.0.15. The focused
  deletion tests also passed with the race detector on Redis 8.6.2.
- An exact transport retry of a committed empty-file creation could recreate
  the file after another client deleted it. Every publication now consumes its
  staging key, including zero-byte writes; live same-token retries remain
  idempotent. Tests cover both cases, counters, and empty-file chunk operations.

These are extraction/lifecycle acceptance findings, not claims that every issue
was present in the original CLI. [The CLI comparison](cli-compatibility.md) maps
old commands to the reduced surface and records intentional differences.

## Verification

Baseline on macOS arm64, Go 1.26.1, isolated Redis/miniredis:

- `go test ./cmd/afs ./internal/controlplane ./internal/worktree`: passed.
- In `mount/`, `go test ./internal/client`: passed with real disposable Redis.
- The same selected packages passed `-race`.
- Results: 682 CLI/sync, 190 control-plane, 5 worktree, 60 client test/subtest
  passes. Five optional feature/environment tests skipped; these are not claimed
  as verification. Raw logs: `/private/tmp/afs-baseline-results`.

Derivative checks on the final production code, macOS arm64 / Go 1.26.1 /
disposable Redis 7.0.15:

- `go build ./...` and `go vet ./...`: passed.
- `go test -json -count=1 ./...`: 396 test/subtest passes; three optional Redis
  Array tests skipped because their dedicated server was not configured.
- `go test -race -json -count=1 ./...`: 396 test/subtest passes; the same three
  optional Redis Array tests skipped. No race reports.
- `GOOS=linux GOARCH=amd64 go build ./...`: cross-build passed. This is compilation
  evidence, separate from executing the Linux test suite.
- `go test -tags=integration -timeout=15m -count=1 -json ./tests/e2e`: passed
  in 27.875 seconds. Fifteen top-level tests, 135 test/subtest passes, zero skips
  or failures. This builds a fresh binary and includes all reduced commands,
  offline help, 50 invalid/confirmation cases, config precedence, exact bytes/JSON,
  and independent-process synchronization/recovery.
- `AFS_BASELINE_BINARY=/path/to/afs-prior go test -tags compatibility -count=1 -v
  ./tests/compat`: both actual CLI binaries passed the paired workflow; nine
  test/subtest passes, zero skips. The recorded package run took 4.997 seconds.
  [Command mapping and reproduction](cli-compatibility.md).
- Linux CI passed build, vet, unit, race and the real-Redis process suite on
  production commit `e983f4d`:
  [verification run](https://github.com/rowantrollope/afs/actions/runs/34916337513).
  No skipped optional test is counted as a pass.

Production Go source fell from 258 files / 91,591 physical lines in the immutable
baseline archive to 64 files / 18,400 lines, approximately an 80% reduction.
The comparison includes comments and excludes test files. The original checkout
still has commit `c3897ac` and only its pre-existing untracked work.

## Performance smoke checks

Same local M4, Go 1.26.1, macOS arm64. These are short smoke samples; cache and
concurrent test load differ, so they do not establish a speedup.

| Retained manifest scanner | Baseline | Derivative |
| --- | ---: | ---: |
| 512 files, serial | 18.53 ms | 9.15 ms |
| 512 files, parallel | 8.13 ms | 5.57 ms |
| 4,096 files, serial | 118.20 ms | 96.35 ms |
| 4,096 files, parallel | 48.77 ms | 39.90 ms |

Command: `go test -run '^$' -bench 'BenchmarkBuildManifest_(Small|Medium)_' -benchmem -benchtime=3x -count=1 ./internal/worktree`.
The retained parallel path remains materially faster than serial in both samples.
No large scanner regression was observed.

Representative independent-process smoke on 1,000 files against disposable
Redis 8.6.2 with AOF
`appendfsync always`: import 142 ms; two initial mounts 381 ms; one small change
reaching the other client 155 ms; checkpoint after sync 2.22 s. Two idle clients
averaged 1.0 Redis commands/second over two seconds. These values are local
observations, not a remote-network latency or capacity promise. Native publication
microbenchmarks and their staging-memory tradeoff are in [publication.md](publication.md).

## Retained tuning and limitations

Optional configuration retains the original sync file-size cap and watcher queue:

```json
{
  "redis": "redis://localhost:6379/0",
  "sync": {"fileSizeCapMB": 2048, "watcherQueueCapacity": 1024}
}
```

A nonpositive file cap selects the original 2 GiB default. Zero queue capacity
selects 1,024; the accepted range is 0 through 1,048,576. `AFS_IMPORT_WORKERS` tunes
the existing manifest scanner; its default is the available CPU count. Redis's
own storage limits still apply. The optional Redis Array backend is retained but
not verified without its dedicated server. No original config file is read.

- macOS is locally verified. Linux passed the CI verification recorded above.
  Windows is unsupported.
- Sync retains regular files, directories, symlinks, mode handling, `.afsignore`
  and the original built-in ignores. It does not capture every transient write.
- A file publication is atomic; recursive CLI deletion is a sequence of
  conditional removals. A concurrent change can stop it after earlier removals.
  Symlinks are removed as links, never traversed by recursive deletion.
- Checkpoint creation flushes registered local mounts, then snapshots published
  remote state. A concurrently arriving remote change can cause an honest flush
  rejection; allow clients to settle and retry. It is not a distributed snapshot.
- Restore/delete fencing makes stale mounts fail. Preserve local edits and mount
  into a new directory after restore. Live authoritative root replacement is
  rejected rather than overwriting pending local content.
- Checkpoint deletion updates references but conservatively retains blob bodies.
  There is no background garbage collection. Staging keys expire after one hour.
- Local mounts match by normalized network endpoint, database and workspace ID,
  independent of credentials. Different DNS aliases for the same Redis server
  are separate endpoints; use a consistent endpoint or explicitly flush/unmount
  the other registrations first.
