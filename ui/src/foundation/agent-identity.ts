import type { AFSAgentSession } from "./types/afs";

export function agentMetadata(agent: AFSAgentSession) {
  return [
    { label: "Session Name", value: agent.sessionName },
    { label: "Agent Name", value: agent.agentName },
    { label: "Label", value: agent.label },
    { label: "Agent ID", value: agent.agentId },
    { label: "User", value: agent.user },
    { label: "Agent / AFS Version", value: agent.afsVersion },
  ]
    .map(({ label, value }) => ({ label, value: value?.trim() ?? "" }))
    .filter(({ value }) => value !== "");
}

// Hosts have their own topology group and table column. Prefer supplied mount
// identity over the default hostname label or the generated session ID.
export function displayAgentPrimaryName(agent: AFSAgentSession): string {
  const host = agent.hostname.trim();
  const name = [agent.sessionName, agent.agentName, agent.label]
    .map((value) => value?.trim())
    .find((value) => value && value !== host);
  return (
    name ||
    agent.agentId?.trim() ||
    agent.user?.trim() ||
    `Session: ${agent.sessionId.trim() || "unknown"}`
  );
}

export function agentMetadataSummary(agent: AFSAgentSession): string {
  const seen = new Set([displayAgentPrimaryName(agent), agent.hostname.trim()]);
  return agentMetadata(agent)
    .filter(({ label, value }) => {
      // --label also populates agent_name; avoid repeating the same name.
      if (["Session Name", "Agent Name", "Label"].includes(label)) {
        if (seen.has(value)) return false;
        seen.add(value);
      }
      return true;
    })
    .map(({ label, value }) => `${label}: ${value}`)
    .join(" · ");
}
