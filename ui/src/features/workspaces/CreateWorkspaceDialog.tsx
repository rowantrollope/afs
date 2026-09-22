import { Button, Select } from "@redis-ui/components";
import { Link } from "@tanstack/react-router";
import type { FormEvent } from "react";
import { useState } from "react";
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
} from "../../components/afs-kit";
import { useCreateWorkspaceMutation } from "../../foundation/hooks/use-afs";
import { useDatabaseScope } from "../../foundation/database-scope";

type Props = { open: boolean; onClose: () => void; resourceLabel?: string };
export function CreateWorkspaceDialog({ open, onClose }: Props) {
  return open ? <CreateWorkspaceForm onClose={onClose} /> : null;
}
function CreateWorkspaceForm({ onClose }: { onClose: () => void }) {
  const create = useCreateWorkspaceMutation();
  const { databases, isLoading } = useDatabaseScope();
  const eligibleDatabases = databases.filter(
    (database) => database.canCreateWorkspaces && database.isHealthy,
  );
  const [selectedDatabaseId, setSelectedDatabaseId] = useState("");
  const selectedDatabase = selectedDatabaseId
    ? eligibleDatabases.find((database) => database.id === selectedDatabaseId)
    : (eligibleDatabases.find((database) => database.isDefault) ??
      eligibleDatabases.at(0));
  const selectionUnavailable = Boolean(selectedDatabaseId && !selectedDatabase);
  const databaseOptions = eligibleDatabases.map((database) => ({
    value: database.id,
    label: `${database.displayName}${database.isDefault ? " (default)" : ""}`,
  }));
  if (selectionUnavailable) {
    databaseOptions.push({
      value: selectedDatabaseId,
      label: "Selected database (unavailable)",
    });
  }
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!selectedDatabase || create.isPending || !name.trim()) return;
    try {
      await create.mutateAsync({
        databaseId: selectedDatabase.id,
        name: name.trim(),
        description: description.trim(),
        cloudAccount: "Direct Redis",
        databaseName: selectedDatabase.databaseName,
        region: "",
        source: "blank",
      });
      onClose();
    } catch {
      /* Mutation error is rendered below. */
    }
  }
  return (
    <DialogOverlay
      onClick={(event) => {
        if (event.target === event.currentTarget && !create.isPending)
          onClose();
      }}
    >
      <DialogCard>
        <DialogHeader>
          <div>
            <DialogTitle>Create Workspace</DialogTitle>
            <DialogBody>
              Create an empty workspace, then add files from a mounted
              directory.
            </DialogBody>
          </div>
          <DialogCloseButton
            type="button"
            aria-label="Close"
            onClick={onClose}
            disabled={create.isPending}
          >
            ×
          </DialogCloseButton>
        </DialogHeader>
        <FormGrid onSubmit={submit}>
          <Field>
            Database
            <Select
              aria-label="Database"
              options={databaseOptions}
              value={selectedDatabaseId || selectedDatabase?.id || ""}
              onChange={setSelectedDatabaseId}
              isDisabled={
                create.isPending || isLoading || eligibleDatabases.length === 0
              }
            />
          </Field>
          {selectionUnavailable && (
            <DialogError role="alert">
              The selected database is unavailable. Choose an available database
              to continue.
            </DialogError>
          )}
          {!isLoading && eligibleDatabases.length === 0 && (
            <>
              <DialogError role="alert">
                {databases.length === 0
                  ? "Add a Redis database before creating a workspace."
                  : "No Redis database is available. Review your connections before creating a workspace."}
              </DialogError>
              <Link to="/databases" onClick={onClose}>
                {databases.length === 0
                  ? "Add database"
                  : "Review database connections"}
              </Link>
            </>
          )}
          <Field>
            Workspace name
            <TextInput
              autoFocus
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
              placeholder="my-workspace"
            />
          </Field>
          <Field>
            Description
            <TextInput
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="Optional description"
            />
          </Field>
          {create.error && (
            <DialogError role="alert">{create.error.message}</DialogError>
          )}
          <DialogActions style={{ justifyContent: "flex-end" }}>
            <Button
              type="button"
              variant="secondary-fill"
              onClick={onClose}
              disabled={create.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={
                create.isPending ||
                !name.trim() ||
                !selectedDatabase ||
                isLoading
              }
            >
              {create.isPending ? "Creating…" : "Create workspace"}
            </Button>
          </DialogActions>
        </FormGrid>
      </DialogCard>
    </DialogOverlay>
  );
}
