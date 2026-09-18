# File history interface compatibility

AFS retains the original file history drawer's HTTP contracts in an internal
control-plane handler. The same service methods back a single `afs history` command group.
At the user's request, the CLI spelling differs from the original: `list`, `show`,
`diff`, `restore`, `undelete`, `export` and `policy` all live under `history`.
There is no server command inside the CLI, including `history serve`, no root `recover`,
`versioning` or `file` commands, and no hidden aliases. HTTP routes and response
contracts are also served by the separate `afs-control-plane` executable. See
the [current control-plane guide](control-plane.md).

## CLI

Workspace names or storage IDs are explicit:

```sh
afs history list project notes.txt --order desc --limit 50
afs history list project notes.txt --order desc --cursor '<next_cursor>'
afs history show project notes.txt --version '<version_id>'
afs history show project notes.txt --file-id '<file_id>' --ordinal 2
afs history diff project notes.txt --from-version '<version_id>' --to-ref working-copy
afs history diff project notes.txt --from-file-id '<file_id>' --from-ordinal 2 --to-ref head
afs history restore project notes.txt --version '<version_id>'
afs history undelete project notes.txt
afs history export project notes.txt --version 2 --to ./notes-recovered.txt
afs history policy project --mode all --max-versions 100
```

`--json` returns the original lineage, version-content, diff, restore, and
undelete response structures. History supports ascending and descending cursor
pagination. Diff operands accept version IDs, lineage ordinals, `head`,
`working-copy`, or a retained checkpoint ID/name.

The history workflows and selectors preserve the original capabilities; the
single command group is an intentional user-requested spelling change, not a
claim of unchanged original CLI syntax. Workspace selection is explicit; the
original optional workspace inference is not retained.
Pages default to 50 versions and accept limits of 1–1,000. The original's
zero/unlimited page mode is replaced by cursor traversal, including for callers
of `GetFileHistory` without an explicit limit. Clients must follow `next_cursor`
to retrieve the complete retained history. Human-readable tables follow the
current AFS CLI style rather than the original terminal formatting.
`list` has one grouped lineage/cursor response format. It includes file IDs for
historical incarnations; there is no compact `--before` or `--lineages` mode.

Normal file reads and writes use mounted directories. The removed `ws` and
`fs` command aliases are not restored. `afs history export ... --to ...` remains the
option for inspecting a historical copy locally before publishing it.

## Control-plane integration point

`internal/controlplane.NewFileHistoryHandler` returns an `http.Handler` over an
existing `Service`. It implements the history drawer and its versioning/activity
dependencies. `FileHistoryHTTPOptions.DatabaseID` names the one configured Redis
connection in scoped routes, and `AllowedOrigins` lists accepted browser origins.
The handler does not start a listener, manage server lifecycle, or provide
authentication. The separate `afs-control-plane` host supplies those
responsibilities and wraps this handler with its management authentication.

Integration tests host this handler on disposable loopback listeners. The original
drawer fixture points `VITE_AFS_API_BASE_URL` at that test host and supplies
`databaseId="local"`, a workspace name or ID, an absolute path, and the `editable`
prop. It does not install or modify the original UI. These fixtures validate the
handler contracts; they are not a shipped control-plane service.

The original application's unrelated catalog, sessions, cloud, search and volume
APIs are outside the retained handler.

This is interface compatibility for histories stored by the new AFS engine.
There is no importer for the original installation's Redis history schema,
workspace catalog, saved cursors, or identities. Pointing an unmodified complete
original application at a host of this handler does not supply its missing application APIs.
The verified browser integration hosts the original drawer, hooks, transport,
and shared components with the required providers and workspace/path props.

## HTTP routes

Every route supports both bases:

- `/v1/workspaces/<workspace>`
- `/v1/databases/<configured-alias>/workspaces/<workspace>`

| Method | Suffix | Request and result |
|---|---|---|
| GET | `/files/history` | `path`, `direction=asc|desc`, `limit`, `cursor`; grouped `lineages`, original version fields, `next_cursor` |
| GET | `/files/version-content` | `version_id` or `file_id` plus `ordinal`; immutable content and metadata |
| POST | `/files/diff` | `{path, from, to}`; unified text diff or `binary: true` |
| POST | `:restore-version` | `{path, version_id}` or `{path, file_id, ordinal}`; new committed version and restored-from identity |
| POST | `:undelete` | `{path}` with optional selector; revived lineage and undeleted-from identity |
| GET, PUT | `/versioning` | Original policy field names; PUT replaces the shared policy |
| GET | `/changes` | `path`, `direction`, `limit`, `since`/`until` exclusive stream cursors (or `cursor`); durable file activity with attribution |
| GET | `/files/content` | `path` and `view=head|working-copy|<checkpoint>` |
| GET | `/files/version-checkpoints` | `version_id`; retained checkpoints whose manifest contains the selected path, type, mode, and bytes/target |

The final route is an extension. It verifies manifest membership instead of
treating a version's source checkpoint annotation as proof of membership.
Specifically, it finds checkpoints with the same recorded path, type, mode,
and bytes or symlink target; it cannot prove that a matching checkpoint was
created from that exact version ID. Identical content may have existed earlier.
The lookup reads retained checkpoint manifests, so its work grows with their
number and size; it is not a bounded history-page operation.

Policy names map to the shared engine as follows:

| Original UI/API field | `history policy` option |
|---|---|
| `mode` | `--mode` |
| `include_globs` | `--include` |
| `exclude_globs` | `--exclude` |
| `max_versions_per_file` | `--max-versions` |
| `max_age_days` | `--max-age-days` |
| `max_total_bytes` | `--max-bytes` |
| `large_file_cutoff_bytes` | `--max-file-bytes` |

