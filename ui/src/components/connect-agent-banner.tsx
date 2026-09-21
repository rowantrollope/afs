import { useState } from "react";
import styled, { keyframes } from "styled-components";
import { workspaceCLICommand } from "../foundation/workspace-commands";
import { displayWorkspaceName } from "../foundation/workspace-display";

type Props = {
  workspaceId: string;
  workspaceName: string;
  databaseId?: string;
  workspaceLabel?: string;
  agentConnected: boolean;
  onDismiss: () => void;
};
export function ConnectAgentBanner({
  workspaceName,
  databaseId,
  workspaceLabel,
  agentConnected,
  onDismiss,
}: Props) {
  const [copied, setCopied] = useState(false);
  const quoted = "'" + workspaceName.replaceAll("'", "'\"'\"'") + "'";
  const command = workspaceCLICommand(
    databaseId,
    `afs mount ${quoted} ~/afs/workspace`,
  );
  return (
    <Banner>
      <BannerHeader>
        <BannerHeaderLeft>
          <BannerTitle>
            {agentConnected ? "Agent connected" : "Connect an agent"}
          </BannerTitle>
          <BannerSubtitle>
            Mount{" "}
            <strong>
              {workspaceLabel || displayWorkspaceName(workspaceName)}
            </strong>{" "}
            using this control plane.
          </BannerSubtitle>
        </BannerHeaderLeft>
        <DismissButton type="button" onClick={onDismiss} aria-label="Dismiss">
          ×
        </DismissButton>
      </BannerHeader>
      <StepContent>
        <StepDescription>
          Your agent can work with normal files in the mounted directory. These
          commands configure the CLI and obtain the workspace connection
          automatically. If the server requires a token, set
          AFS_CONTROL_PLANE_TOKEN before login. See the{" "}
          <a
            href="https://github.com/rowantrollope/afs/blob/main/docs/control-plane.md#connect-the-cli"
            target="_blank"
            rel="noreferrer"
          >
            connection guide
          </a>
          .
        </StepDescription>
        <CodeContainer>
          <CodePre>{command}</CodePre>
          <CopyButton
            type="button"
            onClick={() => {
              void navigator.clipboard
                .writeText(command)
                .then(() => setCopied(true));
            }}
          >
            {copied ? "Copied!" : "Copy"}
          </CopyButton>
        </CodeContainer>
      </StepContent>
    </Banner>
  );
}
const fadeIn = keyframes`
  from { opacity: 0; transform: translateY(-8px); }
  to   { opacity: 1; transform: translateY(0); }
`;

const Banner = styled.div`
  position: relative;
  animation: ${fadeIn} 300ms ease;
  border: 1.5px solid var(--afs-accent, #2563eb);
  border-radius: 16px;
  background: var(--afs-panel);
  overflow: hidden;
  box-shadow: 0 0 0 3px
    color-mix(in srgb, var(--afs-accent, #2563eb) 8%, transparent);
`;

const BannerHeader = styled.div`
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 20px 24px 16px;
`;

const BannerHeaderLeft = styled.div`
  min-width: 0;
`;

const BannerTitle = styled.div`
  color: var(--afs-ink);
  font-size: 18px;
  font-weight: 700;
  line-height: 1.3;
  letter-spacing: -0.01em;
`;

const BannerSubtitle = styled.div`
  color: var(--afs-muted);
  font-size: 14px;
  line-height: 1.55;
  margin-top: 4px;
`;

const DismissButton = styled.button`
  flex-shrink: 0;
  border: none;
  background: transparent;
  color: var(--afs-muted);
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 6px;

  &:hover {
    background: var(--afs-line);
    color: var(--afs-ink);
  }
`;

const StepContent = styled.div`
  padding: 22px 24px 24px;
  animation: ${fadeIn} 200ms ease;
`;

const StepDescription = styled.p`
  margin: 0 0 14px;
  color: var(--afs-muted);
  font-size: 14px;
  line-height: 1.65;

  code {
    background: var(--afs-line);
    padding: 2px 5px;
    border-radius: 4px;
    font-size: 12.5px;
  }
`;

const CodeContainer = styled.div`
  background: #1e1e2e;
  border-radius: 10px;
  display: flex;
  flex-direction: column;
`;

const CodePre = styled.pre`
  margin: 0;
  padding: 16px 20px 12px;
  color: #cdd6f4;
  font-family: "SF Mono", "Fira Code", "Consolas", monospace;
  font-size: 13px;
  line-height: 1.6;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-all;
`;

const CopyButton = styled.button`
  align-self: flex-end;
  margin: 0 12px 12px;
  border: 1px solid rgba(255, 255, 255, 0.15);
  background: rgba(255, 255, 255, 0.08);
  color: #cdd6f4;
  font-size: 12px;
  font-weight: 600;
  padding: 5px 14px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 120ms ease;
  flex-shrink: 0;

  &:hover {
    background: rgba(255, 255, 255, 0.16);
  }
`;
