# AFS

This is the slim Go derivative of redis/agent-filesystem. Follow the current CLI in README.md and decisions in tasks/todo.md; tasks/brief.txt records the original extraction brief.

- Reuse the retained folder sync, Redis client, manifests, checkpoints, and recovery code.
- One workspace is one tree; no public volumes, cloud, MCP, or search. Native FUSE/NFS mounting is optional, with folder sync as the default.
- Workspace actions live at the root; cp manages checkpoints, config manages settings, and sync provides status and an explicit verification wait. No ws/fs aliases. Access files through mounted directories.
- Never run tests against existing user Redis data or change the original installation.
- Retain multi-writer behavior and prove safety changes with focused regressions.
- Local lifecycle belongs in cmd/afs; Redis content and checkpoints remain internal.
- Keep tasks/todo.md current; record user corrections in tasks/lessons.md.
- Validate with build, vet, unit/race tests and isolated real-Redis process tests.
- Preserve scan batching, chunking, worker draining, warm recovery and directory-mode restoration.
