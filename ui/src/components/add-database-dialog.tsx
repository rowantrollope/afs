import { Button } from "@redis-ui/components";
import type { ClipboardEvent, FormEvent, KeyboardEvent } from "react";
import { useEffect, useId, useRef, useState } from "react";
import type { AFSDatabaseScopeRecord } from "../foundation/database-scope";
import {
  useCreateDatabaseMutation,
  useUpdateDatabaseMutation,
} from "../foundation/hooks/use-afs";
import {
  DialogActions,
  DialogBody,
  DialogCard,
  DialogCloseButton,
  DialogError,
  DialogHeader,
  DialogOverlay,
  DialogTitle,
  Field,
  FormGrid,
  TextInput,
  TwoColumnFields,
} from "./afs-kit";

type Props = {
  open: boolean;
  onClose: () => void;
  database?: AFSDatabaseScopeRecord;
};

export function AddDatabaseDialog({ open, onClose, database }: Props) {
  return open ? (
    <DatabaseForm
      key={database?.id ?? "new"}
      onClose={onClose}
      database={database}
    />
  ) : null;
}

function DatabaseForm({
  onClose,
  database: initialDatabase,
}: Omit<Props, "open">) {
  // Polling must not overwrite unsaved values or advance the revision being edited.
  const [database] = useState(initialDatabase);
  const create = useCreateDatabaseMutation();
  const update = useUpdateDatabaseMutation();
  const mutation = database ? update : create;
  const readOnly = database ? !database.canEdit : false;
  const revisionConflict =
    update.error != null &&
    "code" in update.error &&
    update.error.code === "stale_database_settings";
  const isPending = mutation.isPending;
  const fieldsDisabled = isPending || readOnly;
  const titleId = useId();
  const descriptionId = useId();
  const formRef = useRef<HTMLFormElement>(null);
  const submittingRef = useRef(false);
  const [name, setName] = useState(database?.displayName ?? "");
  const [description, setDescription] = useState(database?.description ?? "");
  const [address, setAddress] = useState(database?.endpointLabel ?? "");
  const [username, setUsername] = useState(database?.username ?? "");
  const [password, setPassword] = useState("");
  const [removePassword, setRemovePassword] = useState(false);
  const [databaseIndex, setDatabaseIndex] = useState(database?.dbIndex ?? "0");
  const [useTLS, setUseTLS] = useState(database?.useTLS ?? false);
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    const previousFocus = document.activeElement;
    formRef.current
      ?.querySelector<HTMLElement>(
        "input:not(:disabled), button:not(:disabled)",
      )
      ?.focus();
    return () => {
      if (previousFocus instanceof HTMLElement) previousFocus.focus();
    };
  }, []);

  function pasteConnection(event: ClipboardEvent<HTMLInputElement>) {
    const raw = event.clipboardData
      .getData("text/plain")
      .trim()
      .replace(/^redis-cli\s+-u\s+/, "")
      .replace(/^(['"])(.*)\1$/, "$2");
    if (!/^rediss?:\/\//i.test(raw)) return;
    event.preventDefault();
    try {
      const url = new URL(raw);
      if (
        !url.hostname ||
        !/^\/(\d+)?$|^$/.test(url.pathname) ||
        url.search ||
        url.hash
      ) {
        throw new Error("Invalid Redis connection URL.");
      }
      const parsedUsername = decodeURIComponent(url.username);
      const parsedPassword = decodeURIComponent(url.password);
      const parsedIndex = url.pathname.slice(1) || "0";
      if (!Number.isSafeInteger(Number(parsedIndex))) {
        throw new Error("Invalid database index.");
      }
      const parsedAddress = `${url.hostname}:${url.port || "6379"}`;
      setAddress(parsedAddress);
      setName((current) => current || parsedAddress);
      setUsername(parsedUsername);
      setPassword(parsedPassword);
      setRemovePassword(false);
      setDatabaseIndex(parsedIndex);
      setUseTLS(url.protocol === "rediss:");
      setFormError(null);
      if (!revisionConflict) mutation.reset();
    } catch {
      setFormError(
        "Enter a valid Redis URL, such as redis://user:password@host:6379/0.",
      );
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submittingRef.current || readOnly || revisionConflict) return;
    if (!name.trim() || !address.trim()) {
      setFormError("Enter a display name and Redis address.");
      return;
    }
    const redisDB = Number(databaseIndex);
    if (!/^\d+$/.test(databaseIndex) || !Number.isSafeInteger(redisDB)) {
      setFormError("Database index must be a non-negative whole number.");
      return;
    }
    if (address.includes("://")) {
      setFormError(
        "Use host:port for the Redis address, or paste a Redis URL to fill the connection fields.",
      );
      return;
    }
    submittingRef.current = true;
    setFormError(null);
    try {
      const connection = {
        name: name.trim(),
        description: description.trim(),
        redisAddr: address.trim(),
        redisUsername: username.trim(),
        redisDB,
        redisTLS: useTLS,
      };
      if (database) {
        await update.mutateAsync({
          ...connection,
          databaseId: database.id,
          configRevision: database.configRevision,
          ...(removePassword
            ? { redisPassword: "" }
            : password !== ""
              ? { redisPassword: password }
              : {}),
        });
      } else {
        await create.mutateAsync({ ...connection, redisPassword: password });
      }
      onClose();
    } catch {
      // Keep the connection fields available for correction and retry.
    } finally {
      submittingRef.current = false;
    }
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.stopPropagation();
      if (!submittingRef.current) onClose();
    }
    if (event.key !== "Tab") return;
    const focusable = event.currentTarget.querySelectorAll<HTMLElement>(
      "button:not(:disabled), input:not(:disabled), [tabindex='0']",
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

  const errorMessage = revisionConflict
    ? "These settings changed since you opened them. Close and reopen the database to review the latest settings before saving."
    : formError || mutation.error?.message;
  return (
    <DialogOverlay
      onKeyDown={handleKeyDown}
      onClick={(event) => {
        if (event.target === event.currentTarget && !submittingRef.current)
          onClose();
      }}
    >
      <DialogCard
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
      >
        <DialogHeader>
          <div>
            <DialogTitle id={titleId}>
              {database ? "Database settings" : "Add database"}
            </DialogTitle>
            <DialogBody id={descriptionId}>
              {database
                ? "Review and update this Redis connection. The connection is checked before saving."
                : "Connect an existing Redis database. Its connection is checked before saving."}
            </DialogBody>
          </div>
          <DialogCloseButton
            type="button"
            aria-label="Close"
            onClick={onClose}
            disabled={isPending}
          >
            ×
          </DialogCloseButton>
        </DialogHeader>
        <FormGrid ref={formRef} onSubmit={submit}>
          {database && (
            <DialogBody>
              Connection changes apply to the control plane and new mounts.
              Existing mounts keep their current connection until remounted.
            </DialogBody>
          )}
          <Field>
            Display name
            <TextInput
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
              disabled={fieldsDisabled}
              placeholder="Team Redis"
            />
          </Field>
          <Field>
            Description
            <TextInput
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              disabled={fieldsDisabled}
              placeholder="Optional description"
            />
          </Field>
          <Field>
            Redis address
            <TextInput
              value={address}
              onChange={(event) => setAddress(event.target.value)}
              onPaste={pasteConnection}
              required
              disabled={fieldsDisabled}
              autoComplete="off"
              placeholder="localhost:6379"
            />
          </Field>
          <DialogBody>
            Paste a redis:// or rediss:// URL into the address field to fill the
            connection details.
          </DialogBody>
          <TwoColumnFields>
            <Field>
              Username
              <TextInput
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                disabled={fieldsDisabled}
                autoComplete="off"
                placeholder="default"
              />
            </Field>
            <Field>
              Password
              <TextInput
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                disabled={fieldsDisabled || removePassword}
                autoComplete="new-password"
                aria-describedby={
                  database ? `${descriptionId}-password` : undefined
                }
              />
            </Field>
          </TwoColumnFields>
          {database && (
            <>
              <DialogBody id={`${descriptionId}-password`}>
                {database.hasPassword
                  ? "A password is saved. Leave this field blank to keep it, or enter a replacement."
                  : "No password is saved. Enter one if this connection requires it."}
              </DialogBody>
              {database.hasPassword && (
                <Field style={{ flexDirection: "row", alignItems: "center" }}>
                  <input
                    type="checkbox"
                    checked={removePassword}
                    onChange={(event) => {
                      setRemovePassword(event.target.checked);
                      setPassword("");
                    }}
                    disabled={fieldsDisabled}
                  />
                  Remove saved password
                </Field>
              )}
            </>
          )}
          <TwoColumnFields>
            <Field>
              Database index
              <TextInput
                type="number"
                min="0"
                step="1"
                required
                value={databaseIndex}
                onChange={(event) => setDatabaseIndex(event.target.value)}
                disabled={fieldsDisabled}
              />
            </Field>
            <Field style={{ flexDirection: "row", alignItems: "center" }}>
              <input
                type="checkbox"
                checked={useTLS}
                onChange={(event) => setUseTLS(event.target.checked)}
                disabled={fieldsDisabled}
              />
              Use TLS
            </Field>
          </TwoColumnFields>
          {errorMessage && (
            <DialogError role="alert">{errorMessage}</DialogError>
          )}
          <DialogActions style={{ justifyContent: "flex-end" }}>
            <Button
              type="button"
              variant="secondary-fill"
              onClick={onClose}
              disabled={isPending}
            >
              {readOnly ? "Close" : "Cancel"}
            </Button>
            {!readOnly && (
              <Button
                type="submit"
                disabled={
                  isPending ||
                  revisionConflict ||
                  !name.trim() ||
                  !address.trim()
                }
              >
                {database
                  ? isPending
                    ? "Saving changes…"
                    : "Save changes"
                  : isPending
                    ? "Adding database…"
                    : "Add database"}
              </Button>
            )}
          </DialogActions>
        </FormGrid>
      </DialogCard>
    </DialogOverlay>
  );
}
