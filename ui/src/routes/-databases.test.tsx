import { themesRebrand } from "@redis-ui/styles";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from "react";
import { ThemeProvider } from "styled-components";
import { afterEach, expect, test, vi } from "vitest";
import type { AFSDatabaseScopeRecord } from "../foundation/database-scope";
import { Route } from "./databases";

const { useDatabaseScope } = vi.hoisted(() => ({ useDatabaseScope: vi.fn() }));
vi.mock("../foundation/database-scope", () => ({ useDatabaseScope }));
// The Redis packages include a bundled React runtime that cannot load in Vitest.
vi.mock("@redis-ui/components", () => ({
  Button: ({
    variant: _variant,
    size: _size,
    ...props
  }: ButtonHTMLAttributes<HTMLButtonElement> & {
    variant?: string;
    size?: string;
  }) => <button {...props} />,
  Typography: {
    Heading: (props: HTMLAttributes<HTMLHeadingElement>) => <h2 {...props} />,
    Body: (props: HTMLAttributes<HTMLParagraphElement>) => <p {...props} />,
  },
  TableHeading: {
    SearchInput: ({
      onChange,
      ...props
    }: {
      value: string;
      onChange: (value: string) => void;
      placeholder: string;
    }) => (
      <input {...props} onChange={(event) => onChange(event.target.value)} />
    ),
  },
}));
vi.mock("@redis-ui/table", () => ({
  Table: ({
    data,
    columns,
    onRowClick,
  }: {
    data: AFSDatabaseScopeRecord[];
    columns: {
      cell: (context: {
        row: { original: AFSDatabaseScopeRecord };
      }) => ReactNode;
    }[];
    onRowClick: (row: AFSDatabaseScopeRecord) => void;
  }) => (
    <table>
      <tbody>
        {data.map((row) => (
          <tr key={row.id} onClick={() => onRowClick(row)}>
            {columns.map((column, index) => (
              <td key={index}>{column.cell({ row: { original: row } })}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  ),
}));

const database: AFSDatabaseScopeRecord = {
  id: "local",
  displayName: "Team Redis",
  databaseName: "Team Redis",
  description: "Primary connection",
  canEdit: true,
  canDelete: false,
  canCreateWorkspaces: true,
  endpointLabel: "localhost:6380",
  dbIndex: "2",
  username: "operator",
  hasPassword: true,
  configRevision: "revision-1",
  useTLS: false,
  isDefault: true,
  workspaceCount: 0,
  activeSessionCount: 0,
  isHealthy: true,
  afsTotalBytes: 0,
  afsFileCount: 0,
};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function mount(rows = [database]) {
  useDatabaseScope.mockReturnValue({
    databases: rows,
    isLoading: false,
    errorMessage: null,
  });
  const Page = Route.options.component!;
  render(
    <ThemeProvider theme={themesRebrand.light}>
      <QueryClientProvider client={new QueryClient()}>
        <Page />
      </QueryClientProvider>
    </ThemeProvider>,
  );
}

test("an empty registry offers Add database", () => {
  mount([]);
  expect(screen.getByText(/No Redis databases are connected/)).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Add database" }));
  expect(screen.getByRole("dialog", { name: "Add database" })).toBeVisible();
});

test("offline database settings remain editable and identify the default", () => {
  mount([
    { ...database, isHealthy: false, connectionError: "Connection refused" },
  ]);
  expect(screen.getByText("Default")).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Team Redis" }));
  expect(screen.getByLabelText("Redis address")).toBeEnabled();
  expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
});

test("opens populated settings from a database row and its accessible name button", () => {
  mount();
  const name = screen.getByRole("button", { name: "Team Redis" });
  expect(name).toBeEnabled();
  fireEvent.click(name.closest("tr")!);
  expect(
    screen.getByRole("dialog", { name: "Database settings" }),
  ).toBeVisible();
  expect(screen.getByLabelText("Redis address")).toHaveValue("localhost:6380");
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  fireEvent.click(name);
  expect(
    screen.getByRole("dialog", { name: "Database settings" }),
  ).toBeVisible();
  expect(screen.getByLabelText("Username")).toHaveValue("operator");
});

test("copying the database ID leaves settings closed and Add database starts with empty fields", () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
  mount();
  fireEvent.click(
    screen.getByRole("button", { name: "Copy database ID local" }),
  );
  expect(writeText).toHaveBeenCalledWith("local");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Add database" }));
  expect(screen.getByRole("dialog", { name: "Add database" })).toBeVisible();
  expect(screen.getByLabelText("Redis address")).toHaveValue("");
  expect(
    screen.queryByLabelText("Remove saved password"),
  ).not.toBeInTheDocument();
});
