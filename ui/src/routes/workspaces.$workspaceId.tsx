import { Button, Loader } from "@redis-ui/components";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import styled from "styled-components";
import { z } from "zod";
import {
  DialogActions,
  DialogBody,
  DialogCard,
  DialogCloseButton,
  DialogError,
  DialogHeader,
  DialogOverlay,
  DialogTitle,
  EmptyState,
  NoticeBody,
  NoticeCard,
  NoticeTitle,
  PageStack,
  TabButton,
  Tabs,
} from "../components/afs-kit";
import { SurfaceCard } from "../components/card-shell";
import { ConnectAgentBanner } from "../components/connect-agent-banner";
import { ForkWorkspaceDialog } from "../features/workspaces/ForkWorkspaceDialog";
import { useDatabaseScope } from "../foundation/database-scope";
import type { CommandsDrawerConfig } from "../foundation/drawer-context";
import { useDrawerCommands } from "../foundation/drawer-context";
import {
  useDeleteWorkspaceMutation,
  useUpdateWorkspaceMutation,
  useWorkspace,
  workspaceQueryOptions,
} from "../foundation/hooks/use-afs";
import { queryClient } from "../foundation/query-client";
import type {
  AFSWorkspaceDetail,
  AFSWorkspaceView,
} from "../foundation/types/afs";
import { resolveWorkspaceBrowserView } from "../foundation/workspace-browser-views";
import { displayWorkspaceName } from "../foundation/workspace-display";
import type { StudioTab } from "../foundation/workspace-tabs";
import {
  normalizeStudioTab,
  studioTabSchema,
} from "../foundation/workspace-tabs";
import { BrowseTab } from "./workspace-studio/-browse-tab";
import { ChangesTab } from "./workspace-studio/-changes-tab";
import { CheckpointsTab } from "./workspace-studio/-checkpoints-tab";
import { SettingsTab } from "./workspace-studio/-settings-tab";

const workspaceStudioSearchSchema = z.object({
  tab: studioTabSchema.optional(),
  databaseId: z.string().optional(),
});

export const Route = createFileRoute("/workspaces/$workspaceId")({
  validateSearch: workspaceStudioSearchSchema,
  loaderDeps: ({ search }) => ({ databaseId: search.databaseId }),
  loader: ({ params, deps }) =>
    queryClient.ensureQueryData({
      ...workspaceQueryOptions(deps.databaseId ?? null, params.workspaceId),
      revalidateIfStale: true,
    }),
  component: WorkspaceStudioPage,
});

