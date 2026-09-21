import { Link } from "@tanstack/react-router";
import { ArrowLeft, Check, Copy, Download } from "lucide-react";
import { useState } from "react";
import { SurfaceCard } from "../../components/card-shell";
import { controlPlaneEndpoint } from "../../foundation/api/afs";
import { RecipeCards } from "./recipe-cards";
import { getRecipe, recipes } from "./recipe-catalog";
import { buildRecipePrompt, recipeCommands } from "./recipe-setup";
import "./recipes.css";

function CopyText({ text, label }: { text: string; label: string }) {
  const [status, setStatus] = useState<"idle" | "copied" | "error">("idle");
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setStatus("copied");
    } catch {
      setStatus("error");
    }
  }
  return (
    <span className="recipe-copy">
      <button
        className="home-button recipe-button"
        type="button"
        onClick={() => void copy()}
      >
        {status === "copied" ? <Check size={15} /> : <Copy size={15} />}
        {status === "copied" ? "Copied" : label}
      </button>
      <span role="status">
        {status === "error"
          ? "Copy unavailable. Select the text below to copy it."
          : ""}
      </span>
    </span>
  );
}
function DownloadText({
  text,
  filename,
  children,
}: {
  text: string;
  filename: string;
  children: React.ReactNode;
}) {
  return (
    <a
      className="home-text-link"
      href={`data:text/markdown;charset=utf-8,${encodeURIComponent(text)}`}
      download={filename}
    >
      <Download size={15} />
      {children}
    </a>
  );
}

export function RecipesPage() {
  return (
    <div className="afs-home recipes-page">
      <Link to="/" className="home-text-link">
        <ArrowLeft size={15} />
        Back to Home
      </Link>
      <header className="recipe-heading">
        <span className="home-eyebrow">START WITH SOMETHING USEFUL</span>
        <h1>Recipes for your agents.</h1>
        <p>
          The original AFS starter templates, with complete files and
          instructions for your mounted workspace.
        </p>
      </header>
      <RecipeCards items={recipes} />
    </div>
  );
}

