# AFS

This is the slim Go derivative of redis/agent-filesystem. Follow tasks/brief.txt.

- Reuse the retained folder sync, Redis client, manifests, checkpoints, and recovery code.
- One workspace is one tree; no public volumes, native mount drivers, cloud, MCP, or search.
- Never run tests against existing user Redis data or change the original installation.
- Retain multi-writer behavior and prove safety changes with focused regressions.
- Local lifecycle belongs in cmd/afs; Redis content and checkpoints remain internal.
- Keep tasks/todo.md current; record user corrections in tasks/lessons.md.
- Validate with build, vet, unit/race tests and isolated real-Redis process tests.
- Preserve scan batching, chunking, worker draining, warm recovery and directory-mode restoration.
