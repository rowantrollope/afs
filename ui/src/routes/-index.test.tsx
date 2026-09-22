import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { AnchorHTMLAttributes } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { Route } from "./index";

const { useDatabaseScope } = vi.hoisted(() => ({ useDatabaseScope: vi.fn() }));
vi.mock("../foundation/database-scope", () => ({ useDatabaseScope }));
vi.mock("../foundation/drawer-context", () => ({
  useDrawer: () => ({ open: vi.fn() }),
}));
vi.mock("../components/add-database-dialog", () => ({
  AddDatabaseDialog: ({ open }: { open: boolean }) =>
    open ? <div role="dialog" aria-label="Add database" /> : null,
}));
vi.mock("../features/recipes/recipe-cards", () => ({
  RecipeCards: () => null,
}));
vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: object) => ({ options }),
  Link: ({
    to,
    ...props
  }: AnchorHTMLAttributes<HTMLAnchorElement> & { to: string }) => (
    <a href={to} {...props} />
  ),
}));

afterEach(cleanup);

test("an empty control plane guides setup to Add database", () => {
  useDatabaseScope.mockReturnValue({
    databases: [],
    isLoading: false,
    errorMessage: null,
  });
  const Page = Route.options.component!;
  render(<Page />);
  expect(
    screen.getByRole("region", { name: "Connect your first database" }),
  ).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Add database" }));
  expect(screen.getByRole("dialog", { name: "Add database" })).toBeVisible();
});

test("loading or failing to load connections does not imply an empty setup", () => {
  useDatabaseScope.mockReturnValue({
    databases: [],
    isLoading: true,
    errorMessage: null,
  });
  const Page = Route.options.component!;
  const { rerender } = render(<Page />);
  expect(
    screen.queryByRole("button", { name: "Add database" }),
  ).not.toBeInTheDocument();
  useDatabaseScope.mockReturnValue({
    databases: [],
    isLoading: false,
    errorMessage: "Could not load connections",
  });
  rerender(<Page />);
  expect(
    screen.queryByRole("button", { name: "Add database" }),
  ).not.toBeInTheDocument();
});
