import type { CommandsDrawerConfig } from "./drawer-context";

export function shellQuote(value: string) {
  return "'" + value.replaceAll("'", "'\"'\"'") + "'";
}

export function agentSetupPrompt(endpoint: string, skillURL: string) {
  return `Help me get started with Agent Filesystem (AFS).

Read the AFS skill at ${skillURL}. Check that the afs CLI is installed, then connect to ${endpoint}. If a team token is required, ask me to configure AFS_CONTROL_PLANE_TOKEN locally; never include credentials in chat.

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

export type Cookbook = CommandsDrawerConfig & {
  id: string;
  category: string;
  description: string;
  duration: string;
};

export const homeCookbooks: Cookbook[] = [
  {
    id: "persistent-workspace",
    category: "GET STARTED",
    title: "Give your agent a workspace",
    description:
      "A familiar folder. A persistent home for code, context, and everything your agent creates.",
    duration: "3 min",
    subline:
      "Connect with Quickstart first. Choose a new workspace name and an empty mount directory.",
    sections: [
      {
        title: "Create a file tree",
        description: "One workspace owns one tree, stored in Redis.",
        command: "afs create my-workspace",
      },
      {
        title: "Mount it for your agent",
        description:
          "Folder sync starts in the background. Wait for this command to finish before opening the directory in your agent.",
        command: "afs mount my-workspace ~/afs/my-workspace",
      },
      {
        title: "Work with ordinary files",
        description:
          "Point your agent at this directory. Pause application writes before verifying sync.",
        command:
          "cd ~/afs/my-workspace\nprintf 'Project context\\n' > CONTEXT.md\nafs sync --wait my-workspace",
      },
    ],
  },
  {
    id: "two-agents",
    category: "COLLABORATE",
    title: "Two agents. One file tree.",
    description:
      "Share working files across machines. Changes sync so each agent can pick up where another left off.",
    duration: "5 min",
    subline:
      "Connect both machines to the same control plane. Coordinate edits to the same file.",
    sections: [
      {
        title: "On machine A",
        description:
          "Create the workspace once and mount it for the first agent.",
        command:
          "afs create shared\nafs mount shared ~/agent-a\nprintf 'Hello from agent A\\n' > ~/agent-a/message.txt\nafs sync --wait shared",
      },
      {
        title: "On machine B",
        description:
          "Mount the same workspace after machine A has published, then read and write through the second directory.",
        command:
          "afs mount shared ~/agent-b\ncat ~/agent-b/message.txt\nprintf 'Hello from agent B\\n' > ~/agent-b/reply.txt\nafs sync --wait shared",
      },
      {
        title: "Back on machine A",
        description:
          "Allow folder sync to receive the reply before reading it. A sync wait verifies Redis publication, not delivery to another running client.",
        command: "cat ~/agent-a/reply.txt\nafs status",
      },
    ],
  },
  {
    id: "checkpoints",
    category: "CHECKPOINT & RECOVER",
    title: "Give experiments a save point",
    description:
      "Checkpoint your work, try a new direction, and fork a saved state into a fresh workspace.",
    duration: "4 min",
    subline:
      "A checkpoint captures published Redis state. Pause all writers and verify each writable mount first for a consistent save point.",
    sections: [
      {
        title: "Verify and save",
        description:
          "Wait for this machine’s writable mount to publish, then create a named checkpoint.",
        command:
          "afs sync --wait my-workspace\nafs checkpoint create my-workspace --name before-experiment\nafs checkpoint list my-workspace",
      },
      {
        title: "Fork a saved state",
        description:
          "Create a separate workspace from the checkpoint for your experiment.",
        command:
          "afs fork my-workspace experiment --checkpoint before-experiment\nafs mount experiment ~/afs/experiment",
      },
    ],
  },
  {
    id: "file-history",
    category: "UNDERSTAND CHANGES",
    title: "Follow a file’s story",
    description:
      "See how a file evolved, inspect a previous version, and recover work between checkpoints.",
    duration: "3 min",
    subline:
      "File history is opt-in and records changes made after it is enabled.",
    sections: [
      {
        title: "Enable file history",
        description: "Enable history before making edits you want to retain.",
        command:
          "afs history policy my-workspace --mode all --max-versions 100",
      },
      {
        title: "Capture a change",
        description:
          "With my-workspace mounted, append an example note after enabling history, then verify publication.",
        command:
          "printf 'A note for the next agent\\n' >> ~/afs/my-workspace/CONTEXT.md\nafs sync --wait my-workspace",
      },
      {
        title: "List saved versions",
        description:
          "Use a path relative to the workspace root, after syncing your changes.",
        command:
          "afs sync --wait my-workspace\nafs history list my-workspace CONTEXT.md",
      },
      {
        title: "Recover a version",
        description:
          "Export to a new local path. For a deleted file, omitting --version selects its latest recoverable version. Add --version with an ID from the list to select another version.",
        command:
          "afs history export my-workspace CONTEXT.md --to ./recovered-CONTEXT.md",
      },
    ],
  },
];

export function apiAccessGuide(endpoint: string): CommandsDrawerConfig {
  return {
    title: "API access & team token",
    subline:
      "AFS uses one shared team token configured by your server administrator. Ask them for access; individual API keys are not issued by this console.",
    sections: [
      {
        title: "Connect securely",
        description:
          "If authentication is enabled, set AFS_CONTROL_PLANE_TOKEN in your local environment before login. The CLI verifies access and saves the managed connection.",
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
