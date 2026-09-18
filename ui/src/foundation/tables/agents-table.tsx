import { Typography } from "@redis-ui/components";
import type { ColumnDef, SortingState } from "@redis-ui/table";
import { Table } from "@redis-ui/table";
import type { ReactNode } from "react";
import { useEffect, useMemo, useState } from "react";
import styled, { css, keyframes } from "styled-components";
import {
  DialogCard,
  DialogCloseButton,
  DialogHeader,
  DialogOverlay,
  DialogTitle,
  MetaRow,
  Tag,
} from "../../components/afs-kit";
import { LiveTopologyCard } from "../../components/live-topology-card";
import { BotIcon } from "../../components/lucide-icons";
import { useStoredViewMode } from "../hooks/use-stored-view-mode";
import type { AFSAgentSession, AFSWorkspaceSummary } from "../types/afs";
import type { AgentSortField } from "./agents-table-utils";
import {
  displayAgentIdentityLabel,
  filterAndSortAgents,
  normalizeSearchValue,
} from "./agents-table-utils";
import { StatusNameCell, StatusNameLine } from "./status-name-cell";
import * as S from "./workspace-table.styles";

/* ------------------------------------------------------------------ */
/*  Helper: is the agent "active" (seen in the last 60 s)?            */
/* ------------------------------------------------------------------ */
function isAgentActive(agent: AFSAgentSession): boolean {
  return (
    agent.state === "active" ||
    agent.state === "starting" ||
    agent.state === "syncing"
  );
}

