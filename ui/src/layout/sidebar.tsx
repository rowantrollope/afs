import { SideBar } from "@redis-ui/components";
import {
  ChevronDownIcon,
  DoubleChevronLeftIcon,
  DoubleChevronRightIcon,
} from "@redis-ui/icons";
import {
  RedisLogoDarkFullIcon,
  RedisLogoDarkMinIcon,
} from "@redis-ui/icons/multicolor";
import { queryOptions, useQuery } from "@tanstack/react-query";
import { useLocation, useNavigate, useRouter } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { ThemeModeToggle } from "../components/theme-mode-toggle";
import { afsApi } from "../foundation/api/afs";
import { useAuthSession } from "../foundation/auth-context";
import { useDatabaseScope } from "../foundation/database-scope";
import type { NavigationRouteItem } from "./navigation-items";
import {
  bottomNavigationItems,
  isNavigationItemActive,
  navigationItems,
} from "./navigation-items";
import * as S from "./sidebar.styles";

const SIDEBAR_LOCALSTORAGE_KEY = "afs_sidebar_open";

function readInitialSidebarState() {
  const stored = localStorage.getItem(SIDEBAR_LOCALSTORAGE_KEY);
  if (stored === null) return true;

  try {
    return JSON.parse(stored) as boolean;
  } catch {
    localStorage.removeItem(SIDEBAR_LOCALSTORAGE_KEY);
    return true;
  }
}

function profileInitials(displayName: string) {
  const parts = displayName.trim().split(/\s+/).filter(Boolean);
  const initials =
    parts.length > 1 ? `${parts[0][0]}${parts[1][0]}` : parts[0]?.slice(0, 2);
  return (initials || "AF").toUpperCase();
}

/** Routes that remain active even when no databases are configured. */
const ALWAYS_ENABLED_PATHS = new Set(["/", "/monitor", "/docs", "/api-keys"]);

const serverVersionQueryOptions = () =>
  queryOptions({
    queryKey: ["afs", "server", "version"],
    queryFn: () => afsApi.getServerVersion(),
    staleTime: Infinity,
    gcTime: Infinity,
    retry: 0,
  });

