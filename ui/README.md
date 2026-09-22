# AFS Web UI

The optional console for the AFS control plane retains the original Redis UI
components and layout, with a learning-first Home for agent setup and cookbooks,
Monitor and active-client topology, workspaces and file
browsing, explicit checkpoints, global and per-workspace History, file versioning,
and statistics for the configured Redis backend.

The browser uses the serving origin for API calls. If the server requires a bearer
token, the sign-in form keeps it in session storage for the current tab. Managed
CLI commands use the API; mounts obtain connection details from the server and
continue to access file contents directly in Redis.

```sh
# From the repository root
make web-install
make control-plane
AFS_REDIS_URL=redis://localhost:6379/0 ./bin/afs-control-plane
```

For UI development, keep the already configured API running on port 8091 and run
this command from the repository root:

```sh
npm --prefix ui run dev -- --host 127.0.0.1 --port 5173 --strictPort
```

Open `http://127.0.0.1:5173`. Vite proxies `/v1` to the local API and hot reloads UI
edits. Go/backend changes require `make control-plane` and a server restart with
the existing Redis and authentication settings. The embedded UI on port 8091
also requires a rebuild and restart to show UI changes.

`npm run build` from this directory checks TypeScript and builds production
assets in `dist`; `npm test` runs the retained UI regression suite.

See [the CLI guide](../README.md) and [control-plane setup](../docs/control-plane.md).
