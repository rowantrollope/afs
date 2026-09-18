import type { IconType } from "@redis-ui/icons";
import {
  BellIcon,
  BookOpenIcon,
  DatabaseIcon,
  FoldersIcon,
  PieChartIcon,
} from "../components/lucide-icons";

export type SidebarPanelId = "root" | "workspaces";

export type NavigationRouteItem = {
  kind: "route";
  label: string;
  path: string;
  icon: IconType;
  title?: string;
  adminOnly?: boolean;
};

export type NavigationPanelItem = {
  kind: "panel";
  label: string;
  icon: IconType;
  panelId: Exclude<SidebarPanelId, "root">;
  children: ReadonlyArray<NavigationRouteItem>;
};

export type NavigationItem = NavigationRouteItem | NavigationPanelItem;
export type NavigationTitleParts = {
  section?: string;
  page: string;
  subtitle?: string;
};

export const navigationItems: ReadonlyArray<NavigationRouteItem> = [
  { kind: "route", label: "Monitor", path: "/", icon: PieChartIcon },
  {
    kind: "route",
    label: "Workspaces",
    path: "/workspaces",
    icon: FoldersIcon,
  },
  { kind: "route", label: "Redis", path: "/databases", icon: DatabaseIcon },
  { kind: "route", label: "History", path: "/activity", icon: BellIcon },
];
export const bottomNavigationItems: ReadonlyArray<NavigationRouteItem> = [
  { kind: "route", label: "Docs", path: "/docs", icon: BookOpenIcon },
];

function isPathMatch(pathname: string, path: string) {
  if (path === "/") {
    return pathname === "/";
  }

  return pathname.startsWith(path);
}

export function isNavigationItemActive(item: NavigationItem, pathname: string) {
  if (item.kind === "route") {
    return isPathMatch(pathname, item.path);
  }

  return item.children.some((child) => isPathMatch(pathname, child.path));
}

export function resolveNavigationTitleParts(
  pathname: string,
): NavigationTitleParts {
  if (pathname.startsWith("/workspaces"))
    return {
      page: "Workspaces",
      subtitle: "Manage file trees, checkpoints, and workspace history.",
    };
  if (pathname.startsWith("/databases"))
    return {
      page: "Redis",
      subtitle: "Storage and health of the configured Redis backend.",
    };
  if (pathname.startsWith("/activity"))
    return {
      page: "History",
      subtitle: "Track workspace lifecycle and file changes.",
    };
  if (pathname.startsWith("/settings"))
    return { page: "Settings", subtitle: "Customize the console appearance." };
  if (pathname.startsWith("/docs")) return { page: "Documentation" };
  return {
    page: "Monitor",
    subtitle: "What your CLI and agents are doing right now.",
  };
}

export function getNavigationPanel(
  _panelId: Exclude<SidebarPanelId, "root">,
): NavigationPanelItem | null {
  return null;
}
