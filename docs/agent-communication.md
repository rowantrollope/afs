# Two agent communication with an AFS workspace

Proposed design, 5 October 2026. Connect Moneypenny, the user's ChatGPT dot,
with one other agent so they can exchange messages, share files and hand work
to each other. Moneypenny should process incoming messages unattended.

Use one AFS workspace, two Redis Streams inboxes and one small companion
bridge. Requests, progress updates and replies are all ordinary messages.
The bridge is separate from AFS's CLI, storage engine and control plane.

## Components

| Component | Responsibility |
| --- | --- |
| AFS workspace | Synchronize briefs, work in progress and finished artifacts. |
| Redis Streams | Keep one message inbox for each agent, with recoverable delivery. |
| Companion bridge | Provide authenticated message/file tools and notify Moneypenny. |

The other agent uses its local AFS mount for files and a small command-line
adapter to call the bridge. The bridge also mounts that same workspace and
provides file access to Moneypenny through a connected plugin. This makes the
shared files available to her cloud runtime without assuming she has a local
AFS mount. The bridge host must stay online and reachable over HTTPS.

The other agent's runtime is still to be identified. It needs an inbox check
at an existing turn, idle or scheduled entry point. A Redis consumer can receive
messages, but getting them into an agent's next turn requires that runtime
hook. Unattended receipt on that side is an acceptance condition to confirm.

## Five tools

The plugin and command-line adapter expose the same five operations:

| Tool | Behavior |
| --- | --- |
| `send_message(id, to, text, files)` | Queue a message; retrying the same ID and content returns the original receipt. |
| `get_inbox()` | Return messages that have not been finished, including ones from earlier runs. |
| `finish_message(id, reply, files)` | Record completion and optionally queue a reply in one idempotent Redis operation. |
| `read_file(path, expected_sha256?)` | Read a workspace file; when a hash is supplied, require matching bytes. |
| `write_file(path, content)` | Create an artifact through the bridge's AFS mount and return its path and hash. |

The authenticated connection determines the sender and which inbox it can read
or finish. File paths are relative to the one configured workspace. The file
tools stay within that mount, including when resolving symlinks. Each agent has
its own bridge credential; Redis credentials stay on the bridge.

## A message

```json
{
  "id": "a6b32607-048a-4263-96b4-0c6d20d12614",
  "from": "peer",
  "to": "moneypenny",
  "text": "Please review this brief and write your feedback beside it.",
  "reply_to": null,
  "files": [
    {
      "path": "handoffs/a6b32607-048a-4263-96b4-0c6d20d12614/brief.md",
      "sha256": "<SHA-256 of the file bytes>"
    }
  ]
}
```

The bridge stamps `from` and the creation time. A reply carries its original
message's ID in `reply_to`. The sender creates an ID before its first send and
reuses it for retries. Reusing an ID with different content is rejected.

Start with this folder convention:

```text
shared/                         reusable reference material
handoffs/<message-id>/brief.md   input for one handoff
handoffs/<message-id>/review.md  recipient's output
```

Keep handed-off inputs immutable. Each artifact has one writer; revisions use
new paths. Agents can work on different files concurrently through existing AFS
sync. This avoids requiring a shared Markdown document to act as a task lock.

## One complete handoff

1. The other agent writes `brief.md` into its mount and sends a message with
   that workspace-relative path and the file's hash.
2. The bridge stores the message in Moneypenny's inbox and sends a notification
   through her event subscription.
3. Moneypenny reads the message and brief, writes `review.md`, then calls
   `finish_message` with her reply and the review's path and hash.
4. The other agent receives the reply, reads the review and finishes that
   message. The files remain in the shared workspace.

Messages and files arrive independently. If a referenced file is absent or has
different bytes, `read_file` returns `not_ready` and the message remains pending.
Retry later with backoff; a persistent mismatch needs attention. Never treat a
notification as proof that the referenced file has synchronized.

## Wake Moneypenny

Expose one plugin event, `inbox.updated`, filtered to the connected agent's
inbox. Moneypenny subscribes with an instruction to read incoming messages,
complete authorized work and reply through the bridge.

OpenAI's current documentation describes MCP Events support for dots using
authenticated MCP 2.0, persistent subscriptions and signed HTTPS callbacks.
The bridge translates new inbox entries into those events. Verify subscription
and unattended processing in Moneypenny's actual account before treating this
as operational. See [MCP Events](https://developers.openai.com/plugins/build/mcp-events).

Suggested instruction for Moneypenny:

> Monitor my inbox in the agent communication plugin. For each new message,
> read the referenced files, carry out the requested work within the permissions
> I have granted, and finish the message with a reply and any result files.
> If the files are still synchronizing, leave the message pending and retry.
> Tell me when a decision is needed or a handoff fails. Keep routine exchanges
> in the channel.

Installing the plugin alone does not create the monitoring responsibility.
Confirm the saved subscription and observe an unattended run.

## Small reliability contract

Use an `agentlink:` Redis prefix for inboxes, send deduplication, completion
records and subscription state, separate from AFS's `afs:` keys. Reuse the
existing self-managed Redis if appropriate; the bridge does not consume AFS's
internal change streams. Retain messages without automatic trimming initially.

Use a delivery consumer group per inbox for notification retries. Its Redis
acknowledgement records notification delivery; `finish_message` separately
records completed agent work. An HTTP callback receipt also does not mean the
work finished. Pending messages stay available through `get_inbox` across
restarts. On subscription creation or recovery, surface unfinished messages
with a new `inbox.updated` notification. Preserve that notification's event ID
across delivery retries. Redis [consumer groups](https://redis.io/docs/latest/commands/xreadgroup/)
provide delivery tracking and access to pending entries.

Delivery is at least once. Store completed IDs and make `finish_message`
idempotent. A crash during incomplete work can cause a retry; this does not
promise exactly-once external actions. Redis persistence settings determine
survival of Redis server failures. AFS files and message completion are not a
single transaction, so create the result file before finishing its message.

## First acceptance

The prototype succeeds when a brief goes from the other agent to Moneypenny,
she processes it while the user is away, and her review and reply reach the
other agent through the same workspace and channel. Also verify:

- A delayed file causes a retry instead of work on stale bytes.
- Repeating a send or finish creates no duplicate message or completed reply.
- A bridge restart preserves pending messages and the event subscription.
- A received event followed by a failed agent run leaves the handoff recoverable.
- Each identity accesses its own inbox and file tools cannot escape the mount.

Use disposable Redis and separate client state for implementation tests.
This document proposes the design; no bridge, plugin or monitoring subscription
has been implemented or connected.
