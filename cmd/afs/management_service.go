package main

import (
	"context"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/filehistory"
)

// managementService keeps CLI behavior identical whether the engine is local
// to this process or reached through the configured control plane. File mounts
// have their own direct Redis connection; management commands do not need one.
type managementService interface {
	GetWorkspace(context.Context, string) (controlplane.WorkspaceMeta, error)
	ListWorkspaces(context.Context) ([]controlplane.WorkspaceMeta, error)
	CreateWorkspace(context.Context, string) (controlplane.WorkspaceMeta, error)
	ImportWorkspace(context.Context, string, func(controlplane.BlobSink) (controlplane.Manifest, error)) (controlplane.WorkspaceMeta, error)
	ForkWorkspace(context.Context, string, string, string) error
	DeleteWorkspace(context.Context, string) error
	WorkspaceGeneration(context.Context, string) (string, error)
	SaveCheckpointFromLive(context.Context, string, string) (controlplane.SavepointMeta, error)
	ListCheckpoints(context.Context, string) ([]controlplane.SavepointMeta, error)
	GetCheckpoint(context.Context, string, string) (controlplane.SavepointMeta, controlplane.Manifest, error)
	RestoreCheckpoint(context.Context, string, string) (controlplane.RestoreCheckpointResult, error)
	DeleteCheckpoint(context.Context, string, string) error
	GetFileHistoryPage(context.Context, string, controlplane.FileHistoryRequest) (controlplane.FileHistoryResponse, error)
	GetFileContent(context.Context, string, string, string) (controlplane.FileVersionContentResponse, error)
	GetFileVersionContent(context.Context, string, string) (controlplane.FileVersionContentResponse, error)
	GetFileVersionContentAtOrdinal(context.Context, string, string, int64) (controlplane.FileVersionContentResponse, error)
	DiffFileVersions(context.Context, string, string, controlplane.FileVersionDiffOperand, controlplane.FileVersionDiffOperand) (controlplane.FileVersionDiffResponse, error)
	RestoreFileVersion(context.Context, string, string, controlplane.FileVersionSelector) (controlplane.FileVersionRestoreResponse, error)
	UndeleteFileVersion(context.Context, string, string, controlplane.FileVersionSelector) (controlplane.FileVersionUndeleteResponse, error)
	GetFileHistoryPolicy(context.Context, string) (filehistory.Policy, error)
	UpdateFileHistoryPolicy(context.Context, string, controlplane.FileHistoryPolicyPatch) (filehistory.Policy, error)
	PruneFileHistory(context.Context, string, int) (filehistory.PruneResult, error)
	GetFileHistoryExport(context.Context, string, string, string, string) (filehistory.Record, []byte, error)
}

var (
	_ managementService = (*controlplane.Service)(nil)
	_ managementService = (*controlplane.CLIClient)(nil)
)

func (a *app) managedMode() bool {
	return a.options.redisURL == "" && a.config.ControlPlane != nil && a.config.ControlPlane.URL != ""
}
