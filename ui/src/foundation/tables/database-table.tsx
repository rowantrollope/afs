import type { ColumnDef } from "@redis-ui/table";
import { Table } from "@redis-ui/table";
import { useMemo, useState } from "react";
import styled from "styled-components";
import { SurfaceCard } from "../../components/card-shell";
import { DatabaseIcon } from "../../components/lucide-icons";
import { formatBytes } from "../api/afs";
import { CheckIcon, CopyIcon } from "../clipboard-icons";
import type { AFSDatabaseScopeRecord } from "../database-scope";
import { StatusNameCell, StatusNameLine } from "./status-name-cell";
import * as S from "./workspace-table.styles";
import { DenseTableViewport } from "./workspace-table.styles";

type Props = {
  rows: AFSDatabaseScopeRecord[];
  loading?: boolean;
  error?: boolean;
  errorMessage?: string;
};

/* ------------------------------------------------------------------ */
/*  Small helpers                                                      */
/* ------------------------------------------------------------------ */

function formatOps(value: number): string {
  if (value >= 1000) {
    const k = value / 1000;
    return `${k >= 10 ? k.toFixed(0) : k.toFixed(1)}k`;
  }
  return `${value}`;
}

function formatUsedMegabytes(value: number): string {
  const mb = value / (1024 * 1024);
  const digits = mb === 0 || mb >= 10 ? 0 : 1;
  return `${mb.toLocaleString(undefined, {
    maximumFractionDigits: digits,
    minimumFractionDigits: digits,
  })} MB`;
}

/* ------------------------------------------------------------------ */
/*  Summary strip (4 cards above the table)                            */
/* ------------------------------------------------------------------ */

export function DatabaseSummaryStrip({
  rows,
}: {
  rows: AFSDatabaseScopeRecord[];
}) {
  const metrics = useMemo(() => {
    let healthy = 0;
    let totalAfsBytes = 0;
    let totalWorkspaces = 0;
    let capacitySum = 0;
    let capacityCount = 0;
    let atRisk = 0;
    let firstAtRisk: string | null = null;

    for (const row of rows) {
      if (row.isHealthy) healthy += 1;
      totalAfsBytes += row.afsTotalBytes;
      totalWorkspaces += row.workspaceCount;

      const stats = row.stats;
      if (stats && stats.maxMemoryBytes > 0) {
        const frac = stats.usedMemoryBytes / stats.maxMemoryBytes;
        capacitySum += frac;
        capacityCount += 1;

        if (frac >= 0.8) {
          atRisk += 1;
          if (firstAtRisk == null) firstAtRisk = row.displayName;
        }
      }

      if (!row.isHealthy) {
        atRisk += 1;
        if (firstAtRisk == null) firstAtRisk = row.displayName;
      }
    }

    const avgPct =
      capacityCount === 0
        ? null
        : Math.round((capacitySum / capacityCount) * 100);

    return {
      total: rows.length,
      healthy,
      totalAfsBytes,
      totalWorkspaces,
      avgPct,
      atRisk,
      firstAtRisk,
    };
  }, [rows]);

  if (rows.length === 0) return null;

  return (
    <SummaryGrid>
      <SummaryCard>
        <SummaryLabel>Databases</SummaryLabel>
        <SummaryValue>{metrics.total}</SummaryValue>
        <SummaryDetail>
          {metrics.healthy} healthy
          {metrics.total !== metrics.healthy
            ? `, ${metrics.total - metrics.healthy} unavailable`
            : ""}
        </SummaryDetail>
      </SummaryCard>

      <SummaryCard>
        <SummaryLabel>Total Stored</SummaryLabel>
        <SummaryValue>{formatBytes(metrics.totalAfsBytes)}</SummaryValue>
        <SummaryDetail>
          {metrics.totalWorkspaces} workspace
          {metrics.totalWorkspaces === 1 ? "" : "s"}
        </SummaryDetail>
      </SummaryCard>

      <SummaryCard>
        <SummaryLabel>Capacity Used</SummaryLabel>
        <SummaryValue>
          {metrics.avgPct == null ? "—" : `${metrics.avgPct}%`}
        </SummaryValue>
        <SummaryDetail>
          {metrics.avgPct == null
            ? "No memory limits configured"
            : "Average across databases with a maxmemory limit"}
        </SummaryDetail>
      </SummaryCard>

      <SummaryCard $alert={metrics.atRisk > 0}>
        <SummaryLabel>At Risk</SummaryLabel>
        <SummaryValue>{metrics.atRisk}</SummaryValue>
        <SummaryDetail>
          {metrics.atRisk === 0
            ? "All databases healthy and below 80% capacity"
            : metrics.firstAtRisk
              ? `e.g. ${metrics.firstAtRisk}`
              : ""}
        </SummaryDetail>
      </SummaryCard>
    </SummaryGrid>
  );
}

