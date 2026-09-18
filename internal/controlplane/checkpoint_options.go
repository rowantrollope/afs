package controlplane

import "context"

type CheckpointOptions struct {
	Description, Source, Author string
	AllowUnchanged              bool
}
type checkpointOptionsKey struct{}

// SaveCheckpointFromLiveWithOptions snapshots published Redis state. It never
// flushes client-local files; the journal/head/generation guards reject races.
func (s *Service) SaveCheckpointFromLiveWithOptions(ctx context.Context, workspace, name string, options CheckpointOptions) (SavepointMeta, error) {
	generation, err := s.WorkspaceGeneration(ctx, workspace)
	if err != nil {
		return SavepointMeta{}, err
	}
	ctx = context.WithValue(ctx, checkpointOptionsKey{}, options)
	cp, _, err := s.captureCheckpoint(ctx, workspace, name, generation, "", options.AllowUnchanged)
	return cp, err
}
