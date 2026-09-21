import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import type { AnchorHTMLAttributes } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { recipes } from "./recipe-catalog";
import { RecipePage, RecipesPage } from "./recipe-page";

vi.mock("@tanstack/react-router", async () => {
  const { forwardRef } = await import("react");
  type LinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & {
    to: string;
    params?: Record<string, string>;
  };
  return {
    Link: forwardRef<HTMLAnchorElement, LinkProps>(function TestLink(
      { to, params, ...props },
      ref,
    ) {
      const href = to.replace(/\$([A-Za-z]+)/g, (_, name: string) =>
        encodeURIComponent(params?.[name] ?? ""),
      );
      return <a {...props} href={href} ref={ref} />;
    }),
  };
});

const writeText = vi.fn<(text: string) => Promise<void>>();

beforeEach(() => {
  writeText.mockReset().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText },
  });
});
afterEach(() => {
  cleanup();
  sessionStorage.clear();
});

test("every collection card links to its complete recipe", () => {
  render(<RecipesPage />);
  for (const recipe of recipes) {
    const card = screen
      .getByRole("heading", { name: recipe.title })
      .closest("a");
    expect(card).toHaveAttribute("href", `/recipes/${recipe.slug}`);
  }
  expect(
    screen
      .getAllByRole("link")
      .filter((link) => link.getAttribute("href")?.startsWith("/recipes/")),
  ).toHaveLength(5);
});

test.each(recipes.filter((recipe) => recipe.files.length > 0))(
  "$title exposes complete starter contents and companion downloads",
  (recipe) => {
    render(<RecipePage recipeId={recipe.slug} />);
    expect(
      screen.getByRole("heading", { level: 1, name: recipe.title }),
    ).toBeInTheDocument();
    for (const file of recipe.files) {
      const preview = [...document.querySelectorAll("pre")].find(
        (element) => element.textContent === file.content,
      );
      expect(preview, file.path).toBeDefined();
      const detail = preview!.closest("details")!;
      fireEvent.click(detail.querySelector("summary")!);
      const download = detail.querySelector("a[download]")!;
      expect(download.getAttribute("download")).toBe(
        file.path.split("/").at(-1),
      );
      expect(
        decodeURIComponent(
          download.getAttribute("href")!.split(",").slice(1).join(","),
        ),
      ).toBe(file.content);
    }
    const setupDownload = screen.getByRole("link", {
      name: "Download setup prompt",
    });
    expect(setupDownload).toHaveAttribute(
      "download",
      `${recipe.slug}-setup.md`,
    );
    expect(screen.getByRole("link", { name: "All recipes" })).toHaveAttribute(
      "href",
      "/recipes",
    );
  },
);

test("copies the complete setup prompt without exposing the console token", async () => {
  sessionStorage.setItem("afs_console_token", "sensitive-token-not-for-chat");
  const recipe = recipes.find((item) => item.id === "shared-agent-memory")!;
  render(<RecipePage recipeId={recipe.slug} />);
  fireEvent.click(screen.getByRole("button", { name: "Copy setup prompt" }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument(),
  );
  expect(writeText).toHaveBeenCalledOnce();
  const copied = writeText.mock.calls[0][0];
  expect(copied).toContain(recipe.firstPrompt);
  expect(copied).not.toContain("sensitive-token-not-for-chat");
  for (const file of recipe.files)
    expect(copied).toContain(file.content.trimEnd());
  const download = screen.getByRole("link", { name: "Download setup prompt" });
  expect(
    decodeURIComponent(
      download.getAttribute("href")!.split(",").slice(1).join(","),
    ),
  ).toBe(copied);
});

test("offers selectable text when clipboard access fails", async () => {
  writeText.mockRejectedValue(new Error("Clipboard permission denied"));
  const recipe = recipes.find((item) => item.id === "shared-agent-memory")!;
  render(<RecipePage recipeId={recipe.slug} />);
  fireEvent.click(screen.getByRole("button", { name: "Copy setup prompt" }));
  await waitFor(() =>
    expect(
      screen.getByText("Copy unavailable. Select the text below to copy it."),
    ).toBeInTheDocument(),
  );
  expect(
    screen.getByRole("button", { name: "Copy setup prompt" }),
  ).toBeInTheDocument();
  expect(
    [...document.querySelectorAll("pre")].some((element) =>
      element.textContent.startsWith(`Help me set up ${recipe.title}`),
    ),
  ).toBe(true);
});

test("blank recipe explains that it starts empty", () => {
  const recipe = recipes.find((item) => item.id === "blank")!;
  render(<RecipePage recipeId={recipe.slug} />);
  expect(
    screen.getByText(
      "This recipe begins with an empty workspace. Let your first task determine its structure.",
    ),
  ).toBeInTheDocument();
  expect(
    screen.queryByRole("heading", { name: "Agent instructions" }),
  ).not.toBeInTheDocument();
});

test("unknown recipes link back to the collection", () => {
  render(<RecipePage recipeId="nonexistent-recipe" />);
  expect(
    screen.getByRole("heading", { name: "Recipe not found" }),
  ).toBeInTheDocument();
  expect(
    screen.getByRole("link", { name: "View all recipes" }),
  ).toHaveAttribute("href", "/recipes");
  expect(
    screen.queryByRole("button", { name: "Copy setup prompt" }),
  ).not.toBeInTheDocument();
});
