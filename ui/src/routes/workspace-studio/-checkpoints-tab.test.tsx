import type {
  ButtonHTMLAttributes,
  HTMLAttributes,
  InputHTMLAttributes,
} from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { AFSWorkspaceDetail } from "../../foundation/types/afs";
import { CheckpointsTab } from "./-checkpoints-tab";

const { restore, diff } = vi.hoisted(() => ({
  restore: vi.fn(),
  diff: vi.fn(),
}));
vi.mock("@redis-ui/components", () => ({
  Button: (props: ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button {...props} />
  ),
  Typography: {
    Body: (props: HTMLAttributes<HTMLSpanElement>) => <span {...props} />,
    Heading: (props: HTMLAttributes<HTMLHeadingElement>) => <h2 {...props} />,
  },
  TableHeading: {
    SearchInput: ({
      onChange,
      ...props
    }: Omit<InputHTMLAttributes<HTMLInputElement>, "onChange"> & {
      onChange?: (value: string) => void;
    }) => (
      <input {...props} onChange={(event) => onChange?.(event.target.value)} />
    ),
  },
}));
vi.mock("../../foundation/hooks/use-afs", () => ({
  useCreateSavepointMutation: () => ({ mutate: vi.fn(), isPending: false }),
  useRestoreSavepointMutation: () => ({ mutate: restore, isPending: false }),
  useWorkspaceDiff: (...args: unknown[]) => {
    diff(...args);
    return { isLoading: true, isError: false };
  },
}));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
test("recovers the current head when the live tree is missing without requesting a live diff", () => {
  const workspace = {
    id: "tree-1",
    databaseId: "local",
    name: "recoverable",
    liveRootAvailable: false,
    draftState: "unavailable",
    headSavepointId: "saved",
    capabilities: {
      browseHead: false,
      browseWorkingCopy: false,
      browseCheckpoints: true,
      createCheckpoint: false,
      restoreCheckpoint: true,
      editWorkingCopy: false,
    },
    savepoints: [
      {
        id: "saved",
        name: "saved",
        author: "operator",
        note: "",
        createdAt: "2026-09-17T00:00:00Z",
        fileCount: 1,
        folderCount: 0,
        totalBytes: 5,
        sizeLabel: "5 B",
        filesSnapshot: [],
      },
    ],
  } as unknown as AFSWorkspaceDetail;
  render(
    <CheckpointsTab
      workspace={workspace}
      onBrowserViewChange={vi.fn()}
      onTabChange={vi.fn()}
    />,
  );
  expect(screen.queryByText("Create checkpoint")).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Compare" }),
  ).toBeDisabled();
  const button = screen.getByRole("button", { name: "Restore" });
  expect(button).toBeEnabled();
  fireEvent.click(button);
  expect(diff).toHaveBeenLastCalledWith(expect.anything(), false);
  expect(
    screen.getByText(/Restoring this checkpoint will recreate/),
  ).toBeInTheDocument();
  const confirm = screen.getByRole("button", { name: "Confirm restore" });
  expect(confirm).toBeEnabled();
  fireEvent.click(confirm);
  expect(restore).toHaveBeenCalledWith({
    databaseId: "local",
    workspaceId: "tree-1",
    savepointId: "saved",
  });
});
