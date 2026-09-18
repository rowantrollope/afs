# Original control-plane compatibility and UI reuse

Assessed 2026-09-17: original `redis/agent-filesystem` at `c3897ac`, current
`rowantrollope/afs` at `8d7bcfa`, and the Redis-prefix change accompanying this
report. Original source was read from `~/git/agent-filesystem`; current source
was compared with `~/git/afs`. No existing Redis database was accessed.

Publication update: this change is integrated with main `1b34090`, which adds
per-file history and its control-plane HTTP contracts. The namespace change now
also covers that history engine. The original discovery evidence below remains
specific to the assessed snapshot; current history behavior is documented in
[file history](file-history.md) and its [compatibility report](file-history-compatibility.md).

## Result

The Redis prefix is now **`afs:`**, as requested. Before this change, the current
CLI used `afs-lite:` and could not discover original records. After the change,
`afs list`, `info` and `cp list` can read original single-tree records. The
original control-plane can also discover newly created current AFS trees.

That is storage discovery compatibility, not complete control-plane integration
or safe mixed-version writing. The current CLI has no HTTP login, discovery,
session or heartbeat client. The original web UI is a useful foundation, but its
backend must use the current storage and checkpoint engine.

**Old Volume maps to current Workspace.** An old Agent Workspace is a separate
composition of mounted volumes, not a file tree. Its records are not returned by
current `afs list`. Reuse the old Volumes pages as the new Workspaces pages;
remove the composition pages and their related workflows.

## What the namespace change covers

All retained Redis key builders and matching patterns now use `afs:`: workspace
metadata/name indexes, checkpoints/manifests/blobs, inode/content/directory data,
root markers, import locks, generation markers, change streams, invalidation
channels, sessions, file locks and per-file history. Both sync and native mount clients use it.
Local config, registry and sync-control directory names remain unchanged.

Existing derivative data under `afs-lite:` is not renamed, copied or discovered
automatically. Deploying this change therefore needs an explicit migration if
that namespace contains data to retain. No migration was performed here.

## Compatibility boundaries

| Surface | Result after prefix change |
| --- | --- |
| Original volume metadata | Readable, including name-keyed legacy trees and opaque IDs with a name index |
| Composed Agent Workspaces | Different records (`workspace:composition:meta`); not converted or listed as trees |
| Checkpoints, manifests and blobs | Retained version-1 formats; compatible layouts and hashing |
| Inodes and contents | Retained key layout and inline/external-string/Array readers; Arrays still need a supporting Redis server |
| Current CLI using old HTTP server | Not implemented; CLI connects directly to its configured Redis |
| Old server discovering new AFS trees | Confirmed through its `/v1/workspaces` API |
| Mounting an old tree | Fails without the newer generation marker; no automatic adoption |
| Concurrent original/current writers | Not safe to assume: original writers ignore generation and revision checks |
| Original active-agent monitor | Current clients do not create its sessions/heartbeats |
| File-version history | Current engine supplies per-file history and compatible HTTP contracts; see the history compatibility report for storage/adoption limits |
| Original search | Removed; no search recording/indexing features |

The current [list operation](../internal/controlplane/store.go) scans
`afs:{*}:workspace:meta`. Its reads preserve unknown legacy metadata in Redis.
However, subsequent writes serialize the smaller current `WorkspaceMeta`:
legacy database, cloud account, region, source and tags fields can be lost.
The original SQL catalog also carries ownership, routing, tokens and sessions;
it cannot be reconstructed completely from current workspace metadata.

The [generation guard](../internal/controlplane/generation.go) requires a
`g_...` value at `afs:{id}:generation`. Original trees have no such key. Simply
adding one while old clients continue writing does not make them obey current
[publication checks](../mount/internal/client/publication.go). Original restore
also replaces roots without current generation fencing.

Missing generation is **not** a general protection against every current
command: deletion can remove a legacy tree and its entire `afs:{id}:*` namespace.
Namespace sharing should not be interpreted as an authorization or version
boundary. Original control-plane GET handlers are not universally read-only,
either: some tree/content/stat paths call `EnsureWorkspaceRoot`, which may
materialize a missing live root.

## Reusing the web UI

Keep the visual design and working content components. The main adaptation is
the product model and API integration, rather than a UI redesign.

| Existing UI | Adaptation |
| --- | --- |
| `/volumes` and its table/create dialog | Become `/workspaces`; rename labels and use current CLI examples |
| `VolumeStudio` Browse and Checkpoints | Retain file tree/viewer, checkpoint selection and diff; rename workspace-facing terminology |
| Current `/workspaces` Agent Profiles page | Remove; it manages volume composition |
| Volume attachment editor, mount paths, bookmarks and composition tokens | Remove with the composition model |
| Database selector and database management | Replace with status for one configured Redis backend |
| Search, embeddings, MCP, templates, cloud/admin/account flows | Remove from the initial slim UI |
| History | Reuse the original history drawer with the current control-plane history handlers; host it in the separate control plane |
| Agents and live monitor | Add later only if presence/session reporting is implemented |

