import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ButtonHTMLAttributes } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { afsApi } from "../foundation/api/afs";
import type { AFSDatabaseScopeRecord } from "../foundation/database-scope";
import { useDatabases } from "../foundation/hooks/use-afs";
import type { AFSDatabase } from "../foundation/types/afs";
import { AddDatabaseDialog } from "./add-database-dialog";

vi.mock("@redis-ui/components", () => ({
  Button: ({
    variant: _variant,
    ...props
  }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: string }) => (
    <button {...props} />
  ),
}));

const database: AFSDatabase = {
  id: "db-team",
  name: "Team Redis",
  description: "Shared files",
  canEdit: true,
  canDelete: false,
  canCreateWorkspaces: true,
  redisAddr: "redis.example:6380",
  redisUsername: "operator",
  hasPassword: true,
  configRevision: "revision-1",
  redisDB: 2,
  redisTLS: true,
  isDefault: false,
  workspaceCount: 0,
  activeSessionCount: 0,
  afsTotalBytes: 0,
  afsFileCount: 0,
};

const editableDatabase: AFSDatabaseScopeRecord = {
  ...database,
  displayName: database.name,
  databaseName: database.name,
  endpointLabel: database.redisAddr,
  dbIndex: String(database.redisDB),
  username: database.redisUsername,
  useTLS: database.redisTLS,
  isHealthy: true,
};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function fillRequiredFields() {
  fireEvent.change(screen.getByLabelText("Display name"), {
    target: { value: " Team Redis " },
  });
  fireEvent.change(screen.getByLabelText("Redis address"), {
    target: { value: " redis.example:6380 " },
  });
}

function DatabaseList() {
  const query = useDatabases();
  return (
    <output aria-label="Connected databases">
      {query.data?.map((item) => item.name).join(", ")}
    </output>
  );
}

function renderDialog(
  onClose = vi.fn(),
  includeList = false,
  editing?: AFSDatabaseScopeRecord,
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      {includeList && <DatabaseList />}
      <AddDatabaseDialog open onClose={onClose} database={editing} />
    </QueryClientProvider>,
  );
  return onClose;
}

