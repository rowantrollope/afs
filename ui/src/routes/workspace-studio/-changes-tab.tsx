import { Button } from "@redis-ui/components";
import { useMemo, useState } from "react";
import styled from "styled-components";
import {
  SectionCard,
  SectionGrid,
  SectionHeader,
  SectionTitle,
  TabButton,
  Tabs,
} from "../../components/afs-kit";
import {
  computeChangelogTotals,
  formatChangelogBytes,
} from "../../foundation/changelog-utils";
import {
  useEvents,
  useInfiniteChangelog,
} from "../../foundation/hooks/use-afs";
import type { HistoryTableRow } from "../../foundation/tables/changes-table";
import { ChangesTable } from "../../foundation/tables/changes-table";
import type {
  AFSChangelogEntry,
  AFSEventEntry,
} from "../../foundation/types/afs";
import { FileHistoryDrawer } from "./-file-history-drawer";

const CHANGELOG_PAGE_SIZE = 100;

type HistoryFilter =
  | "timeline"
  | "files"
  | "checkpoints"
  | "sessions"
  | "workspace";

type Props = {
  databaseId?: string;
  workspaceId: string;
  editable: boolean;
};

export function ChangesTab({ databaseId, workspaceId, editable }: Props) {
  const [selectedChange, setSelectedChange] = useState<HistoryTableRow | null>(
    null,
  );
  const [historyFilter, setHistoryFilter] = useState<HistoryFilter>("timeline");
  const query = useInfiniteChangelog({
    databaseId,
    workspaceId,
    limit: CHANGELOG_PAGE_SIZE,
    direction: "desc",
  });
  const eventsQuery = useEvents({
    databaseId,
    workspaceId,
    limit: CHANGELOG_PAGE_SIZE,
    direction: "desc",
  });

  const entries = useMemo(
    () => query.data?.pages.flatMap((page) => page.entries) ?? [],
    [query.data],
  );
  const eventRows = useMemo(
    () =>
      eventsQuery.data?.items
        .filter((event) => event.kind !== "file")
        .map(eventToHistoryRow) ?? [],
    [eventsQuery.data],
  );
  const rows = useMemo(
    () => [...entries, ...eventRows].sort(compareHistoryRowsNewestFirst),
    [entries, eventRows],
  );
  const visibleRows = useMemo(
    () => rows.filter((row) => rowMatchesHistoryFilter(row, historyFilter)),
    [historyFilter, rows],
  );
  const visibleFileRows = useMemo(
    () => visibleRows.filter(isFileHistoryRow) as AFSChangelogEntry[],
    [visibleRows],
  );
  const totals = useMemo(
    () => computeChangelogTotals(visibleFileRows),
    [visibleFileRows],
  );
  const hasRows = visibleRows.length > 0;
  const isLoading = query.isLoading || eventsQuery.isLoading;
  const isError = query.isError || eventsQuery.isError;
  const errorMessage =
    query.error instanceof Error
      ? query.error.message
      : eventsQuery.error instanceof Error
        ? eventsQuery.error.message
        : "Unable to load history. Please retry.";

  return (
    <SectionGrid>
      <SectionCard $span={12}>
        <SectionHeader>
          <SectionTitle title="History" />
          <HeaderSummary>
            {isLoading ? (
              "Loading history..."
            ) : hasRows && visibleFileRows.length > 0 ? (
              <>
                Showing <strong>{visibleRows.length}</strong> history rows ·{" "}
                <strong>{totals.added}</strong> added ·{" "}
                <strong>{totals.modified}</strong> modified ·{" "}
                <strong>{totals.deleted}</strong> deleted ·{" "}
                <PositiveDelta>
                  +{formatChangelogBytes(totals.bytesAdded)}
                </PositiveDelta>
                {" / "}
                <NegativeDelta>
                  −{formatChangelogBytes(totals.bytesRemoved)}
                </NegativeDelta>
              </>
            ) : hasRows ? (
              <>
                Showing <strong>{visibleRows.length}</strong> history rows
              </>
            ) : (
              "No history yet"
            )}
          </HeaderSummary>
        </SectionHeader>
        <HistoryFilterTabs role="tablist" aria-label="History filters">
          {historyFilterOptions.map((option) => (
            <TabButton
              key={option.value}
              type="button"
              role="tab"
              aria-selected={historyFilter === option.value}
              $active={historyFilter === option.value}
              onClick={() => setHistoryFilter(option.value)}
            >
              {option.label}
            </TabButton>
          ))}
        </HistoryFilterTabs>
        <ChangesTable
          rows={visibleRows}
          loading={isLoading}
          error={isError}
          errorMessage={errorMessage}
          emptyStateText="No history matches the current filter."
          detailHeader="Detail"
          filterAllLabel="All history"
          loadingText="Loading history..."
          searchPlaceholder="Search by path, event, agent..."
          onOpenChange={(entry) => {
            if (isFileHistoryRow(entry) && entry.path != null) {
              setSelectedChange(entry);
            }
          }}
        />
        {!isLoading && !isError && entries.length > 0 && query.hasNextPage ? (
          <LoadMoreRow>
            <Button
              size="medium"
              variant="secondary-fill"
              onClick={() => void query.fetchNextPage()}
              disabled={query.isFetchingNextPage}
            >
              {query.isFetchingNextPage
                ? "Loading more..."
                : "Load more file changes"}
            </Button>
          </LoadMoreRow>
        ) : null}
      </SectionCard>

      {selectedChange ? (
        <FileHistoryDrawer
          databaseId={databaseId}
          workspaceId={workspaceId}
          path={selectedChange.path ?? ""}
          editable={editable}
          initialVersionId={selectedChange.versionId}
          onClose={() => setSelectedChange(null)}
        />
      ) : null}
    </SectionGrid>
  );
}

