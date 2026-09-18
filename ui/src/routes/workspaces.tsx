// /workspaces — one file tree and its checkpoints per workspace.

import { Button, Loader } from "@redis-ui/components";
import {
  createFileRoute,
  Outlet,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router";
import { useMemo, useState } from "react";
import styled from "styled-components";
import { PageStack } from "../components/afs-kit";
import { CreateWorkspaceDialog } from "../features/workspaces/CreateWorkspaceDialog";
import {
  useScopedAgents,
  useScopedWorkspaceSummaries,
} from "../foundation/database-scope";
import type { CommandsDrawerConfig } from "../foundation/drawer-context";
import { useDrawerCommands } from "../foundation/drawer-context";
import {
  agentsQueryOptions,
  workspaceSummariesQueryOptions,
} from "../foundation/hooks/use-afs";
import { queryClient } from "../foundation/query-client";
import { WorkspaceTable } from "../foundation/tables/workspace-table";
import type { AFSWorkspaceSummary } from "../foundation/types/afs";
import type { StudioTab } from "../foundation/workspace-tabs";

const WORKSPACE_COMMANDS: CommandsDrawerConfig = {
  title: "Work with workspaces",
  subline: "Common commands. Run from your shell.",
  sections: [
    {
      title: "Create",
      description: "Provision a new workspace.",
      command: "afs create my-workspace",
    },
    {
      title: "Import",
      description: "Import a local directory as a workspace.",
      command: "afs create my-workspace --from ./docs",
    },
    {
      title: "Fork",
      description: "Branch a copy for an experiment.",
      command: "afs fork my-workspace my-experiment",
    },
    {
      title: "Delete",
      description: "Tear down a workspace you no longer need.",
      command: "afs delete my-workspace",
    },
  ],
};

export const Route = createFileRoute("/workspaces")({
  loader: async () => {
    await Promise.all([
      queryClient.ensureQueryData({
        ...workspaceSummariesQueryOptions(null),
        revalidateIfStale: true,
      }),
      queryClient.ensureQueryData({
        ...agentsQueryOptions(null),
        revalidateIfStale: true,
      }),
    ]);
  },
  component: WorkspacesPage,
});

function workspaceRowKey(databaseId: string | undefined, workspaceId: string) {
  return `${databaseId ?? ""}:${workspaceId}`;
}

function WorkspacesPage() {
  const location = useLocation();
  const navigate = useNavigate();
  const router = useRouter();
  const workspacesQuery = useScopedWorkspaceSummaries();
  const agentsQuery = useScopedAgents();
  const [createWorkspaceOpen, setCreateWorkspaceOpen] = useState(false);
  // Register contextual commands so the global Help button opens this page's
  // workspace command reference. Memoized so the registration is stable.
  const drawerConfig = useMemo(() => WORKSPACE_COMMANDS, []);
  useDrawerCommands(location.pathname === "/workspaces" ? drawerConfig : null);

  if (location.pathname !== "/workspaces") {
    return <Outlet />;
  }

  if (workspacesQuery.isLoading) {
    return <Loader data-testid="loader--spinner" />;
  }

  const workspaces = workspacesQuery.data;
  const connectedAgentsByWorkspace = agentsQuery.data.reduce<
    Record<string, number>
  >((counts, session) => {
    const key = workspaceRowKey(session.databaseId, session.workspaceId);
    counts[key] = (counts[key] ?? 0) + 1;
    return counts;
  }, {});
  function openWorkspace(workspace: AFSWorkspaceSummary) {
    void navigate({
      to: "/workspaces/$workspaceId",
      params: { workspaceId: workspace.id },
      search: { databaseId: workspace.databaseId },
    });
  }

  function previewWorkspace(workspace: AFSWorkspaceSummary) {
    void router
      .preloadRoute({
        to: "/workspaces/$workspaceId",
        params: { workspaceId: workspace.id },
        search: { databaseId: workspace.databaseId },
      })
      .catch(() => undefined);
  }

  function openWorkspaceTab(workspace: AFSWorkspaceSummary, tab: StudioTab) {
    void navigate({
      to: "/workspaces/$workspaceId",
      params: { workspaceId: workspace.id },
      search: {
        databaseId: workspace.databaseId,
        ...(tab === "browse" ? {} : { tab }),
      },
    });
  }

  return (
    <WorkspacesPageStack>
      <WorkspaceTable
        rows={workspaces}
        loading={workspacesQuery.isLoading}
        error={workspacesQuery.isError}
        fillAvailableHeight
        connectedAgentsByWorkspace={connectedAgentsByWorkspace}
        onOpenWorkspace={openWorkspace}
        onPreviewWorkspace={previewWorkspace}
        onOpenWorkspaceTab={openWorkspaceTab}
        toolbarAction={
          <Button size="medium" onClick={() => setCreateWorkspaceOpen(true)}>
            Add Workspace
          </Button>
        }
        resourceLabel="workspace"
        resourcePluralLabel="workspaces"
        idLabel="workspace ID"
        // intentionally no onEditWorkspace / onDeleteWorkspace — managed via CLI.
      />
      <CreateWorkspaceDialog
        open={createWorkspaceOpen}
        onClose={() => setCreateWorkspaceOpen(false)}
        resourceLabel="workspace"
      />
    </WorkspacesPageStack>
  );
}

const WorkspacesPageStack = styled(PageStack)`
  flex: 1 1 auto;
  min-height: 0;
`;