export function AppSidebar() {
  const location = useLocation();
  const navigate = useNavigate();
  const router = useRouter();
  const auth = useAuthSession();
  const { databases, isLoading } = useDatabaseScope();

  const [isExpanded, setIsExpanded] = useState(readInitialSidebarState);
  const [isProfileMenuOpen, setIsProfileMenuOpen] = useState(false);
  const profileMenuRef = useRef<HTMLDivElement | null>(null);
  const profileLabel = auth.displayName;
  const avatarLabel = auth.supportsAccountAuth ? auth.displayName : "AFS";

  // Surface the control-plane version in the footer so operators can tell at
  // a glance which build is serving the UI. One fetch per session is plenty;
  // version doesn't change without a server restart.
  const serverVersion = useQuery(serverVersionQueryOptions());

  const isEmpty = !isLoading && databases.length === 0;

  useEffect(() => {
    localStorage.setItem(SIDEBAR_LOCALSTORAGE_KEY, JSON.stringify(isExpanded));
  }, [isExpanded]);

  useEffect(() => {
    const handleResize = () => {
      if (window.innerWidth < 1280) {
        setIsExpanded(false);
      }
    };

    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);

  useEffect(() => {
    const handlePointerDown = (event: MouseEvent) => {
      if (profileMenuRef.current?.contains(event.target as Node)) {
        return;
      }
      setIsProfileMenuOpen(false);
    };

    if (!isProfileMenuOpen) {
      return;
    }

    window.addEventListener("mousedown", handlePointerDown);
    return () => window.removeEventListener("mousedown", handlePointerDown);
  }, [isProfileMenuOpen]);

  const handleNavigate = (path: string) => void navigate({ to: path });
  const handlePrefetch = (path: string) => {
    void router.preloadRoute({ to: path }).catch(() => undefined);
  };
  const renderRouteItem = (item: NavigationRouteItem) => {
    const disabled = isEmpty && !ALWAYS_ENABLED_PATHS.has(item.path);
    const active = isNavigationItemActive(item, location.pathname);
    return (
      <S.NavItemWrapper key={item.path} $active={active} $disabled={disabled}>
        <SideBar.Item
          isActive={active}
          data-afs-nav-item
          data-afs-nav-active={active ? "true" : "false"}
          tooltipProps={{
            text: disabled ? `${item.label} (Redis unavailable)` : item.label,
            placement: "right",
          }}
          onMouseEnter={disabled ? undefined : () => handlePrefetch(item.path)}
          onFocus={disabled ? undefined : () => handlePrefetch(item.path)}
          onClick={disabled ? undefined : () => handleNavigate(item.path)}
        >
          <SideBar.Item.Icon icon={item.icon} aria-label={item.label} />
          <SideBar.Item.Text>{item.label}</SideBar.Item.Text>
        </SideBar.Item>
      </S.NavItemWrapper>
    );
  };

  return (
    <S.SidebarContainer>
      <SideBar isExpanded={isExpanded}>
        <S.CenterSidebarHeader onToggle={() => setIsExpanded((prev) => !prev)}>
          {isExpanded ? (
            <S.LogoWithName>
              <S.LogoWrapper>
                <RedisLogoDarkFullIcon />
              </S.LogoWrapper>
              <S.ProductName>Agent Filesystem</S.ProductName>
            </S.LogoWithName>
          ) : (
            <S.CollapsedLogoWrapper>
              <RedisLogoDarkMinIcon customSize="28px" />
            </S.CollapsedLogoWrapper>
          )}
          <S.HeaderToggleIcon $isExpanded={isExpanded} aria-hidden="true">
            {isExpanded ? (
              <DoubleChevronLeftIcon customSize="14px" />
            ) : (
              <DoubleChevronRightIcon customSize="14px" />
            )}
          </S.HeaderToggleIcon>
        </S.CenterSidebarHeader>

        <SideBar.ScrollContainer>
          <SideBar.ItemsContainer>
            {navigationItems.map(renderRouteItem)}
          </SideBar.ItemsContainer>

          <SideBar.Split />
          <SideBar.Divider fullWidth />

          <SideBar.ItemsContainer>
            {bottomNavigationItems.map(renderRouteItem)}
          </SideBar.ItemsContainer>
        </SideBar.ScrollContainer>

        <SideBar.Footer>
          <>
            <SideBar.Divider fullWidth />
            <S.DarkModeRow $isExpanded={isExpanded}>
              <ThemeModeToggle compact={!isExpanded} />
            </S.DarkModeRow>
            <S.ProfileMenuContainer
              ref={profileMenuRef}
              $isExpanded={isExpanded}
            >
              <S.ProfileButton
                type="button"
                onClick={() => setIsProfileMenuOpen((open) => !open)}
                aria-expanded={isProfileMenuOpen}
                aria-haspopup="menu"
                title={auth.displayName}
                $isExpanded={isExpanded}
              >
                <S.ProfileAvatar>
                  {profileInitials(avatarLabel)}
                </S.ProfileAvatar>
                {isExpanded ? (
                  <S.ProfileTextGroup>
                    <S.ProfileName>{profileLabel}</S.ProfileName>
                    {auth.secondaryLabel ? (
                      <S.ProfileMeta>{auth.secondaryLabel}</S.ProfileMeta>
                    ) : null}
                  </S.ProfileTextGroup>
                ) : null}
                <S.ProfileChevron
                  $isOpen={isProfileMenuOpen}
                  $isExpanded={isExpanded}
                >
                  <ChevronDownIcon customSize="14px" />
                </S.ProfileChevron>
              </S.ProfileButton>
              {isProfileMenuOpen ? (
                <S.ProfileDropdown $isExpanded={isExpanded} role="menu">
                  <S.ProfileMenuItem
                    type="button"
                    role="menuitem"
                    onClick={() => {
                      setIsProfileMenuOpen(false);
                      void navigate({ to: "/settings" });
                    }}
                  >
                    Settings
                  </S.ProfileMenuItem>
                  {auth.config.enabled && (
                    <S.ProfileMenuItem
                      type="button"
                      role="menuitem"
                      onClick={auth.signOut}
                    >
                      Disconnect
                    </S.ProfileMenuItem>
                  )}
                </S.ProfileDropdown>
              ) : null}
            </S.ProfileMenuContainer>
            <SideBar.Footer.MetaData>
              <SideBar.Footer.Text>&copy; 2026 Redis</SideBar.Footer.Text>
              {serverVersion.data?.version && (
                <SideBar.Footer.Text
                  title={
                    serverVersion.data.commit
                      ? `commit ${serverVersion.data.commit}${serverVersion.data.buildDate ? ` · ${serverVersion.data.buildDate}` : ""}`
                      : undefined
                  }
                >
                  afs {serverVersion.data.version}
                </SideBar.Footer.Text>
              )}
            </SideBar.Footer.MetaData>
          </>
        </SideBar.Footer>
      </SideBar>
    </S.SidebarContainer>
  );
}
