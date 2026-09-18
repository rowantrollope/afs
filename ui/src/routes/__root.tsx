import { createRootRoute, Outlet } from "@tanstack/react-router";
import { GlobalDrawer } from "../components/global-drawer";
import { RouteErrorBoundary } from "../error-boundaries/route-error-boundary";
import { DrawerProvider } from "../foundation/drawer-context";
import { AppBar } from "../layout/app-bar";
import { FlexColItem, FlexRow, MainContainer } from "../layout/layout.styles";
import { AppSidebar } from "../layout/sidebar";
import { BgFx } from "../layout/situation-room-chrome";

function RootLayout() {
  return (
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
  );
}
function RootErrorBoundary(props: Parameters<typeof RouteErrorBoundary>[0]) {
  return <RouteErrorBoundary {...props} fullPage />;
}
export const Route = createRootRoute({
  component: RootLayout,
  errorComponent: RootErrorBoundary,
});
