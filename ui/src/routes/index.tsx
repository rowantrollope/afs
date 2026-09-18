import { Loader } from "@redis-ui/components";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import styled from "styled-components";
import { NoticeBody, NoticeCard, PageStack } from "../components/afs-kit";
import { SurfaceCard } from "../components/card-shell";
import { LiveTopologyCard } from "../components/live-topology-card";
import {
  useScopedActivity,
  useScopedAgents,
  useScopedWorkspaceSummaries,
} from "../foundation/database-scope";
import { ActivityTable } from "../foundation/tables/activity-table";
import type {
  AFSActivityEvent,
  AFSAgentSession,
} from "../foundation/types/afs";

export const Route = createFileRoute("/")({ component: MonitorPage });
function MonitorPage() {
  const navigate = useNavigate();
  const workspaces = useScopedWorkspaceSummaries();
  const agents = useScopedAgents();
  const activity = useScopedActivity(50);
  if (workspaces.isLoading || agents.isLoading)
    return <Loader data-testid="loader--spinner" />;
  const error = workspaces.error || agents.error;
  function openActivity(event: AFSActivityEvent) {
    if (event.workspaceId)
      void navigate({
        to: "/workspaces/$workspaceId",
        params: { workspaceId: event.workspaceId },
        search: { databaseId: event.databaseId, tab: "changes" },
      });
  }
  return (
    <PageStack>
      {error && (
        <NoticeCard $tone="danger" role="alert">
          <NoticeBody>{error.message}</NoticeBody>
        </NoticeCard>
      )}
      <StatusHeader
        workspaces={workspaces.data.length}
        activeSessions={agents.data.length}
        opsPerMin={computeOpsPerMin(activity.data)}
        loading={activity.isLoading}
      />
      <MissionHudPanel agents={agents.data} />
      <LiveTopologyCard agents={agents.data} workspaces={workspaces.data} />
      <ActivityCard>
        <ActivityCardHeader>
          <ActivityCardEyebrow>Live activity</ActivityCardEyebrow>
          <ActivityCardSub>
            What your CLI and agents are doing right now.
          </ActivityCardSub>
        </ActivityCardHeader>
        <ActivityTable
          rows={activity.data}
          loading={activity.isLoading}
          error={activity.isError}
          errorMessage={activity.error?.message}
          onOpenActivity={openActivity}
        />
      </ActivityCard>
    </PageStack>
  );
}
function compareMonitorAgents(a: AFSAgentSession, b: AFSAgentSession) {
  return monitorAgentSortKey(a).localeCompare(monitorAgentSortKey(b));
}

function monitorAgentSortKey(agent: AFSAgentSession) {
  return [
    agentDisplayLabel(agent),
    agent.workspaceName,
    agent.hostname,
    agent.sessionId,
  ]
    .map((value) => value.trim().toLowerCase())
    .join("\u0000");
}

function agentDisplayLabel(agent: AFSAgentSession) {
  const sessionName = agent.sessionName?.trim();
  const agentName = agent.agentName?.trim();
  if (sessionName && agentName && sessionName !== agentName) {
    return `${sessionName} · ${agentName}`;
  }
  return (
    sessionName ||
    agentName ||
    agent.label?.trim() ||
    agent.agentId ||
    agent.hostname ||
    agent.sessionId
  );
}

function isAgentIdle(agent: AFSAgentSession) {
  const last = Date.parse(agent.lastSeenAt);
  if (!Number.isFinite(last)) return true;
  return Date.now() - last > 30_000;
}

function relativeAgentSeen(iso: string) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "—";
  const seconds = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

// ──────────────────────────────────────────────────────────────────────
// Helpers — uptime formatting
// ──────────────────────────────────────────────────────────────────────

function uptimeText(iso: string) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "—";
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600)
    return `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`;
  if (s < 86400) {
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    return `${h}h${String(m).padStart(2, "0")}m`;
  }
  return `${Math.floor(s / 86400)}d`;
}