/* ------------------------------------------------------------------ */
/*  Table                                                              */
/* ------------------------------------------------------------------ */

export function DatabaseTable({
  rows,
  loading = false,
  error = false,
  errorMessage = "Unable to load databases. Please retry.",
  toolbarAction,
}: Props & { toolbarAction?: React.ReactNode }) {
  const [search, setSearch] = useState("");
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const filteredRows = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (query === "") return rows;
    return rows.filter((row) =>
      [
        row.displayName,
        row.databaseName,
        row.description,
        row.endpointLabel,
        row.id,
      ].some((value) => value.toLowerCase().includes(query)),
    );
  }, [rows, search]);

  async function copyDatabaseId(id: string) {
    try {
      await navigator.clipboard.writeText(id);
      setCopiedId(id);
      window.setTimeout(() => {
        setCopiedId((current) => (current === id ? null : current));
      }, 1500);
    } catch {
      /* ignore clipboard failures */
    }
  }

  const columns = useMemo(
    () =>
      [
        /* ── Name column: dot + name + star + ID (with copy) ── */
        {
          accessorKey: "displayName",
          header: "Name",
          size: 240,
          enableSorting: false,
          cell: ({ row }) => {
            const nameLabel =
              row.original.displayName || row.original.databaseName;
            const id = row.original.id;
            return (
              <StatusNameCell
                active={row.original.isHealthy}
                icon={<DatabaseIcon customSize="18px" />}
                inactiveTone="danger"
                statusLabel={
                  row.original.isHealthy ? "Connected" : "Unavailable"
                }
                statusTitle={
                  row.original.isHealthy
                    ? "Connected"
                    : row.original.connectionError || "Unavailable"
                }
              >
                <StatusNameLine>
                  <NameButton as="span">{nameLabel}</NameButton>
                </StatusNameLine>

                <IdRow>
                  <IdText title={id}>{id}</IdText>
                  <CopyButton
                    type="button"
                    aria-label={`Copy database ID ${id}`}
                    title={copiedId === id ? "Copied" : "Copy database ID"}
                    onClick={(event) => {
                      event.stopPropagation();
                      void copyDatabaseId(id);
                    }}
                  >
                    {copiedId === id ? <CheckIcon /> : <CopyIcon />}
                  </CopyButton>
                </IdRow>
              </StatusNameCell>
            );
          },
        },

        /* ── Usage column: workspace count + used MB ── */
        {
          id: "usage",
          header: "Usage",
          size: 220,
          enableSorting: false,
          cell: ({ row }) => {
            const usedBytes =
              row.original.stats?.usedMemoryBytes ?? row.original.afsTotalBytes;
            return (
              <UsageStack>
                <UsageText>
                  {row.original.workspaceCount} workspace
                  {row.original.workspaceCount === 1 ? "" : "s"}
                </UsageText>
                <UsageSubline>
                  {formatUsedMegabytes(usedBytes)} used
                </UsageSubline>
              </UsageStack>
            );
          },
        },

        /* ── Load column: ops/sec + clients/keys ── */
        {
          id: "load",
          header: "Load",
          size: 160,
          enableSorting: false,
          cell: ({ row }) => {
            const stats = row.original.stats;
            if (stats == null) {
              return <DimCell>—</DimCell>;
            }
            return (
              <LoadStack>
                <LoadLine>
                  <strong>{formatOps(stats.opsPerSec)}</strong>
                  <Muted> ops/s</Muted>
                </LoadLine>
                <LoadSubline>
                  {stats.connectedClients} client
                  {stats.connectedClients === 1 ? "" : "s"}
                  {" · "}
                  {stats.keyCount.toLocaleString()} key
                  {stats.keyCount === 1 ? "" : "s"}
                </LoadSubline>
              </LoadStack>
            );
          },
        },

        {
          id: "version",
          header: "Version",
          size: 130,
          enableSorting: false,
          cell: ({ row }) => {
            const version = row.original.stats?.redisVersion?.trim();
            return version ? (
              <VersionText>{version}</VersionText>
            ) : (
              <DimCell>—</DimCell>
            );
          },
        },
      ] as ColumnDef<AFSDatabaseScopeRecord>[],
    [copiedId],
  );

  return (
    <S.TableBlock>
      <S.HeadingWrap style={{ padding: 0 }}>
        <S.SearchInput
          value={search}
          onChange={setSearch}
          placeholder="Search databases..."
        />
        {toolbarAction ?? null}
      </S.HeadingWrap>

      {loading ? <S.EmptyState>Loading databases...</S.EmptyState> : null}
      {error ? <S.EmptyState role="alert">{errorMessage}</S.EmptyState> : null}
      {!loading && !error && filteredRows.length === 0 ? (
        <S.EmptyState>
          {rows.length === 0
            ? "The configured Redis backend is unavailable."
            : "No databases match the current filter."}
        </S.EmptyState>
      ) : null}

      {!loading && !error && filteredRows.length > 0 ? (
        <S.TableCard>
          <DatabaseTableViewport>
            <Table
              columns={columns}
              data={filteredRows}
              getRowId={(row) => row.id}
              stripedRows
            />
          </DatabaseTableViewport>
        </S.TableCard>
      ) : null}
    </S.TableBlock>
  );
}

