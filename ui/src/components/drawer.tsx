// Drawer — right-anchored slide-over shell.
//
// Provides the scrim, slide-in/out animation, and Escape/scrim close
// handling. Consumers compose their own header/body/footer inside.
//
// Usage:
//   <Drawer onClose={onClose} ariaLabel="Agent profile" width="min(880px, 96vw)">
//     {({ requestClose }) => (
//       <>
//         <Header onClose={requestClose}>...</Header>
//         <Body>...</Body>
//         <Footer>...</Footer>
//       </>
//     )}
//   </Drawer>
//
// Pattern originally inlined in onboarding-drawer.tsx; extracted here so
// both that drawer and the agent-profile drawer share a single shell.

import type { ReactNode } from "react";
import { useCallback, useEffect, useState } from "react";
import styled, { keyframes } from "styled-components";

const SLIDE_MS = 220;

/**
 * Drives the slide-in/out animation. CSS keyframes play on mount; flipping
 * `closing` reverses them, then `onCloseParent` is called once the
 * animation finishes so the parent can unmount.
 */
export function useDrawerAnimation(onCloseParent: () => void) {
  const [closing, setClosing] = useState(false);

  const requestClose = useCallback(() => {
    setClosing(true);
    window.setTimeout(onCloseParent, SLIDE_MS);
  }, [onCloseParent]);

  return { closing, requestClose };
}

export type DrawerRenderApi = {
  /** Triggers the slide-out animation. After it finishes, `onClose` fires. */
  requestClose: () => void;
};

type Props = {
  /**
   * Called once the slide-out animation completes. The parent should
   * unmount the `<Drawer>` at that point. Use `requestClose` from the
   * render-prop (or rely on scrim/Escape) to start the animation — calling
   * `onClose` directly would skip it.
   */
  onClose: () => void;
  ariaLabel: string;
  /** CSS width string. Default: `min(520px, 96vw)`. */
  width?: string;
  /** When false, scrim click does not close. Default: true. */
  closeOnScrim?: boolean;
  /** When false, Escape does not close. Default: true. */
  closeOnEscape?: boolean;
  children: ReactNode | ((api: DrawerRenderApi) => ReactNode);
};

export function Drawer({
  onClose,
  ariaLabel,
  width = "min(520px, 96vw)",
  closeOnScrim = true,
  closeOnEscape = true,
  children,
}: Props) {
  const { closing, requestClose } = useDrawerAnimation(onClose);

  useEffect(() => {
    if (!closeOnEscape) return undefined;
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") requestClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [requestClose, closeOnEscape]);

  const content =
    typeof children === "function" ? children({ requestClose }) : children;

  return (
    <Backdrop
      $closing={closing}
      onClick={closeOnScrim ? requestClose : undefined}
      role="presentation"
    >
      <DrawerShell
        $closing={closing}
        $width={width}
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        onClick={(event) => event.stopPropagation()}
      >
        {content}
      </DrawerShell>
    </Backdrop>
  );
}

// ─── Animations / shell ───────────────────────────────────────────────

const fadeIn = keyframes`
  from { background: rgba(8, 6, 13, 0); }
  to { background: rgba(8, 6, 13, 0.42); }
`;

const fadeOut = keyframes`
  from { background: rgba(8, 6, 13, 0.42); }
  to { background: rgba(8, 6, 13, 0); }
`;

const slideIn = keyframes`
  from { transform: translateX(100%); }
  to { transform: translateX(0); }
`;

const slideOut = keyframes`
  from { transform: translateX(0); }
  to { transform: translateX(100%); }
`;

const Backdrop = styled.div<{ $closing: boolean }>`
  position: fixed;
  inset: 0;
  z-index: 80;
  background: rgba(8, 6, 13, 0.42);
  display: flex;
  justify-content: flex-end;
  animation: ${(p) => (p.$closing ? fadeOut : fadeIn)} 200ms ease forwards;
`;

const DrawerShell = styled.aside<{ $closing: boolean; $width: string }>`
  width: ${({ $width }) => $width};
  max-width: 96vw;
  height: 100vh;
  display: flex;
  flex-direction: column;
  background: var(--afs-panel);
  border-left: 1px solid var(--afs-line);
  box-shadow: -16px 0 48px rgba(8, 6, 13, 0.32);
  transform: translateX(0);
  animation: ${(p) => (p.$closing ? slideOut : slideIn)} 220ms
    cubic-bezier(0.32, 0.72, 0.24, 1) forwards;
  will-change: transform;
`;
