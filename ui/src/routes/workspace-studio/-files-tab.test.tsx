import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { FilesTab } from "./-files-tab";

const { treeCalls, fileCalls, updateFile } = vi.hoisted(() => ({
  treeCalls: vi.fn(),
  fileCalls: vi.fn(),
  updateFile: vi.fn(),
}));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

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

vi.mock("../../foundation/hooks/use-afs", () => ({
  useWorkspaceTree: (input: unknown) => {
    treeCalls(input);
    return {
      isLoading: false,
      isError: false,
      data: {
        items: [
          {
            path: "/README.md",
            name: "README.md",
            kind: "file",
            size: 42,
            modifiedAt: "2026-04-29T00:00:00Z",
          },
        ],
      },
    };
  },
  useWorkspaceFileContent: (input: { path: string }) => {
    fileCalls(input);
    return {
      isLoading: false,
      data:
        input.path === "/README.md"
          ? {
              path: "/README.md",
              kind: "file",
              revision: "rev-1",
              language: "markdown",
              size: 42,
              binary: false,
              content: "# hello",
            }
          : null,
    };
  },
  useUpdateWorkspaceFileMutation: () => ({
    mutate: updateFile,
    isPending: false,
  }),
}));

vi.mock("./-file-history-drawer", () => ({
  FileHistoryDrawer: ({ path }: { path: string }) => (
    <div data-testid="history-drawer">{path}</div>
  ),
}));

describe("FilesTab version history entry point", () => {
  test("scopes tree, file reads, and edits to the workspace database", async () => {
    render(
      <FilesTab
        workspace={buildWorkspace()}
        browserView="working-copy"
        onBrowserViewChange={vi.fn()}
      />,
    );
    expect(treeCalls).toHaveBeenCalledWith(
      expect.objectContaining({
        databaseId: "db-1",
        workspaceId: "workspace-1",
      }),
    );
    fireEvent.click(screen.getByText("README.md"));
    expect(fileCalls).toHaveBeenCalledWith(
      expect.objectContaining({
        databaseId: "db-1",
        workspaceId: "workspace-1",
        path: "/README.md",
      }),
    );
    fireEvent.change(await screen.findByRole("textbox"), {
      target: { value: "# updated" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(updateFile).toHaveBeenCalledWith({
      databaseId: "db-1",
      workspaceId: "workspace-1",
      path: "/README.md",
      content: "# updated",
    });
  });

  test("opens the history drawer for the selected file", async () => {
    render(
      <FilesTab
        workspace={buildWorkspace()}
        browserView="head"
        onBrowserViewChange={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("README.md"));
    fireEvent.click(await screen.findByRole("button", { name: /history/i }));

    expect(screen.getByTestId("history-drawer")).toHaveTextContent(
      "/README.md",
    );
  });
});

function buildWorkspace() {
  return {
    id: "workspace-1",
    name: "workspace",
    description: "",
    cloudAccount: "Direct Redis",
    databaseId: "db-1",
    databaseName: "db",
    redisKey: "afs:workspace-1",
    region: "us-east-1",
    source: "blank" as const,
    createdAt: "2026-04-29T00:00:00Z",
    updatedAt: "2026-04-29T00:00:00Z",
    draftState: "clean",
    headSavepointId: "cp-1",
    tags: [],
    fileCount: 1,
    folderCount: 0,
    totalBytes: 128,
    checkpointCount: 0,
    files: [],
    savepoints: [],
    activity: [],
    agents: [],
    capabilities: {
      browseHead: true,
      browseCheckpoints: true,
      browseWorkingCopy: true,
      editWorkingCopy: true,
      createCheckpoint: true,
      restoreCheckpoint: true,
    },
  };
}
