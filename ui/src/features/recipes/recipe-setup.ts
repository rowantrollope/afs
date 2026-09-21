import { shellQuote } from "../../foundation/home-content";
import type { Recipe } from "./recipe-catalog";

export function recipeCommands(recipe: Recipe, endpoint: string) {
  return `# Set AFS_CONTROL_PLANE_TOKEN locally if this server requires it.
afs auth login --url ${shellQuote(endpoint)}

# Choose an unused workspace name and an empty local directory.
afs create ${recipe.slug}
afs mount ${recipe.slug} ~/afs/${recipe.slug}`;
}

export function buildRecipePrompt(
  recipe: Recipe,
  endpoint: string,
  skillURL: string,
) {
  const starters = recipe.files.filter((file) => file.kind === "starter");
  const companions = recipe.files.filter((file) => file.kind !== "starter");
  const files = [
    ...starters,
    ...companions.map((file) => ({
      ...file,
      path: `.afs/recipes/${recipe.id}/${file.path}`,
    })),
  ];
  const blocks = files
    .map(
      (file) =>
        `<<<FILE: ${file.path}>>>\n${file.content.trimEnd()}\n<<<END>>>`,
    )
    .join("\n\n");

  return `Help me set up ${recipe.title} in Agent Filesystem (AFS).

Read the current AFS CLI guide at ${skillURL}. Connect to ${endpoint}. If authentication is required, ask me to set AFS_CONTROL_PLANE_TOKEN locally; never request credentials in chat.

Use the afs CLI and ordinary files in a mounted folder. Confirm my workspace name and local directory first; suggested defaults are ${recipe.slug} and ~/afs/${recipe.slug}. Check existing workspaces and mounts before creating anything. Create a new workspace with afs create and mount it with afs mount, or use the workspace I explicitly choose. Do not overwrite existing files or merge these starter files into an existing project without asking.

${starters.length ? `Initialize the mounted workspace with the following starter files. Paths are relative to the mount root. Preserve the supplied content. Companion skill and command documents belong under .afs/recipes/${recipe.id}/ for reference; they are instructions, not automatically installed agent tools. Read and follow AGENTS.md and the companion skill, and use the command guides when relevant.\n\n${blocks}\n\nOnce the files are written, verify their contents, run afs sync --wait with the actual workspace name, and summarize the layout. Background synchronization delivers updates to other agents; this wait verifies this client's publication, not delivery to every client.` : "The workspace starts empty. Suggest three ways to use it based on my work, and wait for me to choose before creating any files."}

Then help me with this first task:
${recipe.firstPrompt}`;
}