/* ---- Name cell ---- */

const NameButton = styled.button`
  border: none;
  background: transparent;
  padding: 0;
  font: inherit;
  font-size: 14px;
  font-weight: 700;
  color: var(--afs-ink, #18181b);
  cursor: pointer;
  flex: 1 1 auto;
  min-width: 0;
  text-align: left;
  line-height: 1.2;
  max-width: 100%;
  overflow-wrap: anywhere;
  white-space: normal;

  &:hover {
    color: var(--afs-accent, #dc2626);
  }

  &:disabled {
    cursor: default;
    color: var(--afs-ink, #18181b);
  }
`;

/* ---- Database ID row ---- */

const IdRow = styled.div`
  display: flex;
  align-items: center;
  width: 100%;
  gap: 4px;
  min-width: 0;
`;

const IdText = styled.span`
  flex: 1 1 auto;
  font-family: var(
    --afs-mono,
    ui-monospace,
    SFMono-Regular,
    Menlo,
    Consolas,
    monospace
  );
  font-size: 11px;
  color: var(--afs-muted, #71717a);
  letter-spacing: 0;
  line-height: 1.2;
  min-width: 0;
  overflow-wrap: anywhere;
  white-space: normal;
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

/* ---- Usage cell ---- */

const UsageStack = styled.div`
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
`;

const UsageText = styled.span`
  font-size: 12.5px;
  color: var(--afs-ink, #18181b);
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  display: inline-flex;
  align-items: center;
  gap: 6px;

  strong {
    font-weight: 700;
  }
`;

const Muted = styled.span`
  color: var(--afs-muted, #71717a);
`;

const UsageSubline = styled.span`
  font-size: 11.5px;
  color: var(--afs-muted, #71717a);
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
  white-space: normal;
`;

/* ---- Load cell ---- */

const LoadStack = styled.div`
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
`;

const LoadLine = styled.span`
  font-size: 13px;
  color: var(--afs-ink, #18181b);
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  display: inline-flex;
  align-items: center;
  gap: 4px;

  strong {
    font-weight: 700;
  }
`;

const LoadSubline = styled.span`
  font-size: 11.5px;
  color: var(--afs-muted, #71717a);
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
`;

const DimCell = styled.span`
  color: var(--afs-muted, #71717a);
  font-size: 13px;
`;

const VersionText = styled.code`
  display: inline-block;
  max-width: 120px;
  color: var(--afs-ink, #18181b);
  font-family: var(
    --afs-mono,
    ui-monospace,
    SFMono-Regular,
    Menlo,
    Consolas,
    monospace
  );
  font-size: 12px;
  line-height: 1.3;
  overflow-wrap: anywhere;
  white-space: normal;
`;

/* ---- Summary strip ---- */

const SummaryGrid = styled.div`
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 16px;

  @media (max-width: 900px) {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  @media (max-width: 540px) {
    grid-template-columns: 1fr;
  }
`;

const SummaryCard = styled(SurfaceCard)<{ $alert?: boolean }>`
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 14px 16px;
  border: 1px solid
    ${({ $alert }) =>
      $alert ? "rgba(220, 38, 38, 0.35)" : "var(--afs-line, #e4e4e7)"};
  background: ${({ $alert }) =>
    $alert ? "rgba(220, 38, 38, 0.04)" : "var(--afs-panel-strong, #fff)"};
`;

const SummaryLabel = styled.span`
  font-size: 10px;
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--afs-muted, #71717a);
`;

const SummaryValue = styled.span`
  font-size: 24px;
  font-weight: 800;
  color: var(--afs-ink, #18181b);
  line-height: 1.1;
  letter-spacing: -0.02em;
  font-variant-numeric: tabular-nums;
`;

const SummaryDetail = styled.span`
  font-size: 12px;
  color: var(--afs-muted, #71717a);
  line-height: 1.35;
`;

/* ---- Table viewport: dense + database-specific hover reveals ---- */

const DatabaseTableViewport = styled(DenseTableViewport)`
  /* Reveal star + copy button on row hover */
  tbody tr:hover [data-default-star]:not(:disabled) {
    opacity: 0.55;
  }
  tbody tr:hover [data-default-star]:not(:disabled):hover {
    opacity: 1;
  }
  tbody tr:hover button[aria-label^="Copy database ID"] {
    opacity: 0.7;
  }
  tbody tr:hover button[aria-label^="Copy database ID"]:hover {
    opacity: 1;
  }
`;
