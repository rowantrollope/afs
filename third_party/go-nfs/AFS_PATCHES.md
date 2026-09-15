# AFS patches to go-nfs

Source: the existing patched third_party/go-nfs from redis/agent-filesystem,
based on github.com/willscott/go-nfs v0.0.3. The original license and headers
remain. Existing batching, rename-handle and directory verifier patches are
retained.

AFS additions:

- COMMIT calls a backend context-aware Sync barrier when supported, checking
  admitted Redis requests and workspace/session validity. It does not promise
  Redis disk persistence.
- The RPC error boundary accepts a narrow handler MapError method so native
  generation/session failure is returned as NFS3ERR_STALE through generic
  filesystem error wrappers.
- AFS opts out of optional post-op attributes for file I/O and SETATTR/COMMIT.
  A separate Stat after I/O can describe a later peer publication and falsely
  validate stale kernel pages; omitting it forces normal NFS cache revalidation.
  Other filesystem implementations retain their existing response behavior.
- Filesystems may expose AccessTime/ChangeTime so NFS does not substitute mtime
  for the actual atime/ctime values.
- RenameTo supports two bound directory views of one export and carries both
  directory inode preconditions through one native namespace mutation.
- Connections always close on reader/handler exit.
- Directory verifier invalidation normalizes root paths and clears affected
  subtrees, including reconnect invalidation.

The native facade supplies inode-bound handles, without changing this library's
ordinary filesystem adapters. Its direct RPC regression tests require no OS
mount. Run the retained vendor tests separately with:

```
go -C third_party/go-nfs test -race ./...
```