function WorkspaceStudioPage() {
  const navigate = useNavigate();
  const { workspaceId } = Route.useParams();
  const search = Route.useSearch();
  const databaseId = search.databaseId ?? null;
  const { unavailableDatabases } = useDatabaseScope();
  const workspaceQuery = useWorkspace(databaseId, workspaceId);
  const deleteWorkspace = useDeleteWorkspaceMutation();
  const updateWorkspace = useUpdateWorkspaceMutation();

  const [browserView, setBrowserView] = useState<AFSWorkspaceView>("head");
  const [bannerDismissed, setBannerDismissed] = useState(false);
  const [userRequestedBanner, setUserRequestedBanner] = useState(false);
  const [forkDialogOpen, setForkDialogOpen] = useState(false);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [isRedirectingAfterDelete, setIsRedirectingAfterDelete] =
    useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const workspace = workspaceQuery.data;
  const tab = normalizeStudioTab(search.tab);
  const drawerConfig = useMemo<CommandsDrawerConfig | null>(() => {
    if (workspace == null) {
      return null;
    }
    return workspaceCommandsFor(workspace, tab);
  }, [tab, workspace]);
  const hasAgents = (workspace?.agents.length ?? 0) > 0;
  const showBanner =
    workspace != null && !bannerDismissed && userRequestedBanner;
  useDrawerCommands(drawerConfig);

  useEffect(() => {
    if (workspace == null) {
      setBrowserView("head");
      return;
    }

    setBrowserView((currentView) =>
      resolveWorkspaceBrowserView(workspace, currentView),
    );
  }, [workspace]);

  function setStudioTab(nextTab: StudioTab) {
    void navigate({
      to: "/workspaces/$workspaceId",
      params: { workspaceId },
      search: {
        ...(search.databaseId ? { databaseId: search.databaseId } : {}),
        ...(nextTab === "browse" ? {} : { tab: nextTab }),
      },
      replace: true,
    });
  }

  function deleteCurrentWorkspace() {
    if (workspace == null) {
      return;
    }
    setDeleteDialogOpen(true);
  }

  async function confirmDeleteCurrentWorkspace() {
    if (workspace == null) {
      return;
    }

    try {
      setDeleteDialogOpen(false);
      setIsRedirectingAfterDelete(true);
      await deleteWorkspace.mutateAsync({
        databaseId: databaseId ?? undefined,
        workspaceId,
      });
      await navigate({ to: "/workspaces", replace: true });
    } catch {
      setIsRedirectingAfterDelete(false);
      setDeleteDialogOpen(true);
      // keep the dialog open and show the mutation error below
    }
  }

  async function saveWorkspaceSettings(input: {
    name: string;
    description: string;
  }) {
    if (workspace == null) {
      return;
    }

    try {
      setSaveError(null);
      await updateWorkspace.mutateAsync({
        databaseId: databaseId ?? undefined,
        workspaceId,
        name: input.name,
        description: input.description,
        cloudAccount: workspace.cloudAccount,
        databaseName: workspace.databaseName,
        region: workspace.region,
      });
    } catch (error) {
      setSaveError(
        error instanceof Error
          ? error.message
          : "Unable to save workspace changes.",
      );
      throw error;
    }
  }

  if (
    workspaceQuery.isLoading ||
    isRedirectingAfterDelete ||
    (workspaceQuery.isError && deleteWorkspace.isSuccess)
  ) {
    return <Loader data-testid="loader--spinner" />;
  }

  if (workspaceQuery.isError) {
    return (
      <PageStack>
        <EmptyState role="alert">
          <NoticeTitle>Workspace unavailable</NoticeTitle>
          <NoticeBody>
            {workspaceQuery.error instanceof Error
              ? workspaceQuery.error.message
              : "This workspace could not be loaded right now."}
          </NoticeBody>
          {unavailableDatabases.length > 0 ? (
            <NoticeBody>
              Disconnected databases:{" "}
              {unavailableDatabases
                .map(
                  (database) => database.displayName || database.databaseName,
                )
                .join(", ")}
              .
            </NoticeBody>
          ) : null}
        </EmptyState>
      </PageStack>
    );
  }

  if (workspace == null) {
    if (deleteWorkspace.isPending || deleteWorkspace.isSuccess) {
      return <Loader data-testid="loader--spinner" />;
    }
    return (
      <PageStack>
        <EmptyState role="alert">
          <NoticeTitle>Workspace not found</NoticeTitle>
          <NoticeBody>
            This workspace may have been deleted or is unavailable in the
            selected database.
          </NoticeBody>
          <Button
            size="medium"
            variant="secondary-fill"
            onClick={() => {
              void navigate({ to: "/workspaces" });
            }}
          >
            Back to Workspaces
          </Button>
        </EmptyState>
      </PageStack>
    );
  }

  const workspaceLabel = displayWorkspaceName(workspace.name);

  return (
    <PageStack>
      {workspace.liveRootAvailable === false && (
        <NoticeCard $tone="warning" role="status">
          <NoticeTitle>Live workspace unavailable</NoticeTitle>
          <NoticeBody>
            {workspace.unavailableReason ||
              "The live workspace tree is missing. Browse a saved checkpoint or restore one to recover it."}
          </NoticeBody>
        </NoticeCard>
      )}
      {unavailableDatabases.length > 0 ? (
        <NoticeCard $tone="warning" role="status">
          <NoticeTitle>Redis is unavailable</NoticeTitle>
          <NoticeBody>
            The configured Redis backend cannot be reached. Workspace data may
            be incomplete.
          </NoticeBody>
        </NoticeCard>
      ) : null}

      {showBanner ? (
        <ConnectAgentBanner
          workspaceId={workspaceId}
          workspaceName={workspace.name}
          workspaceLabel={workspaceLabel}
          agentConnected={hasAgents}
          onDismiss={() => {
            setBannerDismissed(true);
            setUserRequestedBanner(false);
          }}
        />
      ) : null}

      <StudioNavRow>
        <BreadcrumbGroup>
          <BreadcrumbButton
            type="button"
            onClick={() => {
              void navigate({ to: "/workspaces" });
            }}
          >
            <BackArrow aria-hidden>&#8592;</BackArrow>
            Back to Workspaces
          </BreadcrumbButton>
          <BreadcrumbSeparator>/</BreadcrumbSeparator>
          <BreadcrumbCurrent>{workspaceLabel}</BreadcrumbCurrent>
        </BreadcrumbGroup>
        <StudioActions>
          <Button
            size="medium"
            variant="secondary-fill"
            onClick={() => setForkDialogOpen(true)}
          >
            Fork workspace
          </Button>
          {hasAgents ? (
            <ViewAgentsButton
              variant="secondary-fill"
              size="large"
              onClick={() => {
                void navigate({
                  to: "/",
                });
              }}
            >
              View agents
            </ViewAgentsButton>
          ) : (
            <ConnectAgentButton
              variant="secondary-fill"
              size="large"
              onClick={() => {
                setUserRequestedBanner(true);
                setBannerDismissed(false);
              }}
            >
              Connect agent
            </ConnectAgentButton>
          )}
        </StudioActions>
      </StudioNavRow>

      <StudioCard>
        <DetailName>{workspaceLabel}</DetailName>

        <Tabs>
          <TabButton
            $active={tab === "browse"}
            onClick={() => setStudioTab("browse")}
          >
            Browse
          </TabButton>
          <TabButton
            $active={tab === "changes"}
            onClick={() => setStudioTab("changes")}
          >
            History
          </TabButton>
          <TabButton
            $active={tab === "checkpoints"}
            onClick={() => setStudioTab("checkpoints")}
          >
            Checkpoints
          </TabButton>
          <TabButton
            $active={tab === "settings"}
            onClick={() => setStudioTab("settings")}
          >
            Settings
          </TabButton>
        </Tabs>

        <StudioBody>
          {tab === "browse" ? (
            <BrowseTab
              workspace={workspace}
              browserView={browserView}
              onBrowserViewChange={setBrowserView}
            />
          ) : null}

          {tab === "checkpoints" ? (
            <CheckpointsTab
              workspace={workspace}
              onBrowserViewChange={setBrowserView}
              onTabChange={setStudioTab}
            />
          ) : null}

          {tab === "changes" ? (
            <ChangesTab
              databaseId={workspace.databaseId}
              workspaceId={workspaceId}
              editable={workspace.capabilities.editWorkingCopy === true}
            />
          ) : null}

          {tab === "settings" ? (
            <SettingsTab
              workspace={workspace}
              onSave={saveWorkspaceSettings}
              isSaving={updateWorkspace.isPending}
              saveError={saveError}
              onDelete={deleteCurrentWorkspace}
              isDeleting={deleteWorkspace.isPending}
            />
          ) : null}
        </StudioBody>
      </StudioCard>

      <ForkWorkspaceDialog
        workspace={workspace}
        open={forkDialogOpen}
        onClose={() => setForkDialogOpen(false)}
      />
      {deleteDialogOpen ? (
        <DialogOverlay
          role="dialog"
          aria-modal="true"
          aria-labelledby="delete-workspace-dialog-title"
          onClick={() => {
            if (!deleteWorkspace.isPending) {
              setDeleteDialogOpen(false);
            }
          }}
        >
          <ConfirmCard onClick={(event) => event.stopPropagation()}>
            <DialogHeader>
              <div>
                <DialogTitle id="delete-workspace-dialog-title">
                  Delete this workspace?
                </DialogTitle>
                <DialogBody>
                  Delete <strong>{workspaceLabel}</strong> and remove it from
                  the workspace registry. This action cannot be undone.
                </DialogBody>
              </div>
              <DialogCloseButton
                type="button"
                aria-label="Close"
                onClick={() => {
                  if (!deleteWorkspace.isPending) {
                    setDeleteDialogOpen(false);
                  }
                }}
              >
                ×
              </DialogCloseButton>
            </DialogHeader>

            {deleteWorkspace.error instanceof Error ? (
              <DialogError role="alert">
                {deleteWorkspace.error.message}
              </DialogError>
            ) : null}

            <DialogActions
              style={{ justifyContent: "flex-end", marginTop: 20 }}
            >
              <Button
                variant="secondary-fill"
                size="medium"
                onClick={() => setDeleteDialogOpen(false)}
                disabled={deleteWorkspace.isPending}
              >
                Cancel
              </Button>
              <DeleteConfirmButton
                size="medium"
                onClick={() => void confirmDeleteCurrentWorkspace()}
                disabled={deleteWorkspace.isPending}
              >
                {deleteWorkspace.isPending ? "Deleting..." : "Delete workspace"}
              </DeleteConfirmButton>
            </DialogActions>
          </ConfirmCard>
        </DialogOverlay>
      ) : null}
    </PageStack>
  );
}

