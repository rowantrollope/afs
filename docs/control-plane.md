# AFS control plane

AFS includes an optional, separate `afs-control-plane` executable and the copied
React UI from the original project. It manages the current AFS engine: one
workspace is one tree, backed by its selected Redis database. Managed CLI
commands use its HTTP API; mounted file traffic continues directly to Redis.
Standalone CLI use remains available without a control plane.

The UI retains its existing Monitor/topology, workspace browser, checkpoints,
History and versioning settings. Its scope is smaller: no Cloud accounts,
search, hosted MCP or templates. Named API keys provide administrator access
for trusted people, agents and automation.
See the [capability inventory](control-plane-capabilities.md) for the original
features and the migration decisions.

## Build and run

The CLI build remains `make build` and requires no Node installation. To build
the UI and the separate server, use a current Node release supported by Vite
and Go 1.22.2 or newer:

```sh
make web-install
make control-plane
AFS_REDIS_URL='redis://localhost:6379/0' ./bin/afs-control-plane
```

Open `http://127.0.0.1:8091`. The server embeds `ui/dist` at build time. A plain
`go build ./cmd/afs-control-plane` without generated assets produces an API-only
server and reports that at startup. No starter workspace is created implicitly.

The server uses `AFS_REDIS_URL`, defaulting to `redis://localhost:6379/0`;
`--redis` overrides the URL. `AFS_REDIS_PASSWORD` overrides its password,
including an explicitly empty value. It does not read the original project's
configuration or connect to any catalog database.

The default listener is `127.0.0.1:8091`. Set `AFS_CONTROL_PLANE_LISTEN` or pass
`--listen` to change it. Set `AFS_CONTROL_PLANE_TOKEN` to require bearer
authentication. A token is mandatory for a listener outside loopback. The
browser accepts that token or a named API key and retains it for its browser
session. Both grant the same administrative access across configured Redis
connections; neither provides per-workspace authorization. Use HTTPS at the deployment boundary for remote
access, and Redis TLS as appropriate for the Redis connection.

For UI development, run the API on port 8091 and `make web-dev` separately.
The Vite development proxy keeps browser requests on the UI origin. Production
assets use their serving origin. `--allow-origin` can be repeated when an
explicit cross-origin development setup is needed.

## Home recipes

Home links to complete recipes at `/recipes`: Shared Agent Memory, Shared LLM
Wiki, Org Coding Standards, Team Planning Board, and Blank Workspace. These
migrate the original `redis/agent-filesystem` starter templates into guides for
the current mounted-folder workflow. Each guide includes its starter files,
companion instructions, copyable/downloadable setup prompt, and a first task.
Reading or downloading a recipe does not create a workspace; the setup prompt
guides your agent through the CLI and asks it to confirm the destination first.

## Named API keys

Configure `AFS_CONTROL_PLANE_TOKEN` on the server first. It remains the bootstrap
and recovery administrator credential. Named keys require this authenticated
mode; an unauthenticated local server shows key management as disabled.

Open **API Keys** in the sidebar to create a key, review usage and expiration,
or revoke one. The secret appears only after creation. The server stores its
hash, and list/revoke responses contain metadata only. Browser-created secrets
stay in the creation dialog until dismissed; they are not saved in browser
storage or query caches.

The same actions are available to a logged-in CLI:

```sh
afs auth keys create 'build agent' --expires 30d
afs auth keys list
afs auth keys revoke <key-id>
```

Creation defaults to 30 days. `--expires` accepts a positive duration such as
`12h` or `30d`, a future RFC3339 timestamp, or `never`. `--json` creation output
contains `key` metadata and the one-time `token`; keep it out of logs. To replace
a key, create a new one, update the client, verify access, then revoke the old
key. Key management does not replace the administrator's saved login.

Use a key through `AFS_CONTROL_PLANE_TOKEN`, or pipe it from a secret manager
to `afs auth login --url <server-url> --token-stdin`. The existing private CLI
configuration and endpoint/token precedence rules apply. Browser sign-in also
accepts named keys. Every key is a trusted administrator: it can manage keys,
manage every configured database and obtain that database's Redis credentials.
There are no workspace scopes or read-only key permissions.

