export type AFSWorkspaceSource = "blank" | "git-import" | "cloud-import";
export type AFSClientMode = "http";
export type AFSAuthMode = "none" | "token";
export type AFSWorkspaceView = "head" | `checkpoint:${string}` | "working-copy";
export type AFSTreeItemKind = "file" | "dir" | "symlink";

export type AFSAuthUser = {
  subject: string;
  name?: string;
  email?: string;
  groups?: string[];
};

export type AFSProductMode = "self-hosted";

export type AFSAuthConfig = {
  mode: AFSAuthMode;
  enabled: boolean;
  provider: string;
  signInRequired: boolean;
  authenticated: boolean;
  productMode: AFSProductMode;
  user?: AFSAuthUser;
};

export type AFSServerVersion = {
  version: string;
  commit?: string;
  buildDate?: string;
};

export type AFSFile = {
  language: string;
  modifiedAt: string;
  path: string;
  content: string;
};

export type AFSWorkspaceCapabilities = {
  browseHead: boolean;
  browseCheckpoints: boolean;
  browseWorkingCopy: boolean;
  editWorkingCopy: boolean;
  createCheckpoint: boolean;
  restoreCheckpoint: boolean;
};

export type AFSSavepoint = {
  kind?: string;
  source?: string;
  agentName?: string;
  agentId?: string;
  createdBy?: string;
  sessionId?: string;
  parentCheckpointId?: string;
  manifestHash?: string;
  id: string;
  name: string;
  author: string;
  createdAt: string;
  note: string;
  fileCount: number;
  folderCount: number;
  totalBytes: number;
  sizeLabel: string;
  filesSnapshot: AFSFile[];
  isHead?: boolean;
};

export type AFSActivityEvent = {
  id: string;
  workspaceId?: string;
  workspaceName?: string;
  databaseId?: string;
  databaseName?: string;
  actor: string;
  createdAt: string;
  detail: string;
  kind: string;
  path?: string;
  scope: string;
  title: string;
};

export type AFSEventEntry = {
  id: string;
  workspaceId?: string;
  workspaceName?: string;
  databaseId?: string;
  databaseName?: string;
  createdAt?: string;
  kind: string;
  op: string;
  source?: string;
  actor?: string;
  sessionId?: string;
  user?: string;
  label?: string;
  agentVersion?: string;
  hostname?: string;
  path?: string;
  prevPath?: string;
  sizeBytes?: number;
  deltaBytes?: number;
  contentHash?: string;
  prevHash?: string;
  mode?: number;
  checkpointId?: string;
  extras?: Record<string, string>;
};

export type AFSEventListResponse = {
  items: AFSEventEntry[];
  nextCursor?: string;
};

export type AFSChangelogEntry = {
  id: string;
  occurredAt?: string;
  workspaceId?: string;
  workspaceName?: string;
  databaseId?: string;
  databaseName?: string;
  sessionId?: string;
  agentId?: string;
  user?: string;
  label?: string;
  agentVersion?: string;
  op: string;
  path: string;
  prevPath?: string;
  sizeBytes?: number;
  deltaBytes?: number;
  contentHash?: string;
  prevHash?: string;
  mode?: number;
  checkpointId?: string;
  source?: string;
  fileId?: string;
  versionId?: string;
};

export type AFSChangelogResponse = {
  entries: AFSChangelogEntry[];
  nextCursor?: string;
};

export type AFSWorkspaceVersioningMode = "off" | "all" | "paths";

export type AFSWorkspaceVersioningPolicy = {
  mode: AFSWorkspaceVersioningMode;
  includeGlobs: string[];
  excludeGlobs: string[];
  maxVersionsPerFile: number;
  maxAgeDays: number;
  maxTotalBytes: number;
  largeFileCutoffBytes: number;
};

export type AFSFileVersion = {
  versionId: string;
  fileId: string;
  ordinal: number;
  path: string;
  prevPath?: string;
  op: string;
  kind: "file" | "symlink" | "tombstone";
  blobId?: string;
  contentHash?: string;
  prevHash?: string;
  sizeBytes?: number;
  deltaBytes?: number;
  mode?: number;
  target?: string;
  source?: string;
  sessionId?: string;
  agentId?: string;
  user?: string;
  checkpointIds?: string[];
  createdAt: string;
};

