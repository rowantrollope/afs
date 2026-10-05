# AgentConnect service interface

Proposed interface, 5 October 2026. The product is a hosted service that lets a
user connect several existing agents, exchange work and share an AFS workspace.
The first end-to-end example remains Moneypenny and the user's other agent.
AgentConnect is a working title pending the user's naming exploration. The
proposed domain is illustrative; no endpoint has been deployed.

## The customer experience

1. Add `https://agentconnect.redis.io/connect` as an MCP connection in an agent.
2. Sign in, name this agent and approve its access to a private collaboration
   group. The first connection can create the group and its shared workspace.
3. Connect another agent to the same group using the same URL.
4. Ask either agent to hand work to an approved peer. Both can access the shared
   files, ask questions and return results.

The URL is the MCP endpoint itself. A browser visit can show installation and
sign-in guidance through content negotiation; it must not redirect MCP traffic
to an HTML page. Authentication discovery starts from that same resource.
Customers do not configure Redis Streams or exchange Redis credentials.

Initially, one approved connection belongs to one agent in one group. All
members of that group are trusted collaborators with access to its shared files.
The owner controls membership and disconnection through a small management page.
Public agent discovery and cross-owner invitations are outside the first version.

An agent needs a supported MCP client or a small runtime adapter. A URL alone
cannot give an existing agent new tool support or start an unattended turn.
Report whether each integration supports events, periodic polling or access
only during an existing turn. Advertise unattended operation only for integrations
whose wake-up path has been verified.

## Public web surface

| Surface | Contract |
| --- | --- |
| `/connect` | Authenticated MCP Streamable HTTP endpoint: discovery, tool calls and event subscriptions. |
| `/.well-known/oauth-protected-resource/connect` | Public OAuth resource metadata identifying `/connect` and its authorization server. |
| Authorization-server discovery and endpoints | Standard OAuth metadata, authorization-code flow with PKCE, token refresh and revocation, supplied by an established identity provider. |
| Management page | Human sign-in, agent naming, group selection, permission approval and disconnect. |
| `/healthz` | Process liveness; no customer data or backend addresses. |
| `/readyz` | Deployment readiness; unavailable storage returns 503. No customer diagnostics on the public response. |

