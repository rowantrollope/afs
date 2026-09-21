import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ConnectAgentBanner } from "./connect-agent-banner";

afterEach(cleanup);

test("connects the CLI to the workspace's added database before mounting", () => {
  render(
    <ConnectAgentBanner
      workspaceId="same-name"
      workspaceName="same-name"
      databaseId="db-team"
      agentConnected={false}
      onDismiss={vi.fn()}
    />,
  );
  expect(screen.getByText(/afs auth login/).textContent).toContain(
    "/databases/db-team' &&\nafs mount 'same-name'",
  );
});

test("keeps the primary database connection URL unscoped", () => {
  render(
    <ConnectAgentBanner
      workspaceId="same-name"
      workspaceName="same-name"
      databaseId="local"
      agentConnected={false}
      onDismiss={vi.fn()}
    />,
  );
  expect(screen.getByText(/afs auth login/).textContent).not.toContain(
    "/databases/",
  );
});
