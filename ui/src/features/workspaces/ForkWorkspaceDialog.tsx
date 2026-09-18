import { Button } from "@redis-ui/components";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";
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
import { useForkWorkspaceMutation } from "../../foundation/hooks/use-afs";
import type { AFSWorkspaceDetail } from "../../foundation/types/afs";

export function ForkWorkspaceDialog({
  workspace,
  open,
  onClose,
}: {
  workspace: AFSWorkspaceDetail;
  open: boolean;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const fork = useForkWorkspaceMutation();
  const resetFork = fork.reset;
  const [name, setName] = useState("");
  useEffect(() => {
    if (open) {
      setName(`${workspace.name}-copy`);
      resetFork();
    }
  }, [open, workspace.name, resetFork]);
  if (!open) return null;
  return (
    <DialogOverlay
      role="dialog"
      aria-modal="true"
      aria-labelledby="fork-workspace-title"
    >
      <DialogCard>
        <DialogHeader>
          <div>
            <DialogTitle id="fork-workspace-title">Fork workspace</DialogTitle>
            <DialogBody>
              Create an independent workspace from the latest checkpoint of{" "}
              {workspace.name}. Create a checkpoint first to include newer
              published changes.
            </DialogBody>
          </div>
          <DialogCloseButton
            aria-label="Close"
            onClick={onClose}
            disabled={fork.isPending}
          >
            ×
          </DialogCloseButton>
        </DialogHeader>
        <FormGrid
          onSubmit={(event) => {
            event.preventDefault();
            if (!name.trim() || fork.isPending) return;
            fork.mutate(
              {
                databaseId: workspace.databaseId,
                workspaceId: workspace.id,
                name,
              },
              {
                onSuccess: (created) => {
                  onClose();
                  void navigate({
                    to: "/workspaces/$workspaceId",
                    params: { workspaceId: created?.id ?? name.trim() },
                    search: { databaseId: workspace.databaseId },
                  });
                },
              },
            );
          }}
        >
          <Field>
            Workspace name
            <TextInput
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          {fork.error ? (
            <DialogError role="alert">{fork.error.message}</DialogError>
          ) : null}
          <DialogActions style={{ justifyContent: "flex-end" }}>
            <Button
              type="button"
              size="medium"
              variant="secondary-fill"
              disabled={fork.isPending}
              onClick={onClose}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              size="medium"
              disabled={!name.trim() || fork.isPending}
            >
              {fork.isPending ? "Creating…" : "Fork workspace"}
            </Button>
          </DialogActions>
        </FormGrid>
      </DialogCard>
    </DialogOverlay>
  );
}
