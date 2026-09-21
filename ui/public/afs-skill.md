---
name: afs
description: Use Agent Filesystem (AFS) to connect, mount persistent agent workspaces, verify synchronization, create checkpoints and forks, and recover file versions through the current AFS CLI.
---

# Agent Filesystem

AFS gives agents ordinary local directories backed by Redis. One workspace is
one file tree, with checkpoints and optional per-file history. Folder sync is
the default; FUSE and NFS are optional. Work on files through the mounted
directory using normal filesystem tools.

## Connect and mount

Use the user's existing CLI and configured connection when available. Check
`afs --help`, `afs auth status` and `afs list` before choosing a workspace. If
AFS is missing, follow the [CLI installation guide](https://github.com/rowantrollope/afs/blob/main/README.md#install)
for the user's platform and requested installation scope.

To connect to a control plane, use its provided URL:

```sh
afs auth login --url '<control-plane-url>'
afs list
```

If authentication is required, use the operator-provided shared team token via
`AFS_CONTROL_PLANE_TOKEN` or `afs auth login --url '<control-plane-url>' --token-stdin`.
Keep tokens out of prompts, URLs and command arguments. AFS does not issue
per-user API keys. Managed mounts receive connection details from the server
and access Redis directly; that Redis endpoint must be reachable by the client.

Create a new workspace only when the task calls for one. These example names
and paths should be adapted to the user's task:

```sh
afs create my-workspace
afs mount my-workspace ~/afs/my-workspace
afs status ~/afs/my-workspace
```

To import existing files, use `afs create my-workspace --from ./existing-project`.
Mount into a new or empty directory and wait for `mount` to return before
starting work. For an existing workspace, skip creation and mount its name or ID.
Workspace commands live at the root; there are no `afs ws` or `afs fs` aliases.

## Verify and checkpoint

Synchronization happens automatically. `afs sync status` observes progress;
empty queues alone do not prove that file contents were published. When a task
needs a verified completion boundary, pause application writes and other writers
to that workspace, then run:

```sh
afs sync --wait ~/afs/my-workspace
afs checkpoint create my-workspace --name before-refactor
afs checkpoint list my-workspace
```

`sync --wait` verifies the included tree in Redis and creates no checkpoint. A
receipt confirms Redis visibility, not disk persistence or arrival on every
client. Ignore rules still apply. A failed or timed-out wait does not confirm
completion.

CLI checkpoint creation flushes matching local mounts; it cannot include
another machine's unpublished edits. Browser checkpoints capture published
Redis state. Coordinate writers when an application-consistent snapshot matters.

## Fork, recover and finish

Create a checkpoint before forking newly edited work:

```sh
afs fork my-workspace experiment --checkpoint before-refactor
afs mount experiment ~/afs/experiment
```

Forks have independent trees. Without `--checkpoint`, a fork uses the latest
checkpoint, not the current live files.

Per-file history is off by default. When the task calls for retained versions,
ensure writers support history, then enable it with
`afs history policy my-workspace --mode all --max-versions 100`. Inspect and
recover an existing captured version with:

```sh
afs history list my-workspace README.md
afs history export my-workspace README.md --version '<version-id>' --to ./recovered-README.md
```

Export refuses an existing destination. Inspect the recovered file before
copying it into the mounted directory. `afs history undelete my-workspace <path>`
can publish a retained deleted file back to the workspace.

For whole-workspace restore, unmount local clients first, then use
`afs checkpoint restore my-workspace before-refactor` within the user's authorized scope.
Other mounted clients become stale; preserve their needed local edits and mount
into new directories after restore. Do not reuse pre-restore mount baselines.

Disjoint writers converge. Competing edits may create `.conflict-` copies;
preserve and reconcile them instead of discarding either version. Finish with
`afs unmount ~/afs/my-workspace` when the task calls for disconnecting. Normal
unmount verifies pending publication and leaves local files intact; a failed
flush keeps the daemon running. `--force` skips that safeguard.

For connection modes and detailed recovery behavior, consult the
[CLI guide](https://github.com/rowantrollope/afs/blob/main/README.md) and
[control-plane guide](https://github.com/rowantrollope/afs/blob/main/docs/control-plane.md).
