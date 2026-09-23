# Vercel control plane

Vercel runs the same Go control plane and embedded UI. Local deployments retain
SQLite; Vercel uses shared Postgres for connection profiles, the default database,
administrator API keys, browser sessions and CLI login requests. Files,
checkpoints, history and mounted-session
records stay in the Redis databases you register.

## Authentication

`AFS_CONTROL_PLANE_TOKEN` is the bootstrap administrator credential. Store a
random token in the project's sensitive environment variables and keep a copy
for recovery. Sign into the browser with it or an existing named API key; a
Secure HttpOnly session cookie remembers the login across tabs for up to 30
days, capped by the sign-in key's expiry. Key revocation invalidates its browser
sessions, and team-token rotation invalidates sessions established with that
token. Hosted mode always enables Secure cookies, including behind Vercel's
HTTPS proxy. Other HTTPS proxies can use `--secure-cookies` or
`AFS_CONTROL_PLANE_SECURE_COOKIES=true` independently of hosted mode.
CLI login opens the browser for explicit matching-code approval and
exchanges a private one-use proof for a separate named API key. You can also
issue named keys with expiry and revocation from the UI or `afs auth keys`.
Every key is a trusted administrator, with access to every
registered Redis database. Key revocation does not revoke Redis credentials
already issued to a mount.

Hosted startup fails if the token is missing, even when an internal listener
would otherwise qualify as loopback. It also requires a Postgres URL and valid
`PORT`; it never falls back to unauthenticated operation or temporary SQLite.
The login UI, authentication configuration, version and health response are
public. CLI login initiation and polling are public endpoints; issuing a key
requires authenticated browser approval and the CLI's private exchange proof.
Data, connection credentials, key management and workspace operations require
authentication on the server.

Vercel deployment protection is an additional layer. Protected preview URLs may
require a Vercel login before reaching AFS. Use the production domain for normal
CLI access, including browser approval; do not disable AFS authentication to resolve a Vercel
login challenge.

## Configure a project

Create a separate Vercel project with this repository as its root. Do not set
the root to `ui/`: the deployment includes both the UI and Go server. Link the
checkout using `vercel link`, then provision a dedicated Postgres database such
as Neon through the Vercel Marketplace.

Configure these server-side variables for each deployment environment:

| Variable | Value |
| --- | --- |
| `AFS_CONTROL_PLANE_TOKEN` | Random administrator token; sensitive |
| `AFS_METADATA_URL` | Postgres connection URL with TLS; sensitive |

Vercel supplies `VERCEL=1` and `PORT`. No Redis URL is needed for startup. A new
deployment starts with an empty Redis list; sign in and add connections through
the Redis page. Local JSON profiles and Redis credentials are not uploaded or
imported implicitly. The existing `--databases-file` and legacy API-key import
are explicit migration tools for an administrator, not deployment defaults.

The checked-in `vercel.json` selects the Go server and existing entrypoint. The
build script compiles the UI, embeds it, and writes the server binary to Vercel's
requested output path. `.vercel/`, environment files, local binaries, SQLite
files and development artifacts are excluded from uploads.

For Git-connected deployments, select `rowantrollope/afs` with production branch
`main`, keep the root directory at the repository root, and use the Go preset
with Node.js 24 for the UI build. Leave dashboard build, install and output
overrides unset; `vercel.json` selects the service build script. That script
disables Go VCS stamping because Vercel removes Git metadata via `.vercelignore`;
the Vercel deployment record retains the source commit. Pushes to `main` then
build and publish production automatically.

Deploy a preview:

```sh
vercel deploy --target=preview --yes
```

Check the returned deployment target: Vercel can automatically classify a new
project's first deployment as production even with an explicit preview target.
Configure the required token and metadata secrets for both environments before
the first upload. Subsequent previews should report a preview target.

After validating it, publish a production deployment when intended:

```sh
vercel deploy --prod --yes
```

Connect another computer through browser approval:

```sh
afs auth login --url https://your-production-domain
afs auth status
```

Compare the browser code with the terminal and approve the connection. The CLI
saves its own key in private configuration (`0600`), valid for up to 30 days and
capped by the approving key's expiry. A saved valid key is reused. Use
`--browser` to issue a new key, `--no-browser` to print a link for SSH/headless
login, or `--name 'Work laptop'` to replace the default `AFS CLI on <hostname>`
label. The CLI polls the control plane; it does not run a callback server.
Browser login requires HTTPS except for loopback development servers.

Automation can use `AFS_CONTROL_PLANE_TOKEN` or a supplied key through
`--token-stdin`. `afs auth logout` clears only the local CLI connection;
browser sign-out ends the browser session. Revoke a CLI key through **API Keys**
or `afs auth keys revoke <key-id>` to block its subsequent API access.

Use a database-scoped URL from the UI when selecting a particular Redis
connection. Existing mounts continue to exchange file data directly with Redis.

## Shared metadata and limits

Postgres tables live in the dedicated `afs_control_plane` schema. Independent
server instances load consistent registry snapshots before routing data
requests. A registry revision protects profile/default updates: a stale writer
receives a conflict instead of erasing another instance's change. Refresh errors
reject the request instead of silently using stale routing. API-key expiry,
revocation and usage tracking operate directly against the shared store.
Browser sessions and expiring CLI login requests use that same metadata store,
so approval and polling can reach different instances while Redis is unavailable.

Hosted monitor streams end after four minutes and the UI reconnects. The
deployment's function limit is five minutes. Vercel also limits function request
and response payloads to 4.5 MB. In particular, a managed `create --from` import
uses one multipart request, including its manifest and overhead, so large imports
can exceed that limit. For larger transfers, create an empty workspace, mount it
and copy files into the mounted directory; that path goes directly to Redis.
Large individual browser downloads are subject to the same hosting limits.

The supplied configuration uses `iad1`; provision metadata nearby. Managed
Redis endpoints must be reachable from Vercel and from each mounting computer;
`localhost` on your laptop is not reachable from a hosted function. Use your
database provider's backup and recovery tools for Postgres metadata. Restoring a
backup also restores API-key state, including old revocation state.

Sources: [Go runtime](https://vercel.com/docs/functions/runtimes/go),
[SQLite persistence](https://vercel.com/kb/guide/is-sqlite-supported-in-vercel),
[function limits](https://vercel.com/docs/functions/limitations).
