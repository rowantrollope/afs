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
./bin/afs-control-plane
```

Open `http://127.0.0.1:8091`. The server embeds `ui/dist` at build time. A plain
`go build ./cmd/afs-control-plane` without generated assets produces an API-only
server and reports that at startup. A new installation opens with an empty
database list: add a Redis connection through **Databases** when ready. Redis is
not required to start, log in, or manage API keys. No starter workspace is created
implicitly.

The control plane keeps its connection profiles, selected default and API keys
in `~/.config/afs-lite/control-plane.sqlite`. Use `--metadata-file` or
`AFS_METADATA_FILE` to choose another path. The file contains Redis credentials
and API-key hashes, is restricted to its owner (`0600`), and is locked to one
running control-plane process. SQLite metadata is independent of the managed
Redis databases. Set `AFS_METADATA_URL` to use shared Postgres instead;
see the [Vercel deployment guide](control-plane-vercel.md) for hosted setup.
To back up local SQLite metadata with a normal file copy, stop the server first and copy the database
and any accompanying `-wal`/`-shm` files together.

For an optional initial Redis connection, pass `--redis` or set `AFS_REDIS_URL`
on the first launch. `AFS_REDIS_PASSWORD` overrides that URL's password, including
an explicitly empty value. This seeds a fresh metadata database; later restarts
use the saved connections and default. An unavailable Redis connection does not
prevent startup. No implicit `localhost:6379` connection is created.

On the first local SQLite launch with a new metadata database, the server also imports
`~/.config/afs-lite/databases.json` if it exists. `--databases-file` or
`AFS_DATABASES_FILE` selects another legacy JSON import source. Saved connection
IDs, settings and credentials are retained; a saved `local` connection remains
the default, otherwise the explicit initial Redis connection or imported
connection with the smallest ID becomes default. The source JSON is unchanged and is not used after
initialization. Removing every connection intentionally leaves an empty catalog
across restarts; the import and initial seed do not run again.

`GET /healthz` reports whether the control plane's metadata database is available.
Redis connectivity is reported separately on the Databases page and database
API records. A healthy control plane can have zero databases or offline Redis
connections, allowing an administrator to repair their settings.

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

The key registry is stored in the metadata database (local SQLite or shared Postgres). Adding,
editing or removing Redis connections, changing the default, and Redis outages
do not change administrator authentication. The team token remains the bootstrap
and recovery credential. Restoring an old metadata backup can restore old key
state, including revocation state; reconcile credentials when restoring it.

To retain keys created by an earlier AFS control plane, stop the old server and
explicitly select its **startup Redis connection** for a one-time import:

```sh
AFS_MIGRATE_API_KEYS_FROM='redis://localhost:6379/0' ./bin/afs-control-plane
```

Use the legacy registry's actual URL, with `AFS_REDIS_PASSWORD` when needed, or
pass `--migrate-api-keys-from`. This reads the source without changing it and
preserves key IDs, secret hashes, expiration, usage and revocation state, so
existing bearer tokens continue to work. It does not expose or recover secrets.
A failed import prevents launch. Remove the migration setting after a successful
launch; normal startup never searches managed Redis databases for old keys.

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

## Manage Redis databases

On the Databases tab, choose **Add database** and enter a name, Redis host and
port, credentials, database index and TLS setting. You can paste a Redis URL
into the endpoint field. The server checks the connection before saving it.
This registers an existing Redis service; it does not provision a Redis server.
New workspace creation includes a database selector, and Monitor, workspaces
and History combine the configured connections. Unavailable connections remain
listed with their connection status.

Connections and the selected default are saved transactionally in the metadata
database. Ordinary database API
responses exclude credentials. Click a database row or its name to review and
edit its display name,
description, endpoint, username, database index and TLS setting. This includes
the default connection. The saved password is never displayed: leaving the
password field blank preserves it; enter a replacement or explicitly select
**Remove saved password**. Save checks Redis before replacing the settings.
A failed check leaves the previous connection intact. Concurrent
changes require reopening the settings to avoid overwriting another edit.

Database IDs stay fixed when editing. Changing the endpoint
or index points the connection at another existing Redis database; it does not
move workspaces. New requests and mounts use the updated connection, while
in-flight requests finish on their original client and existing mounts keep
their startup credentials until remounted.

Use **Set as default** to select the database used by unscoped CLI management and
new mount setup. The selection persists across restarts. **Remove connection**
removes its saved profile from the control plane; it does not delete workspaces,
file data or checkpoints in Redis. When removing the current default while other
connections remain, select a replacement default. Removing the last connection
returns the control plane to its empty state. Existing mounts retain their
direct Redis connections until stopped or remounted.

New mounts pin their management URL to the database selected during bootstrap,
so changing the default does not redirect their heartbeats or session shutdown.
The user's saved login URL stays unchanged. Mounts already running from a
version before this pinning was added retain their old unscoped management URL;
remount those clients after upgrading and before switching the default. Their
file I/O continues to use the original direct Redis connection.

To use an added database from the CLI, copy the workspace's connection command,
which uses a database-scoped server URL:

```sh
afs auth login --url "http://127.0.0.1:8091/databases/<database-id>" &&
afs list &&
afs mount shared ~/shared
```

That URL scopes management, credential bootstrap and daemon sessions to the
selected Redis database. The unscoped server URL uses the saved default
connection. A failed or unknown database never falls back to another database.

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
Login verifies control-plane authentication before saving the connection; it
works with no configured databases and during Redis outages. Redis credentials
are requested only when needed for mount setup. Failed logins leave the
configuration unchanged. `afs auth status` reports
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
