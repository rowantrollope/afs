import { getRecipe } from "../features/recipes/recipe-catalog";
import { controlPlaneEndpoint } from "./api/afs";
import type { CommandsDrawerConfig } from "./drawer-context";
import { agentSetupPrompt, shellQuote } from "./home-content";

export function pageHelpFor(
  pathname: string,
  search?: { view?: unknown },
): CommandsDrawerConfig {
  const endpoint = controlPlaneEndpoint();
  const connect = `afs auth login --url ${shellQuote(endpoint)}`;
  const connectionTip =
    "Set AFS_CONTROL_PLANE_TOKEN locally when authentication is required. Keep tokens and Redis credentials out of agent prompts.";
  const path = pathname.replace(/\/+$/, "") || "/";

  switch (path) {
    case "/":
      return {
        title: "Get started with AFS",
        subline:
          "Connect an agent to a workspace and work with ordinary files.",
        prompts: [
          {
            title: "Set up my first workspace",
            prompt: agentSetupPrompt(
              endpoint,
              new URL("/afs-skill.md", window.location.origin).href,
            ),
          },
          {
            title: "Choose a workflow",
            prompt:
              "Help me choose an AFS workflow for my project. Explain shared agent memory, a shared knowledge wiki, coding standards, and a planning board. Ask about my existing files and recommend a starting point before creating a workspace.",
          },
        ],
        sections: [
          { title: "Connect to this server", command: connect },
          {
            title: "Find a workspace",
            description: "List workspaces on your configured database.",
            command: "afs list",
          },
          {
            title: "Mount and inspect",
            description:
              "Replace my-workspace with an existing workspace and choose an empty local directory.",
            command:
              "afs mount my-workspace ~/afs/my-workspace\nafs status ~/afs/my-workspace",
          },
        ],
        tips: [
          "Folder sync is the default. Agents read and edit the mounted directory using ordinary filesystem tools.",
          connectionTip,
        ],
      };
    case "/monitor":
      return {
        title: "Understand live agents",
        subline: "Inspect connected sessions, local sync, and recent activity.",
        prompts: [
          {
            title: "Diagnose a missing agent",
            prompt: `Help me diagnose an agent missing from AFS Monitor at ${endpoint}. Check afs auth status and afs status on the agent's machine, confirm its server and database, and inspect sync errors. Explain whether it mounted through this control plane. Report findings before restarting or changing any mount.`,
          },
          {
            title: "Verify my edits were published",
            prompt:
              "Help me verify pending AFS edits from this machine. Identify the writable folder-sync mount with afs status and afs sync status, arrange a pause in application and other workspace writes, then run afs sync --wait on its exact directory. Explain the receipt and any errors; distinguish publication to Redis from delivery to another client.",
          },
        ],
        sections: [
          {
            title: "Inspect this machine",
            description:
              "Auth status shows saved settings without checking connectivity. Status lists this machine's mounts.",
            command: "afs auth status\nafs status\nafs sync status",
          },
          {
            title: "Verify a sync mount",
            description:
              "Use an existing writable folder-sync directory and pause writers first. Native and read-only mounts cannot use this wait.",
            command: "afs sync --wait ~/afs/my-workspace --timeout 2m",
          },
          {
            title: "Identify a new agent session",
            description:
              "After connecting to the workspace's database, mount an existing workspace in an empty directory with a recognizable label.",
            command:
              'afs mount my-workspace ~/afs/my-workspace --label "Research agent" --session "Research session"',
          },
        ],
        tips: [
          "Monitor reports sessions registered through the control plane. Direct Redis clients may be absent, and caller-supplied labels are descriptive.",
          "Live activity and empty sync queues do not prove that a save completed. A successful sync receipt verifies publication to Redis.",
        ],
      };
    case "/workspaces":
      return {
        title: "Choose and create workspaces",
        subline: "Each workspace contains one file tree and its checkpoints.",
        prompts: [
          {
            title: "Connect to an existing workspace",
            prompt:
              "Help me choose an existing AFS workspace from this Workspaces page. Confirm its database, use its detail page's database-scoped connection command, and check existing local mounts. Mount it in an empty directory and explain how to access its files.",
          },
          {
            title: "Import my project",
            prompt:
              "Help me import a local project into AFS. Confirm the source directory, target database, and an unused workspace name. Review which files will be included, create the workspace with afs create --from, and mount it in a separate empty directory. Preserve my source files and verify the resulting workspace.",
          },
        ],
        sections: [
          {
            title: "List the configured database",
            command: "afs list",
          },
          {
            title: "Create an empty workspace",
            description: "Choose an unused name in your intended database.",
            command: "afs create my-workspace",
          },
          {
            title: "Import a local directory",
            description:
              "Use a different unused name for the imported project.",
            command: "afs create imported-project --from ./existing-project",
          },
        ],
        tips: [
          "This page combines databases. CLI commands use one configured database; open a workspace and copy its scoped connection command before working with it.",
          "To experiment independently, create a checkpoint and use afs fork. A fork uses checkpoint contents, excluding later live edits.",
        ],
      };
    case "/activity":
      return search?.view === "events" ? eventsHelp() : changesHelp();
    case "/databases":
      return {
        title: "Manage Redis connections",
        subline:
          "Review server health and the Redis databases available to AFS.",
        prompts: [
          {
            title: "Investigate a disconnected database",
            prompt:
              "Help me investigate a disconnected Redis database on the AFS Redis page. Review the displayed health, host, port, database index, and TLS setting, then check connectivity from the control-plane host. Do not include credentials in the report. Explain the likely cause before changing connection settings.",
          },
          {
            title: "Connect my CLI to the right database",
            prompt:
              "Help me access an AFS workspace on the correct Redis database. Open its workspace detail page and use its database-scoped control-plane connection command. Check afs auth status and afs list afterward. Explain how that database relates to the connections on the Redis page.",
          },
        ],
        sections: [
          {
            title: "Connect to the default database",
            description:
              "This server URL selects its default connection. Use a workspace's scoped command for another database.",
            command: connect,
          },
          {
            title: "Inspect CLI connection settings",
            description:
              "An offline settings check; it does not test Redis health.",
            command: "afs auth status",
          },
          {
            title: "Check workspace access",
            description:
              "Read workspaces on the currently configured database.",
            command: "afs list",
          },
        ],
        tips: [
          "Use Add database to save a server connection, or click a row to edit it. There is no afs database command.",
          "Changing a connection endpoint selects another Redis database; it does not move workspace data. Local afs config settings do not edit these server profiles.",
          connectionTip,
        ],
      };
    case "/api-keys":
      return {
        title: "Manage agent API access",
        subline:
          "Review trusted administrator keys, usage, expiry, and rotation.",
        prompts: [
          {
            title: "Review key usage",
            prompt:
              "Review my AFS API key metadata for expired, unused, or soon-expiring keys. Explain which agents or integrations may need attention and propose a rotation plan. Use names, IDs, expiry, and usage timestamps only; never ask me to paste key secrets into chat.",
          },
          {
            title: "Rotate an integration's key",
            prompt:
              "Help me rotate an AFS integration's API key. Identify the old key by name and ID, create a replacement with an appropriate expiry, and guide me to configure its secret locally. Verify the integration uses the new key before revoking the old one. Explain that existing Redis credentials and mounts are unaffected by API key revocation.",
          },
        ],
        sections: [
          {
            title: "List keys and usage",
            description: "Returns key metadata without secrets.",
            command: "afs auth keys list",
          },
          {
            title: "Create a named key",
            description:
              "The secret is shown once. Store it locally and securely.",
            command: "afs auth keys create 'build agent' --expires 30d",
          },
          {
            title: "Revoke a replaced key",
            description:
              "Replace the placeholder with the old key's ID after verifying the replacement works.",
            command: "afs auth keys revoke '<key-id>'",
          },
        ],
        tips: [
          "Keys are trusted administrator credentials for every configured database; they do not provide workspace isolation.",
          "Expiry and revocation block later API requests, but do not revoke Redis credentials already issued to mounts.",
          "If key issuance is disabled, configure AFS_CONTROL_PLANE_TOKEN on the control-plane server and sign in with the team token first. Use afs auth login --token-stdin to save a key without putting it in command arguments.",
        ],
      };
    case "/docs":
      return {
        title: "Find the right AFS command",
        subline: "Learn the CLI, mounted file workflow, and recovery options.",
        prompts: [
          {
            title: "Explain the workflow",
            prompt:
              "Explain how AFS workspaces, mounted directories, folder sync, checkpoints, and per-file history fit together. Use the current CLI guide linked on this Docs page and afs --help. Give me a small example using an existing workspace and ordinary filesystem tools.",
          },
          {
            title: "Plan a recoverable change",
            prompt:
              "Help me plan a recoverable change in an AFS workspace. Explain when to verify sync, create a checkpoint, and inspect or export file history. Describe what is captured from this machine versus other clients and how to preserve local edits before a restore.",
          },
        ],
        sections: [
          { title: "Explore the CLI", command: "afs --help" },
          {
            title: "Learn connection and mount options",
            command: "afs auth --help\nafs mount --help\nafs sync --help",
          },
          {
            title: "Learn checkpoint and history options",
            command: "afs checkpoint --help\nafs history --help",
          },
        ],
        tips: [
          "Use ordinary filesystem tools in mounted directories. AFS has no separate remote-file command group.",
          "Command help works without Redis. Add --json when you need structured command output for scripts.",
        ],
      };
    case "/settings":
      return {
        title: "Customize the console",
        subline: "Choose the console skin and selection colors for each theme.",
        prompts: [
          {
            title: "Improve readability",
            prompt:
              "Help me choose an AFS console appearance for readability. Review the available skin and selection color controls on Settings, compare selected and hover states in light and dark themes, and recommend a configuration with clearly visible text, borders, and focus.",
          },
          {
            title: "Explain configuration boundaries",
            prompt:
              "Explain which AFS settings affect this browser's appearance, which afs config settings affect the local CLI and newly started mounts, and which Redis page settings belong to the server. Help me find the correct place for my change before modifying anything.",
          },
        ],
        sections: [
          {
            title: "Inspect local CLI configuration options",
            description:
              "These settings affect CLI connections and sync; appearance is configured in this page.",
            command: "afs config --help",
          },
          {
            title: "Identify the installed CLI",
            command: "afs --version",
          },
        ],
        tips: [
          "Console appearance is stored in this browser. There is no AFS CLI command for skins or selection colors.",
          "Local sync configuration changes apply to subsequent commands and newly started mounts.",
        ],
      };
    case "/recipes":
      return {
        title: "Choose a workspace recipe",
        subline:
          "Explore starter workflows and the files each recipe provides.",
        prompts: [
          {
            title: "Choose a recipe for my team",
            prompt:
              "Help me choose an AFS recipe for my team. Compare shared agent memory, the knowledge wiki, coding standards, the planning board, and a blank workspace. Ask about how our agents collaborate and recommend a recipe with a concrete first task.",
          },
          {
            title: "Review before setup",
            prompt:
              "Review the AFS recipe I choose before setup. Explain its starter files, AGENTS.md instructions, and companion guides. Check existing workspaces and local mounts, then propose an unused workspace name and empty directory. Use the complete setup prompt on the recipe page to initialize its files.",
          },
        ],
        sections: [
          { title: "Connect to this server", command: connect },
          {
            title: "Check existing workspaces and mounts",
            command: "afs list\nafs status",
          },
          {
            title: "Review creation and mounting options",
            command: "afs create --help\nafs mount --help",
          },
        ],
        tips: [
          "Open a recipe for its full setup prompt and starter files. Creating and mounting a workspace alone does not install the recipe content.",
        ],
      };
    default: {
      if (path.startsWith("/recipes/")) {
        const recipe = getRecipe(path.slice("/recipes/".length));
        if (recipe) {
          return {
            title: `Set up ${recipe.title}`,
            subline: recipe.tagline,
            prompts: [
              {
                title: "Prepare this recipe",
                prompt: `Help me set up the ${recipe.title} AFS recipe shown on this page. ${recipe.files.length ? "Read its starter files and agent instructions, then use this page's complete setup prompt." : "Use this page's setup prompt to prepare an empty workspace; ask what I want to build before writing files."} Connect to ${endpoint}, check existing workspaces and mounts, and confirm an unused name and empty directory; suggested defaults are ${recipe.slug} and ~/afs/${recipe.slug}. Preserve existing files. Configure any authentication locally, without sharing credentials in chat.`,
              },
              {
                title: "Start using the workspace",
                description: recipe.files.length
                  ? "Use after the recipe's setup and file verification are complete."
                  : "Use after mounting your empty workspace.",
                prompt: `In my mounted workspace for the ${recipe.title} recipe, ${recipe.files.length ? "read AGENTS.md and the recipe's companion guides, then help me with this task:" : "help me decide on a first task before creating any files:"} ${recipe.firstPrompt}`,
              },
            ],
            sections: [
              { title: "Connect to this server", command: connect },
              {
                title: "Create and mount the workspace",
                description:
                  "Check that the name is unused and directory is empty first.",
                command: `afs create ${shellQuote(recipe.slug)}\nafs mount ${shellQuote(recipe.slug)} ~/afs/${recipe.slug}`,
              },
              {
                title: recipe.files.length
                  ? "Verify initialized files"
                  : "Inspect the empty mount",
                description: recipe.files.length
                  ? "Run after using the full setup prompt to write the files, with other writers paused. Adjust the path if needed."
                  : "Check the local mount after setup. Adjust the path if needed.",
                command: recipe.files.length
                  ? `afs sync --wait ~/afs/${recipe.slug}`
                  : `afs status ~/afs/${recipe.slug}`,
              },
            ],
            tips: recipe.files.length
              ? [
                  "Use the recipe page's full setup prompt to initialize starter files. CLI create and mount commands only prepare the workspace.",
                  "Companion guides are reference instructions, not automatically installed agent tools.",
                ]
              : [
                  "This recipe starts empty, with no starter files or companion guides. Choose your first task, then work with ordinary files in the mounted directory.",
                ],
          };
        }
      }
      return {
        title: "Explore AFS",
        subline: "Find a workspace or look up the supported CLI workflow.",
        prompts: [
          {
            title: "Find my next step",
            prompt:
              "Help me find the right AFS page and command for my task. Check the current CLI help, explain which workspace and database are involved, and identify any missing context before changing files or configuration.",
          },
        ],
        sections: [
          { title: "CLI reference", command: "afs --help" },
          { title: "Local mounts", command: "afs status" },
          { title: "Available workspaces", command: "afs list" },
        ],
        tips: [
          "Workspace commands use the CLI's configured database. Open a workspace for its specific connection command.",
        ],
      };
    }
  }
}

