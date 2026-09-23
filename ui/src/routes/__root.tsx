import { createRootRoute, Outlet, useLocation } from "@tanstack/react-router";
import { GlobalDrawer } from "../components/global-drawer";
import { RouteErrorBoundary } from "../error-boundaries/route-error-boundary";
import { DrawerProvider } from "../foundation/drawer-context";
import { DatabaseScopeProvider } from "../foundation/database-scope";
import { AppBar } from "../layout/app-bar";
import { FlexColItem, FlexRow, MainContainer } from "../layout/layout.styles";
import { AppSidebar } from "../layout/sidebar";
import { BgFx } from "../layout/situation-room-chrome";

function RootLayout() {
  const location = useLocation();
  if (location.pathname === "/connect-cli") return <Outlet />;
  return (
    <DatabaseScopeProvider>
      <DrawerProvider>
      <BgFx />
      <FlexRow>
        <AppSidebar />
        <FlexColItem>
          <AppBar />
          <MainContainer>
            <Outlet />
          </MainContainer>
        </FlexColItem>
      </FlexRow>
      <GlobalDrawer />
      </DrawerProvider>
    </DatabaseScopeProvider>
  );
}
function RootErrorBoundary(props: Parameters<typeof RouteErrorBoundary>[0]) {
  return <RouteErrorBoundary {...props} fullPage />;
}
export const Route = createRootRoute({
  component: RootLayout,
  errorComponent: RootErrorBoundary,
});
