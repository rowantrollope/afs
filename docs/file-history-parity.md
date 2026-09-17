# Original per-file versioning contracts and comparative evidence

The original examined source is `redis/agent-filesystem` commit
`1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e`, from a read-only inspection of the
original checkout. The comparison runner archives that revision into a separate
temporary directory; it never changes the original installation or uses existing
user Redis data.

## Capability checklist

The original has storage, service, CLI and web UI layers. Preserving only storage
or providing local exports does not cover its full per-file versioning surface.

| Capability and observable contract | Original implementation reference | Acceptance requirement |
| --- | --- | --- |
| Default-off workspace policy; all/paths modes; include/exclude patterns; count, age, logical byte and large-file limits | [versioning_policy.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/versioning_policy.go) | Shared policy with immediate enforcement, path.Match-compatible segment syntax and ** directories; explicit bounded-input validation |
| Ordered immutable version IDs, per-lineage ordinals, old/new paths, type, mode, size, hashes, size delta, actor/source and checkpoint metadata | [file_versions.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_versions.go#L30) | Preserve API fields and retain equivalent provenance where available; no invented user/session identity |
| No new version for identical contents and mode; mode-only version reuses content-addressed body | [file_version_runtime.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_version_runtime.go#L135) | Avoid identical-write and metadata-change storage amplification while retaining intentional restore events |
| Rename keeps lineage, replacement preserves destination history, delete records tombstone, recreation gets a new lineage | [file_versions_test.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_versions_test.go) | Prove actual accepted rename, replaced destinations and recreation; folder-sync inferred delete/create remains distinct |
| Ascending/descending path history, grouped across incarnations, opaque cursors | [file_history.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_history.go#L14) | Keep compatibility response shape and cursor/order options while bounding underlying reads |
| Content selected by global version ID or file ID plus ordinal, symlink targets, tombstones and binary indicators | [file_history.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_history.go#L227) | Preserve browser response metadata, add exact binary/local export without confusing omitted bodies with empty files |
| Unified text diff between versions or against checkpoint head / working copy; binary marker | [file_version_diff.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_version_diff.go) | Head means the head checkpoint, with live fallback when that checkpoint lacks the path; working-copy means live content |
| Restore selected version into live file, creates new version, checkpoints unchanged | [file_version_actions.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_version_actions.go#L43) | Provide in-place recovery with expected-generation/current-revision fencing, plus safe local export |
| Undelete newest deleted lineage by default or selected historical version; recover latest non-tombstone; reject a live destination | [file_version_actions.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_version_actions.go#L172) | Preserve lineage and reject unexpected destination recreation rather than overwrite a peer |
| Large-file cutoff retains metadata and content hash but omits the body | [file_version_retention.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_version_retention.go#L121) | Show an explicit unavailable-content version, retain prior recoverable payload, never imply an empty body |
| Count/age/workspace logical-byte trimming; current head retained | [file_version_retention.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_version_retention.go) | Bounded cleanup, actual body ownership and reclamation, newest recoverable content survives deletion |
| Fork copies policy, IDs, indexes and historical bodies independently | [file_versions.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/file_versions.go#L921) | Preserve independent ownership and reconcile older selected checkpoint state; reject corrupt/missing source bodies |
| Whole-checkpoint restore records file changes and checkpoint attribution | [service.go](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/internal/controlplane/service.go#L1507) | Keep pre-restore histories and lineage across recreated inode IDs, failures/retries and disabled capture |
| History drawer loads 50-version pages, groups lineages, selects content by ordinal, diffs against head, restores and undeletes; actor-linked recent activity | [file-history-drawer.tsx](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/ui/src/routes/workspace-studio/-file-history-drawer.tsx) | Equivalent browser-callable contracts; compatibility does not mean restoring cloud identity, public volumes or unrelated UI infrastructure |

Checkpoint references are record annotations. The original SavepointMeta does
not contain a separate VersionIDs registry. Native mount observer records only
the mount source; sync uploader passes session/agent/user/source. Metadata fields
must remain optional when the corresponding context does not exist.

## HTTP contract used by the original drawer

The original client uses both unscoped `/v1/workspaces/{workspace}` and
database-scoped workspace base paths. The relevant requests are:

| Request | Parameters/body | Response |
| --- | --- | --- |
| GET `/files/history` | `path`, `direction=asc|desc`, `limit`, `cursor` | `workspace_id,path,order,lineages,next_cursor`; each lineage has `file_id,state,current_path,versions` |
| GET `/files/version-content` | `version_id` or `file_id,ordinal`; client also sends `path` | `workspace_id,file_id,version_id,ordinal,path,kind,source,content,target,binary,encoding,content_type,language,size,created_at` |
| POST `/files/diff` | `{path,from,to}`; each selector uses `ref`, `version_id`, or `file_id,ordinal` | `workspace_id,path,from,to,binary,diff` |
| POST `:restore-version` | `{path,version_id}` or `{path,file_id,ordinal}` | Workspace/path/new file/version IDs plus `restored_from_version_id,restored_from_file_id,restored_from_ordinal` |
| POST `:undelete` | `{path}` with optional version selector | Corresponding `undeleted_from_*` fields |
| GET / PUT `/versioning` | Full policy for PUT | `mode,include_globs,exclude_globs,max_versions_per_file,max_age_days,max_total_bytes,large_file_cutoff_bytes` |

The original mapper and calls are in
[afs.ts](https://github.com/redis/agent-filesystem/blob/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/ui/src/foundation/api/afs.ts#L3738).
The content endpoint resolves version IDs globally within a workspace and does
not reject the old path after a rename. The original diff endpoint instead
requires a historical operand's recorded path to match the requested path; a
more permissive same-lineage implementation is a deliberate extension.

## Original failure behavior that should improve

The observer runs after live publication. Process exit before that observer
records history can leave accepted content without its version. The original
runtime also skips the first deletion of an existing but untracked file, and
one-version retention can retain only the tombstone after deletion, leaving
undelete with no recoverable version. Trimming removes metadata and logical-byte
accounting while keeping original shared blob bodies indefinitely.

Once a lineage is tracked, the original runtime bypasses path-policy selection
on later writes; disabling history can therefore continue recording that
lineage. Enforcing the current shared policy at publication is an intentional
correction. The original pagination implementation also reads the full path
history before returning one page; bounded pagination is an improvement.

## Reproducible comparison

Run [the comparison harness](../tests/history_compare/README.md) against the
pinned original and the final new working source. Both implementations use
enabled history, identical client operations and payloads, the same Redis
executable/settings, separate freshly started localhost servers and no server
persistence. The report retains raw per-run timings, memory estimates,
recoverability checks, source hashes and environment versions.

## Paired measurements

5 repetitions ran on each of standard Redis 7.2.5 and the experimental Array
build `255.255.255` (`ead90ea0`). Each implementation/repetition/scenario used a
fresh owned server with persistence disabled, identical payloads and matching
enabled-history workloads. There were 240 scenario executions without
harness failures. These are local Apple arm64 microbenchmarks, not remote,
production, fsync or large-workspace capacity measurements. The table reports the
median of the per-run median operation times, including history handling.

| Workload | Original / new, standard Redis (ms) | Original / new, Array (ms) |
| --- | ---: | ---: |
| Forty distinct 64 KiB rewrites | 1.679 / 0.914 | 1.868 / 0.755 |
| Forty identical 64 KiB rewrites | 2.925 / 0.914 | 3.167 / 0.805 |
| Forty permission changes | 1.631 / 0.668 | 1.723 / 0.646 |
| Forty 64 KiB rewrites, retain five | 1.180 / 0.988 | 1.353 / 0.924 |
| Sixteen 64-byte appends to a 1 MiB file | 4.731 / 2.303 | 6.060 / 2.759 |

Across these timed scenarios, the new version was 1.19–3.93 times faster.
The concurrent-writer scenario is excluded from speed comparisons because the
implementations did not acknowledge the same number of writes.

With five-version retention, summed workspace `MEMORY USAGE` changed from
4,153,440 to 632,448 bytes on standard Redis (84.8% less), and from
4,125,164 to 479,356 bytes on Array (88.4% less). History payload memory fell
87.8% and 90.8%, respectively. The principal
improvement is reclamation: original pruning removed version metadata while
retaining immutable blob bodies. Both implementations suppressed identical
versions and shared one body across the 41 mode-change records.

The new version is not consistently smaller. Unlimited-history standard Redis
workloads used up to 6.5% more workspace memory.
The Array append case used 21,360,982 bytes
versus 19,594,306 bytes
(9.0% more),
and its history payload memory was
9.6% more.
These estimates are retained key memory, not peak server RSS; staging, allocator
behavior and persistence add costs. Full snapshot/hash work remains significant
for small appends to large files. Logical byte limits remain per-version
accounting, separate from physical sharing and Redis overhead.

Both backends used production Go/Lua fingerprint
`049f82f3231c87136e85e5e8511c467bf3892c785618a7aa0ad5d7aa646da0c5`. Recorded host: `macOS-15.8-arm64-arm-64bit-Mach-O`;
toolchain: `go version go1.26.1 darwin/arm64`. Both servers used the libc allocator. Implementation order alternated
by repetition, and heavy validation paused during measurement. Raw timing rows,
tail latencies, source/harness hashes and API fields are retained in
[standard Redis evidence](evidence/file-history-redis7.json) and
[Array evidence](evidence/file-history-array.json). The
[runner](../tests/history_compare/README.md) reproduces the method. These local
samples do not establish statistical significance or universal superiority.


## Correctness and compatibility evidence

| Recoverability / contract check | Original passing runs | New passing runs |
| --- | ---: | ---: |
| First deletion after enabling history | 0 / 10 | 10 / 10 |
| Deleted contents with a one-version limit | 0 / 10 | 10 / 10 |
| Process exit at publication/observer boundary | 0 / 10 | 10 / 10 |
| Every acknowledged concurrent-write payload retained | 6 / 10 | 10 / 10 |
| Final live contents recoverable after concurrent writes | 0 / 10 | 10 / 10 |
| Eight content/pagination/diff/restore/undelete API checks | 10 / 10 | 10 / 10 |

Concurrent runs attempted 40 writes without retrying conflicts.
The new implementation acknowledged 20 writes in every run;
original acknowledged counts ranged from 2 to 26.
These are consistency stress results, not an equal-success-count throughput
comparison. All rejected counts and error text remain in the raw reports.
The deterministic crash experiment targets the original observer gap; it does
not estimate random crash frequency or Redis server-persistence durability.

The unchanged original drawer, hooks, HTTP client and styles passed an earlier
actual Chrome workflow using a small provider host: 55-version pagination,
content selection, checkpoint diff, restore and undelete. Later component
follow-up runs use jsdom plus real HTTP/Redis and are explicitly distinct from
that browser run. This does not cover the original application's cloud, account
or catalog screens. Exact source and scope are recorded in the
[UI evidence](evidence/file-history-ui.json).

## Browser compatibility gate

The [original-component host](../tests/history_compare/README.md#original-browser-component)
loads the pinned original `FileHistoryDrawer`, its React Query hooks, HTTP client,
shared components and styles without editing them. Only the small host that
supplies providers and workspace/path props is new. Its API and Redis processes
are disposable, and its Vite caches stay in the copied source tree.

The first browser run rendered 55 versions and recent activity, loaded the
remaining five versions through the original pagination button, selected version
2, displayed its contents, and produced a diff against checkpoint head. Restore
created version 56 and a `version_restore` activity entry. On a second deleted
path, the original Undelete button restored the retained payload as version 3 in
the same lineage; selecting that version displayed the exact deleted content.
Screenshots were visually inspected at the loaded, diff and undeleted states,
and the browser reported no console errors.

The final run used the same production fingerprint as the final paired measurements,
`049f82f3231c87136e85e5e8511c467bf3892c785618a7aa0ad5d7aa646da0c5`,
and passed all three original component integration tests against real
HTTP/Redis, including pagination/diff/restore/undelete and activity recorded while
capture was off. This rerun used **jsdom**, because the desktop was locked and
neither Chrome nor the hidden in-app browser was available through the supported
CUA tools. It is supplementary component evidence; the actual-browser evidence
covers the earlier snapshot. [UI evidence](evidence/file-history-ui.json) keeps
the runs distinct and includes independent backend verification. All owned
processes were stopped, and the original checkout remained clean.

The paired service scenario independently checks ID and ordinal content
selection, ascending lineage ordinals returned through descending cursor pages,
checkpoint/live diffs, restore and undelete. It records normalized response
fields so semantic compatibility can be inspected without comparing generated
IDs and timestamps. The new response may include a charset in a text MIME type;
the original returns the same media type without that parameter.

## Deliberate compatibility differences

- Explicit off and path exclusions stop subsequent capture on an existing
  lineage. The original's continued capture contradicted its policy setting.
- Invalid patterns and oversized policy input are rejected: at most 128
  include/exclude patterns combined and 1,024 bytes per pattern. Accepted patterns retain the
  original segment matching syntax, including character classes and escapes.
- Deleted or excluded-large-file heads retain the newest recoverable payload,
  even when that means retaining two records under a one-version setting.
- Historical blobs have independent ownership and reference counts, allowing
  physical reclamation after retention and source-workspace deletion.
- The optional HTTP host exposes the original file-history contracts. It does
  not restore the original cloud, database-management, account or session UI.
