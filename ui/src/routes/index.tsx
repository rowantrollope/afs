import { createFileRoute, Link } from "@tanstack/react-router";
import {
  ArrowDownToLine,
  ArrowRight,
  ArrowUpRight,
  BookOpen,
  Check,
  Copy,
  FileCode2,
  FolderOpen,
  KeyRound,
  Layers,
  Radio,
  Terminal,
  Users,
} from "lucide-react";
import { useState } from "react";
import styled from "styled-components";
import { SurfaceCard } from "../components/card-shell";
import { AddDatabaseDialog } from "../components/add-database-dialog";
import { controlPlaneEndpoint } from "../foundation/api/afs";
import { useDatabaseScope } from "../foundation/database-scope";
import { useDrawer } from "../foundation/drawer-context";
import {
  agentSetupPrompt,
  apiAccessGuide,
  cliQuickstart,
} from "../foundation/home-content";
import { recipes } from "../features/recipes/recipe-catalog";
import { RecipeCards } from "../features/recipes/recipe-cards";
import "../styles/home.css";

export const Route = createFileRoute("/")({ component: HomePage });
// Reuse the application card chrome. Classic's default panel is translucent;
// Home uses its solid counterpart so the page grid cannot show through cards.
const HomeCard = styled(SurfaceCard)`
  [data-skin="classic"] && {
    background: var(--afs-panel-strong);
  }
`;

const featuredRecipes = recipes.filter((recipe) => recipe.id !== "blank");

