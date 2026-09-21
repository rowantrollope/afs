# Protocol for this workspace

Use ordinary file tools inside the AFS-mounted folder. All paths below are
relative to that folder. AFS synchronizes changes in the background; run
`afs sync --wait <mount-path>` on a writable mount when you need to verify
that your local changes have reached Redis.

This workspace coordinates a team. Multiple agents and humans write
here. Follow these rules so you do not step on each other.

## Your handle

On first interaction, ask the user for a short stable handle (for
example `alice` or `bob`). Remember it for the session and use it in
every task file you create or update.

## Claiming a task

1. Look in `tasks/backlog.md` for a line that matches the user's goal.
2. Write `tasks/in-progress/<handle>-<slug>.md` with:

       ---
       owner: <handle>
       started: YYYY-MM-DD
       progress: 0%
       status: active
       ---

       # <Task title>

       **Goal.** One paragraph.

       **Plan.** Bulleted steps.

       **Log.** Append dated bullets as you work.

3. Remove the matching line from `tasks/backlog.md` (edit the file,
   delete only that line). Coordinate claims through one writer when agents
   work concurrently; the shared backlog is not an atomic task queue.

## Making progress

Append a dated bullet to the **Log** section of your task file after
each meaningful step. Update the `progress:` front-matter field.

## Finishing a task

1. Move the content to `tasks/done/<slug>.md`.
2. Verify the completed file exists with the expected content, then remove
   your in-progress file using ordinary filesystem tools. An empty file is
   not a deletion. Use a unique completed-task filename to avoid collisions.
3. Snapshot the workspace with
   `afs checkpoint create team-board --name milestone-<slug>` (replace
   `team-board` if your workspace has a different name). This flushes local
   matching mounts; other writers must publish their edits separately first.

## Recording a question

Write `questions/<slug>.md` with:

    ---
    asked-by: <handle>
    date: YYYY-MM-DD
    status: open
    ---

    # <Question>

    Answer here once resolved.

## Never

- Never edit another owner's in-progress task file.
- Never rewrite `spec.md` or `roadmap.md` — append new sections.
