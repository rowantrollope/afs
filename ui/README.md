# AFS Web UI

The optional console for the AFS control plane retains the original Redis UI
components and layout: Monitor and active-client topology, workspaces and file
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

For UI development, run the API on port 8091 and `npm run dev` from this directory.
Vite proxies `/v1` to that local API. `npm run build` checks TypeScript and builds
production assets in `dist`; `npm test` runs the retained UI regression suite.

See [the CLI guide](../README.md) and [control-plane setup](../docs/control-plane.md).
