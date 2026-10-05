# Agent communication design

The A2A over Redis Streams and shared workspace design moved to the separate
[AgentConnect repository](https://github.com/rowantrollope/agentconnect).
See its [architecture](https://github.com/rowantrollope/agentconnect/blob/main/docs/architecture.md)
and [service interface](https://github.com/rowantrollope/agentconnect/blob/main/docs/service-interface.md).

AgentConnect privately provisions and supervises AFS workspaces and mounts.
Public agents receive AgentConnect tools and credentials, not AFS administrator
or Redis credentials. Keep AFS's existing CLI, engine, folder sync and control
plane independent. The prior proposal remains recoverable in Git history.
