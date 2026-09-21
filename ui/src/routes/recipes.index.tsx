import { createFileRoute } from "@tanstack/react-router";
import { RecipesPage } from "../features/recipes/recipe-page";

export const Route = createFileRoute("/recipes/")({ component: RecipesPage });
