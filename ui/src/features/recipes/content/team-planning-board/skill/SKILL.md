---
name: afs-team-board
description: "Use when coordinating project work in an AFS-mounted team planning board: read the spec, claim tasks, update in-progress work, record done work, and write open questions. Preserve per-owner task files and use focused additions to shared documents."
---

# Team Planning Board — coordination protocol

This skill coordinates a shared project board in an AFS-mounted folder. All
paths below are relative to that folder; use ordinary filesystem tools.
Multiple humans and agents may write here, so keep ownership clear and avoid
broad rewrites. AFS publishes and receives changes through background sync.

## First step in a session

If you do not already know the user's handle, ask for a short stable handle
such as `alice` or `frontend`. Use that handle in every task file you
create or update.

## Before planning work

1. Read `plan/spec.md` and `plan/roadmap.md`.
2. Read `tasks/backlog.md`.
3. List the files in `tasks/in-progress/` so you do not duplicate active work.

## Claiming a task

1. Pick the matching backlog line with the user.
2. Create `tasks/in-progress/<handle>-<slug>.md` with owner, date, progress,
   goal, plan, and log sections.
3. Remove only the claimed line from `tasks/backlog.md`. Coordinate claims
   through one writer when agents work concurrently; shared backlog edits
   across mounted folders are not an atomic task queue.

## Making progress

Append dated notes to your own in-progress file. Do not edit another owner's
in-progress file. Shared files such as `plan/spec.md`,
`plan/roadmap.md`, and `questions/*.md` should grow by focused additions
instead of rewrites.

## Finishing work

1. Move the completed task content to `tasks/done/<slug>.md`.
2. Remove your in-progress file after verifying the done file has the expected
   content. Use a unique completed-task filename to avoid collisions.
3. Create a milestone checkpoint with
   `afs checkpoint create team-board --name milestone-<slug>`, substituting
   your workspace name. The command flushes this machine’s matching mounts;
   other writers must publish separately before the snapshot. Use
   `afs sync --wait <mount-path>` to verify publication on each writer.
