import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ButtonHTMLAttributes } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { CreateWorkspaceDialog } from "./CreateWorkspaceDialog";

const { createWorkspace, databaseState } = vi.hoisted(() => ({
  createWorkspace: vi.fn(),
  databaseState: {
    databases: [
      {
        id: "local",
        displayName: "Primary",
        databaseName: "Primary",
        isDefault: true,
        isHealthy: true,
        canCreateWorkspaces: true,
      },
      {
        id: "db-team",
        displayName: "Team Redis",
        databaseName: "Team Redis",
        isDefault: false,
        isHealthy: true,
        canCreateWorkspaces: true,
      },
      {
        id: "db-offline",
        displayName: "Offline",
        databaseName: "Offline",
        isDefault: false,
        isHealthy: false,
        canCreateWorkspaces: true,
      },
    ],
    isLoading: false,
  },
}));

vi.mock("@redis-ui/components", () => ({
  Button: ({
    variant: _variant,
    ...props
  }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: string }) => (
    <button {...props} />
  ),
  Select: ({
    options,
    onChange,
    isDisabled,
    ...props
  }: {
    options: { value: string; label: string }[];
    value: string;
    onChange: (value: string) => void;
    isDisabled: boolean;
  }) => (
    <select
      {...props}
      disabled={isDisabled}
      onChange={(event) => onChange(event.currentTarget.value)}
    >
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  ),
}));
vi.mock("../../foundation/database-scope", () => ({
  useDatabaseScope: () => databaseState,
}));
vi.mock("../../foundation/hooks/use-afs", () => ({
  useCreateWorkspaceMutation: () => ({
    mutateAsync: createWorkspace,
    isPending: false,
    error: null,
  }),
}));

afterEach(cleanup);
beforeEach(() => {
  createWorkspace.mockReset();
  createWorkspace.mockResolvedValue({});
});

describe("Create workspace database selection", () => {
  test("defaults to the primary and creates in the selected added database", async () => {
    const onClose = vi.fn();
    render(<CreateWorkspaceDialog open onClose={onClose} />);
    expect(screen.getByRole("combobox", { name: "Database" })).toHaveValue(
      "local",
    );
    expect(
      screen.queryByRole("option", { name: "Offline" }),
    ).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole("combobox", { name: "Database" }), {
      target: { value: "db-team" },
    });
    fireEvent.change(screen.getByLabelText("Workspace name"), {
      target: { value: "project" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(createWorkspace).toHaveBeenCalledWith(
      expect.objectContaining({
        databaseId: "db-team",
        databaseName: "Team Redis",
        name: "project",
      }),
    );
  });

  test("prevents workspace creation when no database is available", () => {
    const original = databaseState.databases;
    databaseState.databases = [];
    try {
      render(<CreateWorkspaceDialog open onClose={vi.fn()} />);
      fireEvent.change(screen.getByLabelText("Workspace name"), {
        target: { value: "project" },
      });
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Connect an available Redis database",
      );
      expect(
        screen.getByRole("button", { name: "Create workspace" }),
      ).toBeDisabled();
      fireEvent.submit(
        screen.getByLabelText("Workspace name").closest("form")!,
      );
      expect(createWorkspace).not.toHaveBeenCalled();
    } finally {
      databaseState.databases = original;
    }
  });

  test("does not switch an explicit selection to the primary when its connection disappears", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <CreateWorkspaceDialog open onClose={onClose} />,
    );
    fireEvent.change(screen.getByRole("combobox", { name: "Database" }), {
      target: { value: "db-team" },
    });
    fireEvent.change(screen.getByLabelText("Workspace name"), {
      target: { value: "project" },
    });
    const original = databaseState.databases;
    databaseState.databases = original.filter(
      (database) => database.id !== "db-team",
    );
    try {
      rerender(<CreateWorkspaceDialog open onClose={onClose} />);
      expect(screen.getByRole("combobox", { name: "Database" })).toHaveValue(
        "db-team",
      );
      expect(screen.getByRole("alert")).toHaveTextContent(
        "selected database is unavailable",
      );
      expect(
        screen.getByRole("button", { name: "Create workspace" }),
      ).toBeDisabled();
      fireEvent.submit(
        screen.getByLabelText("Workspace name").closest("form")!,
      );
      expect(createWorkspace).not.toHaveBeenCalled();
    } finally {
      databaseState.databases = original;
    }
  });
});
