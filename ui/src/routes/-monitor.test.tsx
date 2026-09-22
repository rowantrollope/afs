import { cleanup, render, screen, within } from "@testing-library/react";
import type { HTMLAttributes } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { AFSAgentSession } from "../foundation/types/afs";
import { Route } from "./monitor";

const { useScopedAgents, topologyAgents } = vi.hoisted(() => ({
  useScopedAgents: vi.fn(),
  topologyAgents: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: object) => ({ options }),
  useNavigate: () => vi.fn(),
}));
vi.mock("@redis-ui/components", () => ({ Loader: () => <div>Loading</div> }));
vi.mock("../components/afs-kit", () => ({
  NoticeBody: (props: HTMLAttributes<HTMLDivElement>) => <div {...props} />,
  NoticeCard: (props: HTMLAttributes<HTMLDivElement>) => <div {...props} />,
  PageStack: (props: HTMLAttributes<HTMLDivElement>) => <div {...props} />,
}));
vi.mock("../components/live-topology-card", () => ({
  LiveTopologyCard: ({ agents }: { agents: AFSAgentSession[] }) => {
    topologyAgents(agents);
    return <div data-testid="live-topology" />;
  },
}));
vi.mock("../foundation/tables/activity-table", () => ({
  ActivityTable: () => <div />,
}));
vi.mock("../foundation/database-scope", () => ({
  useScopedAgents,
  useScopedWorkspaceSummaries: () => ({ data: [], isLoading: false }),
  useScopedActivity: () => ({ data: [], isLoading: false }),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function session(state: string): AFSAgentSession {
  return {
    sessionId: `${state}-session`,
    workspaceId: "workspace",
    workspaceName: "Workspace",
    agentName: `${state} mount`,
    clientKind: "sync",
    afsVersion: "",
    hostname: "host",
    operatingSystem: "linux",
    localPath: "/tmp/workspace",
    readonly: false,
    state,
    startedAt: "2000-01-01T00:00:00Z",
    lastSeenAt: "2000-01-01T00:00:00Z",
    leaseExpiresAt: "",
  };
}

function setAgents(agents: AFSAgentSession[]) {
  useScopedAgents.mockReturnValue({ data: agents, isLoading: false });
}

function expectSessionCount(count: number) {
  const label = screen.getByText(/^active sessions?$/);
  expect(within(label.parentElement!).getByText(String(count))).toBeVisible();
}

test("counts and displays connected mounts without historical sessions", () => {
  const connected = [session("active"), session("starting"), session("syncing")];
  setAgents([...connected, session("closed"), session("stale"), session("unknown")]);
  const Page = Route.options.component!;
  render(<Page />);

  expectSessionCount(3);
  expect(screen.getByRole("heading", { name: "ACTIVE AGENTS [3]" })).toBeVisible();
  // An older last-seen label alone must not hide an otherwise connected mount.
  expect(screen.getByText("active mount")).toBeVisible();
  expect(screen.getByText("starting mount")).toBeVisible();
  expect(screen.getByText("syncing mount")).toBeVisible();
  expect(screen.queryByText("closed mount")).not.toBeInTheDocument();
  expect(screen.queryByText("stale mount")).not.toBeInTheDocument();
  expect(screen.queryByText("unknown mount")).not.toBeInTheDocument();
  expect(topologyAgents).toHaveBeenLastCalledWith(connected);
});

test("removes an unmounted session from the Monitor count, panel, and topology", () => {
  const mounted = session("active");
  setAgents([mounted]);
  const Page = Route.options.component!;
  const { rerender } = render(<Page />);
  expectSessionCount(1);
  expect(screen.getByRole("heading", { name: "ACTIVE AGENTS [1]" })).toBeVisible();
  expect(topologyAgents).toHaveBeenLastCalledWith([mounted]);

  setAgents([{ ...mounted, state: "closed" }]);
  rerender(<Page />);

  expectSessionCount(0);
  expect(screen.queryByRole("heading", { name: /ACTIVE AGENTS/ })).not.toBeInTheDocument();
  expect(screen.queryByText("active mount")).not.toBeInTheDocument();
  expect(topologyAgents).toHaveBeenLastCalledWith([]);
});
