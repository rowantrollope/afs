import { createFileRoute } from "@tanstack/react-router";
import { controlPlaneEndpoint } from "../foundation/api/afs";
import {
  PageDescription,
  PageStack,
  SectionCard,
  SectionHeader,
  SectionTitle,
} from "../components/afs-kit";

export const Route = createFileRoute("/docs")({ component: DocsPage });
function DocsPage() {
  const endpoint = "'" + controlPlaneEndpoint().replaceAll("'", "'\"'\"'") + "'";
  return (
    <PageStack>
      <PageDescription>
        <a
          href="https://github.com/rowantrollope/afs/blob/main/README.md"
          target="_blank"
          rel="noreferrer"
        >
          CLI guide
        </a>
        {" · "}
        <a
          href="https://github.com/rowantrollope/afs/blob/main/docs/control-plane.md"
          target="_blank"
          rel="noreferrer"
        >
          Control-plane setup
        </a>
      </PageDescription>
      <SectionCard>
        <SectionHeader>
          <SectionTitle
            title="Workspaces"
            body="One file tree and its checkpoints, stored in Redis."
          />
        </SectionHeader>
        <PageDescription>
          Connect the CLI to this console, create a workspace, then mount it in a
          local directory for your agent. The server supplies the connection
          details. If a token is required, set AFS_CONTROL_PLANE_TOKEN first.
        </PageDescription>
        <pre>{`afs auth login --url ${endpoint}
afs create my-workspace
afs mount my-workspace ~/afs/my-workspace
afs status`}</pre>
      </SectionCard>
      <SectionCard>
        <SectionHeader>
          <SectionTitle title="Checkpoints and History" />
        </SectionHeader>
        <PageDescription>
          File edits update the live tree. A checkpoint explicitly captures the
          published Redis state. Use the CLI sync barrier before saving when you
          need pending local changes included. The console does not flush other
          clients.
        </PageDescription>
        <pre>{`afs sync --wait my-workspace
afs checkpoint create my-workspace
afs checkpoint list my-workspace
afs history list my-workspace README.md`}</pre>
      </SectionCard>
      <SectionCard>
        <SectionHeader>
          <SectionTitle title="Monitor" />
        </SectionHeader>
        <PageDescription>
          Active clients appear when mounted through this control plane.
          File history and checkpoints come from Redis independently of client
          reporting. Redis shows storage, connections, and server health for the
          configured backend.
        </PageDescription>
      </SectionCard>
    </PageStack>
  );
}