// ──────────────────────────────────────────────────────────────────────
// MissionHudPanel — htop / ps-aux feel. Mono column header. Per-agent row
// shows uptime, last-seen time, and client kind + RO/RW.
// ──────────────────────────────────────────────────────────────────────
function MissionHudPanel({ agents }: { agents: AFSAgentSession[] }) {
  if (agents.length === 0) return null;
  const ordered = [...agents].sort(compareMonitorAgents);

  return (
    <HudCard>
      <HudHeader>
        <HudEyebrow>
          <HudCursor />
          ACTIVE AGENTS [{agents.length}]
        </HudEyebrow>
      </HudHeader>
      <HudTable>
        <HudColRow $head>
          <HudCol>AGENT</HudCol>
          <HudCol>WORKSPACE</HudCol>
          <HudCol>KIND</HudCol>
          <HudCol>UP</HudCol>
          <HudCol $right>LAST SEEN</HudCol>
        </HudColRow>
        {ordered.map((agent) => {
          const idle = isAgentIdle(agent);
          const lastSeen = relativeAgentSeen(agent.lastSeenAt);
          return (
            <HudColRow key={agent.sessionId}>
              <HudCol>
                <HudActiveMark $idle={idle} />
                <HudAgentName>{agentDisplayLabel(agent)}</HudAgentName>
              </HudCol>
              <HudCol $accent>{agent.workspaceName}</HudCol>
              <HudCol>
                {agent.clientKind} {agent.readonly ? "ro" : "rw"}
              </HudCol>
              <HudCol>{uptimeText(agent.startedAt)}</HudCol>
              <HudCol $muted $right>
                {lastSeen}
              </HudCol>
            </HudColRow>
          );
        })}
      </HudTable>
    </HudCard>
  );
}

// Compact inline status. Replaces the four stat cards with a single line that
// reads like a process header: live indicator, key counts, current op rate.
function StatusHeader({
  workspaces,
  activeSessions,
  opsPerMin,
  loading,
}: {
  workspaces: number;
  activeSessions: number;
  opsPerMin: number;
  loading: boolean;
}) {
  return (
    <StatusBar>
      <StatusLive>
        <StatusDot $live={!loading} />
        <StatusLiveText>{loading ? "loading" : "live"}</StatusLiveText>
      </StatusLive>
      <StatusSep>·</StatusSep>
      <StatusItem>
        <StatusValue>{workspaces}</StatusValue>
        <StatusLabel>workspace{workspaces === 1 ? "" : "s"}</StatusLabel>
      </StatusItem>
      <StatusSep>·</StatusSep>
      <StatusItem>
        <StatusValue>{activeSessions}</StatusValue>
        <StatusLabel>
          active session{activeSessions === 1 ? "" : "s"}
        </StatusLabel>
      </StatusItem>
      <StatusSep>·</StatusSep>
      <StatusItem>
        <StatusValue>{opsPerMin}</StatusValue>
        <StatusLabel>ops/min</StatusLabel>
      </StatusItem>
    </StatusBar>
  );
}

// Count activity events whose createdAt falls within the last 60s.
function computeOpsPerMin(events: AFSActivityEvent[]) {
  const cutoff = Date.now() - 60_000;
  return events.reduce((count, e) => {
    const t = Date.parse(e.createdAt);
    return Number.isFinite(t) && t >= cutoff ? count + 1 : count;
  }, 0);
}

const StatusBar = styled(SurfaceCard)`
  display: flex;
  align-items: baseline;
  gap: 12px;
  flex-wrap: wrap;
  padding: 14px 18px;
  font-family: var(--afs-mono, "Monaco", "Menlo", monospace);
  font-size: 13px;
`;

const StatusLive = styled.div`
  display: inline-flex;
  align-items: center;
  gap: 6px;
`;

const StatusDot = styled.span<{ $live?: boolean }>`
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: ${(p) => (p.$live ? "#22c55e" : "var(--afs-muted)")};
  box-shadow: ${(p) => (p.$live ? "0 0 8px rgba(34, 197, 94, 0.5)" : "none")};
  animation: ${(p) =>
    p.$live ? "afs-status-pulse 2s ease-in-out infinite" : "none"};

  @keyframes afs-status-pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.4;
    }
  }
`;

const StatusLiveText = styled.span`
  color: var(--afs-accent);
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  font-size: 11px;
`;

const StatusSep = styled.span`
  color: var(--afs-line-strong);
`;

const StatusItem = styled.span`
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
`;

const StatusValue = styled.span`
  color: var(--afs-ink);
  font-weight: 700;
  font-variant-numeric: tabular-nums;
`;