const historyFilterOptions: Array<{ value: HistoryFilter; label: string }> = [
  { value: "timeline", label: "Timeline" },
  { value: "files", label: "Files" },
  { value: "checkpoints", label: "Checkpoints" },
  { value: "sessions", label: "Sessions" },
  { value: "workspace", label: "Workspace" },
];

function eventToHistoryRow(event: AFSEventEntry): HistoryTableRow {
  return {
    id: `event:${event.id}`,
    occurredAt: event.createdAt,
    workspaceId: event.workspaceId,
    workspaceName: event.workspaceName,
    databaseId: event.databaseId,
    databaseName: event.databaseName,
    sessionId: event.sessionId,
    user: event.user,
    label: event.label,
    agentVersion: event.agentVersion,
    op: event.op,
    path: event.path,
    prevPath: event.prevPath,
    sizeBytes: event.sizeBytes,
    deltaBytes: event.deltaBytes,
    contentHash: event.contentHash,
    prevHash: event.prevHash,
    mode: event.mode,
    checkpointId: event.checkpointId,
    source: event.source,
    actor: event.actor,
    eventTitle: eventTitle(event),
    eventDetail: eventDetail(event),
    historyType: "event",
    hostname: event.hostname,
    kind: event.kind,
  };
}

function eventTitle(event: AFSEventEntry): string {
  switch (`${event.kind}.${event.op}`) {
    case "checkpoint.save":
      return "Checkpoint created";
    case "checkpoint.restore":
      return "Checkpoint restored";
    case "workspace.create":
      return "Workspace created";
    case "workspace.import":
      return "Workspace imported";
    case "workspace.fork":
      return "Workspace forked";
    case "workspace.update":
      return "Workspace updated";
    case "session.start":
      return "Session started";
    case "session.close":
      return "Session closed";
    case "session.stale":
      return "Session stale";
    case "process.start":
      return "Process started";
    case "process.exit":
      return "Process exited";
    default:
      return "";
  }
}

function eventDetail(event: AFSEventEntry): string {
  if (event.path) {
    return event.path;
  }
  if (event.checkpointId) {
    return `checkpoint ${event.checkpointId}`;
  }
  if (event.hostname && event.label) {
    return `${event.label} on ${event.hostname}`;
  }
  if (event.hostname) {
    return event.hostname;
  }
  return event.actor || event.source || "";
}

function compareHistoryRowsNewestFirst(
  left: HistoryTableRow,
  right: HistoryTableRow,
): number {
  return (right.occurredAt ?? "").localeCompare(left.occurredAt ?? "");
}

function isFileHistoryRow(row: HistoryTableRow): boolean {
  return row.historyType !== "event" || row.kind === "file";
}

function isSessionHistoryRow(row: HistoryTableRow): boolean {
  return row.kind === "session" || row.kind === "process";
}

function rowMatchesHistoryFilter(
  row: HistoryTableRow,
  filter: HistoryFilter,
): boolean {
  switch (filter) {
    case "files":
      return isFileHistoryRow(row);
    case "checkpoints":
      return row.kind === "checkpoint";
    case "sessions":
      return isSessionHistoryRow(row);
    case "workspace":
      return row.kind === "workspace";
    case "timeline":
    default:
      return !isSessionHistoryRow(row);
  }
}

const HistoryFilterTabs = styled(Tabs)`
  margin-bottom: 14px;
  max-width: 100%;
  flex-wrap: wrap;
`;

const HeaderSummary = styled.span`
  color: var(--afs-muted);
  font-size: 13px;
  line-height: 1.5;
  max-width: 100%;
  min-width: 0;
  text-align: right;

  strong {
    color: var(--afs-ink);
    font-weight: 700;
  }

  @media (max-width: 720px) {
    text-align: left;
  }
`;

const PositiveDelta = styled.span`
  color: #16a34a;
  font-weight: 700;
`;

const NegativeDelta = styled.span`
  color: #dc2626;
  font-weight: 700;
`;

const LoadMoreRow = styled.div`
  display: flex;
  justify-content: center;
  padding-top: 16px;
`;
