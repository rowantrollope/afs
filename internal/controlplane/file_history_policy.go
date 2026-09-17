package controlplane

import (
	"context"

	"github.com/rowantrollope/afs/internal/filehistory"
)

const (
	WorkspaceVersioningModeOff   = filehistory.ModeOff
	WorkspaceVersioningModeAll   = filehistory.ModeAll
	WorkspaceVersioningModePaths = filehistory.ModePaths
)

type WorkspaceVersioningPolicy struct {
	Mode                 string   `json:"mode"`
	IncludeGlobs         []string `json:"include_globs,omitempty"`
	ExcludeGlobs         []string `json:"exclude_globs,omitempty"`
	MaxVersionsPerFile   int      `json:"max_versions_per_file,omitempty"`
	MaxAgeDays           int      `json:"max_age_days,omitempty"`
	MaxTotalBytes        int64    `json:"max_total_bytes,omitempty"`
	LargeFileCutoffBytes int64    `json:"large_file_cutoff_bytes,omitempty"`
}

func DefaultWorkspaceVersioningPolicy() WorkspaceVersioningPolicy {
	return WorkspaceVersioningPolicy{Mode: filehistory.ModeOff}
}

func (policy WorkspaceVersioningPolicy) storagePolicy() filehistory.Policy {
	return filehistory.Policy{Mode: policy.Mode, Include: policy.IncludeGlobs, Exclude: policy.ExcludeGlobs,
		MaxVersions: policy.MaxVersionsPerFile, MaxAgeDays: policy.MaxAgeDays, MaxBytes: policy.MaxTotalBytes, MaxFileBytes: policy.LargeFileCutoffBytes}
}

func compatibleVersioningPolicy(policy filehistory.Policy) WorkspaceVersioningPolicy {
	return WorkspaceVersioningPolicy{Mode: policy.Mode, IncludeGlobs: policy.Include, ExcludeGlobs: policy.Exclude,
		MaxVersionsPerFile: policy.MaxVersions, MaxAgeDays: policy.MaxAgeDays, MaxTotalBytes: policy.MaxBytes, LargeFileCutoffBytes: policy.MaxFileBytes}
}

func NormalizeWorkspaceVersioningPolicy(policy WorkspaceVersioningPolicy) WorkspaceVersioningPolicy {
	return compatibleVersioningPolicy(policy.storagePolicy().Normalize())
}

func ValidateWorkspaceVersioningPolicy(policy WorkspaceVersioningPolicy) error {
	return policy.storagePolicy().Validate()
}

func WorkspaceVersioningPolicyTracksPath(policy WorkspaceVersioningPolicy, rawPath string) (bool, error) {
	name, err := filehistory.NormalizePath(rawPath)
	if err != nil {
		return false, err
	}
	storage := policy.storagePolicy()
	if err := storage.Validate(); err != nil {
		return false, err
	}
	return storage.Matches(name), nil
}

func (s *Service) GetWorkspaceVersioningPolicy(ctx context.Context, workspace string) (WorkspaceVersioningPolicy, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return WorkspaceVersioningPolicy{}, err
	}
	policy, err := filehistory.GetPolicy(ctx, s.store.rdb, id)
	return compatibleVersioningPolicy(policy), err
}

func (s *Service) UpdateWorkspaceVersioningPolicy(ctx context.Context, workspace string, policy WorkspaceVersioningPolicy) (WorkspaceVersioningPolicy, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return WorkspaceVersioningPolicy{}, err
	}
	committed, err := filehistory.UpdatePolicy(ctx, s.store.rdb, id, func(filehistory.Policy) (filehistory.Policy, error) {
		return policy.storagePolicy().Normalize(), nil
	})
	return compatibleVersioningPolicy(committed), err
}
