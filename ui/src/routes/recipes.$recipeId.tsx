import { createFileRoute } from "@tanstack/react-router";
import { RecipePage } from "../features/recipes/recipe-page";

export const Route = createFileRoute("/recipes/$recipeId")({
  component: RecipeRoute,
});
function RecipeRoute() {
  const { recipeId } = Route.useParams();
  return <RecipePage key={recipeId} recipeId={recipeId} />;
}
