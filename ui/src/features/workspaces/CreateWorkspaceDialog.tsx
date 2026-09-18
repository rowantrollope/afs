import { Button } from "@redis-ui/components";
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

type Props = { open: boolean; onClose: () => void; resourceLabel?: string };
export function CreateWorkspaceDialog({ open, onClose }: Props) {
  return open ? <CreateWorkspaceForm onClose={onClose} /> : null;
}
function CreateWorkspaceForm({ onClose }: { onClose: () => void }) {
  const create = useCreateWorkspaceMutation();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    try {
      await create.mutateAsync({
        databaseId: "local",
        name: name.trim(),
        description: description.trim(),
        cloudAccount: "Direct Redis",
        databaseName: "Redis",
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
            <Button type="submit" disabled={create.isPending || !name.trim()}>
              {create.isPending ? "Creating…" : "Create workspace"}
            </Button>
          </DialogActions>
        </FormGrid>
      </DialogCard>
    </DialogOverlay>
  );
}
