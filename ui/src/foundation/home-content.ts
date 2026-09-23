import type { CommandsDrawerConfig } from "./drawer-context";

export function shellQuote(value: string) {
  return "'" + value.replaceAll("'", "'\"'\"'") + "'";
}

export function agentSetupPrompt(endpoint: string, skillURL: string) {
  return `Help me get started with Agent Filesystem (AFS).

Read the AFS skill at ${skillURL}. Check that the afs CLI is installed, then run afs auth login --url ${shellQuote(endpoint)}. If authentication is required, have me approve the browser request after matching its code to the terminal. Never include credentials in chat.

List my workspaces and help me choose one, or create a new one. Mount it in an empty local directory so you can work with ordinary files. Before major changes, verify sync and create a checkpoint. Explain each step before making changes.`;
}

export function cliQuickstart(endpoint: string) {
  return `# Connect to this AFS server
# Approve in your browser after matching the terminal code.
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
      "Browser login creates a separate, revocable key for your CLI. For automation, create a named key on the API Keys page. Keys have administrator access to every connection.",
    sections: [
      {
        title: "Connect securely",
        description:
          "Run login to open your browser. Match the code shown in your terminal, then approve the connection. The CLI saves its own key automatically; your browser stays signed in for future connections.",
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
