import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
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
  vi.useRealTimers();
  localStorage.clear();
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

function taggedSession(): AFSAgentSession {
  return {
    ...sessionFixture("primary"),
    sessionId: "session-123",
    sessionName: "Review payments",
    agentName: "Codex",
    label: "Nightly worker",
    agentId: "agent-456",
    user: "maya",
    afsVersion: "custom-agent-v2",
    localPath: "/work/payments",
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

test("defaults to the agent ID with only the mount path shown", () => {
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);

  const node = within(screen.getByRole("button", { name: "Open details for agent-456" }));
  expect(node.getByText("agent-456")).toBeInTheDocument();
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  expect(node.queryByTitle("Session name: Review payments")).not.toBeInTheDocument();
  expect(node.queryByTitle("User: maya")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("agentId");
  expect(screen.getByRole("checkbox", { name: "Mount path" })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: "Uptime" })).not.toBeChecked();
  expect(screen.getAllByRole("checkbox").filter((checkbox) => (checkbox as HTMLInputElement).checked)).toHaveLength(1);
});

test("shows selected mount tags without repeating the primary name", () => {
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  fireEvent.change(screen.getByRole("combobox", { name: "Node label" }), { target: { value: "auto" } });
  for (const label of ["Session name", "Agent name", "Label", "Agent ID", "User", "Agent / AFS version", "Session ID"]) {
    fireEvent.click(screen.getByRole("checkbox", { name: label }));
  }
  const node = within(screen.getByRole("button", { name: "Open details for Review payments" }));
  expect(node.getByText("Review payments")).toBeInTheDocument();
  for (const detail of [
    "Agent name: Codex",
    "Label: Nightly worker",
    "Agent ID: agent-456",
    "User: maya",
    "Agent / AFS version: custom-agent-v2",
    "Session ID: session-123",
  ]) {
    expect(node.getByTitle(detail)).toBeInTheDocument();
  }
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  expect(node.queryByTitle("Session name: Review payments")).not.toBeInTheDocument();
});

test.each<[string, Partial<AFSAgentSession>, string]>([
  ["label", { label: " Legacy worker " }, "Legacy worker"],
  ["agent ID", { label: "host", agentId: " legacy-agent-123 " }, "legacy-agent-123"],
  ["user", { user: " legacy-user " }, "legacy-user"],
  ["session ID", { label: "host" }, "Session: shared-session"],
])("labels legacy metadata-only mounts using the supplied %s", (_field, metadata, label) => {
  render(<LiveTopologyCard agents={[{
    ...sessionFixture("primary"),
    agentName: undefined,
    ...metadata,
  }]} workspaces={[workspace("primary")]} />);

  expect(screen.getByRole("button", { name: `Open details for ${label}` })).toHaveTextContent(label);
});