describe("Add database", () => {
  test("saves connection details and refreshes the database list before closing", async () => {
    const list = vi
      .spyOn(afsApi, "listDatabases")
      .mockResolvedValueOnce([])
      .mockResolvedValue([database]);
    const create = vi
      .spyOn(afsApi, "createDatabase")
      .mockResolvedValue(database);
    const onClose = renderDialog(vi.fn(), true);
    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    fillRequiredFields();
    fireEvent.change(screen.getByLabelText("Description"), {
      target: { value: " Shared files " },
    });
    fireEvent.change(screen.getByLabelText("Username"), {
      target: { value: " operator " },
    });
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: " secret with spaces " },
    });
    fireEvent.change(screen.getByLabelText("Database index"), {
      target: { value: "2" },
    });
    fireEvent.click(screen.getByLabelText("Use TLS"));
    fireEvent.click(screen.getByRole("button", { name: "Add database" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(create).toHaveBeenCalledWith({
      name: "Team Redis",
      description: "Shared files",
      redisAddr: "redis.example:6380",
      redisUsername: "operator",
      redisPassword: " secret with spaces ",
      redisDB: 2,
      redisTLS: true,
    });
    expect(list).toHaveBeenCalledTimes(2);
    expect(screen.getByLabelText("Connected databases")).toHaveTextContent(
      "Team Redis",
    );
  });

  test("keeps failed connection details for retry and prevents duplicate saves or closing while pending", async () => {
    let finish!: (value: AFSDatabase) => void;
    const create = vi
      .spyOn(afsApi, "createDatabase")
      .mockRejectedValueOnce(
        new Error("Redis connection failed: authentication required"),
      )
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finish = resolve;
          }),
      );
    const onClose = renderDialog();
    fillRequiredFields();
    fireEvent.click(screen.getByRole("button", { name: "Add database" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "authentication required",
    );
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Redis address")).toHaveValue(
      " redis.example:6380 ",
    );
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "correct-password" },
    });
    const form = screen.getByLabelText("Display name").closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);
    await screen.findByRole("button", { name: "Adding database…" });
    expect(create).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByLabelText("Redis address")).toBeDisabled();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    finish(database);
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
  });

  test("pastes a TLS Redis URL with encoded credentials and an IPv6 address", () => {
    renderDialog();
    fireEvent.paste(screen.getByLabelText("Redis address"), {
      clipboardData: {
        getData: () =>
          "redis-cli -u 'rediss://agent:p%40ss%3Aword@[::1]:6380/23'",
      },
    });
    expect(screen.getByLabelText("Redis address")).toHaveValue("[::1]:6380");
    expect(screen.getByLabelText("Username")).toHaveValue("agent");
    expect(screen.getByLabelText("Password")).toHaveValue("p@ss:word");
    expect(screen.getByLabelText("Database index")).toHaveValue(23);
    expect(screen.getByLabelText("Use TLS")).toBeChecked();
  });

  test("rejects malformed URL credentials without changing the connection fields", () => {
    renderDialog();
    fillRequiredFields();
    fireEvent.paste(screen.getByLabelText("Redis address"), {
      clipboardData: {
        getData: () => "redis://agent:%zz@redis.example:6379/0",
      },
    });
    expect(screen.getByRole("alert")).toHaveTextContent("valid Redis URL");
    expect(screen.getByLabelText("Redis address")).toHaveValue(
      " redis.example:6380 ",
    );
  });

  test.each(["-1", "1.5", "", "9007199254740992"])(
    "rejects invalid database index %s",
    (value) => {
      const create = vi.spyOn(afsApi, "createDatabase");
      renderDialog();
      fillRequiredFields();
      fireEvent.change(screen.getByLabelText("Database index"), {
        target: { value },
      });
      fireEvent.submit(screen.getByLabelText("Display name").closest("form")!);
      expect(screen.getByRole("alert")).toHaveTextContent(
        "non-negative whole number",
      );
      expect(create).not.toHaveBeenCalled();
    },
  );
});

