import type { AFSAgentSession } from "./types/afs";

export function isConnectedAgentSession(agent: AFSAgentSession): boolean {
  // Closed/stale records remain available as history, but are not live mounts.
  // Keep the syncing state supported by the existing session table.
  return (
    agent.state === "active" ||
    agent.state === "starting" ||
    agent.state === "syncing"
  );
}
