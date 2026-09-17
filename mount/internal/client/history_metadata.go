package client

import (
	"context"
	"encoding/json"
	"strings"
)

// FileVersionMutationMetadata identifies the caller of an ordinary publication.
// These are optional provenance labels, not authenticated identities. Label and
// AgentVersion belong to activity; the other fields also accompany versions.
type FileVersionMutationMetadata struct {
	Source       string `json:"source,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	User         string `json:"user,omitempty"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
	Label        string `json:"label,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
}

type fileVersionMutationMetadataKey struct{}

// WithFileVersionMutationMetadata applies caller attribution to publications
// performed with this context. Values are copied so concurrent callers sharing
// a filesystem client cannot change or inherit each other's attribution.
func WithFileVersionMutationMetadata(ctx context.Context, metadata FileVersionMutationMetadata) context.Context {
	metadata.Source = strings.TrimSpace(metadata.Source)
	metadata.SessionID = strings.TrimSpace(metadata.SessionID)
	metadata.AgentID = strings.TrimSpace(metadata.AgentID)
	metadata.User = strings.TrimSpace(metadata.User)
	metadata.CheckpointID = strings.TrimSpace(metadata.CheckpointID)
	metadata.Label = strings.TrimSpace(metadata.Label)
	metadata.AgentVersion = strings.TrimSpace(metadata.AgentVersion)
	return context.WithValue(ctx, fileVersionMutationMetadataKey{}, metadata)
}

// Attribution travels in the existing publication argument, requiring no extra
// Redis command. Suppressing cache notifications must not suppress attribution
// on a version that is still being captured.
func (c *nativeClient) mutationPayload(ctx context.Context, op string, paths ...string) string {
	metadata, supplied := ctx.Value(fileVersionMutationMetadataKey{}).(FileVersionMutationMetadata)
	if !supplied || metadata == (FileVersionMutationMetadata{}) {
		return c.invalidationPayload(op, paths...)
	}
	activity := false
	value := struct {
		InvalidateEvent
		Metadata             FileVersionMutationMetadata `json:"mutation_metadata"`
		SuppressNotification bool                        `json:"suppress_notification,omitempty"`
	}{
		InvalidateEvent:      InvalidateEvent{Origin: c.originID, Op: op, Paths: paths, Activity: &activity},
		Metadata:             metadata,
		SuppressNotification: c.publishDisabled.Load(),
	}
	payload, _ := json.Marshal(value)
	return string(payload)
}
