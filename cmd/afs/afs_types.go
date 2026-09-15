package main

import (
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/worktree"
)

const (
	afsFormatVersion   = controlplane.FormatVersion
	afsInlineThreshold = controlplane.InlineThreshold
)

type afsLocalState = worktree.LocalState

type workspaceMeta = controlplane.WorkspaceMeta
type savepointMeta = controlplane.SavepointMeta
type manifest = controlplane.Manifest
type manifestEntry = controlplane.ManifestEntry
type blobRef = controlplane.BlobRef

type manifestStats = worktree.ManifestStats

type workspaceBlobStats = controlplane.BlobStats

func validateAFSName(kind, value string) error { return controlplane.ValidateName(kind, value) }

type importStats = worktree.ImportStats