describe("Database settings", () => {
  test("prefills the default connection, preserves its password, and refreshes the list after saving", async () => {
    const list = vi
      .spyOn(afsApi, "listDatabases")
      .mockResolvedValue([database]);
    const update = vi
      .spyOn(afsApi, "updateDatabase")
      .mockResolvedValue(database);
    const onClose = renderDialog(vi.fn(), true, {
      ...editableDatabase,
      id: "local",
      isDefault: true,
    });
    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    expect(
      screen.getByRole("dialog", { name: "Database settings" }),
    ).toBeVisible();
    expect(screen.getByLabelText("Display name")).toHaveValue("Team Redis");
    expect(screen.getByLabelText("Description")).toHaveValue("Shared files");
    expect(screen.getByLabelText("Redis address")).toHaveValue(
      "redis.example:6380",
    );
    expect(screen.getByLabelText("Username")).toHaveValue("operator");
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByLabelText("Database index")).toHaveValue(2);
    expect(screen.getByLabelText("Use TLS")).toBeChecked();
    expect(
      screen.getByText(/Existing mounts keep their current connection/),
    ).toBeVisible();
    fireEvent.change(screen.getByLabelText("Display name"), {
      target: { value: " New name " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(update).toHaveBeenCalledWith({
      databaseId: "local",
      name: "New name",
      description: "Shared files",
      redisAddr: "redis.example:6380",
      redisUsername: "operator",
      redisDB: 2,
      redisTLS: true,
      configRevision: "revision-1",
    });
    expect(list).toHaveBeenCalledTimes(2);
  });

  test.each(["replace", "remove"])(
    "can deliberately %s a saved password",
    async (mode) => {
      const update = vi
        .spyOn(afsApi, "updateDatabase")
        .mockResolvedValue(database);
      const onClose = renderDialog(vi.fn(), false, editableDatabase);
      fireEvent.change(screen.getByLabelText("Password"), {
        target: { value: " new password " },
      });
      if (mode === "remove") {
        fireEvent.click(screen.getByLabelText("Remove saved password"));
        expect(screen.getByLabelText("Password")).toHaveValue("");
        expect(screen.getByLabelText("Password")).toBeDisabled();
      }
      fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
      await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
      expect(update.mock.calls[0][0].redisPassword).toBe(
        mode === "remove" ? "" : " new password ",
      );
    },
  );

  test("keeps edits and their revision through polling, and resets them when reopened", async () => {
    const update = vi
      .spyOn(afsApi, "updateDatabase")
      .mockResolvedValue(database);
    const onClose = vi.fn();
    const queryClient = new QueryClient();
    const content = (open: boolean, record: AFSDatabaseScopeRecord) => (
      <QueryClientProvider client={queryClient}>
        <AddDatabaseDialog open={open} database={record} onClose={onClose} />
      </QueryClientProvider>
    );
    const { rerender } = render(content(true, editableDatabase));
    fireEvent.change(screen.getByLabelText("Display name"), {
      target: { value: "Unsaved name" },
    });
    const updatedRecord = {
      ...editableDatabase,
      displayName: "Someone else's name",
      configRevision: "revision-2",
    };
    rerender(content(true, updatedRecord));
    expect(screen.getByLabelText("Display name")).toHaveValue("Unsaved name");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(update.mock.calls[0][0]).toMatchObject({
      name: "Unsaved name",
      configRevision: "revision-1",
    });
    rerender(content(false, updatedRecord));
    rerender(content(true, updatedRecord));
    expect(screen.getByLabelText("Display name")).toHaveValue(
      "Someone else's name",
    );
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByLabelText("Remove saved password")).not.toBeChecked();
  });

  test("retains failed edits for retry and prevents duplicate saves and closing while pending", async () => {
    let finish!: (value: AFSDatabase) => void;
    const update = vi
      .spyOn(afsApi, "updateDatabase")
      .mockRejectedValueOnce(new Error("Redis connection failed"))
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finish = resolve;
          }),
      );
    const onClose = renderDialog(vi.fn(), false, editableDatabase);
    fireEvent.change(screen.getByLabelText("Redis address"), {
      target: { value: "new.example:6380" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Redis connection failed",
    );
    expect(screen.getByLabelText("Redis address")).toHaveValue(
      "new.example:6380",
    );
    const form = screen.getByLabelText("Display name").closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);
    await screen.findByRole("button", { name: "Saving changes…" });
    expect(update).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Close" })).toBeDisabled();
    expect(screen.getByLabelText("Remove saved password")).toBeDisabled();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    fireEvent.click(screen.getByRole("dialog").parentElement!);
    expect(onClose).not.toHaveBeenCalled();
    finish(database);
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
  });

  test("requires reopening after a revision conflict instead of overwriting newer settings", async () => {
    const conflict = Object.assign(new Error("Database settings changed"), {
      status: 409,
      code: "stale_database_settings",
    });
    const update = vi
      .spyOn(afsApi, "updateDatabase")
      .mockRejectedValue(conflict);
    renderDialog(vi.fn(), false, editableDatabase);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Close and reopen",
    );
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
    fireEvent.paste(screen.getByLabelText("Redis address"), {
      clipboardData: { getData: () => "redis://operator@new.example:6379/0" },
    });
    fireEvent.submit(screen.getByLabelText("Display name").closest("form")!);
    expect(update).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  });

  test("allows correcting a duplicate name conflict without reopening", async () => {
    const conflict = Object.assign(
      new Error("A database with that name already exists"),
      {
        status: 409,
      },
    );
    const update = vi
      .spyOn(afsApi, "updateDatabase")
      .mockRejectedValueOnce(conflict)
      .mockResolvedValueOnce(database);
    const onClose = renderDialog(vi.fn(), false, editableDatabase);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "A database with that name already exists",
    );
    expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
    fireEvent.change(screen.getByLabelText("Display name"), {
      target: { value: "Unique name" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(update).toHaveBeenCalledTimes(2);
    expect(update.mock.calls[1][0].name).toBe("Unique name");
  });
});
