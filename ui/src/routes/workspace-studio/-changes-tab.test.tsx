import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { ChangesTab } from "./-changes-tab";

afterEach(() => cleanup());

vi.mock("@redis-ui/components", () => ({
  Button: Object.assign((props: any) => <button {...props} />, {
    defaultProps: {
      theme: {
        semantic: {
          color: {
            background: {
              danger500: "#dc2626",
              danger600: "#b91c1c",
            },
            text: {
              inverse: "#ffffff",
            },
          },
        },
      },
    },
  }),
  Card: (props: any) => <div {...props} />,
  Typography: {
    Body: (props: any) => <span {...props} />,
    Heading: (props: any) => <h2 {...props} />,
  },
}));

const rows = [
  {
    id: "chg-1",
    occurredAt: "2026-04-30T12:00:00Z",
    op: "put",
    path: "/src/app.ts",
    versionId: "ver-1234567890",
    fileId: "file-1234567890",
  },
];

const events = [
  {
    id: "evt-checkpoint",
    createdAt: "2026-04-30T11:00:00Z",
    kind: "checkpoint",
    op: "save",
    checkpointId: "cp-1",
    actor: "afs",
  },
  {
    id: "evt-session",
    createdAt: "2026-04-30T10:00:00Z",
    kind: "session",
    op: "stale",
    hostname: "MacBook-Air.local",
    label: "sync",
    actor: "afs",
  },
];

vi.mock("../../foundation/hooks/use-afs", () => ({
  useInfiniteChangelog: () => ({
    isLoading: false,
    isError: false,
    hasNextPage: false,
    isFetchingNextPage: false,
    data: {
      pages: [{ entries: rows }],
    },
  }),
  useEvents: () => ({
    isLoading: false,
    isError: false,
    data: {
      items: events,
    },
  }),
}));

vi.mock("../../foundation/tables/changes-table", () => ({
  ChangesTable: ({
    rows: tableRows,
    onOpenChange,
  }: {
    rows: Array<{
      id: string;
      path?: string;
      versionId?: string;
      kind?: string;
    }>;
    onOpenChange?: (entry: {
      path?: string;
      versionId?: string;
      kind?: string;
    }) => void;
  }) => (
    <div>
      {tableRows.map((row) => (
        <div key={row.id}>{row.id}</div>
      ))}
      <button
        type="button"
        onClick={() =>
          onOpenChange?.(tableRows.find((row) => row.path) ?? tableRows[0])
        }
      >
        open change
      </button>
    </div>
  ),
}));

vi.mock("./-file-history-drawer", () => ({
  FileHistoryDrawer: ({
    path,
    initialVersionId,
  }: {
    path: string;
    initialVersionId?: string;
  }) => (
    <div data-testid="history-drawer">{`${path}::${initialVersionId ?? ""}`}</div>
  ),
}));

describe("ChangesTab version deep links", () => {
  test("opens the file history drawer anchored to the changelog version", () => {
    render(<ChangesTab databaseId="db-1" workspaceId="workspace-1" editable />);

    expect(screen.getByText("History")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /open change/i }));

    expect(screen.getByTestId("history-drawer")).toHaveTextContent(
      "/src/app.ts::ver-1234567890",
    );
  });

  test("keeps session events behind the Sessions filter", () => {
    render(<ChangesTab databaseId="db-1" workspaceId="workspace-1" editable />);

    expect(screen.getByText("event:evt-checkpoint")).toBeInTheDocument();
    expect(screen.queryByText("event:evt-session")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Sessions" }));

    expect(screen.getByText("event:evt-session")).toBeInTheDocument();
    expect(screen.queryByText("event:evt-checkpoint")).not.toBeInTheDocument();
  });
  test("closes the selected change and resets filters when database scope changes", () => {
    const { rerender } = render(<ChangesTab databaseId="db-1" workspaceId="workspace-1" editable />);
    fireEvent.click(screen.getByRole("button", { name: /open change/i }));
    expect(screen.getByTestId("history-drawer")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Sessions" }));

    rerender(<ChangesTab databaseId="db-2" workspaceId="workspace-1" editable />);
    expect(screen.queryByTestId("history-drawer")).not.toBeInTheDocument();
    expect(screen.getByText("event:evt-checkpoint")).toBeInTheDocument();
    expect(screen.queryByText("event:evt-session")).not.toBeInTheDocument();
  });

});