The old frontend already separates the relevant APIs: the content/Volumes list
calls **`/v1/workspaces`**, while composed Agent Workspaces call
**`/v2/workspaces`**. Keeping a small subset of the v1 response contracts would
minimize changes to the file-browser components.

Removing navigation links is insufficient. `DatabaseScopeProvider` fetches
database and agent data and opens an event stream automatically. Slim that
provider, API interfaces, hooks and auth bootstrap together. Rewrite generated
command examples (`afs vol ...` and old checkpoint syntax). The old hosted
checkpoint-create implementation currently throws an unsupported-operation
error; retaining a button does not implement the operation.

Original source anchors, all at the inspected revision:

- UI routes: `ui/src/routes/volumes.tsx:142`, `workspaces.tsx:3`.
- Detail tabs: `ui/src/features/volumes/VolumeStudio.tsx:350`.
- Content/composition APIs: `ui/src/foundation/api/afs.ts:3840`.
- Automatic background requests: `ui/src/foundation/database-scope.tsx:123`.
- Checkpoint-create gap: `ui/src/foundation/api/afs.ts:4237`.
- Read projections/tree APIs: `internal/controlplane/service.go:1842,2252`.
- Diff implementation: `internal/controlplane/diff.go:102`.

## Backend work needed

1. Add an optional HTTP server around the **current** Service/Store and serve
   the adapted built frontend. The existing [lightweight server proposal](lightweight-control-plane.md)
   is still a proposal, not implemented functionality. Do not transplant the
   old DatabaseManager, SQL catalog and composition stack just to serve the UI.
2. Provide workspace list/detail, tree/content (live or selected checkpoint),
   checkpoint list/detail/diff, and backend health/version endpoints. Project
   current metadata into the UI's required counts, sizes, head/dirty state,
   timestamps and capabilities. Extract useful old projection/diff code while
   avoiding old lazy-root mutation paths in read handlers.
3. Route create/import/fork/delete and checkpoint create/restore/delete through
   the current engine. Retain import locks, publication checks, safety
   checkpoints and generation fencing. Browser checkpoint creation captures
   published Redis state; it cannot promise to flush every remote client's
   pending files. The current CLI can explicitly flush its own local mounts.
4. Decide local-only versus remotely accessible deployment. Reusing the old
   account/catalog machinery is unnecessary for a local UI; a remote service
   needs an explicit authentication, authorization and credential model. The
   namespace change adds none of these. CLI integration with server login,
   discovery and credentials is separate from making the browser UI work.
5. Add presence/heartbeat and event delivery only if the Agents/Monitor features
   are required. Reuse the current file-history engine and HTTP handlers for
   history; the CLI provides client actions and does not host an HTTP server.

Suggested delivery: adapted read-only Workspaces/Browse/Checkpoints first,
then mutations and history through the current engine, then optional presence.
This retains the strongest existing UI without restoring the removed platform.

## Adopting existing data

Before writable adoption, stop original writers and the old mutating server.
Preserve storage IDs, live roots, name indexes, checkpoint metadata/manifests,
blobs/refcounts and root markers. Define how legacy metadata is retained, then
initialize generation markers under controlled adoption. Validate cold/warm
mounting, pending live edits, checkpoint restore, multi-writer conflicts and
deletion against copied fixtures. Ongoing mixed-version writes would require
additional protocol work; adoption alone is not that guarantee.

Migration from `afs-lite:` is a different operation: copy or rename the complete
namespace with collision checks against existing `afs:` IDs and names, while
writers are stopped. Do not silently merge the two name indexes. Old compositions
need an explicit per-volume mapping or a separately designed flattening process.

## Verification

Build and vet passed. Unit and race checks each covered 753 passing test/subtest
cases, including the new legacy-discovery regressions; four optional Array cases
were skipped without an Array-capable server. All 120 isolated real-Redis
process test/subtest cases passed.

A separate disposable Redis process and a copy of the original control-plane
binary (`c3897ac`, verified by Go build metadata) exercised both revisions:

- Original server created a content tree and a separate composition.
- Previous CLI listed `[]`; updated CLI listed the content tree only.
- Updated `info` and `cp list` read original records successfully.
- Updated mount failed with `afs: redis: nil` for the missing generation marker.
- Redis key/value snapshots were identical before and after those read commands
  and failed mount; no mount directory was created.
- Updated CLI created a tree entirely under `afs:` with its generation marker.
- Original `/v1/workspaces` listed both trees; `/v2/workspaces` listed only the
  composition. This confirms discovery, not all browser flows or write safety.

Runtime script/results, copied binary and validation logs:
`/private/tmp/afs-old-compat-dwPeTW/`. Test-owned processes were terminated.
The original checkout, installation, configured databases and user data were
not changed. No UI implementation or data migration is included in this change.
