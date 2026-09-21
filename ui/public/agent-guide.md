# AFS Agent Guide

Configure the CLI with the control-plane URL to discover and manage workspaces.
The server supplies Redis connection details when mounting. Each workspace owns
one tree and its checkpoints. Agents work with normal files in a mounted local
directory; mounted file traffic goes directly to Redis.

Read the current [CLI guide](https://github.com/rowantrollope/afs/blob/main/README.md)
and [control-plane guide](https://github.com/rowantrollope/afs/blob/main/docs/control-plane.md).

```sh
# Use your server URL; set AFS_CONTROL_PLANE_TOKEN if it requires a token.
afs auth login --url http://127.0.0.1:8091
afs auth status
afs list
afs mount my-workspace ~/afs/my-workspace
afs sync status
afs sync --wait my-workspace
afs checkpoint create my-workspace
afs checkpoint list my-workspace
afs history list my-workspace README.md
```

The control plane provides CLI management, connection setup, the browser console
and client presence. Existing mounts keep using Redis during a server outage.
Standalone operation remains available through an explicit `--redis` option or
by running `afs auth logout` and clearing any `AFS_CONTROL_PLANE_URL` override.
