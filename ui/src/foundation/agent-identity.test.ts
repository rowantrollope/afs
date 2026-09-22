import { describe, expect, test } from "vitest";
import {
  agentMetadata,
  agentMetadataSummary,
  displayAgentPrimaryName,
} from "./agent-identity";
import type { AFSAgentSession } from "./types/afs";

const baseAgent: AFSAgentSession = {
  sessionId: "sess-1",
  workspaceId: "workspace-1",
  workspaceName: "Workspace",
  clientKind: "sync",
  afsVersion: "",
  hostname: "dev-host",
  operatingSystem: "darwin",
  localPath: "/work/tree",
  readonly: false,
  state: "active",
  startedAt: "2026-09-22T10:00:00Z",
  lastSeenAt: "2026-09-22T10:01:00Z",
  leaseExpiresAt: "2026-09-22T10:02:00Z",
};

describe("displayAgentPrimaryName", () => {
  test.each<[string, Partial<AFSAgentSession>, string]>([
    [
      "session name before other mount metadata",
      {
        sessionName: "  Refactor auth  ",
        agentName: "Codex",
        label: "Review worker",
        agentId: "agent-123",
        user: "maya",
      },
      "Refactor auth",
    ],
    [
      "agent name when the session name is blank",
      { sessionName: "  ", agentName: "  Codex  ", label: "Worker" },
      "Codex",
    ],
    ["label", { label: "  Review worker  ", agentId: "agent-123" }, "Review worker"],
    ["agent ID", { agentId: "  agent-123  ", user: "maya" }, "agent-123"],
    ["user", { agentId: "  ", user: "  maya  " }, "maya"],
    ["generated session ID", { user: "  ", sessionId: "  sess-123  " }, "Session: sess-123"],
    ["missing session ID", { sessionId: "  " }, "Session: unknown"],
    [
      "skip the default hostname label",
      { hostname: " dev-host ", label: "  dev-host  ", agentId: "agent-123" },
      "agent-123",
    ],
    [
      "skip duplicate hostname names before a useful label",
      { sessionName: " dev-host ", agentName: "dev-host", label: "Task worker" },
      "Task worker",
    ],
    [
      "use the session ID when all names repeat the hostname",
      { sessionName: "dev-host", agentName: "dev-host", label: "dev-host" },
      "Session: sess-1",
    ],
  ])("uses %s", (_description, overrides, expected) => {
    expect(displayAgentPrimaryName({ ...baseAgent, ...overrides })).toBe(expected);
  });
});

describe("mount metadata", () => {
  test("retains and trims all supplied informational tags", () => {
    expect(
      agentMetadata({
        ...baseAgent,
        sessionName: "  Review auth  ",
        agentName: "  Codex  ",
        label: "  Nightly worker  ",
        agentId: "  agent-123  ",
        user: "  maya  ",
        afsVersion: "  custom-agent-v2  ",
      }),
    ).toEqual([
      { label: "Session Name", value: "Review auth" },
      { label: "Agent Name", value: "Codex" },
      { label: "Label", value: "Nightly worker" },
      { label: "Agent ID", value: "agent-123" },
      { label: "User", value: "maya" },
      { label: "Agent / AFS Version", value: "custom-agent-v2" },
    ]);
  });

  test("omits missing and whitespace-only metadata", () => {
    expect(
      agentMetadata({ ...baseAgent, sessionName: "  ", user: "\t", label: "" }),
    ).toEqual([]);
    expect(agentMetadataSummary(baseAgent)).toBe("");
  });

  test("summarizes supplied metadata without repeating the primary name or host", () => {
    expect(
      agentMetadataSummary({
        ...baseAgent,
        sessionName: "  Review auth  ",
        agentName: "Codex",
        label: " Codex ",
        agentId: "agent-123",
        user: "maya",
        afsVersion: "custom-agent-v2",
      }),
    ).toBe(
      "Agent Name: Codex · Agent ID: agent-123 · User: maya · Agent / AFS Version: custom-agent-v2",
    );
    expect(
      agentMetadataSummary({ ...baseAgent, label: " dev-host ", agentId: "agent-123" }),
    ).toBe("Agent ID: agent-123");
  });
});