const StatusLabel = styled.span`
  color: var(--afs-muted);
  font-size: 12px;
`;

const ActivityCard = styled(SurfaceCard).attrs({ as: "section" })`
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 18px 22px;
`;

// ──────────────────────────────────────────────────────────────────────
// MissionHudPanel styles
// ──────────────────────────────────────────────────────────────────────
const HudCard = styled(SurfaceCard).attrs({ as: "section" })`
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px 16px 10px;
  font-family: var(--afs-mono, "Monaco", "Menlo", monospace);
  background: var(--afs-panel-strong);
`;

const HudHeader = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
`;

const HudEyebrow = styled.h3`
  margin: 0;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--afs-ink);
  font-size: 11px;
  font-weight: 800;
  letter-spacing: 0.14em;
  text-transform: uppercase;
`;

const hudCursorBlink = `
  @keyframes afs-hud-cursor {
    0%, 49% { opacity: 1; }
    50%, 100% { opacity: 0; }
  }
`;

const HudCursor = styled.span`
  ${hudCursorBlink}
  display: inline-block;
  width: 7px;
  height: 12px;
  background: #22c55e;
  box-shadow: 0 0 6px rgba(34, 197, 94, 0.7);
  animation: afs-hud-cursor 1.1s steps(1, end) infinite;
`;

const HudTable = styled.div`
  display: flex;
  flex-direction: column;
  border-top: 1px solid var(--afs-line);
  border-bottom: 1px solid var(--afs-line);
`;

const HudColRow = styled.div<{ $head?: boolean }>`
  display: grid;
  grid-template-columns:
    minmax(0, 3fr)
    minmax(0, 1.4fr)
    minmax(0, 0.9fr)
    minmax(60px, auto)
    minmax(0, 1fr);
  align-items: center;
  gap: 14px;
  padding: ${(p) => (p.$head ? "6px 4px" : "7px 4px")};
  border-bottom: 1px dashed
    ${(p) => (p.$head ? "var(--afs-line)" : "transparent")};
  font-size: ${(p) => (p.$head ? "10px" : "11px")};
  color: ${(p) => (p.$head ? "var(--afs-muted)" : "var(--afs-ink)")};
  letter-spacing: ${(p) => (p.$head ? "0.12em" : "0")};
  text-transform: ${(p) => (p.$head ? "uppercase" : "none")};

  &:not(:first-child):hover {
    background: var(--afs-selection-hover-bg);
    color: var(--afs-selection-hover-ink);
  }
`;

const HudCol = styled.span<{
  $accent?: boolean;
  $muted?: boolean;
  $right?: boolean;
}>`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow-wrap: anywhere;
  white-space: normal;
  justify-content: ${(p) => (p.$right ? "flex-end" : "flex-start")};
  text-align: ${(p) => (p.$right ? "right" : "left")};
  color: ${(p) =>
    p.$accent
      ? "var(--afs-accent)"
      : p.$muted
        ? "var(--afs-muted)"
        : "inherit"};
  font-weight: ${(p) => (p.$accent ? 700 : 400)};
`;

// Solid row indicator. No animation — only the header HudCursor blinks.
const HudActiveMark = styled.span<{ $idle: boolean }>`
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: ${(p) =>
    p.$idle ? "var(--afs-line-strong, var(--afs-muted))" : "#22c55e"};
  box-shadow: ${(p) =>
    p.$idle
      ? "none"
      : "0 0 8px rgba(34,197,94,0.65), 0 0 0 2px rgba(34,197,94,0.18)"};
  flex: 0 0 auto;
`;

const HudAgentName = styled.span`
  color: var(--afs-ink);
  font-weight: 700;
  overflow-wrap: anywhere;
  white-space: normal;
`;

const ActivityCardHeader = styled.div`
  display: flex;
  flex-direction: column;
  gap: 4px;
`;

const ActivityCardEyebrow = styled.h2`
  margin: 0;
  color: var(--afs-ink);
  font-size: 16px;
  font-weight: 700;
  letter-spacing: -0.01em;
`;

const ActivityCardSub = styled.p`
  margin: 0;
  color: var(--afs-muted);
  font-size: 13px;
  line-height: 1.5;
`;