export type AFSFileHistoryLineage = {
  fileId: string;
  state: string;
  currentPath: string;
  versions: AFSFileVersion[];
};

export type AFSFileHistoryResponse = {
  workspaceId: string;
  path: string;
  order: "asc" | "desc";
  lineages: AFSFileHistoryLineage[];
  nextCursor?: string;
};

export type AFSFileVersionContent = {
  workspaceId: string;
  fileId: string;
  versionId: string;
  ordinal: number;
  path: string;
  kind: "file" | "symlink" | "tombstone";
  source?: string;
  content?: string;
  target?: string;
  binary?: boolean;
  encoding?: string;
  contentType?: string;
  language?: string;
  size: number;
  createdAt: string;
};

export type AFSFileVersionSelector =
  | { versionId: string }
  | { fileId: string; ordinal: number }
  | { ref: "head" | "working-copy" };

export type AFSFileVersionDiff = {
  path: string;
  from: string;
  to: string;
  binary: boolean;
  diff?: string;
};

export type AFSFileVersionRestoreResponse = {
  workspaceId: string;
  path: string;
  restoredFromVersionId: string;
  restoredFromFileId: string;
  restoredFromOrdinal: number;
  versionId: string;
  fileId: string;
};

export type AFSFileVersionUndeleteResponse = {
  workspaceId: string;
  path: string;
  undeletedFromVersionId: string;
  undeletedFromFileId: string;
  undeletedFromOrdinal: number;
  versionId: string;
  fileId: string;
};

export type AFSDiffOp = "create" | "update" | "delete" | "rename" | "metadata";

export type AFSTextDiffLine = {
  kind: "context" | "delete" | "insert";
  oldLine?: number;
  newLine?: number;
  text: string;
};

export type AFSTextDiffHunk = {
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: AFSTextDiffLine[];
};

export type AFSTextDiff = {
  available?: boolean;
  skippedReason?: string;
  language?: string;
  previousExists: boolean;
  nextExists: boolean;
  hunks?: AFSTextDiffHunk[];
};

export type AFSDiffEntry = {
  op: AFSDiffOp;
  path: string;
  previousPath?: string;
  kind?: AFSTreeItemKind;
  previousKind?: AFSTreeItemKind;
  sizeBytes?: number;
  previousSizeBytes?: number;
  deltaBytes?: number;
  textDiff?: AFSTextDiff;
};

export type AFSWorkspaceDiffResponse = {
  workspaceId: string;
  workspaceName: string;
  base: {
    view: AFSWorkspaceView;
    checkpointId?: string;
    manifestHash?: string;
    fileCount: number;
    folderCount: number;
    totalBytes: number;
  };
  head: {
    view: AFSWorkspaceView;
    checkpointId?: string;
    manifestHash?: string;
    fileCount: number;
    folderCount: number;
    totalBytes: number;
  };
  summary: {
    total: number;
    created: number;
    updated: number;
    deleted: number;
    renamed: number;
    metadataChanged: number;
    bytesAdded: number;
    bytesRemoved: number;
  };
  entries: AFSDiffEntry[];
};

export type AFSAgentSession = {
  sessionId: string;
  workspaceId: string;
  workspaceName: string;
  databaseId?: string;
  databaseName?: string;
  agentId?: string;
  agentName?: string;
  sessionName?: string;
  user?: string;
  clientKind: string;
  afsVersion: string;
  hostname: string;
  operatingSystem: string;
  localPath: string;
  label?: string;
  readonly: boolean;
  state: string;
  startedAt: string;
  lastSeenAt: string;
  leaseExpiresAt: string;
};

export type AFSTreeItem = {
  path: string;
  name: string;
  kind: AFSTreeItemKind;
  size: number;
  modifiedAt?: string;
  target?: string;
};

export type AFSTreeResponse = {
  workspaceId: string;
  view: AFSWorkspaceView;
  path: string;
  items: AFSTreeItem[];
};

