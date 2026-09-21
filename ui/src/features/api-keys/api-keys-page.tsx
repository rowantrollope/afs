import { Button } from "@redis-ui/components";
import {
  infiniteQueryOptions,
  useInfiniteQuery,
  useQueryClient,
} from "@tanstack/react-query";
import type { FormEvent, KeyboardEvent, PropsWithChildren } from "react";
import { useEffect, useId, useRef, useState } from "react";
import styled from "styled-components";
import {
  DialogActions,
  DialogBody,
  DialogCard,
  DialogCloseButton,
  DialogError,
  DialogHeader,
  DialogOverlay,
  DialogTitle,
  EmptyState,
  Field,
  FormGrid,
  InlineActions,
  NoticeBody,
  NoticeCard,
  NoticeTitle,
  PageDescription,
  PageStack,
  SectionCard,
  SectionHeader,
  SectionTitle,
  TextInput,
} from "../../components/afs-kit";
import { apiKeysApi } from "./api";
import type { APIKey, CreatedAPIKey } from "./api";

const keysQueryKey = ["afs", "api-keys"];
const keysQueryOptions = () =>
  infiniteQueryOptions({
    queryKey: keysQueryKey,
    queryFn: ({ pageParam }) => apiKeysApi.list(pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    refetchInterval: 30_000,
  });
const revocationMessage =
  "Revocation blocks subsequent API requests. Existing Redis mounts and credentials remain usable.";

function dateLabel(value?: string, fallback = "Never") {
  return value ? new Date(value).toLocaleString() : fallback;
}

export function APIKeysPage() {
  const client = useQueryClient();
  const query = useInfiniteQuery(keysQueryOptions());
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<APIKey | null>(null);
  const enabled = query.data?.pages[0]?.enabled === true;
  const keys = Array.from(
    new Map(
      query.data?.pages.flatMap((page) =>
        page.keys.map((key) => [key.id, key]),
      ),
    ).values(),
  );
  const refresh = () => {
    void client.invalidateQueries({ queryKey: keysQueryKey });
  };
  return (
    <PageStack>
      <NoticeCard>
        <NoticeTitle>Trusted administrator access</NoticeTitle>
        <NoticeBody>
          Every key can manage all workspaces, Redis connections, and API keys
          on this server. Keep the team token available for administration and
          recovery. {revocationMessage}
        </NoticeBody>
      </NoticeCard>
      {query.data && !enabled && (
        <NoticeCard $tone="warning" role="status">
          <NoticeTitle>API key issuance is disabled</NoticeTitle>
          <NoticeBody>
            Configure AFS_CONTROL_PLANE_TOKEN on the control-plane server to
            enable API keys, then sign in with the team token.
          </NoticeBody>
        </NoticeCard>
      )}
      <SectionCard>
        <SectionHeader>
          <SectionTitle
            title="API keys"
            body="Give each developer, agent, or integration its own named key."
          />
          <InlineActions>
            <Button
              variant="secondary-fill"
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            >
              Refresh
            </Button>
            <Button disabled={!enabled} onClick={() => setCreating(true)}>
              Create key
            </Button>
          </InlineActions>
        </SectionHeader>
        {query.error && (
          <NoticeCard $tone="danger" role="alert">
            <NoticeBody>{query.error.message}</NoticeBody>
          </NoticeCard>
        )}
        {query.isPending ? (
          <PageDescription role="status">Loading API keys…</PageDescription>
        ) : keys.length > 0 ? (
          <TableScroll>
            <KeyTable aria-label="API keys">
              <thead>
                <tr>
                  <th scope="col">Name</th>
                  <th scope="col">Status</th>
                  <th scope="col">Created</th>
                  <th scope="col">Last used</th>
                  <th scope="col">Expires</th>
                  <th scope="col">Actions</th>
                </tr>
              </thead>
              <tbody>
                {keys.map((key) => (
                  <tr key={key.id}>
                    <th scope="row">
                      <KeyName>{key.name}</KeyName>
                      <KeyID>{key.id}</KeyID>
                    </th>
                    <td>
                      <StatusBadge $status={key.status}>
                        {key.status}
                      </StatusBadge>
                      {key.revoked_at && (
                        <KeyID>Revoked {dateLabel(key.revoked_at)}</KeyID>
                      )}
                    </td>
                    <td>{dateLabel(key.created_at)}</td>
                    <td>{dateLabel(key.last_used_at, "Not yet used")}</td>
                    <td>{dateLabel(key.expires_at)}</td>
                    <td>
                      {key.status !== "revoked" && (
                        <Button
                          size="small"
                          variant="secondary-fill"
                          aria-label={`Revoke ${key.name}`}
                          disabled={!enabled}
                          onClick={() => setRevoking(key)}
                        >
                          Revoke
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </KeyTable>
          </TableScroll>
        ) : !query.error ? (
          <EmptyState>
            <NoticeTitle>No API keys yet</NoticeTitle>
            <NoticeBody>
              Create a key to connect the CLI or an integration without sharing
              the team token.
            </NoticeBody>
          </EmptyState>
        ) : null}
        {query.hasNextPage && (
          <InlineActions style={{ marginTop: 16 }}>
            <Button
              variant="secondary-fill"
              disabled={query.isFetching}
              onClick={() => void query.fetchNextPage()}
            >
              {query.isFetchingNextPage ? "Loading more…" : "Load more"}
            </Button>
          </InlineActions>
        )}
      </SectionCard>
      <PageDescription>
        To replace a key, create a new one, update the clients that use it, and
        revoke the previous key. Use an API key anywhere the CLI accepts
        AFS_CONTROL_PLANE_TOKEN or <code>afs auth login --token-stdin</code>.
      </PageDescription>
      {creating && (
        <CreateKeyDialog
          onClose={() => setCreating(false)}
          onCreated={refresh}
        />
      )}
      {revoking && (
        <RevokeKeyDialog
          apiKey={revoking}
          onClose={() => setRevoking(null)}
          onRevoked={refresh}
        />
      )}
    </PageStack>
  );
}

function KeyDialog({
  title,
  description,
  pending,
  onClose,
  children,
}: PropsWithChildren<{
  title: string;
  description: string;
  pending: boolean;
  onClose: () => void;
}>) {
  const titleId = useId();
  const bodyId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement;
    const firstField =
      dialogRef.current?.querySelector<HTMLElement>("input, select");
    (
      firstField ?? dialogRef.current?.querySelector<HTMLElement>("button")
    )?.focus();
    return () => {
      if (previous instanceof HTMLElement) previous.focus();
    };
  }, []);
  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.stopPropagation();
      if (!pending) onClose();
    }
    if (event.key !== "Tab") return;
    const focusable = event.currentTarget.querySelectorAll<HTMLElement>(
      "button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled)",
    );
    const first = Array.from(focusable).at(0);
    const last = Array.from(focusable).at(-1);
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last?.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first?.focus();
    }
  }
  return (
    <DialogOverlay onKeyDown={handleKeyDown}>
      <DialogCard
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bodyId}
      >
        <DialogHeader>
          <div>
            <DialogTitle id={titleId}>{title}</DialogTitle>
            <DialogBody id={bodyId}>{description}</DialogBody>
          </div>
          <DialogCloseButton
            type="button"
            aria-label="Close"
            disabled={pending}
            onClick={onClose}
          >
            ×
          </DialogCloseButton>
        </DialogHeader>
        {children}
      </DialogCard>
    </DialogOverlay>
  );
}

function CreateKeyDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [expiry, setExpiry] = useState("30");
  const [pending, setPending] = useState(false);
  const submitting = useRef(false);
  const [error, setError] = useState("");
  // The response contains a secret: keep it out of React Query's mutation cache
  // and browser storage. Closing this dialog destroys its only UI reference.
  const [created, setCreated] = useState<CreatedAPIKey | null>(null);
  const [copied, setCopied] = useState(false);
  const secretRef = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (created) secretRef.current?.focus();
  }, [created]);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current || created || !name.trim()) return;
    submitting.current = true;
    setPending(true);
    setError("");
    try {
      const result = await apiKeysApi.create({
        name: name.trim(),
        ...(expiry === "30"
          ? {}
          : {
              expires_at:
                expiry === "never"
                  ? ""
                  : new Date(
                      Date.now() + Number(expiry) * 86_400_000,
                    ).toISOString(),
            }),
      });
      setCreated(result);
      onCreated();
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Could not create key.",
      );
    } finally {
      submitting.current = false;
      setPending(false);
    }
  }
  async function copy() {
    if (!created) return;
    setError("");
    try {
      await navigator.clipboard.writeText(created.token);
      setCopied(true);
    } catch {
      setError("Copy was unavailable. Select and copy the key below.");
    }
  }
  return (
    <KeyDialog
      title={created ? "Save your API key" : "Create API key"}
      description={
        created
          ? "Copy this key now. You cannot view it again after closing this dialog."
          : "This key grants administrator access to all connections and workspaces."
      }
      pending={pending}
      onClose={onClose}
    >
      {created ? (
        <SecretContent>
          <Field>
            API key for {created.key.name}
            <SecretInput
              ref={secretRef}
              aria-label="New API key"
              value={created.token}
              readOnly
              rows={3}
              spellCheck={false}
              autoComplete="off"
              onFocus={(event) => event.currentTarget.select()}
            />
          </Field>
          <DialogBody>
            Store it in your secret manager. Expires:{" "}
            {dateLabel(created.key.expires_at)}.
          </DialogBody>
          {error && <DialogError role="alert">{error}</DialogError>}
          <DialogActions>
            <span role="status">{copied ? "Copied to clipboard" : ""}</span>
            <InlineActions>
              <Button variant="secondary-fill" onClick={() => void copy()}>
                Copy key
              </Button>
              <Button onClick={onClose}>Done</Button>
            </InlineActions>
          </DialogActions>
        </SecretContent>
      ) : (
        <FormGrid onSubmit={submit}>
          <Field>
            Key name
            <TextInput
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="CI deploy agent"
              maxLength={128}
              required
              disabled={pending}
              autoComplete="off"
            />
          </Field>
          <Field>
            Expiration
            <ExpirySelect
              value={expiry}
              onChange={(event) => setExpiry(event.target.value)}
              disabled={pending}
            >
              <option value="7">7 days</option>
              <option value="30">30 days</option>
              <option value="90">90 days</option>
              <option value="365">1 year</option>
              <option value="never">Never</option>
            </ExpirySelect>
          </Field>
          {error && <DialogError role="alert">{error}</DialogError>}
          <DialogActions style={{ justifyContent: "flex-end" }}>
            <Button
              type="button"
              variant="secondary-fill"
              disabled={pending}
              onClick={onClose}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !name.trim()}>
              {pending ? "Creating key…" : "Create API key"}
            </Button>
          </DialogActions>
        </FormGrid>
      )}
    </KeyDialog>
  );
}

