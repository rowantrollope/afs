# Value of a standalone verified save

Analysis requested 2026-09-16. No new save command is implemented in this change.

Folder sync is asynchronous. A standalone `afs save DIR --timeout 10m --json`
would give a script a verified completion boundary while leaving the mount
running. An agent could finish a batch of edits, pause its local writers, save,
then hand the workspace to another worker or continue its next batch.

Today, callers obtain that boundary by creating a checkpoint or unmounting.
Checkpoints remain useful recovery points, but creating one after every small
batch adds history and work even when only publication confirmation is needed.
Unmounting interrupts access. Queue counts in `status` cannot establish that
all current file bytes and metadata were verified in Redis.

AFS already retains the save engine: pause and drain workers, detect local
changes during the pause, publish against the observed remote state, verify the
tree and resume synchronization. Its receipt contains entry/file counts, byte
count, a tree SHA-256 and completion time. Exposing that receipt makes successful
publication machine-checkable; a configurable deadline accommodates larger
trees or slower connections than today's fixed two-minute limit.

The receipt confirms visibility in the Redis live tree. It does not certify
Redis disk persistence, promise an atomic snapshot across concurrent writers,
or freeze later peer changes. Applications must pause their own writers, and
failures/timeouts must remain failures even if some writes reached Redis.

This is a relatively small CLI/lifecycle addition because the difficult save
engine and regression tests remain. A clean implementation would add a root
command, validate positive bounded deadlines, return the existing receipt in
JSON, preserve failure/resume behavior and reject read-only sync mounts. Native
mounts need an explicit contract: their current flush barrier does not produce
the same whole-tree verification receipt, so it must not be labelled as one.

Recommendation: restore it if scripts, short-lived agents or handoffs are common.
It improves operational control without adding another storage mechanism.

Implementation references: [save engine](../cmd/afs/sync_save_engine.go),
[save lifecycle](../cmd/afs/sync_save_service.go),
[current timeout and receipt validation](../cmd/afs/sync_save_command.go).