Expiration and revocation reject subsequent authenticated HTTP requests and
credential bootstrap. They do not cancel an already authorized request, stop
existing mounts, or invalidate Redis credentials already delivered to a client.
File access still goes directly to Redis. Complete storage revocation requires
separate Redis credential changes and connection termination. Creating a
replacement key does not invalidate the old one automatically.

The key registry is stored in the **startup Redis connection**, selected by
`--redis` / `AFS_REDIS_URL`, with its effective password. Adding databases or
editing the default database in the UI does not relocate this registry. Keep
that startup connection available and persist its Redis data across restarts.
Changing it selects a different key registry; key records are not automatically
migrated. If it is unavailable, named-key authentication fails closed; the team
token remains usable for recovery operations that do not need that registry.
Restore of old Redis backups can restore old key state, including revocation
state; reconcile credentials when restoring the registry.

API lifecycle activity records the key's stable ID and name. Session records
keep authenticated key identity separate from caller-supplied user/agent labels.
These records are operational attribution for trusted administrators, not a
human identity service or a tamper-proof audit trail. Direct Redis writes can
bypass the control-plane activity records.

The HTTP contract is `GET /v1/api-keys`, `POST /v1/api-keys` with `name` and
optional `expires_at`, and `DELETE /v1/api-keys/<key-id>`. Omitted expiration
means 30 days; an empty string means no expiration. Lists accept `limit`
(default 100, maximum 1000) and `cursor`, returning `next_cursor` when another
page exists. Database-scoped endpoints use the same server-wide key registry.

## Add a Redis database

On the Databases tab, choose **Add database** and enter a name, Redis host and
port, credentials, database index and TLS setting. You can paste a Redis URL
into the endpoint field. The server checks the connection before saving it.
This registers an existing Redis service; it does not provision a Redis server.
New workspace creation includes a database selector, and Monitor, workspaces
and History combine the configured connections. Unavailable connections remain
listed with their connection status.

The startup `--redis` connection remains the default. Added connections are
saved atomically with mode `0600` in `~/.config/afs-lite/databases.json`.
Override that path with `--databases-file` or `AFS_DATABASES_FILE`, including
for separate server instances. One server owns each file at a time. The file
contains credentials and belongs to the control-plane host; ordinary database
API responses exclude them. Profiles persist across restarts without a SQL
catalog. Click a database row or its name to review and edit its display name,
description, endpoint, username, database index and TLS setting. This includes
the default connection. The saved password is never displayed: leaving the
password field blank preserves it; enter a replacement or explicitly select
**Remove saved password**. Save checks Redis before atomically replacing the
settings. A failed check leaves the previous connection intact. Concurrent
changes require reopening the settings to avoid overwriting another edit.

Database IDs and default status stay fixed when editing. Changing the endpoint
or index points the connection at another existing Redis database; it does not
move workspaces. New requests and mounts use the updated connection, while
in-flight requests finish on their original client and existing mounts keep
their startup credentials until remounted. Edited default settings are persisted
in the same private file and take precedence over the startup Redis URL on
restart. The startup URL supplies the initial default before it has been edited.
Removing connections or selecting a different default database is not part of
this flow.

To use an added database from the CLI, copy the workspace's connection command,
which uses a database-scoped server URL:

```sh
afs auth login --url "http://127.0.0.1:8091/databases/<database-id>" &&
afs list &&
afs mount shared ~/shared
```

That URL scopes management, credential bootstrap and daemon sessions to the
selected Redis database. The unscoped server URL continues to use the startup
connection. A failed or unknown database never falls back to the default.

## Connect the CLI

Log in with the server URL. No Redis configuration is needed on a managed client:

```sh
# If the server requires a token, pass it via the environment:
export AFS_CONTROL_PLANE_TOKEN='your-shared-token'
afs auth login --url http://127.0.0.1:8091
afs auth status
afs list
afs mount shared ~/shared
```

For the local server without a token, just run `afs auth login`. Without `--url`,
login uses the environment or saved endpoint, then defaults to
`http://127.0.0.1:8091`. The original self-managed spelling,
`afs auth login --self-hosted --control-plane-url <url>`, also works.
Login verifies access to credential bootstrap before saving the connection;
failed logins leave the configuration unchanged. `afs auth status` reports
effective settings offline, without testing server availability.

