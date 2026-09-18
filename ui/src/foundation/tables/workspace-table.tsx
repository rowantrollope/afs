import { Typography } from "@redis-ui/components";
import { FoldersIcon as FoldersIconMono } from "@redis-ui/icons/monochrome";
import type { ColumnDef, SortingState } from "@redis-ui/table";
import { Table } from "@redis-ui/table";
import { Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { useEffect, useMemo, useState } from "react";
import styled, { css, keyframes } from "styled-components";
import { FoldersIcon } from "../../components/lucide-icons";
import { formatBytes } from "../api/afs";
import { CheckIcon, CopyIcon } from "../clipboard-icons";
import { useStoredViewMode } from "../hooks/use-stored-view-mode";
import { compareValues } from "../sort-compare";
import { shortDateTime } from "../time-format";
import type { AFSWorkspaceSummary } from "../types/afs";
import { displayWorkspaceName } from "../workspace-display";
import type { StudioTab } from "../workspace-tabs";
import * as S from "./workspace-table.styles";

type WorkspaceSortField =
  | "name"
  | "cloudAccount"
  | "connectedAgents"
  | "databaseName"
  | "totalBytes"
  | "updatedAt";

type RowWorkspaceSortField = Exclude<WorkspaceSortField, "connectedAgents">;

/** Spelled-out relative time: "5 seconds ago", "2 minutes ago", "3 hours ago", "4 days ago" */
function relativeTimeAgo(iso: string): string {
  const seconds = Math.max(
    0,
    Math.floor((Date.now() - new Date(iso).getTime()) / 1000),
  );
  if (seconds < 60) return `${seconds} second${seconds === 1 ? "" : "s"} ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.floor(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

/** Re-render once per minute so relative times stay fresh. */
function useMinuteTick() {
  const [, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 30000);
    return () => clearInterval(id);
  }, []);
}

type Props = {
  rows: AFSWorkspaceSummary[];
  loading?: boolean;
  error?: boolean;
  errorMessage?: string;
  toolbarAction?: ReactNode;
  connectedAgentsByWorkspace?: Record<string, number>;
  onOpenWorkspace: (workspace: AFSWorkspaceSummary) => void;
  onPreviewWorkspace?: (workspace: AFSWorkspaceSummary) => void;
  onOpenWorkspaceTab?: (workspace: AFSWorkspaceSummary, tab: StudioTab) => void;
  // optional. when omitted the inline actions column is hidden — workspaces
  // are managed via the CLI (`afs delete <name>`) and the detail page.
  onEditWorkspace?: (workspace: AFSWorkspaceSummary) => void;
  onDeleteWorkspace?: (workspace: AFSWorkspaceSummary) => void;
  deletingWorkspaceKey?: string | null;
  fillAvailableHeight?: boolean;
  resourceLabel?: string;
  resourcePluralLabel?: string;
  idLabel?: string;
};

function workspaceRowKey(workspace: AFSWorkspaceSummary) {
  return `${workspace.databaseId}:${workspace.id}`;
}

export function WorkspaceTable({
  rows,
  loading = false,
  error = false,
  errorMessage = "Unable to load workspaces. Please retry.",
  toolbarAction,
  connectedAgentsByWorkspace = {},
  onOpenWorkspace,
  onPreviewWorkspace,
  onOpenWorkspaceTab,
  onDeleteWorkspace,
  deletingWorkspaceKey,
  fillAvailableHeight = false,
  resourceLabel = "workspace",
  resourcePluralLabel = "workspaces",
  idLabel = "workspace ID",
}: Props) {
  const [search, setSearch] = useState("");
  const [sortBy, setSortBy] = useState<WorkspaceSortField>("updatedAt");
  const [sortDirection, setSortDirection] = useState<"asc" | "desc">("desc");
  const [viewMode, setViewMode] = useStoredViewMode<"table" | "cards">(
    "afs.workspaces.viewMode",
    "table",
  );
  const [copiedId, setCopiedId] = useState<string | null>(null);
  useMinuteTick();

  async function copyWorkspaceId(id: string) {
    try {
      await navigator.clipboard.writeText(id);
      setCopiedId(id);
      window.setTimeout(() => {
        setCopiedId((current) => (current === id ? null : current));
      }, 1500);
    } catch {
      /* ignore */
    }
  }

  const filteredRows = useMemo(() => {
    const query = search.trim().toLowerCase();
    const baseRows =
      query === ""
        ? rows
        : rows.filter((row) =>
            [
              row.name,
              row.databaseName,
              row.redisKey,
              row.region,
              row.cloudAccount,
            ].some((value) => value.toLowerCase().includes(query)),
          );

    return [...baseRows].sort((left, right) => {
      const leftValue =
        sortBy === "connectedAgents"
          ? (connectedAgentsByWorkspace[workspaceRowKey(left)] ?? 0)
          : left[sortBy as RowWorkspaceSortField];
      const rightValue =
        sortBy === "connectedAgents"
          ? (connectedAgentsByWorkspace[workspaceRowKey(right)] ?? 0)
          : right[sortBy as RowWorkspaceSortField];
      return compareValues(leftValue, rightValue, sortDirection);
    });
  }, [connectedAgentsByWorkspace, rows, search, sortBy, sortDirection]);

  const sorting = useMemo<SortingState>(
    () => [{ id: sortBy, desc: sortDirection === "desc" }],
    [sortBy, sortDirection],
  );
  const isFiltering = search.trim() !== "";

  const showActionsColumn = !!onDeleteWorkspace;

  const columns = useMemo(
    () =>
      [
        {
          accessorKey: "name",
          header: "Name",
          size: 240,
          enableSorting: true,
          cell: ({ row }) => {
            const id = row.original.id;
            return (
              <NameCell>
                <NameIconBox>
                  <FoldersIcon customSize="18px" />
                </NameIconBox>
                <NameStack>
                  <WorkspaceNameButton
                    onClick={(event) => {
                      event.stopPropagation();
                      onOpenWorkspace(row.original);
                    }}
                    onMouseEnter={() => onPreviewWorkspace?.(row.original)}
                    onFocus={() => onPreviewWorkspace?.(row.original)}
                  >
                    {displayWorkspaceName(row.original.name)}
                  </WorkspaceNameButton>
                  {row.original.liveRootAvailable === false && (
                    <DetailsMuted title={row.original.unavailableReason}>
                      Live tree unavailable
                    </DetailsMuted>
                  )}
                  <IdRow>
                    <IdText title={id}>{id}</IdText>
                    <CopyButton
                      type="button"
                      aria-label={`Copy ${idLabel} ${id}`}
                      title={copiedId === id ? "Copied" : `Copy ${idLabel}`}
                      onClick={(event) => {
                        event.stopPropagation();
                        void copyWorkspaceId(id);
                      }}
                    >
                      {copiedId === id ? <CheckIcon /> : <CopyIcon />}
                    </CopyButton>
                  </IdRow>
                </NameStack>
              </NameCell>
            );
          },
        },
        {
          id: "connectedAgents",
          header: "Agents",
          size: 90,
          enableSorting: true,
          cell: ({ row }) => {
            const count =
              connectedAgentsByWorkspace[workspaceRowKey(row.original)] ?? 0;
            return (
              <S.CountCell>
                <LiveDot $active={count > 0} />
                <Typography.Body component="span">{count}</Typography.Body>
              </S.CountCell>
            );
          },
        },
        {
          accessorKey: "totalBytes",
          header: "Size",
          size: 110,
          enableSorting: true,
          cell: ({ row }) => (
            <SizeCell>
              <span>{formatBytes(row.original.totalBytes)}</span>
              <DetailsMuted>
                {" "}
                · {row.original.fileCount} file
                {row.original.fileCount === 1 ? "" : "s"}
              </DetailsMuted>
            </SizeCell>
          ),
        },
        {
          accessorKey: "databaseName",
          header: "Database",
          size: 160,
          enableSorting: true,
          cell: ({ row }) => (
            <DatabaseName title={row.original.databaseName}>
              {row.original.databaseName}
            </DatabaseName>
          ),
        },
        {
          accessorKey: "updatedAt",
          header: "Last updated",
          size: 150,
          enableSorting: true,
          cell: ({ row }) => (
            <UpdatedStack>
              <UpdatedDate>{shortDateTime(row.original.updatedAt)}</UpdatedDate>
              <UpdatedAgo>{relativeTimeAgo(row.original.updatedAt)}</UpdatedAgo>
            </UpdatedStack>
          ),
        },
        ...(showActionsColumn
          ? [
              {
                id: "actions",
                header: "",
                size: 48,
                enableSorting: false,
                cell: ({ row }: { row: { original: AFSWorkspaceSummary } }) => {
                  const rowKey = workspaceRowKey(row.original);
                  const isDeleting = deletingWorkspaceKey === rowKey;
                  return (
                    <ActionsCell>
                      <DeleteRowButton
                        type="button"
                        aria-label={`Remove ${resourceLabel} ${row.original.name}`}
                        title={`Remove ${resourceLabel}`}
                        disabled={isDeleting}
                        onClick={(event) => {
                          event.stopPropagation();
                          onDeleteWorkspace(row.original);
                        }}
                      >
                        <Trash2
                          size={16}
                          strokeWidth={1.75}
                          aria-hidden="true"
                        />
                      </DeleteRowButton>
                    </ActionsCell>
                  );
                },
              },
            ]
          : []),
      ] as ColumnDef<AFSWorkspaceSummary>[],
    [
      connectedAgentsByWorkspace,
      copiedId,
      deletingWorkspaceKey,
      idLabel,
      onDeleteWorkspace,
      onOpenWorkspace,
      onOpenWorkspaceTab,
      onPreviewWorkspace,
      resourceLabel,
      showActionsColumn,
    ],
  );

  return (
    <>
      <WorkspaceTableBlock $fillAvailableHeight={fillAvailableHeight}>
        <S.HeadingWrap style={{ padding: 0 }}>
          <S.SearchInput
            value={search}
            onChange={setSearch}
            placeholder={`Search ${resourceLabel}, database, ...`}
          />
          <S.ToggleGroup>
            <S.ToggleButton
              $active={viewMode === "cards"}
              aria-pressed={viewMode === "cards"}
              onClick={() => setViewMode("cards")}
            >
              Cards
            </S.ToggleButton>
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
          <S.EmptyState>Loading {resourcePluralLabel}...</S.EmptyState>
        ) : null}
        {error ? (
          <S.EmptyState role="alert">{errorMessage}</S.EmptyState>
        ) : null}
        {!loading && !error && filteredRows.length === 0 ? (
          <S.EmptyState>
            {isFiltering
              ? `No ${resourcePluralLabel} match the current filter.`
              : `No ${resourcePluralLabel} yet. Use the CLI to create one.`}
          </S.EmptyState>
        ) : null}

        {/* ---- TABLE VIEW ---- */}
        {!loading &&
        !error &&
        filteredRows.length > 0 &&
        viewMode === "table" ? (
          <WorkspaceTableCard $fillAvailableHeight={fillAvailableHeight}>
            <WorkspaceTableViewport $fillAvailableHeight={fillAvailableHeight}>
              <Table
                columns={columns}
                data={filteredRows}
                getRowId={(row) => workspaceRowKey(row)}
                sorting={sorting}
                manualSorting
                onSortingChange={(nextState) => {
                  if (nextState.length === 0) {
                    setSortBy("updatedAt");
                    setSortDirection("desc");
                    return;
                  }

                  const next = nextState[0];
                  const nextSortBy = next.id as WorkspaceSortField;
                  setSortBy(nextSortBy);
                  setSortDirection(next.desc ? "desc" : "asc");
                }}
                enableSorting
                stripedRows
                onRowClick={(rowData) => onOpenWorkspace(rowData)}
              />
            </WorkspaceTableViewport>
          </WorkspaceTableCard>
        ) : null}

        {/* ---- CARD VIEW ---- */}
        {!loading &&
        !error &&
        filteredRows.length > 0 &&
        viewMode === "cards" ? (
          <S.WorkspaceCardGrid>
            {filteredRows.map((ws) => {
              const agentCount =
                connectedAgentsByWorkspace[workspaceRowKey(ws)] ?? 0;
              const hasAgents = agentCount > 0;
              return (
                <S.WorkspaceCard
                  key={ws.id}
                  onMouseEnter={() => onPreviewWorkspace?.(ws)}
                  onFocus={() => onPreviewWorkspace?.(ws)}
                  onClick={() => onOpenWorkspace(ws)}
                >
                  <S.CardTopRow>
                    <S.CardIconBox>
                      <FoldersIconMono size="XL" />
                    </S.CardIconBox>
                    <div
                      style={{
                        display: "flex",
                        flexDirection: "column",
                        gap: 2,
                        minWidth: 0,
                      }}
                    >
                      <S.CardName>{ws.name}</S.CardName>
                      <S.CardDescription>
                        {ws.cloudAccount} · {ws.region || "Local"}
                      </S.CardDescription>
                    </div>
                  </S.CardTopRow>

                  <S.CardBody>
                    <S.CardDetailLines>
                      <S.CardDetailLine>
                        <S.CardDetailLabel>Database</S.CardDetailLabel>
                        <S.CardDetailValue>{ws.databaseName}</S.CardDetailValue>
                      </S.CardDetailLine>
                      <S.CardDetailLine>
                        <S.CardDetailLabel>ID</S.CardDetailLabel>
                        <S.CardDetailValue>{ws.id}</S.CardDetailValue>
                      </S.CardDetailLine>
                    </S.CardDetailLines>

                    <S.CardStatsRow>
                      <S.CardStatBox>
                        <S.CardStatLabel>Files</S.CardStatLabel>
                        <S.CardStatValue>{ws.fileCount}</S.CardStatValue>
                      </S.CardStatBox>
                      <S.CardStatBox>
                        <S.CardStatLabel>Folders</S.CardStatLabel>
                        <S.CardStatValue>{ws.folderCount}</S.CardStatValue>
                      </S.CardStatBox>
                      <S.CardStatBox>
                        <S.CardStatLabel>Size</S.CardStatLabel>
                        <S.CardStatValue>
                          {formatBytes(ws.totalBytes)}
                        </S.CardStatValue>
                      </S.CardStatBox>
                    </S.CardStatsRow>

                    <S.CardInfoRow>
                      <S.CardInfoBox $highlight={hasAgents}>
                        <LiveDot $active={hasAgents} />
                        {agentCount} Agent{agentCount !== 1 ? "s" : ""}
                      </S.CardInfoBox>
                      <S.CardInfoBox>
                        {new Date(ws.updatedAt).toLocaleDateString()}
                      </S.CardInfoBox>
                    </S.CardInfoRow>
                  </S.CardBody>

                  <S.CardButtonRow>
                    <S.CardPrimaryButton
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        onOpenWorkspaceTab?.(ws, "browse");
                      }}
                    >
                      Browse Files
                    </S.CardPrimaryButton>
                    <S.CardSecondaryButton
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        onOpenWorkspaceTab?.(ws, "checkpoints");
                      }}
                    >
                      Checkpoints
                    </S.CardSecondaryButton>
                    <S.CardSecondaryButton
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        onOpenWorkspaceTab?.(ws, "changes");
                      }}
                    >
                      History
                    </S.CardSecondaryButton>
                  </S.CardButtonRow>
                </S.WorkspaceCard>
              );
            })}
          </S.WorkspaceCardGrid>
        ) : null}
      </WorkspaceTableBlock>
    </>
  );
}

const WorkspaceTableBlock = styled(S.TableBlock)<{
  $fillAvailableHeight: boolean;
}>`
  ${({ $fillAvailableHeight }) =>
    $fillAvailableHeight &&
    css`
      flex: 1 1 auto;
      min-height: 0;
    `}
`;

const WorkspaceTableCard = styled(S.TableCard)<{
  $fillAvailableHeight: boolean;
}>`
  overflow-x: auto;
  overflow-y: hidden;

  ${({ $fillAvailableHeight }) =>
    $fillAvailableHeight &&
    css`
      display: flex;
      flex-direction: column;
      flex: 1 1 auto;
      min-height: 0;
    `}
`;

const WorkspaceTableViewport = styled(S.DenseTableViewport)<{
  $fillAvailableHeight: boolean;
}>`
  width: max-content;
  min-width: 100%;
  overflow-x: hidden;
  overflow-y: auto;

  ${({ $fillAvailableHeight }) =>
    $fillAvailableHeight &&
    css`
      flex: 1 1 auto;
      min-height: 0;
      max-height: none;
    `}

  /* Reveal copy button on row hover */
  tbody tr:hover button[aria-label^="Copy workspace ID"] {
    opacity: 0.7;
  }
  tbody tr:hover button[aria-label^="Copy workspace ID"]:hover {
    opacity: 1;
  }

  /* Reveal delete button on row hover */
  tbody tr:hover button[aria-label^="Remove workspace"] {
    opacity: 1;
  }
`;

const ActionsCell = styled.div`
  display: flex;
  align-items: center;
  justify-content: flex-end;
`;

const DeleteRowButton = styled.button`
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: none;
  background: transparent;
  color: #dc2626;
  cursor: pointer;
  border-radius: 6px;
  opacity: 0;
  transition:
    background 140ms ease,
    opacity 140ms ease;

  &:hover:not(:disabled) {
    background: color-mix(in srgb, #dc2626 12%, transparent);
  }

  &:focus-visible {
    outline: 2px solid #dc2626;
    outline-offset: 1px;
    opacity: 1;
  }

  &:disabled {
    cursor: not-allowed;
  }
`;

const SizeCell = styled.span`
  font-size: 15px;
  color: var(--afs-ink, #18181b);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
`;

const DetailsMuted = styled.span`
  color: var(--afs-muted, #71717a);
`;

const NameStack = styled.div`
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
`;

const NameCell = styled.div`
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
`;

const NameIconBox = styled.span`
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  color: var(--afs-muted, #71717a);
`;

const IdRow = styled.div`
  display: flex;
  align-items: center;
  width: 100%;
  gap: 4px;
  min-width: 0;
`;

const IdText = styled.span`
  flex: 1 1 auto;
  font-size: 13.5px;
  color: var(--afs-muted, #71717a);
  line-height: 1.2;
  min-width: 0;
  overflow-wrap: anywhere;
  white-space: normal;
`;

const DatabaseName = styled.span`
  font-size: 15px;
  color: var(--afs-ink, #18181b);
  overflow-wrap: anywhere;
  white-space: normal;
  min-width: 0;
`;

const CopyButton = styled.button`
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 16px;
  height: 16px;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--afs-muted, #71717a);
  cursor: pointer;
  border-radius: 4px;
  transition:
    background 140ms ease,
    color 140ms ease;
  opacity: 0;

  &:hover {
    background: rgba(8, 6, 13, 0.06);
    color: var(--afs-ink, #18181b);
  }

  &:focus-visible {
    outline: 2px solid var(--afs-accent, #dc2626);
    outline-offset: 1px;
    opacity: 1;
  }
`;

const UpdatedStack = styled.div`
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
`;

const UpdatedDate = styled.span`
  font-size: 15px;
  color: var(--afs-ink, #18181b);
  font-variant-numeric: tabular-nums;
  line-height: 1.2;
  white-space: nowrap;
`;

const WorkspaceNameButton = styled(S.WorkspaceNameButton)`
  && {
    flex: 1 1 auto;
    font-size: 15px;
    font-weight: 700;
    min-width: 0;
    max-width: 100%;
    overflow-wrap: anywhere;
    white-space: normal;
  }
`;

const UpdatedAgo = styled.span`
  font-size: 14px;
  color: var(--afs-muted, #71717a);
  line-height: 1.2;
  white-space: nowrap;
`;

const pulse = keyframes`
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
`;

const LiveDot = styled.span<{ $active: boolean }>`
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
