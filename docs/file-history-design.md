# Clean implementation of optional file history

Analysis requested 2026-09-16, based on AFS `ac31fda` and upstream `c3897ac`.
This feature is not implemented by the current audit follow-up. The accompanying
import/mount/chmod fixes are separate implementation work.

## Value and scope

Checkpoints recover a whole tree at deliberately chosen points. File history
recovers the version an agent overwrote or deleted **between** checkpoints,
without rolling back unrelated files. Conflict copies preserve competing edits;
they do not retain a sequence of ordinary, nonconflicting edits.

The useful initial scope is optional history for regular files and symlinks,
rename lineage, deletion tombstones, retrieval of historical bytes/targets and
bounded retention. Keep it off by default, as upstream did. Track only mutations
that AFS actually publishes; neither folder sync nor this feature can promise to
capture every transient local write.

Use the existing Redis/workspace machinery. No HTTP service, cloud identity,
MCP/search, new always-running control-plane process or public volume model is
needed. The history policy must be stored **per workspace in Redis**, so all
writers agree; a setting in one client's local config is insufficient.

## What can be reused

The principal upstream implementation is about 2,855 production lines across
`file_versions.go`, `file_version_runtime.go`, `file_version_actions.go`,
`file_history.go`, `file_version_retention.go`, `file_version_diff.go`,
`versioning_policy.go` and `mount_version_observer.go`, plus tests and integration
in the larger service/CLI. This is a useful implementation base, not a forecast
of how many lines AFS should add.

| Piece | Recommended treatment |
| --- | --- |
| Policy validation and path filters | Retain `off/all/paths`, include/exclude rules and explicit limits; adapt workspace lookup. |
| Lineage/version metadata | Retain stable file IDs, monotonically ordered versions, old/new paths, modes, targets and deletion records. |
| History pagination and selectors | Adapt the existing implementation; bounded reads, no whole-workspace scan for one file. |
| Restore/undelete tests | Reuse as behavioral requirements, particularly rename, replaced destinations and independent forks. |
| Blob storage | Reuse immutable checkpoint blobs where ownership permits; define history references explicitly. |
| Upstream post-write observer | Useful contract/reference, insufficient as the durability boundary. |
| Web/session/account attribution and rich diff UI | Leave out. Existing mount/client origin and operation source are sufficient initially; normal diff tools can compare recovered files. |

Keep workspace history policy/query orchestration in `internal/controlplane`,
where workspace/checkpoint operations already live. Put any shared history key
and record definitions needed by publication in a small internal leaf package,
avoiding a client→controlplane→client import cycle. Lifecycle stays in `cmd/afs`.

## The main engineering work: capture at publication

Simply reconnecting `NewWithObserver` would be incomplete:

- Current whole-file methods commit live bytes before calling the observer. A
  process exit in between can lose the history entry even when the write succeeded.
- Observer snapshots load whole file contents, adding reads and allocations.
- The newer inode range/append/truncate path in `native_range.go` publishes
  directly and does not use that observer. Native workloads would be missed.
- Reading a path after publication can observe a peer's subsequent value. Version
  metadata must refer to the exact accepted revision, not whichever bytes are
  visible by the time an asynchronous observer runs.

