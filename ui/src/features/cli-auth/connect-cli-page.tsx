import { Button } from "@redis-ui/components";
import { queryOptions, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Clock3, Terminal, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import styled from "styled-components";
import {
  InlineActions,
  NoticeBody,
  NoticeCard,
  NoticeTitle,
  PageStack,
  SectionCard,
} from "../../components/afs-kit";
import { browserAuthApi } from "../../foundation/api/browser-auth";
import { useAuthSession } from "../../foundation/auth-context";

export const validCLIRequestID = (value: string) => /^cli_[a-f0-9]{32}$/.test(value);

const cliRequestOptions = (requestId: string) => queryOptions({
  queryKey: ["afs", "auth", "cli-request", requestId],
  queryFn: () => browserAuthApi.getCLIRequest(requestId),
});

export function ConnectCLIPage({ requestId }: { requestId: string }) {
  // A new request always gets new confirmation and mutation state, even during
  // client-side navigation between two approval URLs.
  return <CLIRequest key={requestId} requestId={requestId} />;
}

function CLIRequest({ requestId }: { requestId: string }) {
  const { config } = useAuthSession();
  const client = useQueryClient();
  const [confirmedCode, setConfirmedCode] = useState("");
  const [pending, setPending] = useState<"approve" | "deny" | null>(null);
  const [error, setError] = useState("");
  const [now, setNow] = useState(Date.now());
  const submitting = useRef(false);
  const validID = validCLIRequestID(requestId);
  const supported = config.browserLogin === true;
  const queryKey = cliRequestOptions(requestId).queryKey;
  const request = useQuery({
    ...cliRequestOptions(requestId),
    enabled: supported && validID,
    retry: false,
    refetchInterval: (query) => !pending && query.state.data?.status === "pending" ? 3000 : false,
    refetchOnWindowFocus: true,
  });
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const data = request.data;
  const matchesRequest = data?.id === requestId;
  const confirmed = !!confirmedCode && confirmedCode === data?.user_code;
  const expiry = data ? Date.parse(data.expires_at) : NaN;
  const secondsLeft = Math.max(0, Math.ceil((expiry - now) / 1000));
  const status = data?.status === "pending" && (!Number.isFinite(expiry) || expiry <= now)
    ? "expired"
    : data?.status;
  const canDecide = matchesRequest && status === "pending" && !pending && !request.error;

  async function decide(decision: "approve" | "deny") {
    if (!data || !canDecide || submitting.current || (decision === "approve" && !confirmed)) return;
    submitting.current = true;
    setPending(decision);
    setError("");
    try {
      // An older in-flight poll must not overwrite the decision response.
      await client.cancelQueries({ queryKey });
      const result = await browserAuthApi.decideCLIRequest(requestId, decision, data.user_code);
      client.setQueryData(queryKey, { ...data, status: result.status });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not update this request. Please try again.");
      // Another tab may have already decided, or the terminal request expired.
      void request.refetch();
    } finally {
      submitting.current = false;
      setPending(null);
    }
  }

  let title = "Connect your terminal";
  let message = "";
  let icon = <Terminal aria-hidden="true" size={24} />;
  if (!validID) {
    title = "This connection link is incomplete";
    message = "Run afs auth login again and open the new link from your terminal.";
  } else if (!supported) {
    title = "Browser login is unavailable";
    message = "This control plane does not support browser login. Update the server or use afs auth login --token-stdin.";
  } else if (request.isPending) {
    title = "Finding your terminal…";
  } else if (request.error) {
    const httpStatus = (request.error as Error & { status?: number }).status;
    title = httpStatus === 404 || httpStatus === 410 ? "This connection link is no longer available" : "Could not load this request";
    message = httpStatus === 404 || httpStatus === 410
      ? "Run afs auth login again and open the new link from your terminal."
      : request.error.message;
  } else if (!matchesRequest) {
    title = "Could not verify this request";
    message = "Run afs auth login again and open the new link from your terminal.";
  } else if (status === "approved" || status === "consumed") {
    title = status === "consumed" ? "Your CLI is connected" : "Your CLI is approved";
    message = "Return to your terminal to continue. You can close this tab.";
    icon = <Check aria-hidden="true" size={26} />;
  } else if (status === "denied") {
    title = "Connection declined";
    message = "This terminal was not granted access. You can close this tab.";
    icon = <X aria-hidden="true" size={26} />;
  } else if (status === "expired") {
    title = "This request has expired";
    message = "Run afs auth login again to get a new connection link and code.";
    icon = <Clock3 aria-hidden="true" size={26} />;
  }
  const showApproval = validID && supported && matchesRequest && status === "pending" && !request.error;
  return (
    <ApprovalPage>
      <Brand>AFS <span>/</span> CLI access</Brand>
      <ApprovalCard>
        <IconBadge>{icon}</IconBadge>
        <Heading>{title}</Heading>
        {message && <Description role="status">{message}</Description>}
        {showApproval && (
          <ApprovalBody>
            <Description>
              <ClientName>{data.name}</ClientName> is requesting access to this control plane.
            </Description>
            <CodePanel>
              <CodeLabel>Match this code in your terminal</CodeLabel>
              <UserCode aria-label={`Verification code ${data.user_code}`}>{data.user_code}</UserCode>
              <Expiry>Expires in {Math.floor(secondsLeft / 60)}:{String(secondsLeft % 60).padStart(2, "0")}</Expiry>
            </CodePanel>
            <NoticeCard>
              <NoticeTitle>Administrator access</NoticeTitle>
              <NoticeBody>
                Connecting creates a separate API key for this CLI, valid for up to 30 days.
                It can manage all registered Redis connections, workspaces, and API keys.
                You can revoke it from API Keys at any time.
              </NoticeBody>
            </NoticeCard>
            <Confirmation>
              <input
                type="checkbox"
                checked={confirmed}
                disabled={!!pending}
                onChange={(event) => setConfirmedCode(event.target.checked ? data.user_code : "")}
              />
              <span>I started this login and the code matches my terminal.</span>
            </Confirmation>
            {error && <NoticeCard $tone="danger" role="alert"><NoticeBody>{error}</NoticeBody></NoticeCard>}
            <Actions>
              <Button variant="secondary-fill" disabled={!canDecide} onClick={() => void decide("deny")}>
                {pending === "deny" ? "Declining…" : "Decline"}
              </Button>
              <Button disabled={!canDecide || !confirmed} onClick={() => void decide("approve")}>
                {pending === "approve" ? "Connecting…" : "Connect CLI"}
              </Button>
            </Actions>
          </ApprovalBody>
        )}
        {request.error && validID && supported && (
          <Actions style={{ marginTop: 24 }}>
            <Button variant="secondary-fill" onClick={() => void request.refetch()} disabled={request.isFetching}>Try again</Button>
          </Actions>
        )}
        {(status === "approved" || status === "consumed") && <ConsoleLink href="/api-keys">Manage API keys →</ConsoleLink>}
      </ApprovalCard>
      {showApproval && <Footnote>Only connect a terminal you recognize.</Footnote>}
    </ApprovalPage>
  );
}

const ApprovalPage = styled(PageStack)`
  max-width: 600px;
  min-height: 100dvh;
  justify-content: center;
  padding-top: 48px;
  padding-bottom: 64px;
`;
const Brand = styled.div`
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 12px;
  color: var(--afs-ink);
  font-size: 13px;
  font-weight: 600;
  span { color: var(--afs-muted); }
`;
const ApprovalCard = styled(SectionCard)`
  padding: 32px;
  @media (max-width: 600px) { padding: 24px; }
`;
const IconBadge = styled.div`
  display: flex;
  width: 48px;
  height: 48px;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--afs-line);
  border-radius: 12px;
  background: var(--afs-panel);
  color: var(--afs-accent);
  margin-bottom: 20px;
`;
const Heading = styled.h1`
  margin: 0;
  color: var(--afs-ink);
  font-size: 26px;
  line-height: 1.25;
  font-weight: 600;
  letter-spacing: -0.025em;
`;
const Description = styled.p`
  margin: 12px 0 0;
  font-size: 14px;
  line-height: 1.6;
  color: var(--afs-muted);
  overflow-wrap: anywhere;
`;
const ClientName = styled.strong`
  color: var(--afs-ink);
  font-weight: 600;
`;
const ApprovalBody = styled.div`
  display: grid;
  gap: 22px;
`;
const CodePanel = styled.div`
  padding: 22px 12px;
  text-align: center;
  border: 1px solid var(--afs-line);
  border-radius: 10px;
  background: var(--afs-panel);
`;
const CodeLabel = styled.div`
  color: var(--afs-muted);
  font-size: 12px;
`;
const UserCode = styled.div`
  margin: 12px 0;
  font: 600 clamp(24px, 6vw, 34px) / 1.2 var(--afs-mono);
  letter-spacing: 0.1em;
  color: var(--afs-ink);
`;
const Expiry = styled.div`
  color: var(--afs-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
`;
const Confirmation = styled.label`
  display: flex;
  gap: 12px;
  align-items: flex-start;
  color: var(--afs-ink-soft);
  font-size: 13px;
  line-height: 1.6;
  cursor: pointer;
  input { margin-top: 4px; width: 16px; height: 16px; flex-shrink: 0; accent-color: var(--afs-accent); }
`;
const Actions = styled(InlineActions)`
  justify-content: flex-end;
  flex-wrap: wrap;
  gap: 12px;
`;
const Footnote = styled.div`
  text-align: center;
  color: var(--afs-muted);
  font-size: 12px;
`;
const ConsoleLink = styled.a`
  display: inline-block;
  margin-top: 24px;
  color: var(--afs-accent);
  font-size: 13px;
`;
