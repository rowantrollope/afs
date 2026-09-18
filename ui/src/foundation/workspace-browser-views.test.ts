import { describe, expect, test } from "vitest";
import type { AFSWorkspaceDetail } from "./types/afs";
import {
  getActiveWorkspaceView,
  getDefaultWorkspaceBrowserView,
  getWorkspaceBrowserViewOptions,
  resolveWorkspaceBrowserView,
} from "./workspace-browser-views";

function cloneInitialAFSState() {
  return {
    workspaces: [
      {
        id: "support-sandbox",
        draftState: "clean",
        headSavepointId: "sandbox-head",
        capabilities: {
          browseHead: true,
          browseWorkingCopy: true,
          browseCheckpoints: true,
        },
        savepoints: [{ id: "sandbox-head", name: "initial" }],
      },
      {
        id: "payments-portal",
        draftState: "dirty",
        headSavepointId: "sp-payments-before-refactor",
        capabilities: {
          browseHead: true,
          browseWorkingCopy: true,
          browseCheckpoints: true,
        },
        savepoints: [
          { id: "sp-payments-before-refactor", name: "before-refactor" },
          { id: "sp-payments-baseline-ui", name: "baseline-ui" },
        ],
      },
    ] as AFSWorkspaceDetail[],
  };
}

describe("workspace browser views", () => {
  test("shows the active workspace for a clean workspace whose checkpoint matches active state", () => {
    const workspace = cloneInitialAFSState().workspaces.find(
      (item) => item.id === "support-sandbox",
    );
    expect(workspace).toBeDefined();

    expect(getWorkspaceBrowserViewOptions(workspace!)).toEqual([
      { value: "head", label: "Active workspace" },
    ]);
    expect(getActiveWorkspaceView(workspace!)).toBe("head");
    expect(getDefaultWorkspaceBrowserView(workspace!)).toBe("head");
  });

  test("shows dirty active state separately from every saved checkpoint", () => {
    const workspace = cloneInitialAFSState().workspaces.find(
      (item) => item.id === "payments-portal",
    );
    expect(workspace).toBeDefined();

    expect(getWorkspaceBrowserViewOptions(workspace!)).toEqual([
      { value: "working-copy", label: "Active workspace" },
      {
        value: "checkpoint:sp-payments-before-refactor",
        label: "before-refactor",
      },
      { value: "checkpoint:sp-payments-baseline-ui", label: "baseline-ui" },
    ]);
    expect(getActiveWorkspaceView(workspace!)).toBe("working-copy");
    expect(getDefaultWorkspaceBrowserView(workspace!)).toBe("working-copy");
  });

  test("preserves a selected checkpoint view across workspace refreshes", () => {
    const workspace = cloneInitialAFSState().workspaces.find(
      (item) => item.id === "payments-portal",
    );
    expect(workspace).toBeDefined();

    expect(
      resolveWorkspaceBrowserView(
        workspace!,
        "checkpoint:sp-payments-baseline-ui",
      ),
    ).toBe("checkpoint:sp-payments-baseline-ui");
  });

  test("falls back to the default view when a selected checkpoint is no longer available", () => {
    const workspace = cloneInitialAFSState().workspaces.find(
      (item) => item.id === "payments-portal",
    );
    expect(workspace).toBeDefined();

    expect(resolveWorkspaceBrowserView(workspace!, "checkpoint:missing")).toBe(
      "working-copy",
    );
  });
  test("keeps the head checkpoint available when the live tree is missing", () => {
    const workspace = cloneInitialAFSState().workspaces[1];
    workspace.liveRootAvailable = false;
    workspace.draftState = "unavailable";
    workspace.capabilities.browseHead = false;
    workspace.capabilities.browseWorkingCopy = false;
    expect(getWorkspaceBrowserViewOptions(workspace)).toEqual([
      {
        value: "checkpoint:sp-payments-before-refactor",
        label: "before-refactor",
      },
      { value: "checkpoint:sp-payments-baseline-ui", label: "baseline-ui" },
    ]);
    expect(resolveWorkspaceBrowserView(workspace, "head")).toBe(
      "checkpoint:sp-payments-before-refactor",
    );
    expect(resolveWorkspaceBrowserView(workspace, "working-copy")).toBe(
      "checkpoint:sp-payments-before-refactor",
    );
  });

  test("does not offer an unavailable live tree without any checkpoints", () => {
    const workspace = cloneInitialAFSState().workspaces[0];
    workspace.liveRootAvailable = false;
    workspace.capabilities.browseHead = false;
    workspace.capabilities.browseWorkingCopy = false;
    workspace.savepoints = [];
    expect(getWorkspaceBrowserViewOptions(workspace)).toEqual([]);
  });
});