const StudioNavRow = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-height: 24px;

  @media (max-width: 1040px) {
    align-items: flex-start;
    flex-wrap: wrap;
  }
`;

const StudioActions = styled.div`
  display: flex;
  align-items: center;
  gap: 12px;

  @media (max-width: 720px) {
    width: 100%;
    justify-content: flex-end;
    flex-wrap: wrap;
  }
`;

const BreadcrumbGroup = styled.div`
  display: flex;
  align-items: center;
  gap: 10px;
`;

const BreadcrumbButton = styled.button`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: none;
  background: transparent;
  padding: 0;
  color: var(--afs-muted);
  font: inherit;
  font-size: 14px;
  font-weight: 400;
  cursor: pointer;

  &:hover {
    color: var(--afs-ink);
    text-decoration: underline;
  }
`;

const BackArrow = styled.span`
  font-size: 16px;
  line-height: 1;
`;

const BreadcrumbSeparator = styled.span`
  color: var(--afs-muted);
  font-size: 14px;
`;

const BreadcrumbCurrent = styled.span`
  color: var(--afs-ink);
  font-size: 14px;
  font-weight: 700;
`;

const StudioCard = styled(SurfaceCard)`
  display: flex;
  flex-direction: column;
  gap: 24px;
  min-height: calc(100vh - 210px);
  padding: 22px;
  overflow: hidden;

  @media (max-width: 720px) {
    padding: 16px;
  }