export type AFSFileContent = {
  workspaceId: string;
  view: AFSWorkspaceView;
  path: string;
  kind: Exclude<AFSTreeItemKind, "dir">;
  revision: string;
  language: string;
  encoding: string;
  contentType: string;
  size: number;
  modifiedAt?: string;
  binary: boolean;
  content?: string;
  target?: string;
};

export type AFSWorkspaceContentStorageProfile =
  | "none"
  | "legacy"
  | "array"
  | "mixed";

export type AFSWorkspaceContentStorage = {
  profile: AFSWorkspaceContentStorageProfile;
  fileCount: number;
  arrayFileCount: number;
  legacyFileCount: number;
};

export type AFSWorkspaceSearchIndexStatus =
  | "ready"
  | "building"
  | "missing"
  | "unavailable"
  | "error";

export type AFSWorkspaceSearchIndex = {
  name: string;
  present: boolean;
  ready: boolean;
  status: AFSWorkspaceSearchIndexStatus;
  documentCount: number;
  percentIndexed: number;
  error?: string;
};

export type AFSDatabaseWorkspaceStorage = {
  workspaceId: string;
  workspaceName: string;
  redisKey: string;
  contentStorage: AFSWorkspaceContentStorage;
};

export type AFSWorkspace = {
  liveRootAvailable?: boolean;
  unavailableReason?: string;
  id: string;
  name: string;
  description: string;
  cloudAccount: string;
  databaseId: string;
  databaseName: string;
  databaseSupportsArrays?: boolean;
  ownerSubject?: string;
  ownerLabel?: string;
  databaseManagementType?: string;
  databaseCanEdit?: boolean;
  databaseCanDelete?: boolean;
  redisKey: string;
  region: string;
  mountedPath?: string;
  source: AFSWorkspaceSource;
  templateSlug?: string;
  createdAt: string;
  updatedAt: string;
  draftState: string;
  headSavepointId: string;
  tags: string[];
  fileCount: number;
  folderCount: number;
  totalBytes: number;
  contentStorage?: AFSWorkspaceContentStorage;
  searchIndex?: AFSWorkspaceSearchIndex;
  checkpointCount: number;
  files: AFSFile[];
  savepoints: AFSSavepoint[];
  activity: AFSActivityEvent[];
  agents: AFSAgentSession[];
  capabilities: AFSWorkspaceCapabilities;
};

export type AFSWorkspaceSummary = {
  liveRootAvailable?: boolean;
  unavailableReason?: string;
  id: string;
  name: string;
  cloudAccount: string;
  databaseId: string;
  databaseName: string;
  ownerSubject?: string;
  ownerLabel?: string;
  databaseManagementType?: string;
  databaseCanEdit?: boolean;
  databaseCanDelete?: boolean;
  redisKey: string;
  fileCount: number;
  folderCount: number;
  totalBytes: number;
  checkpointCount: number;
  lastCheckpointAt: string;
  updatedAt: string;
  region: string;
  source: AFSWorkspaceSource;
  templateSlug?: string;
};

export type AFSWorkspaceDetail = AFSWorkspace;

export type ForkWorkspaceInput = {
  databaseId?: string;
  workspaceId: string;
  name: string;
};

export type AFSRedisStats = {
  redisVersion?: string;
  usedMemoryBytes: number;
  maxMemoryBytes: number; // 0 = no limit
  fragmentationRatio: number;
  keyCount: number;
  opsPerSec: number;
  cacheHitRate: number; // 0..1 (0 if no hits/misses sampled yet)
  connectedClients: number;
  sampledAt?: string;
};

export type AFSDatabase = {
  id: string;
  name: string;
  description: string;
  ownerSubject?: string;
  ownerLabel?: string;
  managementType?: string;
  purpose?: string;
  canEdit: boolean;
  canDelete: boolean;
  canCreateWorkspaces: boolean;
  redisAddr: string;
  redisUsername: string;
  hasPassword: boolean;
  configRevision: string;
  redisDB: number;
  redisTLS: boolean;
  isDefault: boolean;
  workspaceCount: number;
  activeSessionCount: number;
  connectionError?: string;
  lastWorkspaceRefreshAt?: string;
  lastWorkspaceRefreshError?: string;
  lastSessionReconcileAt?: string;
  lastSessionReconcileError?: string;
  // AFS-specific footprint across all workspaces in this database
  afsTotalBytes: number;
  afsFileCount: number;
  supportsArrays?: boolean;
  supportsSearch?: boolean;
  workspaceStorage?: AFSDatabaseWorkspaceStorage[];
  // Redis server stats snapshot (undefined while the poller warms up or the
  // database is unreachable)
  stats?: AFSRedisStats;
};

