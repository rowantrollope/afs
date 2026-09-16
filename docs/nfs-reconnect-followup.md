# Separate follow-up: retained NFS reconnect directory discovery

## Verified result

The final implementation and pristine baseline ac31fda both fail the same bounded Linux native-reconnect acceptance scenario on kernel 5.15.49-linuxkit aarch64, Redis 7.0.15. Each used four clients (FUSE, NFS, sync, FUSE), files=4, rounds=2, seed=17, scenario timeout=90 seconds. A second run of the final implementation reproduced the failure. The pristine source was extracted with git archive into /private/tmp/afs-native-baseline-ac31fda-gkbmn2xm/source; the original installation was not changed.

Only NFS client-1's directory manifest omits created-offline (25 bytes, SHA256 de4a0dfc2591ebd69da090cd168ac0a1a567602de7c855985a8d2a8d1d927b5a). The other three clients exactly match the expected manifest. The NFS client does see the new bytes of the existing cached file. This establishes a pre-existing NFS discovery defect in this tested environment, independent of the current read-only, ownership, import, and chmod changes. The artifacts demonstrate failed directory enumeration, not whether direct lookup/open of the missing pathname succeeds.

Pristine baseline took 90.683 seconds in the scenario and 91.378 seconds under its supervisor. Capture and cleanup errors are empty, and no owned processes remained. The baseline CLI SHA256 is c1e85f905f7ccc2f866e49cabadd5673559c6dfac52d7710ea54365bebe1b043; helper SHA256 is 0fba64246c73e12d7b0c41f00fb2706cc6b16c5bf98adf165df9b5adedaafbd9.

Baseline artifacts: /private/tmp/afs-followups-native-baseline-artifacts/native-reconnect-baseline/report.json and native-reconnect/client-1/differences.json. Build log: /private/tmp/afs-followups-native-baseline-build.log. Run log: /private/tmp/afs-followups-native-baseline-run.log. Container image: afs-audit-baseline-native:ac31fda. The container was isolated with network none, read-only root, tmpfs /tmp, and owned artifact mount, and was removed after completion.

## Hypothesis, not yet proven

A directory cache validation race is plausible: third_party/go-nfs/nfs_onreaddir.go:48 and nfs_onreaddirplus.go:54 obtain a directory listing, then lines108 and124 respectively fetch fresh directory attributes after assembling it. If peer creation lands between these operations, old directory entries may be certified with the newer directory timestamp. Retained NFS file I/O already avoids this class of race through mount/internal/nfsfs/fs.go:671-674, but READDIR and READDIRPLUS do not consult that optional-attributes policy. This causal explanation has not been proven by packet capture or a deterministic RPC reproducer.

A second area to inspect is verifier reuse: nfs_onreaddir.go:149-153 reuses a supplied nonzero verifier without the caller's cookie, and helpers/cachinghandler.go:232-251 retains listing snapshots. Reconnect does clear the client cache and all directory verifiers (mount/native/session.go:139-144), so stale publication timing across invalidation should be measured before changing it. These NFS paths are unchanged from ac31fda (`git diff ac31fda -- third_party/go-nfs mount/internal/nfsfs mount/internal/client/native_core.go` is empty).

## Bounded next investigation

1. Extend diagnostics to compare the failing client's direct pathname lookup/open with readdir and with an independent direct NFS RPC listing; capture directory mtime and cookie verifier across the partition/reconnect.
2. Create a deterministic direct RPC test that pauses listing after its snapshot, publishes a peer entry, then resumes response attribute construction. Confirm whether the response can certify the stale listing with the new mtime.
3. If confirmed, apply the smallest directory post-attribute/cache correction with RPC regressions and rerun the unchanged native-reconnect kernel assertion on both the failing kernel and the previously accepted kernel. Preserve the existing assertions and clean ownership bounds.

No production changes were made for this separate issue.
