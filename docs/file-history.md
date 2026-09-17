# Per-file history

File history recovers published file contents between checkpoints. It is an
optional workspace feature, off by default. Checkpoints still capture a whole
tree at explicit points; file versions do not create or change checkpoints.

## Enable and filter history

```sh
afs versioning shared
afs versioning shared --mode all --max-versions 100
afs versioning shared --mode paths --include 'src/**' --include 'docs/**' --exclude '**/generated/**'
```

The policy lives in Redis for the workspace, rather than one client's config.
Changes apply to every upgraded writer, including mounted clients. Upgrade all
CLI and native helper binaries before enabling history. Older binaries do not
implement history capture, and an old binary remounting is not prevented from
writing. Mixed old/new writers therefore cannot guarantee complete history.

Policy updates are rejected while a checkpoint restore is actively running.
If a restore fails because its required history exceeds the byte budget, its
tree stays fenced; raise the budget after that command has failed, then retry
the restore. Policy changes cannot revive a deleted workspace.

`all` tracks every eligible path except exclusions. `paths` requires at least one
include pattern, and exclusions take precedence. Patterns match from the workspace
root: `*` stays within one path segment, `?` matches one character, and a complete
`**` segment spans directories. Character classes (`[a-z]`, `[^ab]`) and escapes
use the original Go `path.Match` grammar. Policy input supports at most 128
patterns of 1024 bytes each, and rejects malformed patterns. Quote patterns to
prevent your shell from expanding them. Each
command replaces a supplied include/exclude list while keeping omitted fields;
use `--include=` or `--exclude=` to clear a list.

```sh
afs versioning shared --mode off
```

Disabling capture keeps existing retained history readable. Re-enabling starts
capturing published changes again; it cannot reconstruct mutations made while
capture was disabled. Enabling performs no full-tree backfill. An existing file's
pre-change value is saved before its first changed tracked overwrite or deletion.
An equivalent write creates no version. Unlike the original observer, disabling
capture or excluding a path also stops capture of already tracked lineages.

## Browse versions and file identity

```sh
afs history shared docs/notes.txt
afs --json history shared docs/notes.txt --limit 20
afs history shared docs/notes.txt --limit 20 --before 37
afs history shared docs/notes.txt --lineages
afs history shared docs/notes.txt --file-id <file-id>
```

History shows newest versions first, including operation, publication time,
size and whether the record represents content or deletion. Each file has a stable lineage ID and an
increasing version ordinal. Rename keeps the lineage and records both paths;
history reached through either indexed path includes the lineage's retained
versions. Delete records a tombstone. A different file created at that path gets
a new lineage. The default selects the most recently indexed lineage; use
`--lineages` to discover previous ones and `--file-id` to inspect one.

Both lists default to 50 results and accept `--limit` from 1 to 1000. Pass the
returned `next_before` value as `--before` to continue. Version-list cursors are
exclusive version ordinals within the selected lineage; lineage-list cursors are
exclusive workspace history sequence numbers. Keep the path, selected file ID and
list mode unchanged across pages. Deleted or pruned versions are not returned.

## Recover exact content

```sh
afs recover shared docs/notes.txt --version 2 --to ./notes-recovered.txt
afs recover shared docs/notes.txt --version <version-id> --to ./notes-recovered.txt
afs recover shared docs/deleted.txt --to ./deleted-recovered.txt
afs recover shared docs/notes.txt --file-id <old-file-id> --to ./old-notes.txt
```

`--version` accepts an ordinal or an exact version ID. Omitting it selects the
latest recoverable version in the selected lineage, including a deleted lineage.
Deletion tombstones have no content. Files exceeding the configured size cap
retain metadata-only versions, including their hash, size and operation, but
their omitted bodies cannot be recovered. In that case, the latest
recoverable version can be older than the most recently published content.

The destination must be a new local path with an existing parent directory.
Recovery never replaces an existing file, directory or symlink, including a
dangling symlink. Binary and empty files retain exact bytes and file permissions.
Symlinks are recreated with their stored target and are never followed during
recovery; symlink permissions remain platform-specific.

Inspect or compare the recovered content, then copy it into your mounted workspace
using ordinary tools. That publication participates in normal multi-writer
conflict handling and produces a new version when capture is enabled. Recovery
itself only writes the selected local destination and leaves the live tree and
checkpoints unchanged. A destination inside a synchronized directory will be
published by that directory's normal synchronizer.

