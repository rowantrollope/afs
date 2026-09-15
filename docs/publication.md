# File publication and recovery

AFS retains the original Redis inode, directory-entry, external content and
chunk formats, plus the existing synchronization and conflict machinery.
Multiple clients remain writable. These changes close failures reproduced
while extracting the original `c3897ac` baseline.

## Publication guarantees

- A file upload changes the published content and inode metadata together.
  Dirty chunks are applied to a temporary Redis key; a single Redis operation
  replaces the live content only after staging completes. Redis `COPY` keeps
  unchanged chunks on the server, preserving delta-transfer bandwidth.
- File mutations compare the current path, inode identity and publication
  revision. Callers bind the candidate to a prior remote stat observation: sync captures
  it before comparing remote bytes; the CLI captures it before reading candidate
  input. A competing update returns a conflict instead of silently overwriting
  it. The existing sync conflict/reconciliation path retains the local candidate.
- Each publication has an opaque operation identity. A lost response is checked
  against that identity; retrying the same Redis operation cannot replay its
  counters or overwrite a later publication. A caller retry against an obsolete
  observation fails. Identical competing creates can still succeed as an
  already-published result.
  Empty-file writes also consume staging; replay after a peer deletes the
  committed file fails instead of recreating it.
- File content, deletion and metadata changes append to the existing change
  stream and notify subscribers in the same Redis operation. Directory creation
  and rename queue these notifications in their existing transactions.
  An interrupted publisher cannot leave committed file bytes without a durable
  reconnect event. Invalidation delivery remains at least once; repeated
  notifications do not represent repeated file mutations.
- Managed operations carry the workspace generation captured at mount startup.
  Restore and deletion change that generation. Publication, deletion, metadata,
  directory/symlink creation and rename reject a stale generation at the Redis
  mutation boundary. An old mount cannot recreate a missing managed root.
- Journal catch-up is nonblocking once its cursor reaches the end. Subscription
  reconnection retains the existing replay/full-reconciliation behavior.

## Why additional code was necessary

The original chunk writer updated live content before its metadata pipeline.
A deterministic interruption after one range write exposed mixed old/new bytes.
The extracted code therefore stages those same range operations and publishes
through a small conditional Redis script. This adds an inode revision and
expiring staging keys, without replacing the storage format or sync algorithm.

A second baseline regression committed bytes, then failed the separate journal
append. Moving the existing stream append and notification into publication
closes that crash window. No second journal or background service was added.
A third regression showed that the original catch-up call waited indefinitely
for a future event; setting the existing Redis read to nonblocking fixes it.

The generation marker belongs to workspace lifecycle management. Native clients
without a generation remain available to isolated tests and internal setup;
user-facing commands and mounted daemons supply one.

## Verification performed

On macOS arm64, Go 1.26.1, disposable Redis 8.6.2:

- Original CLI, worktree, control-plane and native client tests passed, including
  the race detector, before extraction. Optional Redis Array and PostgreSQL
  tests were skipped because their dedicated servers were not configured.
- The interrupted-chunk, missing-journal and blocked-catch-up regressions failed
  against the original baseline and pass with the fixes.
- Real-Redis tests cover concurrent publication, lost acknowledgement followed by
  a newer writer or deletion, live same-token retries, empty-file chunk paths,
  conditional delete versus edit, complete chunked creation,
  and generation rotation immediately before put/chunks/delete/rename/chmod/
  mkdir/symlink mutations.
- Preserved native tests and focused strict-save, recovery and uploader tests
  passed after removing rich editing, search and native-driver-only operations.

Reproduce the focused tests with:

```sh
go -C mount test -count=1 ./internal/client
go -C mount test -race -count=1 ./...
go test -count=1 -run 'TestSyncSaveEngine|TestSyncRecovery|TestSyncUploader' ./cmd/afs
```

The final repository validation and multi-process acceptance results are
recorded in the simplification report. The tests above do not establish Redis
Array or other-platform support; the optional Array suite remains available.

## Costs and limits

Staging temporarily consumes another copy of the file in Redis. Abandoned
staging keys expire after one hour; successful publication keeps the content
without an expiry. Atomic publication applies to one file or namespace mutation,
not an application-wide or distributed filesystem transaction. Checkpoints
retain their documented local-flush and published-remote-state scope.

Brief 100-operation local benchmark samples retained the create fast path:
0.736 ms/create before extraction, 0.726 ms after these fixes. Batched metadata
updates were 0.137 ms before and 0.156 ms after, versus 0.429 ms for separate
metadata updates in the derivative. These short samples are smoke checks, not
statistical performance claims or estimates for remote Redis latency.