Code references: [whole-file post-commit observer](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/mount/internal/client/native_core.go#L986),
[native range publication](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/mount/internal/client/native_range.go#L147),
[existing atomic publication](https://github.com/rowantrollope/afs/blob/ac31fda403bcc1bfd0733968954dc1679f603d76/mount/internal/client/publication.go#L83).

The recommended invariant is: **an accepted tracked mutation also commits enough
immutable content and metadata to recover its promised history after a client
crash.** A conflict/rejected mutation creates no successful version. An exact
transport retry creates no duplicate version and cannot replay a deleted file.

Extend the existing staged/conditional commit path rather than introducing a
second unguarded write route. Prepare immutable history payloads outside the
short commit section; bind them to the same inode revision, parent identity,
workspace generation and native lease as the live publication. Commit the durable
history reference/record along with the live change. Cover deletion, rename,
symlinks and mode updates as well as full/chunk/range writes.

Redis scripts support atomic conditional updates; expensive snapshot preparation
and retention scans should stay outside the short script because execution
blocks other server work. This fits the existing AFS staging design.
[Redis scripting documentation](https://redis.io/docs/latest/develop/programmability/eval-intro/)

Atomic execution is not rollback on a later command error. Preflight history key
types, counters and other fallible conditions before changing live content, as
today's publication script already does for the change-stream type. Add
wrong-type, counter-overflow and injected-error regressions so an error cannot
leave the new live bytes without their promised history.
[Redis transaction error semantics](https://redis.io/docs/latest/develop/using-commands/transactions/)

A later indexing/promotion step may be asynchronous **only if** the accepted
commit already pins the exact immutable payload and a recoverable descriptor.
Do not use the current approximately 10,000-entry invalidation stream as the
sole history store: it is trimmed and does not hold recoverable file bodies.
Committed history must not inherit the one-hour TTL of abandoned upload stages.

The first spike should choose between synchronous immutable-blob preparation
and preserving a server-side snapshot of the replaced content for later
content-addressed promotion. Measure string **and Array** behavior, Redis memory,
server blocking time and large-file amplification before choosing. Do not assume
snapshotting every native 4-KiB write is cheap.

## Data model and lifecycle decisions

Use the existing `afs:{workspace-id}:...` namespace/hash tag for related
atomic keys. Reuse the upstream lineage/version shape, with immutable version
records, per-file ordinal indexes, path-to-lineage history and a retention index.
This preserves the current key locality; it does not itself add Redis Cluster
client/discovery support.

Five decisions need explicit semantics and tests:

1. **Enablement and baseline.** History starts when enabled. Capture an existing
   file's pre-change value before its first tracked overwrite or deletion; a
   lazily initialized lineage must not miss that first deletion. A full backfill
   is a separate optional operation. Store a policy revision and enforce policy
   changes at publication, not only in stale client-side caches.
2. **Rename and recreation.** A file keeps its lineage across rename. Replacing
   another file must preserve the displaced destination's recoverable history.
   Deleting and then creating a different file at the same path creates a new
   lineage. Retain the inode/parent conditions that already prevent stale renames.
3. **Native write granularity.** Capturing every accepted write is clear but can
   multiply storage for append/truncate workloads. Coalescing at close/time windows
   is a different guarantee and is difficult with multiple handles, writers and
   crashes. Start with an explicit, bounded published-mutation guarantee; treat
   coalescing as a later measured optimization, not an undocumented shortcut.
4. **Fork/checkpoint restore.** Preserve upstream's independent fork history and
   policy if full parity is required. Copy/pin history ownership before exposing
   the fork. AFS can fork an arbitrary older checkpoint: copying today's source
   lineage heads blindly can conflict with the older selected file contents.
   Define whether the fork copies a history subset or retains all history and
   records the selected snapshot as its new live head; test the first subsequent
   edit and visibility of post-checkpoint source versions. Whole-tree restore
   must preserve pre-restore history, fence writers,
   and rebuild lineage mappings despite recreated inode IDs. Do not clear history
   just because the live-root namespace is reset. These bulk paths bypass ordinary
   file-write hooks and require their own integration.
5. **Older clients.** Enabling history requires all writers to implement the new
   contract. Generation changes stop active stale clients, but old binaries can
   remount and do not understand a newly added history capability field. Define
   an enforced protocol/format transition or a controlled all-writer upgrade
   requirement; do not claim transparent mixed-version completeness. Test it.

## Retention needs real ownership accounting

Reusing upstream retention removes history metadata, but AFS currently retains
checkpoint blob bodies conservatively and has no garbage collector. Removing a
history index entry therefore must not be presented as reclaimed Redis memory.

For bounded **physical** storage, history needs its own explicit blob ownership
or references shared correctly with checkpoints, forks and current content.
Cleanup must never delete a body still referenced by any of them. Apply per-file
count/age limits and workspace byte limits in bounded batches with a lease/CAS
guard so writers and concurrent cleanup cannot undercount or double-delete.
Pin in-progress fork/restore/retrieval operations as well as durable references.
No filesystem mutation should perform an unbounded retention scan.

Document whether a byte limit counts logical version bytes or unique retained
payload bytes. Deduplication makes these different. Preserve at least the required
head/baseline, and decide whether inability to retain it rejects a tracked write
or creates an explicit, observable coverage gap. Recommend rejecting a tracked
write if its guaranteed history cannot be committed; ordinary history-off writes
keep today's behavior. Explicit large-file exclusions are acceptable, but must
be visible as exclusions rather than implying those bytes are recoverable.

This is a significant part of the work. An initial release can conservatively
retain orphan bodies, matching upstream's logical retention behavior, but then
it cannot claim a hard physical storage budget. Upstream's trimming also removes
metadata/index entries and decrements logical byte accounting without deleting
the shared blob body. Physical reclamation is an additional product decision,
not a prerequisite for parity with that upstream behavior.

## Recovery UX without restoring the old `fs` surface

Provide a small root-level history/recovery surface consistent with the current
CLI. Exact spelling can be settled during implementation; no new nested command
family or remote editing suite is required.

The safest first recovery action writes the selected version to a new local
destination. The user can inspect/compare it and place it into a mounted tree
using ordinary tools. That reuses today's multi-writer conflict/publication path
and avoids quietly replacing a peer's current file.

Direct in-place historical restore/undelete, if required for parity, needs a
generation-pinned expected-current-revision condition (or expected absence for
undelete). A changed/recreated destination must fail or preserve the candidate,
never be silently overwritten. A recovery is itself a new version and leaves
explicit checkpoints unchanged. Symlink targets must be restored as symlinks;
retrieval must not follow them into an unrelated local path.

## Delivery plan and acceptance

| Stage | Deliverable | Main acceptance gate |
| --- | --- | --- |
| 1 | Extract policy/types/lineage queries and agree enablement, limits and compatibility | Upstream default-off, rename/path and selector tests; migration/old-client behavior |
| 2 | Integrate durable capture with all current publication paths | Faults before/after commit, lost acknowledgement, simultaneous writers, range/chunk/Array operations; exact bytes |
| 3 | Add history browsing and safe local recovery | Binary/empty/symlink round trips, deletion/recreation, renamed/replaced destination; ordinary mounted publish of recovered content |
| 4 | Integrate fork, checkpoint restore, workspace deletion and bounded cleanup | Independent ownership, crash during bulk lifecycle, cleanup under writers, actual Redis memory/reference checks |
| 5 | Run full acceptance and performance comparisons | Two independent clients plus mixed native/sync, partitions/restarts, history off overhead, enabled write/append/large-file costs |

This is a medium-sized storage feature with a substantial correctness surface,
not a flag-and-hook restoration. The reusable upstream logic makes policy,
queries and metadata relatively straightforward. The effort is concentrated in
atomic capture, native range-write costs, mixed-version behavior and safe
retention. A precise calendar estimate should follow the Stage 2 snapshot/capture
spike; quoting one before choosing that mechanism would conceal the main risk.

Recommend implementing this as a separate sequence of focused changes after the
current fixes. Ship history off by default with explicit bounded policy, preserve
the existing publication guarantees, and defer rich diffs/UI and background
optimization until capture/recovery/ownership have reproducible failure tests.
