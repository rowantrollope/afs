import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { AFSAgentSession, AFSWorkspaceSummary } from "../foundation/types/afs";
import { LiveTopologyCard } from "./live-topology-card";

const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("@redis-ui/icons/multicolor", () => ({ RedisLogoDarkMinIcon: () => <svg /> }));
vi.mock("./lucide-icons", () => ({
  BotIcon: () => <svg />,
  FoldersIcon: () => <svg />,
  LaptopIcon: () => <svg />,
}));
vi.mock("../foundation/tables/agents-table", () => ({
  AgentDetailDialog: ({ agent, onOpenWorkspace }: {
    agent: AFSAgentSession;
    onOpenWorkspace: (agent: AFSAgentSession) => void;
  }) => <button onClick={() => onOpenWorkspace(agent)}>Open agent workspace</button>,
}));

afterEach(() => {
  cleanup();
  navigate.mockClear();
});

function workspace(databaseId: string): AFSWorkspaceSummary {
  return {
    id: "shared-workspace", name: databaseId, databaseId, databaseName: databaseId,
    cloudAccount: "Self-managed", redisKey: "shared-workspace", fileCount: 0,
    folderCount: 0, totalBytes: 0, checkpointCount: 0, lastCheckpointAt: "",
    updatedAt: "", region: "", source: "blank",
  };
}

function sessionFixture(databaseId: string): AFSAgentSession {
  return {
    sessionId: "shared-session", workspaceId: "shared-workspace",
    workspaceName: databaseId, databaseId, agentName: `${databaseId} agent`,
    clientKind: "sync", afsVersion: "", hostname: "host", operatingSystem: "linux",
    localPath: "/tmp/workspace", readonly: false, state: "active", startedAt: "",
    lastSeenAt: "", leaseExpiresAt: "",
  };
}

test("keeps identical workspace IDs separate when marking mounts and opening agent workspaces", () => {
  render(<LiveTopologyCard agents={[sessionFixture("primary")]} workspaces={[workspace("primary"), workspace("secondary")]} />);

  expect(screen.getByText(/1\/2 workspaces connected/)).toBeInTheDocument();
  const primaryNode = screen.getByRole("button", { name: "Open Workspace primary" });
  const secondaryNode = screen.getByRole("button", { name: "Open Workspace secondary" });
  const agentNode = screen.getByRole("button", { name: "Open details for primary agent" });
  fireEvent.mouseEnter(secondaryNode);
  expect(agentNode).toHaveAttribute("data-highlighted", "false");
  fireEvent.mouseEnter(primaryNode);
  expect(agentNode).toHaveAttribute("data-highlighted", "true");

  fireEvent.click(agentNode);
  fireEvent.click(screen.getByRole("button", { name: "Open agent workspace" }));
  expect(navigate).toHaveBeenCalledWith({
    to: "/workspaces/$workspaceId",
    params: { workspaceId: "shared-workspace" },
    search: { databaseId: "primary" },
  });
});

test("keeps identical session IDs in separate database hover scopes", () => {
  render(<LiveTopologyCard agents={[sessionFixture("primary"), sessionFixture("secondary")]} workspaces={[workspace("primary"), workspace("secondary")]} />);
  const first = screen.getByRole("button", { name: "Open details for primary agent" });
  const second = screen.getByRole("button", { name: "Open details for secondary agent" });
  fireEvent.mouseEnter(first);
  expect(first).toHaveAttribute("data-highlighted", "true");
  expect(second).toHaveAttribute("data-highlighted", "false");
  expect(screen.getByRole("button", { name: "Open Workspace primary" })).toHaveAttribute("data-highlighted", "true");
  expect(screen.getByRole("button", { name: "Open Workspace secondary" })).toHaveAttribute("data-highlighted", "false");
});