Use the MCP SDK's transport and discovery behavior. For ChatGPT Events, support
the documented MCP version `2026-07-28`, including `server/discover`,
`events/list`, `events/subscribe` and `events/unsubscribe`. Tool calls use the
SDK's `tools/list` and `tools/call` interfaces. Separate REST versions of the
collaboration tools are unnecessary for the first version. See
[MCP servers](https://developers.openai.com/plugins/build/mcp-server) and
[MCP Events](https://developers.openai.com/plugins/build/mcp-events).

## Identity and permission contract

The service derives `ownerId`, `groupId`, `agentId`, `connectionId` and allowed
operations from a validated authorization grant. Clients never supply a trusted
`from` field. Each request checks issuer, audience, expiry, permissions and current
connection membership/revocation. The OAuth resource identifier is exactly
`https://agentconnect.redis.io/connect`.

Use these tool permissions: `agents:read`, `tasks:read`, `tasks:write`,
`files:read`, `files:write` and `events:subscribe`. The approval page explains
them in plain language. A full collaborator grant enables all six. Reading a
task requires being its requester or assigned executor; updating an execution
requires being that executor. Sending requires an active recipient in the same
group. File access is restricted to the group's configured workspace.

Agent names are labels; server-issued agent IDs identify recipients. Bind each
agent to an independent approved connection. A runtime that shares one OAuth
connection between several agents needs separate connection configurations or an
adapter that supplies independently authenticated identities. Do not infer a
particular dot's identity from a shared user login or a claimed display name.

Disconnect revokes the connection's service access, refresh permission and event
subscriptions. It does not undo earlier file writes or actions already performed.
Optional native AFS mounts have a separate credential lifecycle; bridge revocation
does not revoke previously issued Redis credentials. Existing AFS control-plane
administrator keys do not provide customer isolation and must not become these
agent grants. See [MCP authentication](https://developers.openai.com/plugins/build/auth).

## Collaboration tools

These are executor-facing tools on `/connect`. Canonical A2A request, message,
task and artifact types come from the pinned SDK; bridge-only fields sit outside
those objects. JSON field names use camelCase. IDs are opaque strings, revisions
are positive integers and timestamps use UTC ISO 8601.

| Tool | Arguments | Structured result |
| --- | --- | --- |
| `list_agents` | `{}` | `{selfAgentId, groupId, workspaceId, agents[]}`; approved peers with ID, name, wake mode and optional last contact time. |
| `send_message` | `{to, request: A2A.SendMessageRequest}` | Canonical `A2A.SendMessageResponse`; this prototype creates a Task for a new handoff. |
| `receive_work` | `{operationId, limit?, acknowledge?}` | `{items[]}`; claimed incoming work or task updates, with leases. |
| `update_task` | `{operationId, workId, leaseToken, taskId, expectedRevision, state, message?, artifacts?}` | `{task: A2A.Task, revision, leaseExpiresAt?}`. |
| `get_task` | `{taskId, historyLength?}` | `{task: A2A.Task, revision}`; authoritative current state. |
| `cancel_task` | `{operationId, taskId}` | `{task: A2A.Task, revision}`; requester cancellation. |
| `read_file` | `{path, expectedSha256?, encoding?}` | `{file: FileRef, content, encoding}`. |
| `write_file` | `{operationId, path, content, encoding?}` | `{file: FileRef}`; immutable, create-only artifact write. |

`list_agents`, `get_task` and `read_file` are read-only tools. The other tools
have effects, including `receive_work` claiming delivery. Declare correct MCP
annotations and the required permissions on every tool.

Each agent listing is `{id, name, wakeMode, lastContactAt?}`. `wakeMode` is
`events`, `poll` or `on_demand`; it describes the configured integration, not a
promise that the runtime is currently online. `operationId` is a caller-generated
unique string, scoped to `(group, agent, tool)`. Persist successful mutation
results; repeating an ID with different arguments returns `IdempotencyConflict`.

Unknown bridge arguments are invalid. Canonical A2A objects retain their standard
metadata and extension rules. Defaults: inbox `limit = 1` (maximum 20), lease
duration 15 minutes, file `encoding = "utf8"`. Also support `"base64"`. The
prototype file tool limit is 1 MiB of decoded bytes per call; expose the limit in
tool descriptions. Local AFS sync can handle larger files independently.

### Sending and retrying

`request.message.messageId` is supplied by the caller and is the send idempotency
key within `(group, sender, recipient)`. Repeating the same message and ID returns
the same task identity and its current state. Reusing an ID with different
content fails. The receiver assigns task and context IDs.

Use `configuration.returnImmediately = true` for unattended handoffs. A transport
timeout has an uncertain outcome: retry the same message ID. Never manufacture
a new ID solely because the first request timed out. Follow-up input carries the
existing task ID and a new message ID. New work after a terminal task starts a
new task. A2A roles describe message direction, not authentication identity.

### Receiving and finishing

An inbox item is `{workId, kind, fromAgentId, task, revision, leaseToken,
leaseExpiresAt}`. `kind` is `execute` or `task_update`; the task is a canonical
A2A snapshot. An incoming handoff or new input creates execution work. Changes
to a task this agent requested create update work, coalesced to the latest
revision while unclaimed. The inbox is a bridge/runtime facility, not an
additional A2A operation.

`receive_work` atomically leases eligible items to this connection. A leased item
is unavailable to another run. Every call has a fresh `operationId`; retries use
the original ID and receive the original live claim. An expired claim returns
`LeaseLost`; start a new call with a new ID. A crash or abandoned run leaves work
eligible again after lease expiry, rather than losing it when the webhook arrives.

`acknowledge`, when supplied, is an array of `{workId, leaseToken}` for processed
`task_update` items. Acknowledgement releases those items before claiming the
next batch, and is idempotent. A newer revision remains pending. Execution items
finish through `update_task`, not inbox acknowledgement. This distinction keeps
delivery separate from the work's outcome.

`update_task` is an executor hook, not a new A2A wire operation. It requires a
live execution lease and the current revision. Use canonical `TASK_STATE_*`
values: working, input-required, auth-required, completed, failed or rejected.
The server stamps the time and validates the transition. A working update renews
the lease; another working update can serve as a heartbeat during a long run.
Interrupted or terminal updates release execution work. New input makes an
interrupted task actionable again. Terminal tasks cannot be reopened.

A status `message`, if present, is an A2A `ROLE_AGENT` message associated with
this task/context. A clarification sets input-required and includes the question;
the requester replies using `send_message`. `artifacts` appends completed A2A
artifacts with stable IDs; it does not silently replace earlier results.

Commit the task change, revision, work disposition and outgoing transport update
atomically in the task store. Persist operation results before acknowledging a
successful tool call. Concurrent revision changes return `RevisionConflict`;
expired or superseded execution claims return `LeaseLost`. These are bridge
execution errors, separate from canonical A2A errors.

Cancellation goes through A2A `CancelTask`. When accepted, it makes the task
terminal and invalidates its execution lease atomically. A racing stale completion
fails. Cancellation cannot undo an external action that an agent already started.

### File contract

`FileRef` is `{workspaceId, path, sha256, sizeBytes}`. `path` is relative to the
one authorized workspace. SHA-256 is lowercase hexadecimal over the actual file
bytes. For A2A messages/artifacts, embed the reference as `{"data":{"afs":FileRef}}`.
This is our file-reference convention, not a standard A2A filesystem URI.

Reject absolute paths, traversal and symlink escapes, including races while
resolving a path. Check access on the actual file operation. `read_file` with an
expected hash returns `FileNotReady` when the bytes have not arrived or do not
match; it never returns mismatched bytes as a successful read.

`write_file` decodes the content, creates the file in the bridge's ordinary AFS
mount and computes the returned hash. It fails if a different file already exists
at that path. Use unique handoff/artifact paths and one writer for each published
file; revisions get new paths. Create-only behavior on one mount is not a global
distributed file lock across all AFS writers.

The write response confirms bytes on that mount. It does not promise every mount
has caught up. Create the result file before completing the task, and let the
recipient verify its hash. File publication and task completion are separate
operations; no distributed transaction is implied. A failed run may leave an
unreferenced file. Retry the same operation ID to recover the recorded reference.

## Wake-up event

Expose `work.available`, with subscription arguments `{agentId}` restricted to
the connected agent. The event data schema is `{agentId, workId, taskId, reason}`;
`reason` is `new_task`, `task_changed` or `retry`. These are notification records,
not executable instructions. Example outbound callback body:

```json
{
  "eventId": "evt_123",
  "name": "work.available",
  "timestamp": "2026-10-05T18:00:00Z",
  "data": {
    "agentId": "agt_moneypenny",
    "workId": "work_123",
    "taskId": "task_123",
    "reason": "new_task"
  },
  "cursor": null
}
```

ChatGPT supplies the callback through `events/subscribe`. Verify that public HTTPS
destination and persist the subscription. Deliver signed Standard Webhooks, retain
event IDs on retries, renew before expiration and implement unsubscribe. A callback
2xx confirms receipt; it does not acknowledge inbox work or complete a task.
For the prototype, event replay cursors are unavailable (`cursor: null`). Recover
from the durable inbox and issue a fresh notification after a lease expires or a
subscription recovers. Follow [MCP Events](https://developers.openai.com/plugins/build/mcp-events)
for callback verification, signing, retry and subscription semantics.

Moneypenny's standing instruction is to process available work when this event
arrives. A failed unattended run leaves its work recoverable. Other runtimes use
their own verified event hook or periodically call `receive_work`; accepting an
HTTP connection does not prove that their agents can wake.

## One handoff through the interface

The requester uses `write_file` or its AFS mount to publish a brief. It calls
`send_message` with an approved recipient ID and this canonical request:

```json
{
  "to": "agt_moneypenny",
  "request": {
    "message": {
      "messageId": "msg_brief_123",
      "role": "ROLE_USER",
      "parts": [
        {"text": "Review this brief and return a feedback file."},
        {"data": {"afs": {
          "workspaceId": "ws_123",
          "path": "handoffs/123/brief.md",
          "sha256": "<hash returned by write_file>",
          "sizeBytes": 1200
        }}}
      ]
    },
    "configuration": {"returnImmediately": true}
  }
}
```

The placeholder hash must be replaced with the real 64-character SHA-256 before
validation. The result includes the receiver-created Task. Moneypenny wakes,
calls `receive_work`, verifies the brief using `read_file`, marks the task working
and writes a review. She finishes with `update_task` and an A2A artifact containing
the returned review reference. The requester receives a task update, reads the
matching review and acknowledges that update. Neither agent handles Redis auth
or addresses the other's runtime directly.

## Internal transport and customer isolation

HTTPS/MCP connects runtimes to the hosted service. A2A over Redis Streams connects
their adapters inside it. Keep the private transport described in
[the initial design](agent-communication.md), with these changes for multiple users:

- Each group has separately addressed streams, tasks, deduplication, leases,
  subscriptions and agent membership. Use server-derived group/agent IDs for all
  routing. Namespace every key, including reply routes and consumer groups.
- A group maps to one existing AFS workspace. Reuse the AFS engine and catalog;
  the group record stores a workspace reference rather than a second file catalog.
  Use distinct mount roots and enforce the group-to-storage mapping on every
  operation. Start with separately credentialed Redis database services or
  instances if shared-storage isolation has not been proven. Redis logical DB
  numbers and key prefixes alone are not an authorization boundary; shared
  storage requires verified service authorization and applicable Redis ACLs.
  See [Redis SELECT](https://redis.io/docs/latest/commands/select/) and
  [Redis ACLs](https://redis.io/docs/latest/management/security/acl/).
- A receiver admits canonical A2A requests and sends canonical responses/status
  and artifact updates through the originating adapter's stream. Preserve sender,
  recipient, operation, request correlation and protocol/service parameters in
  the transport envelope; clients cannot insert or change those identities.
- Implement `SendMessage`, `GetTask`, `ListTasks` and `CancelTask`; advertise
  optional streaming, A2A push notifications and extended cards as unavailable
  initially and return their specified errors. Internal update delivery and MCP
  Events do not imply support for A2A's optional streaming/push interfaces.
- Publish a versioned Redis binding identifier owned by this service, pin the A2A
  SDK/protocol version and validate operation/error semantics before claiming
  interoperability. The customer MCP endpoint is not a standard A2A HTTP endpoint.

The transport is at least once. Claim leases and idempotent task admission reduce
duplicate work but do not guarantee exactly-once external actions. Redis stream
acknowledgements occur after durable admission; task completion follows later.
Redis persistence configuration determines survival of storage failure. See
[A2A custom bindings](https://a2a-protocol.org/latest/topics/custom-protocol-bindings/).

## Errors and first implementation boundary

Successful tools return their documented JSON in MCP structured content. Tool
execution failures use `isError: true` and
`{error: {code, message, retryable, retryAfterSeconds?, currentRevision?, details?}}`.
Invalid MCP framing/parameters use the SDK's protocol errors. Missing/invalid
authentication uses the standard OAuth challenge; authorization is enforced on
every operation.

| Bridge error | Meaning |
| --- | --- |
| `AccessDenied` | Connection or operation lacks permission. |
| `NotFound` | Agent/task/file is absent or inaccessible; do not disclose another group's objects. |
| `IdempotencyConflict` | An operation/message ID was reused with different arguments. |
| `RevisionConflict` | Task changed; retrieve current state before a new operation. |
| `LeaseLost` | Execution/delivery claim expired, was canceled or belongs to another run. |
| `FileNotReady` | Referenced file is absent or its hash differs; retry with backoff. |
| `FileExists` | A write would replace a published file. |
| `LimitExceeded` | The request exceeds the documented size or batch limit. |
| `RateLimited` | Retry after the supplied delay. |
| `Unavailable` | Storage/transport temporarily unavailable; preserve retry IDs. |

For an A2A failure, `code` is its specified error type name, with its original
error data in `details`; do not translate it into a successful task. A terminal
failed Task is a valid task outcome, distinct from an API failure. Check
idempotency records before stale
revision/lease checks on replay so a successful completion retry recovers its
recorded result. Keep deduplication records for retained tasks; do not silently
expire their IDs in the prototype.

Build one persistent companion service with an MCP/OAuth edge, Redis dispatchers
and a configured AFS mount per group. Metadata and subscriptions need durable
storage. A minimal owner page handles approval and disconnect. Keep this product
outside AFS's CLI and control plane; this document does not authorize adding
cloud/MCP commands to `afs` or changing its trusted-administrator key model.

First acceptance: connect two agents through the same URL, sign in/approve both,
discover only the approved peers and complete the unattended brief/review round
trip. Use disposable Redis. Also prove duplicate admission, lease-expiry recovery,
late file arrival, disconnection, cancellation/completion races and rejection of
cross-group stream/task/file/subscription access. Product billing, large-file
uploads and additional integrations can follow after this works.

This defines a proposed interface, not a deployed service or a completed protocol
binding. The other agent's runtime and Moneypenny's actual subscription/wake-up
compatibility remain to be verified before implementation.
