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

test("shows connected mounts without drawing retained session history", () => {
  const agents = ["active", "starting", "syncing", "closed", "stale", "unknown"].map((state) => ({
    ...sessionFixture(state), state, hostname: `${state}-host`,
    // An idle mount still has a live heartbeat; file activity is not presence.
    lastSeenAt: "2020-01-01T00:00:00Z",
  }));
  render(<LiveTopologyCard agents={agents} workspaces={[workspace("active"), workspace("closed")]} />);

  for (const state of ["active", "starting", "syncing"]) {
    expect(screen.getByRole("button", { name: `Open details for ${state} agent` })).toBeInTheDocument();
  }
  for (const state of ["closed", "stale", "unknown"]) {
    expect(screen.queryByRole("button", { name: `Open details for ${state} agent` })).not.toBeInTheDocument();
    expect(screen.queryByText(`${state}-host`)).not.toBeInTheDocument();
  }
  expect(screen.getByText(/3 agents connected.*3\/4 workspaces connected/)).toBeInTheDocument();
  // Catalog workspaces stay visible; only live sessions can add fallback nodes.
  expect(screen.getByRole("button", { name: "Open Workspace closed" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Open Workspace stale" })).not.toBeInTheDocument();
});

test.each(["closed", "stale"])("removes a mount and its connections when it becomes %s, and restores it on reconnect", (state) => {
  const agent = sessionFixture("primary");
  const workspaces = [workspace("primary")];
  const { container, rerender } = render(<LiveTopologyCard agents={[agent]} workspaces={workspaces} />);
  expect(container.querySelectorAll("polyline")).toHaveLength(2);

  rerender(<LiveTopologyCard agents={[{ ...agent, state }]} workspaces={workspaces} />);
  expect(screen.queryByRole("button", { name: "Open details for primary agent" })).not.toBeInTheDocument();
  expect(screen.queryByText("host")).not.toBeInTheDocument();
  expect(screen.getByText("No reported agent sessions")).toBeInTheDocument();
  expect(screen.getByText(/0 agents connected.*0\/1 workspaces connected/)).toBeInTheDocument();
  expect(container.querySelectorAll("polyline")).toHaveLength(0);

  rerender(<LiveTopologyCard agents={[agent]} workspaces={workspaces} />);
  expect(screen.getByRole("button", { name: "Open details for primary agent" })).toBeInTheDocument();
  expect(screen.getByText(/1 agent connected.*1\/1 workspaces connected/)).toBeInTheDocument();
  expect(container.querySelectorAll("polyline")).toHaveLength(2);
});