function timeAgo(iso: string): string {
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

function displayMountPath(path: string): string {
  return path.trim().replace(/^\/Users\/[^/]+\/?/, "~/");
}

function displayAgentName(agent: AFSAgentSession): string {
  return (
    agent.agentName?.trim() ||
    (!agent.sessionName?.trim() ? agent.label?.trim() : "") ||
    agent.agentId?.trim() ||
    ""
  );
}

function displaySessionName(agent: AFSAgentSession): string {
  const sessionName = agent.sessionName?.trim();
  if (sessionName) return sessionName;
  const label = agent.label?.trim();
  const agentName = agent.agentName?.trim();
  if (label && agentName && label !== agentName) return label;
  return "";
}

function displaySessionTitle(agent: AFSAgentSession): string {
  return displaySessionName(agent) || agent.sessionId.trim() || "session";
}

function displaySystemName(agent: AFSAgentSession): string {
  return agent.hostname.trim() || "unknown host";
}

/** Hook that ticks every second so uptime counters stay live. */
function useTick(intervalMs = 1000) {
  const [, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
}

/* ------------------------------------------------------------------ */
/*  Styled helpers                                                     */
/* ------------------------------------------------------------------ */
const pulse = keyframes`
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
`;

const ActiveDot = styled.span<{ $active: boolean }>`
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
  background: ${({ $active }) => ($active ? "#22c55e" : "#d1d5db")};
  ${({ $active }) =>
    $active &&
    css`
      box-shadow: 0 0 6px rgba(34, 197, 94, 0.5);
      animation: ${pulse} 2s ease-in-out infinite;
    `}
`;

const TablePrimaryText = styled.span`
  flex: 1 1 auto;
  min-width: 0;
  color: var(--afs-ink, #18181b);
  font-size: 14px;
  font-weight: 700;
  line-height: 1.2;
  overflow-wrap: anywhere;
  white-space: normal;
`;

const TableSecondaryText = styled.span`
  display: block;
  min-width: 0;
  color: var(--afs-muted, #71717a);
  font-size: 11.5px;
  font-weight: 600;
  line-height: 1.2;
  overflow-wrap: anywhere;
  white-space: normal;
`;

/* ---- Detail dialog ---- */
const DetailGrid = styled.div`
  display: grid;
  gap: 16px;
  grid-template-columns: 1fr 1fr;
  margin-top: 8px;

  @media (max-width: 600px) {
    grid-template-columns: 1fr;
  }
`;

const DetailField = styled.div`
  display: flex;
  flex-direction: column;
  gap: 4px;
`;

const DetailLabel = styled.span`
  color: var(--afs-muted, #71717a);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
`;

const DetailValue = styled.span`
  color: var(--afs-ink, #18181b);
  font-size: 14px;
  word-break: break-all;
`;

const DetailTimeValue = styled(DetailValue)`
  white-space: nowrap;
  word-break: normal;
`;

const TableTimeText = styled(Typography.Body)`
  white-space: nowrap;
`;

/* ------------------------------------------------------------------ */
/*  Types                                                              */
/* ------------------------------------------------------------------ */
type Props = {
  rows: AFSAgentSession[];
  // Required for the Map (topology) view. When omitted the toggle hides Map.
  workspaces?: AFSWorkspaceSummary[];
  loading?: boolean;
  error?: boolean;
  errorMessage?: string;
  toolbarAction?: ReactNode;
  onOpenWorkspace: (agent: AFSAgentSession) => void;
};

/* ------------------------------------------------------------------ */
/*  Detail dialog component                                            */
/* ------------------------------------------------------------------ */
export function AgentDetailDialog({
  agent,
  onClose,
  onOpenWorkspace,
}: {
  agent: AFSAgentSession;
  onClose: () => void;
  onOpenWorkspace: (agent: AFSAgentSession) => void;
}) {
  const active = isAgentActive(agent);

  return (
    <DialogOverlay onClick={onClose}>
      <DialogCard onClick={(e) => e.stopPropagation()}>
        <DialogHeader>
          <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
            <ActiveDot $active={active} style={{ width: 10, height: 10 }} />
            <DialogTitle>{agent.hostname || "Unknown Agent"}</DialogTitle>
          </div>
          <DialogCloseButton onClick={onClose}>&times;</DialogCloseButton>
        </DialogHeader>

        <DetailGrid>
          <DetailField>
            <DetailLabel>Status</DetailLabel>
            <DetailValue>
              {active ? "Active" : "Inactive"} &mdash; {agent.state}
            </DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Workspace</DetailLabel>
            <DetailValue>{agent.workspaceName}</DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Client Kind</DetailLabel>
            <DetailValue>{agent.clientKind || "client"}</DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Access Mode</DetailLabel>
            <DetailValue>
              {agent.readonly ? "Read-only" : "Read / Write"}
            </DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Operating System</DetailLabel>
            <DetailValue>{agent.operatingSystem || "Not reported"}</DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>AFS Version</DetailLabel>
            <DetailValue>{agent.afsVersion || "Unknown"}</DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Local Path</DetailLabel>
            <DetailValue>{agent.localPath || "Not reported"}</DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Agent Name</DetailLabel>
            <DetailValue>{displayAgentName(agent) || "Not set"}</DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Session Name</DetailLabel>
            <DetailValue>{displaySessionName(agent) || "Not set"}</DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Agent ID</DetailLabel>
            <DetailValue style={{ fontSize: 12 }}>
              {agent.agentId?.trim() || "Not set"}
            </DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Session ID</DetailLabel>
            <DetailValue style={{ fontSize: 12 }}>
              {agent.sessionId}
            </DetailValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Started</DetailLabel>
            <DetailTimeValue>
              {new Date(agent.startedAt).toLocaleString()}
            </DetailTimeValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Last Seen</DetailLabel>
            <DetailTimeValue>
              {new Date(agent.lastSeenAt).toLocaleString()}
            </DetailTimeValue>
          </DetailField>
          <DetailField>
            <DetailLabel>Lease Expires</DetailLabel>
            <DetailTimeValue>
              {new Date(agent.leaseExpiresAt).toLocaleString()}
            </DetailTimeValue>
          </DetailField>
        </DetailGrid>

        <MetaRow style={{ marginTop: 20 }}>
          <Tag>{agent.clientKind || "client"}</Tag>
          <Tag>{agent.readonly ? "readonly" : "read/write"}</Tag>
          {agent.operatingSystem ? <Tag>{agent.operatingSystem}</Tag> : null}
        </MetaRow>

        <div style={{ display: "flex", gap: 10, marginTop: 18 }}>
          <S.TextActionButton
            type="button"
            onClick={() => onOpenWorkspace(agent)}
          >
            Open Workspace
          </S.TextActionButton>
        </div>
      </DialogCard>
    </DialogOverlay>
  );
}

/* ------------------------------------------------------------------ */
/*  Main component                                                     */
/* ------------------------------------------------------------------ */
export function AgentsTable({
  rows,
  workspaces,
  loading = false,
  error = false,
  errorMessage = "Unable to load connected agents. Please retry.",
  toolbarAction,
  onOpenWorkspace,
}: Props) {
  const [search, setSearch] = useState("");
  const [sortBy, setSortBy] = useState<AgentSortField>("agentName");
  const [sortDirection, setSortDirection] = useState<"asc" | "desc">("asc");
  // viewMode = "map" | "table". Default to map (topology graphic).
  // The hook persists whichever value is set; legacy stored "cards" or
  // anything else is sanitized away by the validModes whitelist.
  const [viewMode, setViewMode] = useStoredViewMode<"map" | "table">(
    "afs.agents.viewMode",
    "map",
    ["map", "table"],
  );
  const mapAvailable = !!workspaces;
  const [selectedAgent, setSelectedAgent] = useState<AFSAgentSession | null>(
    null,
  );

  // Tick every second so live counters (uptime, time-ago) update in real-time.
  useTick();

  const filteredRows = useMemo(
    () => filterAndSortAgents(rows, search, sortBy, sortDirection),
    [rows, search, sortBy, sortDirection],
  );

  const sorting = useMemo<SortingState>(
    () => [{ id: sortBy, desc: sortDirection === "desc" }],
    [sortBy, sortDirection],
  );
  const isFiltering = normalizeSearchValue(search) !== "";

  const columns = useMemo(
    () =>
      [
        {
          accessorKey: "agentName",
          header: "Name",
          size: 240,
          enableSorting: true,
          cell: ({ row }) => {
            const active = isAgentActive(row.original);
            const identityLabel = displayAgentIdentityLabel(row.original);
            return (
              <StatusNameCell
                active={active}
                icon={<BotIcon customSize="18px" />}
                statusLabel={active ? "Active" : "Inactive"}
                statusTitle={active ? "Active" : "Inactive"}
              >
                <StatusNameLine>
                  <TablePrimaryText title={identityLabel}>
                    {identityLabel}
                  </TablePrimaryText>
                </StatusNameLine>
              </StatusNameCell>
            );
          },
        },
        {
          accessorKey: "sessionName",
          header: "Session",
          size: 240,
          enableSorting: true,
          cell: ({ row }) => {
            const sessionTitle = displaySessionTitle(row.original);
            const sessionId = row.original.sessionId.trim();
            return (
              <>
                <S.SingleLineText title={sessionTitle}>
                  {sessionTitle}
                </S.SingleLineText>
                {sessionId && sessionId !== sessionTitle ? (
                  <TableSecondaryText title={sessionId}>
                    {sessionId}
                  </TableSecondaryText>
                ) : null}
              </>
            );
          },
        },
        {
          accessorKey: "workspaceName",
          header: "Workspace",
          size: 180,
          enableSorting: true,
          cell: ({ row }) => (
            <S.SingleLineText title={row.original.workspaceName}>
              {row.original.workspaceName}
            </S.SingleLineText>
          ),
        },
        {
          accessorKey: "localPath",
          header: "Mount Path",
          size: 240,
          enableSorting: true,
          cell: ({ row }) => {
            const mountPath = row.original.localPath.trim();
            const displayPath = displayMountPath(mountPath);
            if (!mountPath) {
              return (
                <Typography.Body component="span" color="secondary">
                  &mdash;
                </Typography.Body>
              );
            }
            return (
              <S.SingleLineText title={mountPath}>
                {displayPath}
              </S.SingleLineText>
            );
          },
        },
        {
          accessorKey: "hostname",
          header: "System Name",
          size: 180,
          enableSorting: true,
          cell: ({ row }) => {
            const systemName = displaySystemName(row.original);
            return (
              <S.SingleLineText title={systemName}>
                {systemName}
              </S.SingleLineText>
            );
          },
        },
        {
          accessorKey: "lastSeenAt",
          header: "Last Active",
          size: 120,
          enableSorting: true,
          cell: ({ row }) => {
            const active = isAgentActive(row.original);
            return (
              <TableTimeText
                component="span"
                color={active ? undefined : "secondary"}
              >
                {active ? "Active now" : timeAgo(row.original.lastSeenAt)}
              </TableTimeText>
            );
          },
        },
      ] as ColumnDef<AFSAgentSession>[],
    [],
  );

  return (
    <>
      <S.TableBlock>
        <S.HeadingWrap style={{ padding: 0 }}>
          <S.SearchInput
            value={search}
            onChange={setSearch}
            placeholder="Search by name, path, workspace..."
          />
          <S.ToggleGroup>
            {mapAvailable ? (
              <S.ToggleButton
                $active={viewMode === "map"}
                aria-pressed={viewMode === "map"}
                onClick={() => setViewMode("map")}
              >
                Map
              </S.ToggleButton>
            ) : null}
            <S.ToggleButton
              $active={viewMode === "table"}
              aria-pressed={viewMode === "table"}
              onClick={() => setViewMode("table")}
            >
              Table
            </S.ToggleButton>
          </S.ToggleGroup>
          {toolbarAction}
        </S.HeadingWrap>

        {loading ? (
          <S.EmptyState>Loading connected agents...</S.EmptyState>
        ) : null}
        {error ? (
          <S.EmptyState role="alert">{errorMessage}</S.EmptyState>
        ) : null}
        {!loading && !error && filteredRows.length === 0 ? (
          <S.EmptyState>
            {isFiltering
              ? "No agents match the current filter."
              : "No connected agents are currently reporting in."}
          </S.EmptyState>
        ) : null}

        {/* ---- MAP (TOPOLOGY) VIEW ---- */}
        {!loading &&
        !error &&
        filteredRows.length > 0 &&
        viewMode === "map" &&
        mapAvailable ? (
          <LiveTopologyCard agents={filteredRows} workspaces={workspaces} />
        ) : null}

        {/* ---- TABLE VIEW ---- */}
        {!loading &&
        !error &&
        filteredRows.length > 0 &&
        (viewMode === "table" || !mapAvailable) ? (
          <S.TableCard>
            <S.DenseTableViewport>
              <Table
                columns={columns}
                data={filteredRows}
                sorting={sorting}
                manualSorting
                onSortingChange={(nextState) => {
                  if (nextState.length === 0) {
                    setSortBy("agentName");
                    setSortDirection("asc");
                    return;
                  }
                  const next = nextState[0];
                  setSortBy(next.id as AgentSortField);
                  setSortDirection(next.desc ? "desc" : "asc");
                }}
                enableSorting
                stripedRows
                onRowClick={(rowData) => setSelectedAgent(rowData)}
              />
            </S.DenseTableViewport>
          </S.TableCard>
        ) : null}

        {/* Card view removed \u2014 replaced by Map (topology) view above. */}
      </S.TableBlock>

      {/* ---- DETAIL DIALOG ---- */}
      {selectedAgent != null ? (
        <AgentDetailDialog
          agent={selectedAgent}
          onClose={() => setSelectedAgent(null)}
          onOpenWorkspace={onOpenWorkspace}
        />
      ) : null}
    </>
  );
}
