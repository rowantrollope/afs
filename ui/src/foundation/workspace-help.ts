import { controlPlaneEndpoint } from "./api/afs";
import type { CommandsDrawerConfig } from "./drawer-context";
import { shellQuote } from "./home-content";
import type { AFSWorkspaceDetail, AFSWorkspaceView } from "./types/afs";
import { workspaceCLICommand } from "./workspace-commands";
import { displayWorkspaceName } from "./workspace-display";
import type { StudioTab } from "./workspace-tabs";

export function workspaceHelpFor(
  workspace: AFSWorkspaceDetail,
  activeTab: StudioTab,
  view: AFSWorkspaceView = "head",
): CommandsDrawerConfig {
  const name = shellQuote(workspace.name);
  const endpoint = controlPlaneEndpoint(workspace.databaseId);
  const context = `Use AFS workspace ${JSON.stringify(workspace.name)} on ${endpoint}. Check afs auth status, then connect to this endpoint before running workspace commands. If authentication is needed, have me set AFS_CONTROL_PLANE_TOKEN locally; do not request secrets in chat.`;
  const scoped = (sections: CommandsDrawerConfig["sections"]) =>
    sections.map((section) => ({
      ...section,
      command: workspaceCLICommand(workspace.databaseId, section.command),
    }));
  const scopeTip =
    "Each workspace command first selects this workspace’s Redis connection. Set AFS_CONTROL_PLANE_TOKEN locally if required. Local status commands run on the machine where the CLI is installed.";
  const tab = activeTab === "activity" ? "changes" : activeTab;
  const title = displayWorkspaceName(workspace.name);

  if (tab === "checkpoints")
    return {
      title: `${title} · Checkpoints`,
      subline: "Save a recovery point or plan a fork from a saved tree.",
      prompts: [
        {
          title: "Plan a checkpoint",
          prompt: `${context} Inspect the workspace and its local mounts, then help me create a named checkpoint before my next change. Explain which local changes it can include and how to pause writers for a consistent snapshot. Confirm the name and timing with me first.`,
        },
        {
          title: "Choose a recovery point",
          prompt: `${context} List checkpoints and help me choose one to inspect or fork for recovery. Explain the effect of restoring on active clients and pending local edits. Do not restore or delete anything yet.`,
        },
      ],
      tips: [
        scopeTip,
        "Checkpoint creation flushes this machine’s registered writable mounts. It cannot collect unpublished edits from other machines. A fork uses a saved checkpoint, not the current live tree.",
      ],
      sections: scoped([
        {
          title: "List saved checkpoints",
          command: `afs checkpoint list ${name}`,
        },
        {
          title: "Inspect a checkpoint",
          description:
            "Replace checkpoint-name with a name or ID from the list.",
          command: `afs checkpoint show ${name} checkpoint-name`,
        },
        {
          title: "Create a recovery point",
          description:
            "Choose an unused checkpoint name. Pause application writes on all clients when consistency matters.",
          command: `afs checkpoint create ${name} --name before-change`,
        },
      ]).concat({
        title: "Checkpoint options",
        command: "afs checkpoint --help",
      }),
    };

  if (tab === "changes")
    return {
      title: `${title} · History`,
      subline:
        "Investigate retained file versions and understand what changed.",
      prompts: [
        {
          title: "Investigate a file change",
          prompt: `${context} Help me investigate a file from this workspace’s History tab. Ask for its relative path, inspect the history policy and retained versions, then compare relevant versions and summarize the change. Do not edit or restore the file.`,
        },
        {
          title: "Plan file recovery",
          prompt: `${context} Help me recover a changed or deleted file. Ask for its relative path and the version I need, inspect available history, and explain restore versus undelete. Propose the recovery steps before changing anything.`,
        },
      ],
      tips: [
        scopeTip,
        "History depends on the workspace policy and retention limits. Missing history does not prove a file never changed. Replace README.md with a path relative to the workspace root.",
      ],
      sections: scoped([
        {
          title: "Inspect history policy",
          command: `afs history policy ${name}`,
        },
        {
          title: "List versions of a file",
          command: `afs history list ${name} README.md`,
        },
        {
          title: "Read a retained version",
          description: "Replace version-id with an ID from history list.",
          command: `afs history show ${name} README.md --version version-id`,
        },
      ]).concat({
        title: "Compare and recover files",
        command: "afs history --help",
      }),
    };

  if (tab === "settings")
    return {
      title: `${title} · Settings`,
      subline:
        "Understand workspace metadata, history policy, and capabilities.",
      prompts: [
        {
          title: "Review workspace settings",
          prompt: `${context} Inspect this workspace’s details and file-history policy. Explain its capabilities and the retention tradeoffs for my workload. Recommend settings and identify which changes belong in the UI before modifying anything.`,
        },
        {
          title: "Plan a history policy",
          prompt: `${context} Help me choose a file-history policy. Ask about the files I need to recover and my retention needs, inspect afs history policy --help, then propose the exact command and explain its effects before applying it.`,
        },
      ],
      tips: [
        scopeTip,
        "Edit workspace name and description on this page. File-history policy is separate from checkpoints. Local sync settings under afs config affect newly started mounts, not workspace metadata.",
      ],
      sections: scoped([
        {
          title: "Inspect workspace details",
          command: `afs --json info ${name}`,
        },
        { title: "Read history policy", command: `afs history policy ${name}` },
      ]).concat({
        title: "History policy options",
        command: "afs history policy --help",
      }),
    };

  const checkpoint = view.startsWith("checkpoint:")
    ? view.slice("checkpoint:".length)
    : null;
  return {
    title: `${title} · Browse`,
    subline: checkpoint
      ? "Inspect the selected checkpoint and plan work on a mounted copy."
      : "Explore this workspace and work with its files through a local mount.",
    prompts: [
      {
        title: checkpoint
          ? "Understand this snapshot"
          : "Explore the workspace",
        prompt: `${context} ${checkpoint ? `I am browsing checkpoint ${JSON.stringify(checkpoint)}. Inspect that checkpoint’s metadata and explain how to fork it to explore its files separately; do not assume a live mount contains this snapshot.` : "Inspect workspace details and local mounts, then help me explore its files. Reuse the correct existing mount or ask me to choose an empty directory before mounting. Read the project instructions and summarize the file layout without editing anything."}`,
      },
      {
        title: "Verify local publication",
        prompt: `${context} Help me check whether my local edits have reached Redis. Identify the exact writable folder-sync mount, review afs sync status, and explain any pending work or errors. Coordinate pausing writers before running afs sync --wait on that directory; explain what its verification receipt does and does not guarantee.`,
      },
    ],
    tips: [
      scopeTip,
      checkpoint
        ? "You are viewing a saved snapshot. A normal mount reads the current live tree; fork the selected checkpoint to explore that snapshot through a mount."
        : "Use ordinary filesystem tools in the mounted directory to list, read, and edit files. afs info reports workspace details; it does not list the file tree.",
      "Sync verification confirms publication to Redis, not delivery to every other client. Choose an empty mount directory and wait for mount to complete before working.",
    ],
    sections: scoped([
      {
        title: checkpoint
          ? "Inspect the selected checkpoint"
          : "Inspect workspace details",
        command: checkpoint
          ? `afs checkpoint show ${name} ${shellQuote(checkpoint)}`
          : `afs info ${name}`,
      },
      ...(checkpoint
        ? []
        : [
            {
              title: "Mount this workspace",
              description:
                "Replace ~/afs/my-workspace with your chosen empty local directory.",
              command: `afs mount ${name} ~/afs/my-workspace`,
            },
          ]),
    ]).concat([
      {
        title: "Inspect local mounts and sync",
        command: "afs status\nafs sync status",
      },
      {
        title: "Mount and verification options",
        command: "afs mount --help\nafs sync --help",
      },
    ]),
  };
}