export function RecipePage({ recipeId }: { recipeId: string }) {
  const recipe = getRecipe(recipeId);
  if (!recipe)
    return (
      <div className="afs-home recipes-page">
        <h1>Recipe not found</h1>
        <p>Choose a recipe from the collection.</p>
        <Link to="/recipes" className="home-text-link">
          View all recipes
        </Link>
      </div>
    );
  const endpoint = controlPlaneEndpoint();
  const prompt = buildRecipePrompt(
    recipe,
    endpoint,
    new URL("/afs-skill.md", window.location.origin).href,
  );
  const commands = recipeCommands(recipe, endpoint);
  const starterFiles = recipe.files.filter((file) => file.kind === "starter");
  const companions = recipe.files.filter((file) => file.kind !== "starter");
  return (
    <div className="afs-home recipes-page" key={recipe.id}>
      <Link to="/recipes" className="home-text-link">
        <ArrowLeft size={15} />
        All recipes
      </Link>
      <header className="recipe-heading">
        <span className="home-eyebrow">{recipe.category}</span>
        <h1>{recipe.title}</h1>
        <p>{recipe.tagline}</p>
      </header>
      <div className="recipe-overview">
        <SurfaceCard as="section" className="recipe-panel">
          <h2>What you get</h2>
          <ul>
            {recipe.summary.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </SurfaceCard>
        <SurfaceCard as="section" className="recipe-panel">
          <h2>Why it matters</h2>
          <p>{recipe.whyItMatters}</p>
          {recipe.id === "shared-llm-wiki-karpathy" && (
            <a
              className="home-text-link"
              href="https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f"
              target="_blank"
              rel="noreferrer"
            >
              Karpathy’s original LLM wiki note
            </a>
          )}
        </SurfaceCard>
      </div>
      <SurfaceCard as="section" className="recipe-panel">
        <span className="home-eyebrow">01 / SET UP</span>
        <h2>Give this recipe to your agent</h2>
        <p>
          The setup prompt includes{" "}
          {starterFiles.length
            ? `all ${starterFiles.length} starter files and the companion instructions`
            : "the steps to create an empty workspace"}
          . Your agent will confirm the destination before writing anything.
        </p>
        <div className="recipe-actions">
          <CopyText
            key={`${recipe.id}-prompt`}
            text={prompt}
            label="Copy setup prompt"
          />
          <DownloadText text={prompt} filename={`${recipe.slug}-setup.md`}>
            Download setup prompt
          </DownloadText>
        </div>
        <details className="recipe-file">
          <summary>Read the complete setup prompt</summary>
          <pre tabIndex={0}>{prompt}</pre>
        </details>
        <details className="recipe-file">
          <summary>Prefer to set up the workspace yourself?</summary>
          <p>
            Use a new workspace name and an empty local directory. After
            mounting, copy the starter files below into that folder, keeping
            their paths, then verify publication with{" "}
            <code>afs sync --wait {recipe.slug}</code>.
          </p>
          <CopyText text={commands} label="Copy CLI commands" />
          <pre tabIndex={0}>{commands}</pre>
        </details>
        {recipe.id === "org-coding-standards" && (
          <div className="recipe-note">
            <h3>Share a read-only copy</h3>
            <p>
              A maintainer initializes and updates the standards. Other agents
              can mount them on their machines with the command below. The
              read-only setting applies to that mount; API keys still grant
              administrator access.
            </p>
            <pre
              tabIndex={0}
            >{`afs mount ${recipe.slug} ~/afs/${recipe.slug} --readonly`}</pre>
          </div>
        )}
      </SurfaceCard>
      <SurfaceCard as="section" className="recipe-panel">
        <span className="home-eyebrow">02 / EXPLORE THE FILES</span>
        <h2>
          Starter files{" "}
          <span className="recipe-count">{starterFiles.length}</span>
        </h2>
        <p>
          {starterFiles.length
            ? "These paths are relative to your mounted workspace. Open any file to read or download its complete contents."
            : "This recipe begins with an empty workspace. Let your first task determine its structure."}
        </p>
        {starterFiles.map((file) => (
          <details className="recipe-file" key={file.path}>
            <summary>{file.path}</summary>
            <div className="recipe-actions">
              <CopyText text={file.content} label={`Copy ${file.path}`} />
              <DownloadText
                text={file.content}
                filename={file.path.split("/").at(-1)!}
              >
                Download file
              </DownloadText>
            </div>
            <pre tabIndex={0} aria-label={file.path}>
              {file.content}
            </pre>
          </details>
        ))}
      </SurfaceCard>
      {companions.length > 0 && (
        <SurfaceCard as="section" className="recipe-panel">
          <h2>Agent instructions</h2>
          <p>
            The full skill and task guides explain how to use and maintain this
            workspace. The setup prompt includes them under{" "}
            <code>.afs/recipes/{recipe.id}/</code> for your agent to read.
          </p>
          {companions.map((file) => (
            <details className="recipe-file" key={file.path}>
              <summary>
                {file.kind === "skill"
                  ? "Workspace skill"
                  : file.path.split("/").at(-1)}
                <span className="recipe-kind">{file.kind}</span>
              </summary>
              <div className="recipe-actions">
                <CopyText text={file.content} label={`Copy ${file.path}`} />
                <DownloadText
                  text={file.content}
                  filename={file.path.split("/").at(-1)!}
                >
                  Download instructions
                </DownloadText>
              </div>
              <pre tabIndex={0}>{file.content}</pre>
            </details>
          ))}
        </SurfaceCard>
      )}
      <SurfaceCard as="section" className="recipe-panel">
        <span className="home-eyebrow">03 / TRY IT</span>
        <h2>Your first conversation</h2>
        <blockquote>{recipe.firstPrompt}</blockquote>
        <CopyText text={recipe.firstPrompt} label="Copy first prompt" />
      </SurfaceCard>
      <p className="recipe-source">
        Adapted from the{" "}
        <a
          href={`https://github.com/redis/agent-filesystem/tree/1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e/templates/${recipe.id}`}
          target="_blank"
          rel="noreferrer"
        >
          original Agent Filesystem template
        </a>
        .
      </p>
    </div>
  );
}
