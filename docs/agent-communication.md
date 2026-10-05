# A2A over Redis Streams with an AFS workspace

Proposed design, 5 October 2026. Connect Moneypenny, the user's ChatGPT dot,
with one other agent so they can communicate, hand off work and share files.
Moneypenny should process incoming work unattended.

Use A2A for the conversation and task model, Redis Streams for delivery and AFS
for the shared files. Small runtime adapters connect the two existing agents.
This companion integration stays separate from AFS's CLI, storage engine and
control plane.

## Three responsibilities

| Component | Responsibility |
| --- | --- |
| A2A | Messages, task state, follow-up questions and result artifacts. |
| Redis Streams | Transport requests, responses and updates between the adapters. |
| AFS workspace | Synchronize briefs, work in progress and result files. |

Both agents can initiate work for the other. Their adapters translate runtime
actions into A2A operations and expose incoming work to the actual agents.
One companion process can host the adapters and shared Redis task state.

## Redis Streams transport

Start with one inbound stream per adapter, under a separate `agentlink:`
namespace. Each stream carries requests, responses and task updates. A small
transport envelope identifies the authenticated sender, request correlation,
agreed A2A version and record kind; its payload uses the canonical A2A objects.

A receiver's consumer group reads its stream. Persist request deduplication,
task state and any outgoing response or notification before acknowledging
receipt. Replies return through the sender's stream with the same request
correlation. Use one active dispatcher per adapter initially to keep task
updates ordered. Retain stream entries initially, without automatic trimming.

A Redis acknowledgement means the adapter durably admitted the request.
An A2A task reaching a terminal state records the work's outcome. Keep those
two events distinct. Duplicate transport delivery returns the recorded result
or current task instead of starting a second task.

This is a proposed private Redis Streams binding. A2A explicitly permits custom
bindings, but using its object names alone does not establish compatibility.
Declare the binding in the Agent Card with a URI owned by this project, pin the
supported protocol version, map the core operations and errors, and document
authentication and replay behavior. Optional capabilities can be advertised as
unavailable with the specified error responses. See
[custom bindings](https://a2a-protocol.org/latest/topics/custom-protocol-bindings/)
and the [A2A specification](https://a2a-protocol.org/latest/specification/).
Full conformance remains implementation work.

Use existing A2A SDK definitions and validation where supported. The existing
[a2a-redis integration](https://github.com/redis-developer/a2a-redis) supplies
Redis task stores and Streams event queues for the Python SDK; evaluate its
reusable components and version compatibility before implementation. Those
components are not, by themselves, a complete agent-to-agent Redis transport.

## Shared files

Use one AFS workspace with this convention:

```text
shared/                       reusable reference material
handoffs/<handoff-id>/brief.md input for one handoff
handoffs/<handoff-id>/review.md recipient's output
```

A2A messages refer to input files. A2A result artifacts refer to output files.
The file bytes stay in AFS. Our small file-reference convention carries the
workspace ID, relative path and SHA-256 in a structured data part:

```json
{
  "message": {
    "messageId": "a6b32607-048a-4263-96b4-0c6d20d12614",
    "role": "ROLE_USER",
    "parts": [
      {"text": "Please review this brief and write your feedback beside it."},
      {
        "data": {
          "afs": {
            "workspaceId": "<configured workspace ID>",
            "path": "handoffs/a6b32607-048a-4263-96b4-0c6d20d12614/brief.md",
            "sha256": "<SHA-256 of the file bytes>"
          }
        }
      }
    ]
  },
  "configuration": {"returnImmediately": true}
}
```

This illustrates the A2A v1 request model and our proposed file-reference data;
it is not a complete Redis transport frame. The AFS reference is a convention
agreed by these two adapters, not a standard A2A filesystem reference.
The receiver creates the task ID; the message ID identifies the sender's
request. Follow-up messages use the returned task and context IDs.

Keep handed-off inputs immutable. Give each artifact one writer and use new
paths for revisions. Messages and files arrive independently: the receiver
starts from a referenced file only after its bytes match the supplied hash.
If the file is absent or different, leave work unfinished and retry with
backoff. A persistent mismatch needs attention.

## Moneypenny adapter

Moneypenny connects through a plugin that provides send, receive, update-task,
read-file and write-file tools. Her adapter maps these to the A2A task model;
the file tools use an ordinary AFS mount on the companion host. This provides
cloud access without assuming the dot has a persistent AFS mount herself.
The host must stay online and reachable through authenticated HTTPS.

The plugin exposes one `work.available` event, filtered to Moneypenny. It
translates new A2A requests and relevant task updates into MCP Events
notifications. OpenAI currently
documents this integration for dots using persistent subscriptions and signed
HTTPS callbacks. Confirm an unattended run in Moneypenny's actual account.
See [MCP Events](https://developers.openai.com/plugins/build/mcp-events).

MCP connects the dot to her adapter; Redis Streams connects the A2A adapters.
An MCP callback receipt does not complete the A2A task. Persist pending work and
subscription state across restarts, preserve event IDs across delivery retries,
and surface unfinished work again on subscription recovery.

Each agent has its own adapter credential. Credentials determine the sender
and access to assigned work. File tools stay within the configured mount,
including when resolving symlinks. Redis credentials remain on the companion.

## One complete handoff

1. The other agent writes the brief into its AFS mount and sends an A2A message
   through Redis Streams, including the file reference.
2. Moneypenny's adapter admits a task and notifies her through the plugin.
3. She reads the matching brief, marks the task working and writes her review.
4. Her adapter records the completed task with an artifact referencing the
   review, then sends the task update through the other agent's stream.
5. The other agent reads the matching review from its AFS mount.

Questions use A2A messages and the input-required task state. Failures and
cancellation use the A2A task outcomes. Further work after a terminal task starts
a new task. The sender can retrieve current task state if an update was missed.

The other agent's runtime is still to be identified. Its adapter needs an
existing turn, idle or scheduled entry point to get incoming work into the
agent's next turn. Stream receipt alone does not start that turn.

## First acceptance

The prototype succeeds when the other agent sends a brief, Moneypenny reviews
it unattended, and her result and task update reach the other agent. Verify
delayed file arrival, duplicate delivery, adapter restart, failed dot execution
and a clarification round trip using disposable Redis and separate client state.

Delivery is at least once; idempotent admission and completion prevent duplicate
finished handoffs. A crash during unfinished work may cause a retry, so this
does not promise exactly-once external actions. Redis persistence settings
determine survival of server failures. Files and task completion are not one
transaction: create the result file before completing its task.

This is a design proposal. No adapters, Redis binding, plugin or monitoring
subscription have been implemented or connected.