function RevokeKeyDialog({
  apiKey,
  onClose,
  onRevoked,
}: {
  apiKey: APIKey;
  onClose: () => void;
  onRevoked: () => void;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  async function revoke() {
    if (submitting.current) return;
    submitting.current = true;
    setPending(true);
    setError("");
    try {
      await apiKeysApi.revoke(apiKey.id);
      onRevoked();
      onClose();
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Could not revoke key.",
      );
    } finally {
      submitting.current = false;
      setPending(false);
    }
  }
  return (
    <KeyDialog
      title={`Revoke ${apiKey.name}?`}
      description={`${revocationMessage} This cannot be undone. If this is your sign-in key, you will need another key or the team token to reconnect.`}
      pending={pending}
      onClose={onClose}
    >
      {error && <DialogError role="alert">{error}</DialogError>}
      <DialogActions style={{ justifyContent: "flex-end", marginTop: 24 }}>
        <Button variant="secondary-fill" disabled={pending} onClick={onClose}>
          Cancel
        </Button>
        <Button disabled={pending} onClick={() => void revoke()}>
          {pending ? "Revoking key…" : "Revoke key"}
        </Button>
      </DialogActions>
    </KeyDialog>
  );
}

const TableScroll = styled.div`
  overflow-x: auto;
`;
const KeyTable = styled.table`
  width: 100%;
  border-collapse: collapse;
  text-align: left;
  font-size: 13px;
  th,
  td {
    padding: 16px 12px;
    border-bottom: 1px solid var(--afs-line);
    vertical-align: top;
  }
  thead th {
    color: var(--afs-muted);
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    white-space: nowrap;
  }
  td {
    color: var(--afs-ink-soft);
    line-height: 1.5;
  }
  tbody tr:last-child th,
  tbody tr:last-child td {
    border-bottom: 0;
  }
`;
const KeyName = styled.span`
  display: block;
  min-width: 150px;
  max-width: 260px;
  color: var(--afs-ink);
  overflow-wrap: anywhere;
`;
const KeyID = styled.span`
  display: block;
  margin-top: 5px;
  color: var(--afs-muted);
  font: 11px / 1.4 var(--afs-mono);
  overflow-wrap: anywhere;
`;
const StatusBadge = styled.span<{ $status: APIKey["status"] }>`
  display: inline-block;
  padding: 3px 8px;
  border: 1px solid var(--afs-line-strong);
  border-radius: 6px;
  color: ${({ $status }) =>
    $status === "active" ? "var(--afs-accent)" : "var(--afs-muted)"};
  font-size: 11px;
  text-transform: capitalize;
`;
const ExpirySelect = styled.select`
  width: 100%;
  min-height: 44px;
  padding: 12px;
  border: 1px solid var(--afs-line);
  border-radius: 8px;
  color: var(--afs-ink);
  background: var(--afs-panel);
  font: inherit;
  &:focus-visible {
    outline: 2px solid var(--afs-focus);
  }
`;
const SecretContent = styled.div`
  display: grid;
  gap: 16px;
`;
const SecretInput = styled.textarea`
  width: 100%;
  resize: none;
  padding: 12px;
  border: 1px solid var(--afs-line-strong);
  border-radius: 8px;
  background: var(--afs-panel);
  color: var(--afs-ink);
  font: 13px / 1.5 var(--afs-mono);
  word-break: break-all;
  &:focus-visible {
    outline: 2px solid var(--afs-focus);
  }
`;
