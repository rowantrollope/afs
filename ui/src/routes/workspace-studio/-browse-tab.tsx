import { EmptyState } from "../../components/afs-kit";
import {
  getWorkspaceBrowserViewOptions,
  resolveWorkspaceBrowserView,
} from "../../foundation/workspace-browser-views";
import type {
  AFSWorkspaceDetail,
  AFSWorkspaceView,
} from "../../foundation/types/afs";
import { FilesTab } from "./-files-tab";

type Props = {
  workspace: AFSWorkspaceDetail;
  browserView: AFSWorkspaceView;
  onBrowserViewChange: (view: AFSWorkspaceView) => void;
};

export function BrowseTab({
  workspace,
  browserView,
  onBrowserViewChange,
}: Props) {
  if (getWorkspaceBrowserViewOptions(workspace).length === 0)
    return (
      <EmptyState>
        The live workspace tree is unavailable and this workspace has no saved
        checkpoints.
      </EmptyState>
    );
  return (
    <FilesTab
      workspace={workspace}
      browserView={resolveWorkspaceBrowserView(workspace, browserView)}
      onBrowserViewChange={onBrowserViewChange}
    />
  );
}
