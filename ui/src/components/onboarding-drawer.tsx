import { Check, Copy } from "lucide-react";
import { useEffect, useState } from "react";
import styled from "styled-components";
import type { CommandsDrawerConfig } from "../foundation/drawer-context";
import { Drawer } from "./drawer";

const DrawerHeader = styled.div`
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 20px 22px 14px;
  border-bottom: 1px solid var(--afs-line);
`;

const DrawerTitleStack = styled.div`
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
`;

const DrawerEyebrow = styled.div`
  color: var(--afs-accent);
  font-size: 11px;
  font-weight: 800;
  letter-spacing: 0.14em;
  text-transform: uppercase;
`;

const DrawerTitle = styled.h2`
  margin: 0;
  color: var(--afs-ink);
  font-size: 22px;
  font-weight: 750;
  line-height: 1.2;
  letter-spacing: -0.01em;
  overflow-wrap: anywhere;
`;

const DrawerSubline = styled.p`
  margin: 0;
  color: var(--afs-muted);
  font-size: 13.5px;
  line-height: 1.5;
`;

const CloseButton = styled.button`
  flex: 0 0 auto;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  border: 1px solid var(--afs-line);
  background: transparent;
  color: var(--afs-muted);
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  transition:
    background 120ms ease,
    color 120ms ease;

  &:hover {
    background: var(--afs-bg-soft);
    color: var(--afs-ink);
  }
`;

const DrawerBody = styled.div`
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 18px 22px 28px;
  display: flex;
  flex-direction: column;
  gap: 16px;
`;

const BodyStack = styled.section`
  display: flex;
  flex-direction: column;
  gap: 14px;
`;

// Status chip
const StepCommandBlock = styled.div`
  position: relative;
  padding: 10px 44px 10px 14px;
  border-radius: 8px;
  background: #0d1117;
  border: 1px solid #1f2937;
  font-family: var(--afs-mono, "SF Mono", "Fira Code", monospace);
  font-size: 12.5px;
  line-height: 1.55;
  overflow-x: auto;
`;

const StepCommandText = styled.code`
  color: #4ade80;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  text-shadow: 0 0 6px rgba(74, 222, 128, 0.22);
`;

const StepCopyButton = styled.button`
  position: absolute;
  top: 6px;
  right: 6px;
  width: 28px;
  height: 28px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  border: 1px solid rgba(255, 255, 255, 0.18);
  background: transparent;
  color: #9ca3af;
  cursor: pointer;
  transition:
    background 120ms ease,
    color 120ms ease,
    border-color 120ms ease;

  &:hover {
    background: rgba(74, 222, 128, 0.12);
    color: #4ade80;
    border-color: #4ade80;
  }
`;

// Path card (peer choice)
export function CommandsDrawer({
  title,
  subline,
  prompts,
  tips,
  sections,
  onClose,
}: CommandsDrawerConfig & {
  onClose: () => void;
}) {
  return (
    <Drawer onClose={onClose} ariaLabel={title}>
      {({ requestClose }) => (
        <>
          <DrawerHeader>
            <DrawerTitleStack>
              <DrawerEyebrow>
                {prompts ? "Agent help" : "Commands"}
              </DrawerEyebrow>
              <DrawerTitle>{title}</DrawerTitle>
              {subline ? <DrawerSubline>{subline}</DrawerSubline> : null}
            </DrawerTitleStack>
            <CloseButton
              type="button"
              onClick={requestClose}
              aria-label="Close"
            >
              ×
            </CloseButton>
          </DrawerHeader>

          <DrawerBody>
            {prompts?.length ? (
              <BodyStack aria-label="Suggested agent prompts">
                <GroupTitle>Suggested agent prompts</GroupTitle>
                <CommandDescription>
                  Copy a prompt into your agent to get help with this page.
                </CommandDescription>
                {prompts.map(({ prompt, ...section }) => (
                  <CopySection
                    key={prompt}
                    {...section}
                    text={prompt}
                    kind="prompt"
                  />
                ))}
              </BodyStack>
            ) : null}
            {tips?.length ? (
              <Guidance aria-label="Page guidance">
                <GroupTitle>What to know</GroupTitle>
                {tips.map((tip) => (
                  <p key={tip}>{tip}</p>
                ))}
              </Guidance>
            ) : null}
            <BodyStack aria-label="AFS command line">
              {prompts ? <GroupTitle>AFS command line</GroupTitle> : null}
              {sections.map(({ command, ...section }) => (
                <CopySection
                  key={`${section.title}:${command}`}
                  {...section}
                  text={command}
                  kind="command"
                />
              ))}
            </BodyStack>
          </DrawerBody>
        </>
      )}
    </Drawer>
  );
}