`;

const DetailName = styled.h1`
  margin: 0;
  color: var(--afs-ink);
  font-size: 28px;
  font-weight: 700;
  line-height: 1.18;
  letter-spacing: 0;
`;

const StudioBody = styled.div`
  flex: 1 1 auto;
  min-height: 0;
`;

const ViewAgentsButton = styled(Button)`
  && {
    white-space: nowrap;
    box-shadow: none;
  }
`;

const ConnectAgentButton = styled(Button)`
  && {
    white-space: nowrap;
    box-shadow: none;
  }
`;

const ConfirmCard = styled(DialogCard)`
  max-width: 540px;
`;

const DeleteConfirmButton = styled(Button)`
  && {
    background: ${({ theme }) => theme.semantic.color.background.danger500};
    color: ${({ theme }) => theme.semantic.color.text.inverse};
    box-shadow: none;
  }

  &&:hover:not(:disabled),
  &&:focus-visible:not(:disabled) {
    background: ${({ theme }) => theme.semantic.color.background.danger600};
    color: ${({ theme }) => theme.semantic.color.text.inverse};
    box-shadow: none;
  }
`;

type WorkspaceCommandSection = CommandsDrawerConfig["sections"][number] & {
  tab: StudioTab;
};

function workspaceCommandsFor(
  workspace: AFSWorkspaceDetail,
  activeTab: StudioTab,
): CommandsDrawerConfig {
  const name = "'" + workspace.name.replaceAll("'", "'\"'\"'") + "'";
  const sections: WorkspaceCommandSection[] = [
    {
      tab: "browse",
      title: "Show workspace",
      description: "Inspect this workspace's content tree.",
      command: `afs info ${name}`,
    },
    {
      tab: "changes",
      title: "Review history",
      description:
        "Inspect versions of a file; replace README.md with its path.",
      command: `afs --json history list ${name} README.md`,
    },
    {
      tab: "checkpoints",
      title: "List checkpoints",
      description: "See saved checkpoints for this workspace.",
      command: `afs --json cp list ${name}`,
    },
    {
      tab: "settings",
      title: "Inspect settings",
      description: "Fetch workspace details and capabilities.",
      command: `afs --json info ${name}`,
    },
  ];

  const orderedSections = [
    ...sections.filter((section) => section.tab === activeTab),
    ...sections.filter((section) => section.tab !== activeTab),
  ].map(({ tab: _tab, ...section }) => section);

  return {
    title: `Work with ${displayWorkspaceName(workspace.name)}`,
    subline: "Workspace CLI commands for the current view.",
    sections: orderedSections,
  };
}