export type AFSDatabaseListResponse = {
  items: AFSDatabase[];
};

export type CreateDatabaseInput = {
  name: string;
  description: string;
  redisAddr: string;
  redisUsername: string;
  redisPassword: string;
  redisDB: number;
  redisTLS: boolean;
};

export type UpdateDatabaseInput = Omit<CreateDatabaseInput, "redisPassword"> & {
  databaseId: string;
  configRevision: string;
  // Omit to retain the saved password; an empty string explicitly removes it.
  redisPassword?: string;
};

export type CreateWorkspaceInput = {
  databaseId?: string;
  name: string;
  description: string;
  cloudAccount?: string;
  databaseName?: string;
  region?: string;
  source: AFSWorkspaceSource;
  templateSlug?: string;
};

export type UpdateWorkspaceInput = {
  databaseId?: string;
  workspaceId: string;
  name?: string;
  description?: string;
  cloudAccount?: string;
  databaseName?: string;
  region?: string;
};

export type UpdateWorkspaceFileInput = {
  databaseId?: string;
  workspaceId: string;
  path: string;
  content: string;
  expectedRevision?: string;
};

export type CreateSavepointInput = {
  databaseId?: string;
  workspaceId: string;
  name: string;
  note: string;
};

export type RestoreSavepointInput = {
  databaseId?: string;
  workspaceId: string;
  savepointId: string;
};

export type GetWorkspaceTreeInput = {
  databaseId?: string;
  workspaceId: string;
  view: AFSWorkspaceView;
  path: string;
  depth?: number;
};

export type GetWorkspaceFileContentInput = {
  databaseId?: string;
  workspaceId: string;
  view: AFSWorkspaceView;
  path: string;
};

export type GetWorkspaceDiffInput = {
  databaseId?: string;
  workspaceId: string;
  base: AFSWorkspaceView;
  head: AFSWorkspaceView;
};

export type GetWorkspaceVersioningPolicyInput = {
  databaseId?: string;
  workspaceId: string;
};

export type GetWorkspaceConfigInput = {
  databaseId?: string;
  workspaceId: string;
};

export type UpdateWorkspaceVersioningPolicyInput =
  GetWorkspaceVersioningPolicyInput & {
    policy: AFSWorkspaceVersioningPolicy;
  };

export type GetWorkspaceQueryIndexStatusInput = {
  databaseId?: string;
  workspaceId: string;
  path?: string;
};

export type GetFileHistoryInput = {
  databaseId?: string;
  workspaceId: string;
  path: string;
  direction?: "asc" | "desc";
  limit?: number;
  cursor?: string;
};

export type GetFileVersionContentInput = {
  databaseId?: string;
  workspaceId: string;
  path: string;
} & (
  | { versionId: string; fileId?: never; ordinal?: never }
  | { versionId?: never; fileId: string; ordinal: number }
);

export type DiffFileVersionsInput = {
  databaseId?: string;
  workspaceId: string;
  path: string;
  from: AFSFileVersionSelector;
  to?: AFSFileVersionSelector;
};

export type RestoreFileVersionInput = {
  databaseId?: string;
  workspaceId: string;
  path: string;
} & (
  | { versionId: string; fileId?: never; ordinal?: never }
  | { versionId?: never; fileId: string; ordinal: number }
);

export type UndeleteFileVersionInput = {
  databaseId?: string;
  workspaceId: string;
  path: string;
} & (
  | { versionId?: string; fileId?: string; ordinal?: number }
  | { versionId: string; fileId?: never; ordinal?: never }
  | { versionId?: never; fileId: string; ordinal: number }
);