History versions retain `version_id`, `file_id`, `ordinal`, `path`,
`prev_path`, `op`, `kind`, hashes, byte counts, mode, target, source, actor
labels, checkpoint annotations, and RFC3339 creation times. Stable sequence
numbers and `metadata_only` are additive fields.

Text content retains the original UTF-8 response. Binary content adds
`data_base64` so API clients can recover exact bytes without UTF-8 conversion.
Oversized metadata-only versions explicitly report `metadata_only: true`.
Requests to restore such a version fail before changing live state.
Detected MIME types may be more specific, or include a charset, compared with
the original's small extension-based mapping.

The handler accepts optional `X-AFS-Session-ID`, `X-AFS-Agent-ID`, and
`X-AFS-User` labels for explicit actions. These are recorded provenance labels,
not authenticated identities. Successful restore/undelete responses identify
the exact committed record. Concurrent policy updates return the policy that
that request committed, even if another writer changes it immediately afterward.

Folder-sync publications use the original `source: agent_sync` and accept the
original optional session, agent and user fields through `syncDaemonConfig`.
The daemon carries this metadata through initial reconciliation, automatic
uploads, explicit verification, and resumed workers. Activity also retains the
original optional display label and agent-version fields. The CLI supplies these
through per-mount flags and environment defaults:

```sh
afs mount project ./project --session review-session --agent-id review-agent \
  --user reviewer --label 'Review Agent' --agent-version '1.2'
```

`--session-id` is an alias for `--session`. Defaults come from `AFS_SESSION_ID`,
`AFS_AGENT_ID`, `AFS_USER`, `AFS_AGENT_LABEL` and `AFS_AGENT_VERSION`; explicit
flags override them. Without an agent-version override, activity identifies the
actual AFS build. Session, agent and user labels remain empty when omitted.
They are saved in the mount's private registry/bootstrap so a background process
or `sync --wait` worker restart does not change the actor. These flags apply to
folder sync, and never reinterpret the Redis username as a history user.

Unlike the original `--session`, this label does not open a managed application
session. Original session detail links and identity lookups still require
application services outside this handler. Native mounts keep the original
`source: mount` behavior and an opaque publisher `origin`; an origin is not a
user identity. Direct filesystem API callers can supply mutation context using
`client.WithFileVersionMutationMetadata`. Explicit HTTP recovery actions retain
their supplied actor labels; CLI recovery actions have no actor labels. A
restore records the current caller, not the author of the historical version.

Checkpoint IDs on versions describe available mutation context. Ordinary mount
and folder-sync writes do not automatically annotate the current checkpoint
head; explicit version restore uses the current head, and whole-checkpoint
restore records its selected checkpoint. Use the manifest lookup above to find
matching retained checkpoint content, rather than assuming every matching
checkpoint appears in `checkpoint_ids`.

File activity uses the existing durable change stream, independently of version
capture and pruning. It retains approximately the latest 10,000 notifications
per workspace. Actual file mutations include operation, path, mode, size, and
available attribution; captured versions also provide version IDs and hashes.
Supplemental cache notifications are hidden. Older notifications without the
new metadata retain their original generic operation names. Activity can filter
by `session_id` and either the current or previous path of a rename. A page scans
at most 4,096 stream entries in batches of 128; an empty filtered page may still
have a `next_cursor` when older activity remains to scan.

## Safety and compatibility corrections

Restore publishes bytes, type, permissions, lineage, history, and notification
atomically after checking the observed file revision and workspace generation.
Changing between a file and a symlink never creates a delete-then-create gap.
Undelete requires the destination to remain absent and the selected lineage to
remain deleted. Renamed files can restore earlier content at their current path.
An explicit restore records an action even when automatic capture is disabled.
Diff `head` means the checkpoint head and falls back to the working copy when
there is no head checkpoint or that checkpoint lacks the requested file. A direct
`/files/content?view=head` request still returns 404 for a missing checkpoint file.

Invalid selectors, paths, cursors, methods, and request bodies return errors.
Conflicting live revisions and generations return HTTP 409. Unsupported browser
origins return HTTP 403. Requests cannot silently switch the configured database.

## Validation

`internal/controlplane/file_history_compat_test.go` sends the original drawer's
request bodies to the HTTP handler and checks response fields, ascending
pagination, content selectors, diffs, exact checkpoint membership, restore,
undelete, actor attribution, binary and metadata-only contents, request
validation, origin restrictions, and concurrent policy responses.
It also checks head-diff fallback both before the first checkpoint and when an
existing checkpoint lacks a newly created path.

`internal/controlplane/file_history_changes_test.go` checks activity with capture
disabled, absence of duplicate cache notifications, rename filtering, original
exclusive stream cursors, sparse bounded pagination, and legacy entries.

`cmd/afs/sync_attribution_test.go` checks flag/environment precedence, daemon
bootstrap persistence, and attribution through initial upload, explicit save,
and resumed automatic workers. `tests/e2e/history_attribution_test.go` verifies
the actual background CLI process across creates, updates, deletes, verification
and worker restart, with capture both enabled and disabled.

`tests/e2e/history_compatibility_test.go` builds the actual AFS executable and
uses a disposable Redis process plus private synchronization directories. It
exercises the seven-action history CLI. HTTP behavior is covered by the handler
tests above and the test-only host compiled from
`tests/history_compare/ui_server.go.in`; that host invokes
`NewFileHistoryHandler` on a disposable loopback listener. The Python component
runner builds it only in a temporary source snapshot. These tests do not use
the original installation or user data.

Browser validation of the unchanged original drawer is recorded in the
[parity evidence](file-history-parity.md#browser-compatibility-gate). That evidence
covers history pagination, content selection, diff, restore, undelete and recent
file activity; it does not claim compatibility for every original application
screen or for importing existing original data.