test("Config changes the visible node label and falls back when that field is missing", () => {
  const tagged = taggedSession();
  render(<LiveTopologyCard agents={[
    tagged,
    { ...sessionFixture("primary"), sessionId: "no-user", sessionName: "Docs cleanup", user: "  " },
  ]} workspaces={[workspace("primary")]} />);

  const configButton = screen.getByRole("button", { name: "Config" });
  expect(configButton).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(configButton);
  expect(configButton).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByRole("region", { name: "Live Topology configuration" })).toBeInTheDocument();
  fireEvent.change(screen.getByRole("combobox", { name: "Node label" }), { target: { value: "user" } });
  fireEvent.click(screen.getByRole("checkbox", { name: "Session name" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "User" }));

  const namedNode = screen.getByRole("button", { name: "Open details for maya" });
  expect(namedNode).toHaveTextContent("maya");
  expect(within(namedNode).queryByTitle("User: maya")).not.toBeInTheDocument();
  expect(namedNode).toHaveAccessibleDescription(/Session name: Review payments/);
  expect(screen.getByRole("button", { name: "Open details for Docs cleanup" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Open details for agent-456" })).not.toBeInTheDocument();
  fireEvent.click(configButton);
  expect(screen.queryByRole("region", { name: "Live Topology configuration" })).not.toBeInTheDocument();
});

test("Config checkboxes hide and restore user, version, session ID, and mount path details", () => {
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  const node = within(screen.getByRole("button", { name: "Open details for agent-456" }));

  for (const [label, title] of [
    ["User", "User: maya"],
    ["Agent / AFS version", "Agent / AFS version: custom-agent-v2"],
    ["Session ID", "Session ID: session-123"],
  ]) {
    const checkbox = screen.getByRole("checkbox", { name: label });
    expect(checkbox).not.toBeChecked();
    expect(node.queryByTitle(title)).not.toBeInTheDocument();
    fireEvent.click(checkbox);
    expect(checkbox).toBeChecked();
    expect(node.getByTitle(title)).toBeInTheDocument();
    fireEvent.click(checkbox);
    expect(node.queryByTitle(title)).not.toBeInTheDocument();
  }

  const mountPath = screen.getByRole("checkbox", { name: "Mount path" });
  expect(mountPath).toBeChecked();
  fireEvent.click(mountPath);
  expect(node.queryByTitle("Mount path: /work/payments")).not.toBeInTheDocument();
  fireEvent.click(mountPath);
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
});

test("persists Config choices across remounts and restores defaults", () => {
  const props = { agents: [taggedSession()], workspaces: [workspace("primary")] };
  const { unmount } = render(<LiveTopologyCard {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  fireEvent.change(screen.getByRole("combobox", { name: "Node label" }), { target: { value: "agentName" } });
  fireEvent.click(screen.getByRole("checkbox", { name: "User" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Session ID" }));
  unmount();

  render(<LiveTopologyCard {...props} />);
  const node = within(screen.getByRole("button", { name: "Open details for Codex" }));
  expect(node.getByTitle("User: maya")).toBeInTheDocument();
  expect(node.getByTitle("Session ID: session-123")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("agentName");
  expect(screen.getByRole("checkbox", { name: "User" })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: "Session ID" })).toBeChecked();
  fireEvent.click(screen.getByRole("button", { name: "Reset defaults" }));

  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("agentId");
  expect(screen.getByRole("checkbox", { name: "User" })).not.toBeChecked();
  expect(screen.getByRole("checkbox", { name: "Session ID" })).not.toBeChecked();
  const resetNode = within(screen.getByRole("button", { name: "Open details for agent-456" }));
  expect(resetNode.queryByTitle("User: maya")).not.toBeInTheDocument();
  expect(resetNode.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  expect(resetNode.queryByTitle("Session ID: session-123")).not.toBeInTheDocument();
});

test.each([
  "invalid JSON",
  JSON.stringify({ name: "unknown-field", details: "all" }),
])("uses safe defaults for invalid saved Config: %s", (saved) => {
  localStorage.setItem("afs.topology.display.v1", saved);
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);

  const node = within(screen.getByRole("button", { name: "Open details for agent-456" }));
  expect(node.queryByTitle("User: maya")).not.toBeInTheDocument();
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  expect(node.queryByTitle("Session ID: session-123")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("agentId");
});

test("migrates the original default Config to agent ID and mount path", () => {
  localStorage.setItem("afs.topology.display.v1", JSON.stringify({
    name: "auto",
    details: ["sessionName", "agentName", "label", "agentId", "user", "afsVersion", "localPath"],
  }));
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);

  const node = within(screen.getByRole("button", { name: "Open details for agent-456" }));
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  expect(node.queryByTitle("User: maya")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("agentId");
  expect(screen.getAllByRole("checkbox").filter((checkbox) => (checkbox as HTMLInputElement).checked)).toHaveLength(1);
});

test("migrates version 2 agent-name defaults to agent ID and mount path", () => {
  localStorage.setItem("afs.topology.display.v1", JSON.stringify({
    defaultsVersion: 2,
    name: "agentName",
    details: ["localPath"],
  }));
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);

  const node = within(screen.getByRole("button", { name: "Open details for agent-456" }));
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("agentId");
  expect(screen.getAllByRole("checkbox").filter((checkbox) => (checkbox as HTMLInputElement).checked)).toHaveLength(1);
});

test("preserves version 3 Config that deliberately selects agent name and mount path", () => {
  localStorage.setItem("afs.topology.display.v1", JSON.stringify({
    defaultsVersion: 3,
    name: "agentName",
    details: ["localPath"],
  }));
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);

  const node = within(screen.getByRole("button", { name: "Open details for Codex" }));
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("agentName");
  expect(screen.getAllByRole("checkbox").filter((checkbox) => (checkbox as HTMLInputElement).checked)).toHaveLength(1);
});

test("preserves custom saved Config when defaults change", () => {
  localStorage.setItem("afs.topology.display.v1", JSON.stringify({
    name: "auto",
    details: ["user", "localPath"],
  }));
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);

  const node = within(screen.getByRole("button", { name: "Open details for Review payments" }));
  expect(node.getByTitle("User: maya")).toBeInTheDocument();
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("auto");
  expect(screen.getByRole("checkbox", { name: "User" })).toBeChecked();
});

test("preserves version 2 Config that deliberately selects the original defaults", () => {
  localStorage.setItem("afs.topology.display.v1", JSON.stringify({
    defaultsVersion: 2,
    name: "auto",
    details: ["sessionName", "agentName", "label", "agentId", "user", "afsVersion", "localPath"],
  }));
  render(<LiveTopologyCard agents={[taggedSession()]} workspaces={[workspace("primary")]} />);

  const node = within(screen.getByRole("button", { name: "Open details for Review payments" }));
  expect(node.getByTitle("Agent name: Codex")).toBeInTheDocument();
  expect(node.getByTitle("User: maya")).toBeInTheDocument();
  expect(node.getByTitle("Mount path: /work/payments")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  expect(screen.getByRole("combobox", { name: "Node label" })).toHaveValue("auto");
  expect(screen.getByRole("checkbox", { name: "User" })).toBeChecked();
});

test("updates selected uptime every second from the mount start time", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-22T10:01:05Z"));
  render(<LiveTopologyCard agents={[{
    ...taggedSession(),
    startedAt: "2026-09-22T10:00:00Z",
  }]} workspaces={[workspace("primary")]} />);
  const node = within(screen.getByRole("button", { name: "Open details for agent-456" }));
  expect(node.queryByTitle(/^Uptime:/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  const checkbox = screen.getByRole("checkbox", { name: "Uptime" });
  expect(checkbox).not.toBeChecked();
  fireEvent.click(checkbox);
  expect(node.getByTitle("Uptime: 1m 05s")).toBeInTheDocument();

  act(() => vi.advanceTimersByTime(1000));
  expect(node.getByTitle("Uptime: 1m 06s")).toBeInTheDocument();
  fireEvent.click(checkbox);
  expect(node.queryByTitle(/^Uptime:/)).not.toBeInTheDocument();
});

test.each(["", "invalid timestamp"])("omits uptime when the mount start time is unavailable: %s", (startedAt) => {
  render(<LiveTopologyCard agents={[{ ...taggedSession(), startedAt }]} workspaces={[workspace("primary")]} />);
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Uptime" }));

  const node = within(screen.getByRole("button", { name: "Open details for agent-456" }));
  expect(node.queryByTitle(/^Uptime:/)).not.toBeInTheDocument();
});

test("clamps uptime to zero when the mount start time is in the future", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-22T10:00:00Z"));
  render(<LiveTopologyCard agents={[{
    ...taggedSession(),
    startedAt: "2026-09-22T10:01:00Z",
  }]} workspaces={[workspace("primary")]} />);
  fireEvent.click(screen.getByRole("button", { name: "Config" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Uptime" }));

  const node = within(screen.getByRole("button", { name: "Open details for agent-456" }));
  expect(node.getByTitle("Uptime: 0s")).toBeInTheDocument();
});
