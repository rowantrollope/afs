import "@fontsource-variable/geist/index.css";
import { CommonStyles, themesRebrand } from "@redis-ui/styles";
import "@redis-ui/styles/fonts.css";
import "@redis-ui/styles/normalized-styles.css";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import "modern-normalize/modern-normalize.css";
import { StrictMode } from "react";
import ReactDOM from "react-dom/client";
import { ThemeProvider } from "styled-components";
import { RouteErrorBoundary } from "./error-boundaries/route-error-boundary";
import "./index.css";
import "./styles/fonts.css";
import "./styles/skin-classic.css";
import "./styles/skin-overrides.css";
import "./styles/skin-situation-room.css";

// Import the generated route tree
import { QueryClientProvider } from "@tanstack/react-query";
import { AppErrorBoundary } from "./error-boundaries/app-error-boundary";
import { AuthProvider } from "./foundation/auth-context";
import { queryClient } from "./foundation/query-client";
import { SkinProvider } from "./foundation/skin-context";
import { ColorModeProvider } from "./foundation/theme-context";
import { routeTree } from "./routeTree.gen";

// Create a new router instance
const router = createRouter({
  routeTree,
  defaultErrorComponent: RouteErrorBoundary,
  defaultPreload: "intent",
  defaultPreloadDelay: 35,
  defaultStaleTime: 15_000,
  defaultPreloadStaleTime: 30_000,
  defaultOnCatch: (error, errorInfo) => {
    console.error("Unhandled route error", error, errorInfo);
  },
});

// Register the router instance for type safety
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

// Render the app
const rootElement = document.getElementById("root")!;
if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement);
  root.render(
    <StrictMode>
      <SkinProvider>
        <ColorModeProvider>
          {(colorMode) => (
            <ThemeProvider theme={themesRebrand[colorMode]}>
              <CommonStyles />
              <AppErrorBoundary>
                <QueryClientProvider client={queryClient}>
                  <AuthProvider>
                    <RouterProvider router={router} />
                  </AuthProvider>
                </QueryClientProvider>
              </AppErrorBoundary>
            </ThemeProvider>
          )}
        </ColorModeProvider>
      </SkinProvider>
    </StrictMode>,
  );
}
