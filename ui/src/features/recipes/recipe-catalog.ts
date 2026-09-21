import memory from "./content/shared-agent-memory/manifest.json";
import wiki from "./content/shared-llm-wiki-karpathy/manifest.json";
import standards from "./content/org-coding-standards/manifest.json";
import planning from "./content/team-planning-board/manifest.json";
import blank from "./content/blank/manifest.json";

export type RecipeFile = {
  path: string;
  content: string;
  kind: "starter" | "skill" | "command";
};

export type Recipe = {
  id: string;
  slug: string;
  title: string;
  tagline: string;
  category: string;
  summary: string[];
  whyItMatters: string;
  firstPrompt: string;
  files: RecipeFile[];
};

const content = import.meta.glob<string>(
  [
    "./content/*/seed/**/*.md",
    "./content/*/skill/**/*.md",
    "./content/*/commands/**/*.md",
  ],
  { eager: true, query: "?raw", import: "default" },
);

function filesForRecipe(id: string): RecipeFile[] {
  const prefix = `./content/${id}/`;
  return Object.entries(content)
    .filter(([path]) => path.startsWith(prefix))
    .map(([sourcePath, fileContent]): RecipeFile => {
      const relativePath = sourcePath.slice(prefix.length);
      if (relativePath.startsWith("seed/")) {
        return {
          path: relativePath.slice("seed/".length),
          content: fileContent,
          kind: "starter",
        };
      }
      return {
        path: relativePath,
        content: fileContent,
        kind: relativePath.startsWith("skill/") ? "skill" : "command",
      };
    })
    .sort((a, b) => {
      const order = { starter: 0, skill: 1, command: 2 };
      return order[a.kind] - order[b.kind] || a.path.localeCompare(b.path);
    });
}

// Keep the four complete recipes first for Home; blank introduces the basic workflow.
export const recipes: Recipe[] = [memory, wiki, standards, planning, blank].map(
  (manifest) => ({
    ...manifest,
    files: filesForRecipe(manifest.id),
  }),
);

export function getRecipe(slugOrId: string): Recipe | undefined {
  return recipes.find(
    (recipe) => recipe.slug === slugOrId || recipe.id === slugOrId,
  );
}