function HomePage() {
  const { open } = useDrawer();
  const { databases, isLoading, errorMessage } = useDatabaseScope();
  const [addDatabaseOpen, setAddDatabaseOpen] = useState(false);
  const needsDatabase = !isLoading && !errorMessage && databases.length === 0;
  const [mode, setMode] = useState<"agent" | "cli">("agent");
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">(
    "idle",
  );
  const [skillState, setSkillState] = useState<"idle" | "loading" | "error">(
    "idle",
  );
  const endpoint = controlPlaneEndpoint();
  const skillURL = new URL("/afs-skill.md", window.location.origin).href;
  const prompt = agentSetupPrompt(endpoint, skillURL);
  const commands = cliQuickstart(endpoint);

  async function copySetup() {
    try {
      await navigator.clipboard.writeText(mode === "agent" ? prompt : commands);
      setCopyState("copied");
    } catch {
      setCopyState("error");
    }
  }

  async function viewSkill() {
    setSkillState("loading");
    try {
      const response = await fetch("/afs-skill.md");
      if (!response.ok) throw new Error("Skill unavailable");
      const skill = await response.text();
      open({
        kind: "commands",
        title: "AFS agent skill",
        subline:
          "Read or copy this guide, or download SKILL.md from Quickstart for your agent.",
        sections: [{ title: "SKILL.md", command: skill }],
      });
      setSkillState("idle");
    } catch {
      setSkillState("error");
    }
  }

  return (
    <div className="afs-home">
      {needsDatabase && (
        <HomeCard
          as="section"
          className="home-workspace-callout"
          aria-label="Connect your first database"
        >
          <div>
            <h3>Connect your first Redis database.</h3>
            <p>
              Add a database before creating a workspace. You can manage API
              keys now.
            </p>
          </div>
          <button
            type="button"
            className="home-button home-button-dark"
            onClick={() => setAddDatabaseOpen(true)}
          >
            Add database <ArrowRight size={16} />
          </button>
        </HomeCard>
      )}
      <AddDatabaseDialog
        open={addDatabaseOpen}
        onClose={() => setAddDatabaseOpen(false)}
      />
      <HomeCard as="section" className="home-hero" aria-labelledby="home-title">
        <div className="home-hero-copy">
          <span className="home-eyebrow">
            PERSISTENT FILES. ENDLESS POSSIBILITIES.
          </span>
          <h1 id="home-title">
            Learn Agent
            <br />
            Filesystem<span className="home-title-dot">.</span>
          </h1>
          <p>
            Your agents come and go. Their work stays.
            <br />
            Give every agent a filesystem to call home.
          </p>
          <div className="home-hero-actions">
            <Link to="/docs" className="home-button home-button-dark">
              Explore the guide <ArrowRight size={16} />
            </Link>
            <a href="#quickstart" className="home-text-link">
              Start building <ArrowUpRight size={15} />
            </a>
          </div>
        </div>
        <div className="home-hero-visual">
          <img
            src="/images/afs-home-workspace.png"
            alt=""
            width="600"
            height="400"
          />
          <span className="home-visual-caption">
            ONE WORKSPACE. EVERY AGENT. ANYWHERE.
          </span>
        </div>
      </HomeCard>
      <div className="home-principles" aria-label="How AFS works">
        <span>
          <FolderOpen size={15} /> Ordinary files
        </span>
        <span>
          <Users size={15} /> Shared across agents
        </span>
        <span>
          <Layers size={15} /> Backed by Redis
        </span>
      </div>
      <div className="home-main-grid">
        <HomeCard
          as="section"
          className="home-cookbooks"
          aria-labelledby="cookbooks-title"
        >
          <div className="home-section-heading">
            <div>
              <span className="home-eyebrow">
                FROM FIRST FILE TO WHAT’S NEXT
              </span>
              <h2 id="cookbooks-title">AFS in action</h2>
            </div>
            <span className="home-section-label">RECIPES / 01–04</span>
          </div>
          <p className="home-section-intro">
            Small recipes for agents that do real work.
          </p>
          <RecipeCards items={featuredRecipes} />
          <Link to="/recipes" className="home-text-link home-all-cookbooks">
            Explore all recipes <ArrowRight size={16} />
          </Link>
          <HomeCard className="home-workspace-callout">
            <div className="home-callout-icon">
              <FolderOpen size={23} strokeWidth={1.5} />
            </div>
            <div>
              <h3>A place for your next idea.</h3>
              <p>Pick up a workspace and make something with your agent.</p>
            </div>
            <Link to="/workspaces" aria-label="Open workspaces">
              <ArrowRight size={20} />
            </Link>
          </HomeCard>
        </HomeCard>
        <HomeCard
          as="aside"
          className="home-quickstart"
          id="quickstart"
          aria-labelledby="quickstart-title"
        >
          <div className="home-quickstart-heading">
            <h2 id="quickstart-title">Quickstart</h2>
            <Terminal size={22} strokeWidth={1.5} />
          </div>
          <p className="home-section-intro">
            From zero to your agent’s first workspace.
          </p>
          <div className="home-setup-tabs" aria-label="Setup method">
            <button
              aria-pressed={mode === "agent"}
              onClick={() => {
                setMode("agent");
                setCopyState("idle");
              }}
              type="button"
            >
              Agent prompt
            </button>
            <button
              aria-pressed={mode === "cli"}
              onClick={() => {
                setMode("cli");
                setCopyState("idle");
              }}
              type="button"
            >
              Use the CLI
            </button>
          </div>
          <HomeCard className="home-prompt-box">
            <div className="home-prompt-caption">
              <span className="home-eyebrow">
                {mode === "agent"
                  ? "GIVE THIS TO YOUR CODING AGENT"
                  : "RUN IN YOUR TERMINAL"}
              </span>
              <span className="home-file-type">
                {mode === "agent" ? "TXT" : "SH"}
              </span>
            </div>
            <pre
              tabIndex={0}
              aria-label={
                mode === "agent" ? "Agent setup prompt" : "CLI setup commands"
              }
            >
              {mode === "agent" ? prompt : commands}
            </pre>
            <button
              className="home-button home-copy-button"
              type="button"
              onClick={() => {
                void copySetup();
              }}
            >
              {copyState === "copied" ? (
                <Check size={15} />
              ) : (
                <Copy size={15} />
              )}
              {copyState === "copied"
                ? "Copied to clipboard"
                : mode === "agent"
                  ? "Copy agent prompt"
                  : "Copy commands"}
            </button>
          </HomeCard>
          <p className="home-copy-status" role="status">
            {copyState === "error"
              ? "Copy unavailable. Select and copy the text above."
              : copyState === "copied"
                ? "Ready to paste into your " +
                  (mode === "agent" ? "coding agent." : "terminal.")
                : mode === "agent"
                  ? "Works with any agent that can read a skill."
                  : "Install the afs CLI first. Login opens your browser for approval."}
          </p>
          <div className="home-skill-row">
            <FileCode2 size={18} />
            <div>
              <a href="/afs-skill.md" download="SKILL.md">
                SKILL.md <ArrowDownToLine size={13} />
              </a>
              <span>The AFS guide for your agent</span>
            </div>
            <button
              className="home-text-link"
              type="button"
              disabled={skillState === "loading"}
              onClick={() => {
                void viewSkill();
              }}
            >
              {skillState === "loading" ? "Loading…" : "View"}{" "}
              <ArrowUpRight size={13} />
            </button>
          </div>
          {skillState === "error" && (
            <p className="home-copy-status" role="alert">
              Could not load the skill. Try again or download SKILL.md.
            </p>
          )}
          <div className="home-resources">
            <button
              type="button"
              onClick={() =>
                open({ kind: "commands", ...apiAccessGuide(endpoint) })
              }
            >
              <KeyRound size={20} strokeWidth={1.6} />
              <strong>API access</strong>
              <span>
                Connect your CLI <ArrowUpRight size={13} />
              </span>
            </button>
            <Link to="/docs">
              <BookOpen size={20} strokeWidth={1.6} />
              <strong>Documentation</strong>
              <span>
                Find your next step <ArrowUpRight size={13} />
              </span>
            </Link>
          </div>
          <Link to="/monitor" className="home-monitor-link">
            <Radio size={17} />
            <span>See your agents in action</span>
            <ArrowRight size={16} />
          </Link>
        </HomeCard>
      </div>
      <footer className="home-footer">
        <span>BUILT FOR AGENTS. GROUNDED IN FILES.</span>
        <a
          href="https://github.com/rowantrollope/afs"
          target="_blank"
          rel="noreferrer"
        >
          AFS on GitHub <ArrowUpRight size={13} />
        </a>
      </footer>
    </div>
  );
}
