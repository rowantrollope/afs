# AgentConnect service interface

AgentConnect is now a separate project at
[rowantrollope/agentconnect](https://github.com/rowantrollope/agentconnect).
See its [service interface](https://github.com/rowantrollope/agentconnect/blob/main/docs/service-interface.md)
and [pairing/account ownership contract](https://github.com/rowantrollope/agentconnect/blob/main/docs/pairing.md).

AgentConnect completely wraps AFS server-side. Agents use its communication and
file tools without installing AFS or configuring Redis. AFS remains independently
usable; this repository does not acquire public agent APIs, pairing or Account
authentication. No AgentConnect service has been deployed.
