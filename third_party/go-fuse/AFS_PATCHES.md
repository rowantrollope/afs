# AFS patch to go-fuse v2.7.2

Source: github.com/hanwen/go-fuse/v2 v2.7.2 from the Go module cache.
The original BSD license and source headers are retained. Examples and benchmark
programs are omitted; library source and upstream tests remain.

Optional `NodeFlusherWithOwner` and `FileFlusherWithOwner` interfaces preserve
`FlushIn.LockOwner` for filesystems providing remote POSIX locks. Existing Flush
interfaces retain their behavior. AFS releases exactly the closing process's
record locks, including locks acquired through a different descriptor, while
preserving other processes' locks on inherited/shared handles. The portable
`go -C third_party/go-fuse test -race ./fs/flush_owner_test.go -run '^TestFlushOwnerBridge$'` regression
checks owner forwarding and both legacy-interface fallbacks without a mount.

`MountOptions.MountContext` bounds only startup: mount utility execution,
communication-socket descriptor handoff, initial kernel request and readiness.
The default nil context preserves upstream behavior. Successful mount lifetime
and normal unmount semantics are unchanged. A failed readiness wait returns its
server to the caller so AFS can normally unmount or retain authenticated control
if that fails. AFS never automatically forces or lazily unmounts a live mount.

This fixes a reproduced macOS startup hang: mount_macfuse never returned a
communication descriptor while the system extension was unavailable. The
unmodified library ignored the caller's startup deadline and retained its child
process indefinitely.

Run the portable regression suite without a FUSE installation:

```
go -C third_party/go-fuse test -race ./fuse -run '^TestMountContext'
```

The tests cover utility cancellation and joining, descriptor handoff cancellation,
successful descriptor ownership after cancellation, initial request cancellation
and readiness cancellation. macOS also verifies a utility failure after successful
descriptor handoff reaches the readiness result, correcting a shadowed-error
variable in the original code. Actual successful kernel mounts are validated by the
AFS native acceptance lab separately.