function CopySection({
  title,
  description,
  text,
  kind,
}: {
  title: string;
  description?: string;
  text: string;
  kind: "prompt" | "command";
}) {
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">(
    "idle",
  );

  useEffect(() => {
    if (copyState === "idle") return;
    const timer = window.setTimeout(() => setCopyState("idle"), 2500);
    return () => window.clearTimeout(timer);
  }, [copyState]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopyState("copied");
    } catch {
      setCopyState("error");
    }
  }

  return (
    <CommandRow>
      <CommandHeader>
        <CommandTitle>{title}</CommandTitle>
        {description ? (
          <CommandDescription>{description}</CommandDescription>
        ) : null}
      </CommandHeader>
      <CopyBlock $prompt={kind === "prompt"}>
        {kind === "prompt" ? (
          <PromptText>{text}</PromptText>
        ) : (
          <StepCommandText>{text}</StepCommandText>
        )}
        <StepCopyButton
          type="button"
          onClick={copy}
          aria-label={`Copy ${kind}: ${title}`}
          title={`Copy ${kind}`}
        >
          {copyState === "copied" ? (
            <Check size={14} strokeWidth={2.4} />
          ) : (
            <Copy size={14} strokeWidth={2.4} />
          )}
        </StepCopyButton>
      </CopyBlock>
      <CopyStatus role="status">
        {copyState === "copied"
          ? "Copied"
          : copyState === "error"
            ? "Could not copy. Select the text and copy it manually."
            : ""}
      </CopyStatus>
    </CommandRow>
  );
}

const CommandRow = styled.div`
  display: flex;
  flex-direction: column;
  gap: 6px;
`;

const CommandHeader = styled.div`
  display: flex;
  flex-direction: column;
  gap: 2px;
`;

const CommandTitle = styled.div`
  color: var(--afs-ink);
  font-size: 14px;
  font-weight: 700;
  letter-spacing: -0.01em;
`;

const CommandDescription = styled.div`
  color: var(--afs-muted);
  font-size: 12.5px;
  line-height: 1.45;
`;

const GroupTitle = styled.h3`
  margin: 0;
  color: var(--afs-ink);
  font-size: 16px;
  font-weight: 750;
`;

const CopyBlock = styled(StepCommandBlock)<{ $prompt: boolean }>`
  ${({ $prompt }) =>
    $prompt &&
    `
    background: var(--afs-bg-soft);
    border-color: var(--afs-line);
    button {
      color: var(--afs-muted);
      border-color: var(--afs-line);
    }
  `}
`;

const PromptText = styled.p`
  margin: 0;
  color: var(--afs-ink);
  font-family: var(--afs-sans, inherit);
  font-size: 14px;
  line-height: 1.6;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
`;

const Guidance = styled.section`
  padding: 14px;
  border: 1px solid var(--afs-line);
  border-radius: 8px;
  color: var(--afs-muted);
  font-size: 13px;
  line-height: 1.5;
  p {
    margin: 8px 0 0;
  }
`;

const CopyStatus = styled.span`
  color: var(--afs-muted);
  font-size: 12px;
  &:empty {
    display: none;
  }
`;
