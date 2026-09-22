import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { CommandsDrawerConfig } from "../foundation/drawer-context";
import {
  DrawerProvider,
  useDrawer,
  useDrawerCommands,
} from "../foundation/drawer-context";
import { GlobalDrawer, HelpButton } from "./global-drawer";

const location = vi.hoisted(() => ({
  pathname: "/monitor",
  search: {} as { view?: string },
  searchStr: "",
}));
vi.mock("@tanstack/react-router", () => ({ useLocation: () => location }));

const workspaceHelp: CommandsDrawerConfig = {
  title: "Research workspace · Browse",
  prompts: [
    {
      title: "Explore research",
      prompt:
        "Read AGENTS.md.\nExplain the project's layout without editing files.",
    },
  ],
  sections: [
    {
      title: "Inspect research",
      command:
        "afs auth login --url 'https://afs.example/databases/research' &&\nafs info 'team'\"'\"'s research'",
    },
  ],
};
const explicitCommands: CommandsDrawerConfig = {
  title: "Connect this agent",
  sections: [{ title: "Inspect local mounts", command: "afs status" }],
};

function WorkspaceRegistration({ config }: { config: CommandsDrawerConfig }) {
  useDrawerCommands(config);
  return null;
}

function ExplicitCommandsButton() {
  const { open } = useDrawer();
  return (
    <button onClick={() => open({ kind: "commands", ...explicitCommands })}>
      Connect agent
    </button>
  );
}

function Harness({ dynamicHelp }: { dynamicHelp?: CommandsDrawerConfig }) {
  return (
    <DrawerProvider>
      {dynamicHelp ? <WorkspaceRegistration config={dynamicHelp} /> : null}
      <HelpButton />
      <ExplicitCommandsButton />
      <GlobalDrawer />
    </DrawerProvider>
  );
}

const originalClipboard = Object.getOwnPropertyDescriptor(
  navigator,
  "clipboard",
);
const writeText = vi.fn<(text: string) => Promise<void>>();

beforeEach(() => {
  Object.assign(location, { pathname: "/monitor", search: {}, searchStr: "" });
  writeText.mockReset().mockResolvedValue();
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText },
  });
});
afterEach(() => {
  cleanup();
  if (originalClipboard)
    Object.defineProperty(navigator, "clipboard", originalClipboard);
  else Reflect.deleteProperty(navigator, "clipboard");
});

test("Agent help follows page and query changes while the drawer stays open", () => {
  const { rerender } = render(<Harness />);
  const helpButton = screen.getByRole("button", {
    name: "Agent help: Understand live agents",
  });
  expect(helpButton).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(helpButton);
  const monitor = screen.getByRole("dialog", {
    name: "Understand live agents",
  });
  expect(
    within(monitor).getByRole("region", { name: "Suggested agent prompts" }),
  ).toBeInTheDocument();
  expect(
    within(monitor).getByText("Diagnose a missing agent"),
  ).toBeInTheDocument();
  expect(within(monitor).getByText("Inspect this machine")).toBeInTheDocument();
  expect(helpButton).toHaveAttribute("aria-expanded", "true");

  Object.assign(location, { pathname: "/activity", search: {}, searchStr: "" });
  rerender(<Harness />);
  expect(
    screen.getByRole("dialog", { name: "Investigate file changes" }),
  ).toBeInTheDocument();
  expect(
    screen.queryByText("Diagnose a missing agent"),
  ).not.toBeInTheDocument();

  Object.assign(location, {
    search: { view: "events" },
    searchStr: "?view=events",
  });
  rerender(<Harness />);
  expect(
    screen.getByRole("dialog", { name: "Investigate workspace events" }),
  ).toBeInTheDocument();
  expect(screen.getByText("Explain an event sequence")).toBeInTheDocument();
  expect(screen.queryByText("Explain a change")).not.toBeInTheDocument();
});

test("workspace help overrides the fallback and clears when its page unmounts", () => {
  location.pathname = "/workspaces/research";
  const { rerender } = render(<Harness dynamicHelp={workspaceHelp} />);
  fireEvent.click(
    screen.getByRole("button", { name: `Agent help: ${workspaceHelp.title}` }),
  );
  expect(
    screen.getByRole("dialog", { name: workspaceHelp.title }),
  ).toBeInTheDocument();
  expect(screen.getByText("Inspect research")).toBeInTheDocument();

  // Navigation must not display the previous workspace while its cleanup is pending.
  location.pathname = "/monitor";
  rerender(<Harness dynamicHelp={workspaceHelp} />);
  expect(
    screen.getByRole("dialog", { name: "Understand live agents" }),
  ).toBeInTheDocument();
  expect(screen.queryByText("Inspect research")).not.toBeInTheDocument();

  rerender(<Harness />);
  location.pathname = "/workspaces/another-workspace";
  rerender(<Harness />);
  expect(
    screen.queryByRole("dialog", { name: workspaceHelp.title }),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("Inspect research")).not.toBeInTheDocument();
  expect(screen.getByRole("dialog")).toBeInTheDocument();
});

test("explicit command drawers retain their action-specific content across navigation", () => {
  const { rerender } = render(<Harness />);
  fireEvent.click(screen.getByRole("button", { name: "Connect agent" }));
  expect(
    screen.getByRole("dialog", { name: explicitCommands.title }),
  ).toBeInTheDocument();
  expect(screen.getByText("afs status")).toBeInTheDocument();
  expect(
    screen.queryByRole("region", { name: "Suggested agent prompts" }),
  ).not.toBeInTheDocument();

  location.pathname = "/docs";
  rerender(<Harness />);
  expect(
    screen.getByRole("dialog", { name: explicitCommands.title }),
  ).toBeInTheDocument();
  fireEvent.click(
    screen.getByRole("button", {
      name: "Agent help: Find the right AFS command",
    }),
  );
  expect(
    screen.getByRole("dialog", { name: "Find the right AFS command" }),
  ).toBeInTheDocument();
});

test("copies exact prompt and command text, preserving line breaks and shell quoting", async () => {
  location.pathname = "/workspaces/research";
  render(<Harness dynamicHelp={workspaceHelp} />);
  fireEvent.click(
    screen.getByRole("button", { name: `Agent help: ${workspaceHelp.title}` }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Copy prompt: Explore research" }),
  );
  await waitFor(() => expect(screen.getByText("Copied")).toBeInTheDocument());
  expect(writeText).toHaveBeenNthCalledWith(
    1,
    workspaceHelp.prompts![0].prompt,
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Copy command: Inspect research" }),
  );
  await waitFor(() => expect(screen.getAllByText("Copied")).toHaveLength(2));
  expect(writeText).toHaveBeenNthCalledWith(
    2,
    workspaceHelp.sections[0].command,
  );
});

test("shows actionable feedback when clipboard access fails", async () => {
  writeText.mockRejectedValue(new Error("Clipboard permission denied"));
  render(<Harness />);
  fireEvent.click(
    screen.getByRole("button", { name: "Agent help: Understand live agents" }),
  );
  fireEvent.click(
    screen.getByRole("button", {
      name: "Copy prompt: Diagnose a missing agent",
    }),
  );
  const failure = await screen.findByText(
    "Could not copy. Select the text and copy it manually.",
  );
  expect(failure).toHaveAttribute("role", "status");
  expect(screen.queryByText("Copied")).not.toBeInTheDocument();
  expect(
    screen.getByText(/Help me diagnose an agent missing/),
  ).toBeInTheDocument();
});
