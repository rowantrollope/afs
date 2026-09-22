import { useLocation } from "@tanstack/react-router";
import styled, { keyframes } from "styled-components";
import { useDrawer } from "../foundation/drawer-context";
import { pageHelpFor } from "../foundation/page-help";
import { CommandsDrawer } from "./onboarding-drawer";

function usePageHelp() {
  const location = useLocation();
  const { pageHelp } = useDrawer();
  // Only workspace detail contributes dynamic, database-scoped help.
  const help =
    location.pathname.startsWith("/workspaces/") && pageHelp
      ? pageHelp
      : pageHelpFor(location.pathname, location.search);
  return { help, contextKey: `${location.pathname}${location.searchStr}` };
}

export function GlobalDrawer() {
  const { state, close } = useDrawer();
  const { help, contextKey } = usePageHelp();
  if (!state) return null;
  return (
    <CommandsDrawer
      key={state.kind === "page-help" ? contextKey : state.title}
      {...(state.kind === "page-help" ? help : state)}
      onClose={close}
    />
  );
}
export function HelpButton() {
  const { open, state } = useDrawer();
  const { help } = usePageHelp();
  return (
    <HelpButtonRoot
      type="button"
      onClick={() => open({ kind: "page-help" })}
      aria-label={`Agent help: ${help.title}`}
      aria-haspopup="dialog"
      aria-expanded={state?.kind === "page-help"}
      title={`Agent help: ${help.title}`}
    >
      <span>agents &gt;</span>
      <TerminalCursor aria-hidden>_</TerminalCursor>
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
  flex-shrink: 0;
  gap: 6px;
  padding: 6px 8px 6px 10px;
  border-radius: 8px;
  border: 1px solid #1f2937;
  background: #0d1117;
  color: #4ade80;
  font-family: var(--afs-mono, "SF Mono", "Fira Code", monospace);
  font-size: 14px;
  font-weight: 600;
  white-space: nowrap;
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
