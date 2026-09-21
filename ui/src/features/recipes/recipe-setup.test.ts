import { afterEach, describe, expect, test } from "vitest";
import { getRecipe, recipes } from "./recipe-catalog";
import { buildRecipePrompt, recipeCommands } from "./recipe-setup";

const endpoint = "http://127.0.0.1:8091";
const skillURL = `${endpoint}/afs-skill.md`;

afterEach(() => sessionStorage.clear());

describe("migrated recipe catalog", () => {
  test("retains all original starters and companion guides, including the blank template", () => {
    expect(recipes.map((recipe) => recipe.id).sort()).toEqual([
      "blank",
      "org-coding-standards",
      "shared-agent-memory",
      "shared-llm-wiki-karpathy",
      "team-planning-board",
    ]);
    const files = recipes.flatMap((recipe) => recipe.files);
    expect(files.filter((file) => file.kind === "starter")).toHaveLength(32);
    expect(files.filter((file) => file.kind === "skill")).toHaveLength(4);
    expect(files.filter((file) => file.kind === "command")).toHaveLength(4);
    for (const recipe of recipes) {
      expect(getRecipe(recipe.slug)).toBe(recipe);
      expect(new Set(recipe.files.map((file) => file.path)).size).toBe(
        recipe.files.length,
      );
      for (const file of recipe.files) {
        expect(file.path).not.toMatch(/^(?:\/|\.\.)/);
        expect(file.content.trim()).not.toBe("");
      }
    }
    expect(getRecipe("unknown-recipe")).toBeUndefined();
  });

  test("removes legacy template placeholders and hosted MCP tool instructions", () => {
    const content = recipes
      .flatMap((recipe) => [
        recipe.firstPrompt,
        ...recipe.files.map((file) => file.content),
      ])
      .join("\n");
    expect(content).not.toMatch(
      /\{\{(?:workspace|mcp|server|mount|skillName|toolPrefix)[^}]*\}\}/i,
    );
    expect(content).not.toMatch(
      /file_(?:read|write|grep|list|insert|delete_lines)|checkpoint_create|mcp__|mcp\.json|\.mcp\.json|workspace_(?:read|write|list)|afs (?:ws|fs)\b/,
    );
  });
});

describe("recipe setup handoff", () => {
  test("includes every migrated file verbatim with safe mounted-folder destinations", () => {
    for (const recipe of recipes.filter((item) => item.files.length > 0)) {
      const prompt = buildRecipePrompt(recipe, endpoint, skillURL);
      expect(prompt).toContain(skillURL);
      expect(prompt).toContain(
        "Confirm my workspace name and local directory first",
      );
      expect(prompt).toContain("Do not overwrite existing files");
      expect(prompt).toContain("afs sync --wait");
      expect(prompt).toContain(recipe.firstPrompt);
      for (const file of recipe.files) {
        const path =
          file.kind === "starter"
            ? file.path
            : `.afs/recipes/${recipe.id}/${file.path}`;
        expect(prompt).toContain(
          `<<<FILE: ${path}>>>\n${file.content.trimEnd()}\n<<<END>>>`,
        );
      }
      expect(prompt.match(/<<<FILE: /g)).toHaveLength(recipe.files.length);
    }
  });

  test("keeps the authenticated console token out of agent prompts and shell commands", () => {
    const token = "private-console-token-for-regression";
    sessionStorage.setItem("afs_console_token", token);
    for (const recipe of recipes) {
      const prompt = buildRecipePrompt(recipe, endpoint, skillURL);
      const commands = recipeCommands(recipe, endpoint);
      expect(prompt).not.toContain(token);
      expect(commands).not.toContain(token);
      expect(prompt).toContain("set AFS_CONTROL_PLANE_TOKEN locally");
      expect(prompt).toContain("never request credentials in chat");
      expect(commands).toContain(`afs auth login --url '${endpoint}'`);
      expect(commands).toContain(`afs create ${recipe.slug}`);
      expect(commands).toContain(
        `afs mount ${recipe.slug} ~/afs/${recipe.slug}`,
      );
    }
  });

  test("does not seed the blank template before the user chooses its purpose", () => {
    const blank = recipes.find((recipe) => recipe.id === "blank")!;
    expect(blank.files).toHaveLength(0);
    const prompt = buildRecipePrompt(blank, endpoint, skillURL);
    expect(prompt).toContain("The workspace starts empty");
    expect(prompt).toContain("wait for me to choose before creating any files");
    expect(prompt).not.toContain("<<<FILE:");
  });
});
