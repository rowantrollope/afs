import { expect, test } from "vitest";
import { controlPlaneEndpoint } from "./api/afs";
import type { AFSWorkspaceDetail } from "./types/afs";
import { workspaceHelpFor } from "./workspace-help";
import type { StudioTab } from "./workspace-tabs";

function workspace(databaseId = "db-research", name = "shared-name") {
  return { id: "workspace-1", name, databaseId } as AFSWorkspaceDetail;
}

test.each<StudioTab>([
  "browse",
  "checkpoints",
  "changes",
  "settings",
  "activity",
])(
  "%s help selects the workspace's database before every workspace command",
  (tab) => {
    const help = workspaceHelpFor(workspace(), tab);
    const workspaceCommands = help.sections.filter(({ command }) =>
      command.includes("'shared-name'"),
    );
    expect(workspaceCommands.length).toBeGreaterThan(0);
    for (const { command } of workspaceCommands) {
      expect(
        command.startsWith(
          `afs auth login --url '${controlPlaneEndpoint()}/databases/db-research' &&\nafs `,
        ),
      ).toBe(true);
    }
    for (const { prompt } of help.prompts ?? []) {
      expect(prompt).toContain(
        `${controlPlaneEndpoint()}/databases/db-research`,
      );
      expect(prompt).toContain('workspace "shared-name"');
    }
  },
);

test("primary workspace help explicitly reselects the unscoped primary endpoint", () => {
  const help = workspaceHelpFor(workspace("local"), "browse");
  expect(help.sections[0].command).toBe(
    `afs auth login --url '${controlPlaneEndpoint()}' &&\nafs info 'shared-name'`,
  );
});

test("workspace and checkpoint identifiers remain single shell arguments", () => {
  const help = workspaceHelpFor(
    workspace("db-research", "team's $(touch nope)"),
    "browse",
    "checkpoint:before's `echo nope`",
  );
  expect(help.sections[0].command).toBe(
    `afs auth login --url '${controlPlaneEndpoint()}/databases/db-research' &&\nafs checkpoint show 'team'"'"'s $(touch nope)' 'before'"'"'s \`echo nope\`'`,
  );
});

test("saved snapshot help inspects its checkpoint and avoids a misleading live mount command", () => {
  const help = workspaceHelpFor(workspace(), "browse", "checkpoint:release-1");
  expect(help.prompts?.[0].title).toBe("Understand this snapshot");
  expect(help.prompts?.[0].prompt).toContain('browsing checkpoint "release-1"');
  expect(help.prompts?.[0].prompt).toContain(
    "do not assume a live mount contains this snapshot",
  );
  expect(help.sections[0].command).toContain(
    "afs checkpoint show 'shared-name' 'release-1'",
  );
  expect(
    help.sections.some(({ command }) =>
      command.includes("afs mount 'shared-name'"),
    ),
  ).toBe(false);
  expect(help.tips?.join(" ")).toContain("fork the selected checkpoint");
});

test("live browse help includes a scoped mount and local publication checks", () => {
  const help = workspaceHelpFor(workspace(), "browse", "head");
  expect(
    help.sections.some(({ command }) =>
      command.endsWith("afs mount 'shared-name' ~/afs/my-workspace"),
    ),
  ).toBe(true);
  expect(
    help.sections.some(
      ({ command }) => command === "afs status\nafs sync status",
    ),
  ).toBe(true);
  expect(help.prompts?.[0].title).toBe("Explore the workspace");
  expect(
    help.prompts?.some(({ prompt }) =>
      prompt.includes("exact writable folder-sync mount"),
    ),
  ).toBe(true);
});
