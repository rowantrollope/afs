import { Link } from "@tanstack/react-router";
import {
  ArrowUpRight,
  BookOpen,
  Brain,
  ClipboardList,
  FolderPlus,
  ShieldCheck,
} from "lucide-react";
import styled from "styled-components";
import { cardSurface } from "../../components/card-shell";
import type { Recipe } from "./recipe-catalog";
import "../../styles/home.css";

const Card = styled(Link)`
  ${cardSurface}
  text-decoration: none;
  overflow-wrap: anywhere;
  [data-skin="classic"] && {
    background: var(--afs-panel-strong);
  }
`;
const icons: Partial<Record<string, typeof BookOpen>> = {
  "shared-agent-memory": Brain,
  "shared-llm-wiki-karpathy": BookOpen,
  "org-coding-standards": ShieldCheck,
  "team-planning-board": ClipboardList,
  blank: FolderPlus,
};

export function RecipeCards({ items }: { items: readonly Recipe[] }) {
  return (
    <div className="home-recipe-grid">
      {items.map((recipe, index) => {
        const Icon = icons[recipe.id] ?? BookOpen;
        const count = recipe.files.filter(
          (file) => file.kind === "starter",
        ).length;
        return (
          <Card
            key={recipe.id}
            to={`/recipes/${recipe.slug}`}
            className="home-recipe"
          >
            <div className="home-recipe-top">
              <Icon size={23} strokeWidth={1.5} />
              <span>{String(index + 1).padStart(2, "0")}</span>
            </div>
            <span className="home-recipe-category">{recipe.category}</span>
            <h3>{recipe.title}</h3>
            <p>{recipe.tagline}</p>
            <span className="home-recipe-footer">
              <span>
                {count
                  ? `${count} starter files · Read recipe`
                  : "Start from scratch"}
              </span>
              <ArrowUpRight size={17} />
            </span>
          </Card>
        );
      })}
    </div>
  );
}