function changesHelp(): CommandsDrawerConfig {
  return {
    title: "Investigate file changes",
    subline:
      "Trace published file changes to their workspace, path, and actor.",
    prompts: [
      {
        title: "Explain a change",
        prompt:
          "Help me investigate a change in AFS History. Open the affected workspace on its correct database, identify the file path and actor, then use history list and diff to explain what changed. Distinguish recorded published mutations from transient local edits and report any gaps in retained history.",
      },
      {
        title: "Recover a file for inspection",
        prompt:
          "Help me recover an earlier version of an AFS file for inspection. Confirm the workspace, database, and relative path, list retained versions, and export the chosen version to a new local path. Compare it with the mounted file before deciding whether to publish a restore.",
      },
    ],
    sections: [
      {
        title: "Inspect retained versions",
        description:
          "Connect to the selected workspace's database and replace the example name and path.",
        command:
          "afs history list my-workspace README.md --order desc --limit 50",
      },
      {
        title: "Compare with the live file",
        description: "Use a version ID returned by history list.",
        command:
          "afs history diff my-workspace README.md --from-version '<version-id>' --to-ref head",
      },
      {
        title: "Export for review",
        description: "The destination must not already exist.",
        command:
          "afs history export my-workspace README.md --version '<version-id>' --to ./recovered-README.md",
      },
    ],
    tips: [
      "History capture is off by default. Records depend on the workspace's capture policy and retention; they do not contain every transient edit.",
      "This page combines databases. Open the affected workspace for its scoped connection command before running CLI history commands.",
    ],
  };
}

