import { useEffect, useState } from "react";
import styled from "styled-components";
import { displayAgentPrimaryName } from "../foundation/agent-identity";
import type { AFSAgentSession } from "../foundation/types/afs";

const fields = [
  { key: "sessionName", label: "Session name" },
  { key: "agentName", label: "Agent name" },
  { key: "label", label: "Label" },
  { key: "agentId", label: "Agent ID" },
  { key: "user", label: "User" },
  { key: "afsVersion", label: "Agent / AFS version" },
  { key: "sessionId", label: "Session ID" },
  { key: "localPath", label: "Mount path" },
  { key: "uptime", label: "Uptime" },
  { key: "clientKind", label: "Client kind" },
  { key: "operatingSystem", label: "Operating system" },
  { key: "readonly", label: "Access mode" },
] as const;

type DetailField = (typeof fields)[number]["key"];
const nameFields = ["sessionName", "agentName", "label", "agentId", "user", "sessionId"] as const;
type NameField = (typeof nameFields)[number];
type TopologyDisplay = {
  name: "auto" | NameField;
  details: DetailField[];
};
const storageKey = "afs.topology.display.v1";
const defaults: TopologyDisplay = {
  name: "agentName",
  details: ["localPath"],
};
const previousDefaultDetails = ["sessionName", "agentName", "label", "agentId", "user", "afsVersion", "localPath"];

function readDisplay(): TopologyDisplay {
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(storageKey) ?? "null");
    if (!stored || typeof stored !== "object") return defaults;
    const { name, details, defaultsVersion } = stored as Record<string, unknown>;
    // Earlier versions saved defaults on first render, even without a choice.
    // Upgrade that default while retaining every customized configuration.
    if (defaultsVersion !== 2 && name === "auto" && Array.isArray(details) &&
      details.length === previousDefaultDetails.length &&
      previousDefaultDetails.every((field) => details.includes(field))) {
      return defaults;
    }
    return {
      name: name === "auto" || nameFields.some((field) => field === name)
        ? name as TopologyDisplay["name"] : defaults.name,
      details: Array.isArray(details)
        ? fields.filter(({ key }) => details.includes(key)).map(({ key }) => key)
        : defaults.details,
    };
  } catch {
    return defaults;
  }
}

export function useTopologyDisplay() {
  const [display, setDisplay] = useState(readDisplay);
  useEffect(() => {
    try {
      localStorage.setItem(storageKey, JSON.stringify({ ...display, defaultsVersion: 2 }));
    } catch {
      // The controls still work if browser storage is unavailable.
    }
  }, [display]);
  return [display, setDisplay] as const;
}

export function topologyNodeName(agent: AFSAgentSession, display: TopologyDisplay) {
  return (display.name !== "auto" && agent[display.name]?.trim()) || displayAgentPrimaryName(agent);
}

export function useTopologyUptime(enabled: boolean) {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    const timer = window.setInterval(() => setTick((tick) => tick + 1), 1000);
    return () => window.clearInterval(timer);
  }, [enabled]);
}

function uptimeText(startedAt: string): string {
  const started = Date.parse(startedAt);
  if (!Number.isFinite(started)) return "";
  const seconds = Math.max(0, Math.floor((Date.now() - started) / 1000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, "0")}s`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ${String(Math.floor(seconds / 60) % 60).padStart(2, "0")}m`;
  return `${Math.floor(seconds / 86400)}d ${String(Math.floor(seconds / 3600) % 24).padStart(2, "0")}h`;
}

export function topologyNodeDetails(agent: AFSAgentSession, display: TopologyDisplay) {
  const seenNames = new Set([topologyNodeName(agent, display), agent.hostname.trim()]);
  return fields.flatMap(({ key, label }) => {
    if (!display.details.includes(key)) return [];
    const value = key === "readonly"
      ? agent.readonly ? "Read-only" : "Read / Write"
      : key === "uptime" ? uptimeText(agent.startedAt)
      : agent[key]?.trim();
    if (!value) return [];
    if (nameFields.some((name) => name === key)) {
      if (seenNames.has(value)) return [];
      seenNames.add(value);
    }
    return [{ key, label, value }];
  });
}

const Panel = styled.section`
  margin: -4px 0 20px;
  padding: 16px;
  border: 1px solid var(--afs-line);
  border-radius: 10px;
  color: var(--afs-ink);
  font-size: 13px;
  p { color: var(--afs-muted); line-height: 1.5; margin: 8px 0 16px; }
  select {
    max-width: 100%;
    margin-left: 10px;
    padding: 6px 10px;
    border: 1px solid var(--afs-line);
    border-radius: 6px;
    background: var(--afs-panel-strong);
    color: var(--afs-ink);
    font: inherit;
  }
  fieldset { border: 0; margin: 0; padding: 0; }
  legend { font-weight: 600; margin-bottom: 10px; }
`;

const Checkboxes = styled.div`
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
  gap: 10px 16px;
  label { display: flex; align-items: center; gap: 8px; cursor: pointer; }
  input { accent-color: var(--afs-accent, #dc2626); }
`;

export const TopologyConfigButton = styled.button`
  border: 1px solid var(--afs-line);
  background: var(--afs-panel-strong);
  color: var(--afs-ink);
  border-radius: 7px;
  padding: 7px 12px;
  cursor: pointer;
  font: inherit;
  font-size: 13px;
  white-space: nowrap;
  &:hover { background: var(--afs-selection-hover-bg); }
  &:focus-visible { outline: 2px solid var(--afs-accent, #dc2626); outline-offset: 2px; }
`;

export function LiveTopologyConfig({ id, display, onChange }: {
  id: string;
  display: TopologyDisplay;
  onChange: (display: TopologyDisplay) => void;
}) {
  return (
    <Panel id={id} aria-label="Live Topology configuration">
      <label>
        Node label
        <select value={display.name} onChange={(event) => onChange({ ...display, name: event.target.value as TopologyDisplay["name"] })}>
          <option value="auto">Automatic</option>
          {fields.filter(({ key }) => nameFields.some((name) => name === key)).map(({ key, label }) => (
            <option key={key} value={key}>{label}</option>
          ))}
        </select>
      </label>
      <p>Automatic uses the session name, agent name, label, agent ID or user, then the session ID. Missing labels use the same fallback. Choices are saved in this browser.</p>
      <fieldset>
        <legend>Show details when available</legend>
        <Checkboxes>
          {fields.map(({ key, label }) => (
            <label key={key}>
              <input type="checkbox" checked={display.details.includes(key)} onChange={(event) => onChange({
                ...display,
                details: event.target.checked ? [...display.details, key] : display.details.filter((field) => field !== key),
              })} />
              {label}
            </label>
          ))}
        </Checkboxes>
      </fieldset>
      <TopologyConfigButton type="button" style={{ marginTop: 16 }} onClick={() => onChange(defaults)}>Reset defaults</TopologyConfigButton>
    </Panel>
  );
}
