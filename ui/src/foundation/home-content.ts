import type { CommandsDrawerConfig } from "./drawer-context";

export function shellQuote(value: string) {
  return "'" + value.replaceAll("'", "'\"'\"'") + "'";
}

export function agentSetupPrompt(endpoint: string, skillURL: string) {
  return `Help me get started with Agent Filesystem (AFS).

Read the AFS skill at ${skillURL}. Check that the afs CLI is installed, then connect to ${endpoint}. If authentication is required, ask me to configure AFS_CONTROL_PLANE_TOKEN locally with an API key or team token; never include credentials in chat.

List my workspaces and help me choose one, or create a new one. Mount it in an empty local directory so you can work with ordinary files. Before major changes, verify sync and create a checkpoint. Explain each step before making changes.`;
}

export function cliQuickstart(endpoint: string) {
  return `# Connect to this AFS server
# Set AFS_CONTROL_PLANE_TOKEN if required.
afs auth login --url ${shellQuote(endpoint)}

# Create and mount your first workspace
afs create my-workspace
afs mount my-workspace ~/afs/my-workspace

# Check your local mounts
afs status`;
}

export function apiAccessGuide(endpoint: string): CommandsDrawerConfig {
  return {
    title: "API access & keys",
    subline:
      "Create a named key on the API Keys page for each developer, agent, or integration. Keys have administrator access to every connection. The team token remains available for administration and recovery.",
    sections: [
      {
        title: "Connect securely",
        description:
          "Set AFS_CONTROL_PLANE_TOKEN to your API key or team token locally before login. The CLI verifies access and saves the managed connection. Never paste credentials into chat.",
        command: `afs auth login --url ${shellQuote(endpoint)}`,
      },
      {
        title: "Check your connection",
        description:
          "Show your active endpoint and authentication status without exposing the token.",
        command: "afs auth status",
      },
    ],
  };
}
