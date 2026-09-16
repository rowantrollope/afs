# Verified sync completion

The standalone-save analysis led to `afs sync --wait <workspace|directory>` and
`afs sync status [workspace|directory]`. There is no `afs save` command:
synchronization is automatic, and `--wait` makes the completion boundary explicit.

For an agent handoff or a script that needs publication confirmation without
creating a checkpoint or unmounting, stop local application writes and other
writers to the workspace, then run:

```sh
afs sync --wait ./mounted-directory --timeout 10m --json
```

The existing verification engine pauses and drains workers, detects local
changes during the pause, publishes against observed remote state, verifies
actual bytes and metadata, and resumes synchronization. Success returns a
`receipt` containing counts, bytes, tree SHA-256 and completion time. A failure
returns a nonzero status and never claims verification; partial publication may
have occurred. The timeout defaults to two minutes and accepts 1ms through 24h.

Only active writable folder-sync mounts support `--wait`. Native flush barriers
have a different contract and do not produce this whole-tree receipt. Targets
must identify a unique registered mount; subdirectories do not select a subset.

`afs sync status` observes folder-sync connection, queued work, tracked uploads,
conflicts and errors. It performs no verified flush. Zero queued work is not
proof of publication. `afs status` remains the mount overview for all backends.

A receipt confirms visibility in Redis, not Redis disk persistence, an atomic
snapshot while writers are active, or delivery to every peer. Checkpoints and
normal unmount continue using the same verification engine.

Implementation: [CLI](../cmd/afs/sync_command.go),
[engine](../cmd/afs/sync_save_engine.go),
[lifecycle](../cmd/afs/sync_save_service.go).
