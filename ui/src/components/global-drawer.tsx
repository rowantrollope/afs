import { ChevronRight } from "lucide-react";
import styled, { keyframes } from "styled-components";
import { useDrawer } from "../foundation/drawer-context";
import { CommandsDrawer } from "./onboarding-drawer";

const defaults = {
  title: "AFS commands",
  subline: "The CLI connects directly to your configured Redis backend.",
  sections: [
    { title: "List workspaces", command: "afs list" },
    {
      title: "Mount a workspace",
      command: "afs mount my-workspace ~/afs/my-workspace",
    },
    { title: "Check local sync", command: "afs sync status" },
  ],
};
export function GlobalDrawer() {
  const { state, close } = useDrawer();
  return state ? <CommandsDrawer {...state} onClose={close} /> : null;
}
export function HelpButton() {
  const { open, pageHelp } = useDrawer();
  const help = pageHelp ?? defaults;
  return (
    <HelpButtonRoot
      type="button"
      onClick={() => open({ kind: "commands", ...help })}
      aria-label={help.title}
      title={help.title}
    >
      <TerminalCursor aria-hidden>_</TerminalCursor>
      <ChevronRight size={16} strokeWidth={2.4} />
    </HelpButtonRoot>
  );
}
const cursorBlink = keyframes`
  0%, 49% { opacity: 1; }
  50%, 100% { opacity: 0.25; }
`;

const HelpButtonRoot = styled.button`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px 6px 10px;
  border-radius: 8px;
  border: 1px solid #1f2937;
  background: #0d1117;
  color: #4ade80;
  cursor: pointer;
  transition:
    background 120ms ease,
    border-color 120ms ease,
    box-shadow 120ms ease;

  &:hover {
    border-color: #4ade80;
    background: #0a1f15;
    box-shadow: 0 0 0 3px rgba(74, 222, 128, 0.12);
  }

  &:focus-visible {
    outline: 2px solid #4ade80;
    outline-offset: 2px;
  }
`;

const TerminalCursor = styled.span`
  display: inline-flex;
  align-items: flex-end;
  width: 12px;
  height: 16px;
  color: #4ade80;
  font-family: var(--afs-mono, "SF Mono", "Fira Code", monospace);
  font-size: 18px;
  font-weight: 700;
  line-height: 1;
  letter-spacing: 0;
  text-shadow: 0 0 6px rgba(74, 222, 128, 0.45);
  animation: ${cursorBlink} 1.1s steps(1) infinite;
`;