function eventsHelp(): CommandsDrawerConfig {
  return {
    title: "Investigate workspace events",
    subline:
      "Correlate lifecycle activity and checkpoint events with workspace changes.",
    prompts: [
      {
        title: "Explain an event sequence",
        prompt:
          "Help me review the events shown in AFS History. Group related events by database, workspace, actor, and time; distinguish mount lifecycle events from file changes and checkpoints. Open the affected workspace to investigate and clearly state what the recorded events cannot establish.",
      },
      {
        title: "Check a recovery point",
        prompt:
          "Help me inspect a checkpoint associated with an AFS event. Confirm the workspace and database, list its checkpoints, and inspect the relevant checkpoint. Explain which published state it captured without restoring or deleting anything.",
      },
    ],
    sections: [
      { title: "Inspect local mount state", command: "afs status" },
      {
        title: "List workspace checkpoints",
        description: "Connect to the event's workspace database first.",
        command: "afs checkpoint list my-workspace",
      },
      {
        title: "Inspect a checkpoint",
        description: "Replace the example workspace and checkpoint name.",
        command: "afs checkpoint show my-workspace before-refactor",
      },
    ],
    tips: [
      "The global event feed is available in this console; the CLI provides workspace, checkpoint, and local mount inspection rather than an events command.",
      "A checkpoint captures published Redis state. A console checkpoint does not flush pending edits from other clients.",
    ],
  };
}
