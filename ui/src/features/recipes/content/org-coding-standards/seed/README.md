# Org Coding Standards

A shared source of truth for your team's coding standards. Every developer's
agents consult this workspace before writing code. Updates flow through a
small set of maintainers; consumer agents treat the standards as read-only.

## Layout

- `AGENTS.md` — the protocol agents follow.
- `standards/languages/<lang>.md` — per-language rules.
- `standards/review-checklist.md` — what to check in every PR.
- `standards/security.md` — security rules.
- `standards/architecture-principles.md` — org-wide architecture defaults.

## Sharing this workspace

After connecting AFS to the team's database, mount the workspace for
consultation:

```sh
afs mount coding-standards ~/coding-standards --readonly
```

A read-only folder-sync mount receives remote updates without publishing local
edits. Tell the agent where the mounted folder lives and ask it to read
`AGENTS.md` before using the standards. These instructions apply to files
relative to that mounted folder.

Read-only mount behavior is not workspace-level authorization: a control-plane
API key grants trusted administrator access, and a client with Redis credentials
can create a writable mount. Share credentials only with trusted users.

## Updating the standards

Maintainers can edit through the AFS web UI or their own writable mount.
Changes reach consumer mounts through background synchronization. On the
maintainer's writable mount, `afs sync --wait <mount-path>` verifies publication
before asking the team to read the updated rules.