Login saves `controlPlane.url`, selecting managed operation for workspace,
checkpoint and history commands. The API uses the same storage engine as standalone AFS.
Directory imports stream bounded batches to the server instead of buffering all
file contents in memory. Mounts fetch the server's effective Redis URL, including
its password override, then connect directly to Redis. That address must be
reachable from the client. Redis credentials stay in memory and private daemon
bootstrap files; they are not saved into the client's user configuration.

The configured team token or a named API key authorizes both administration and credential delivery
for the server's configured Redis connections. The returned credentials have the same
Redis permissions as the server; this does not provision per-client Redis ACLs.
Only the authenticated connection endpoint returns them, with caching disabled.

Saved Redis settings and `AFS_REDIS_URL`/`AFS_REDIS_PASSWORD` are ignored in managed
operation. An explicit `afs --redis <url> ...` selects standalone operation for
that command. To restore standalone defaults, run `afs auth logout` and unset
`AFS_CONTROL_PLANE_URL` if present. Logout clears the saved URL and token;
it preserves Redis settings and does not stop existing mounts or revoke tokens.
There is no automatic Redis fallback when the management API fails.

To persist a token without putting it in command arguments, pipe it to
`afs auth login --url <url> --token-stdin`; the config file is written with mode
`0600`. Environment tokens are used without saving them. Daemon bootstrap files
are private; tokens are excluded from the mount registry and child command lines.

`AFS_CONTROL_PLANE_URL` and `AFS_CONTROL_PLANE_TOKEN` override the corresponding
configuration settings. Login rejects conflicting explicit values so a saved
connection cannot be silently overridden. A saved token is reused only with its
saved endpoint. For offline configuration, `afs config set controlPlane.url <url>`
and `afs config set controlPlane.token -` remain available.
Changes apply to new mounts; existing daemons retain their startup configuration.
Disabling management does not disable file sync.

Each managed sync or native daemon registers its workspace, client/session
identity, host, backend, local path, version and access mode. It sends periodic
heartbeats while idle as well as busy, and closes the session at shutdown.
The same session ID is included in published file activity and captured versions.
A short-lived Redis challenge confirms that the daemon and server use the same
backend before the session becomes active. Native storage leases remain separate
from these informational sessions. `afs status` reports management availability.

The server must be available for managed commands and new mount setup.
Registration and heartbeats are best effort once the connection is established:
a control-plane outage does not stop direct Redis file I/O. Local status, sync
verification and unmount remain available. Presence may become stale while a
mount continues working; reporting retries after the service returns. Closing
a session or changing the bearer token does not revoke existing Redis access.

## History and checkpoints

The server reads the existing file-change journal; daemons do not send an HTTP
copy of every file write. File activity exists even when per-file version capture
is off. Versioning policy controls whether historical contents are retained.
Managed sessions add the link between file changes and the named client shown
in Monitor. Session/lifecycle events and file changes feed workspace and global
History; the monitor stream refreshes the browser after changes.

Browser checkpoints capture published Redis state. They do not flush pending
local writes on every connected machine. Use `afs sync --wait` on each relevant
client before a checkpoint when those pending edits need to be included. Pause
application writes for an application-consistent snapshot.

Managed CLI checkpoint creation still flushes the matching local mounts before
requesting the snapshot. Restore and workspace deletion retain their local
unmount guard, including mounts created in standalone mode against the same
Redis backend. These checks do not flush or stop mounts on other machines.

History is operational evidence, not a tamper-proof audit service. Client labels
are descriptive; the shared API token does not authenticate a different human
for every label. Raw Redis writes may bypass AFS journals. The server does not
collect daemon log files, terminate remote processes or manage their local queues.

## Code ownership

- `cmd/afs-control-plane`: standalone listener, configuration and shutdown.
- `internal/controlplane`: shared workspace/checkpoint/history engine and HTTP
  management adapters; a single storage implementation.
- `internal/managedclient`: small optional daemon registration client.
- `ui`: copied UI source with unsupported product surfaces removed.
- `internal/uistatic`: compiled UI embedding, populated by `make control-plane`.

No original binaries, services, configuration or Redis data need to be modified
to build this project. Sharing the `afs:` namespace is not a promise that old
and new binaries can concurrently mutate existing data safely.

See [migration validation](control-plane-validation.md) for exercised paths and
the limits of the acceptance run.