The original in-place workflows are available through `afs file restore` and
`afs file undelete`. These conditionally publish selected content, type and mode
with a new history record, preserving the current file if another writer changes
it during the operation. They also record an explicit action when automatic
capture is disabled or the selected content equals the current content. See
[original CLI and web UI compatibility](file-history-compatibility.md) for the
history, content, diff, restore, undelete and HTTP interfaces.

## Retention and storage cost

```sh
afs versioning shared --max-versions 100 --max-age-days 30 --max-bytes 1073741824
afs versioning shared --max-file-bytes 16777216
afs versioning shared --prune
```

All limits default to zero, meaning unlimited. Count and age limits remove old
versions while preserving the newest record and newest recoverable snapshot of
each lineage. A deleted file may therefore retain its deletion record plus its
last contents even with `--max-versions 1`. `--max-bytes` counts logical retained
regular-file bytes per version, matching the original budget semantics even when
several versions share a single physical body. It is not total Redis memory.
Redis metadata, allocator overhead,
live files and checkpoint blobs are outside this number. Lowering a limit can
leave existing retained heads above the requested limit: heads are not discarded
to make a quota appear satisfied. Publication can evict obsolete versions in a
bounded pass to make room for new heads, so a budget holding one full version
can support repeated edits. A tracked mutation fails if its required history
cannot fit, or the bounded pass cannot find enough space. Run `--prune` to remove
eligible old versions or raise the budget before retrying.

`--max-file-bytes` omits bodies of larger regular files while keeping their
metadata and SHA256 hashes. Symlink targets remain recoverable, matching the
original behavior. Existing retained versions remain available; an eligible
pre-change baseline is retained when a file grows beyond the cap.

Cleanup uses bounded batches. `--prune` applies the current policy to existing
history, inspecting up to 10,000 records per invocation; repeat when the command
reports more history remains to inspect. History owns immutable content
independently of current files and checkpoints. Identical bytes share one
SHA256-addressed body within the workspace. Removing its last retained history
reference releases that body without removing live or checkpoint content. Existing
checkpoint deletion continues to retain checkpoint blob bodies conservatively.

New content is hashed before publication and stored as an immutable Redis
snapshot. Equivalent writes create no version; metadata-only changes and repeated
content reuse existing bodies. A 4 KiB change producing previously unseen content
in a large file still costs a full-file snapshot. There are no delta chains or
implicit close/time-window coalescing. Hashing streams bounded chunks, but a range
write may require reading the complete staged file; this cost must be included
in throughput comparisons. Temporary copies can exceed the logical cap during
preparation. Recovering a version loads its entire body into client memory and
verifies SHA256 before returning it. Use filters and limits appropriate to the
workload; logical byte limits do not promise a hard physical Redis-memory ceiling.

## Publication, forks and restore

Capture is part of accepted publication, including file writes, native range
writes, symlink updates, chmod, rename and deletion. Recoverable metadata and
immutable content are committed with the live mutation. Rejected stale writes
do not create successful versions, and an exact retried publication does not
create an extra version. Content snapshots do not inherit abandoned upload-stage
expiry. Redis persistence remains governed by the server's configuration.

History follows mutations that AFS publishes. Folder synchronization may combine
multiple rapid application edits into one publication, so it cannot preserve
every transient write. A local rename discovered after watcher events were missed
or coalesced may be published as deletion plus creation, producing a new lineage;
the old lineage remains accessible through its former path. An accepted rename
publication preserves identity. Directory metadata is not a recoverable per-file version.

Forks retain the source policy and history with independent destination ownership.
Forking an older checkpoint retains source history and records the selected
checkpoint contents as the fork's new live heads where necessary. Later writes
and retention in either workspace do not alter the other workspace's history.

Whole-tree checkpoint restore keeps prior history and records restored file
contents. It fences existing writers through the normal generation boundary and
rebuilds live lineage mappings for the restored tree. Workspace deletion removes
that workspace's history while leaving independent forks readable.

Per-file activity is recorded even while version capture is off. Checkpoint
restore publishes its per-file activity after materialization succeeds; interrupted
retries retain their original activity baseline. A restore begun by an older
binary without that baseline remains recoverable, but reports only a workspace
replacement event rather than inventing missing pre-restore file activity.

The earlier [design analysis](file-history-design.md) explains the publication
and lifecycle risks that guided the implementation. See the
[original capability comparison](file-history-parity.md) and
[validation record](file-history-validation.md) for coverage, measured costs and
bulk-operation limits. Measurements from the earlier independent-body design
must not be presented as measurements of the shared-body implementation.
