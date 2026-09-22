import type {
  AFSActivityEvent,
  AFSAgentSession,
  AFSAuthConfig,
  AFSChangelogEntry,
  AFSChangelogResponse,
  AFSClientMode,
  AFSDatabase,
  AFSDatabaseListResponse,
  AFSEventEntry,
  AFSEventListResponse,
  AFSFileContent,
  AFSFileHistoryResponse,
  AFSFileVersionContent,
  AFSFileVersionDiff,
  AFSFileVersionRestoreResponse,
  AFSFileVersionUndeleteResponse,
  AFSSavepoint,
  AFSServerVersion,
  AFSTreeItem,
  AFSTreeResponse,
  AFSWorkspaceCapabilities,
  AFSWorkspaceDetail,
  AFSWorkspaceDiffResponse,
  AFSWorkspaceSource,
  AFSWorkspaceSummary,
  AFSWorkspaceVersioningPolicy,
  AFSWorkspaceView,
  CreateDatabaseInput,
  CreateSavepointInput,
  CreateWorkspaceInput,
  DiffFileVersionsInput,
  ForkWorkspaceInput,
  GetFileHistoryInput,
  GetFileVersionContentInput,
  GetWorkspaceDiffInput,
  GetWorkspaceFileContentInput,
  GetWorkspaceTreeInput,
  GetWorkspaceVersioningPolicyInput,
  RestoreFileVersionInput,
  RestoreSavepointInput,
  UndeleteFileVersionInput,
  UpdateDatabaseInput,
  UpdateWorkspaceFileInput,
  UpdateWorkspaceInput,
  UpdateWorkspaceVersioningPolicyInput,
} from "../types/afs";
import { authorizationHeaders, notifyUnauthorized } from "./session-token";

const HTTP_REQUEST_TIMEOUT_MS = 8000;

type AFSClient = {
  mode: AFSClientMode;
  listDatabases: () => Promise<AFSDatabase[]>;
  createDatabase: (input: CreateDatabaseInput) => Promise<AFSDatabase>;
  updateDatabase: (input: UpdateDatabaseInput) => Promise<AFSDatabase>;
  deleteDatabase: (databaseId: string) => Promise<void>;
  setDefaultDatabase: (databaseId: string) => Promise<AFSDatabase>;
  listWorkspaceSummaries: (
    databaseId?: string,
  ) => Promise<AFSWorkspaceSummary[]>;
  getWorkspace: (
    databaseId: string | undefined,
    workspaceId: string,
  ) => Promise<AFSWorkspaceDetail | null>;
  listAgents: (databaseId?: string) => Promise<AFSAgentSession[]>;
  createWorkspace: (input: CreateWorkspaceInput) => Promise<AFSWorkspaceDetail>;
  forkWorkspace: (
    input: ForkWorkspaceInput,
  ) => Promise<AFSWorkspaceDetail | null>;
  deleteWorkspace: (databaseId: string, workspaceId: string) => Promise<void>;
  updateWorkspace: (
    input: UpdateWorkspaceInput,
  ) => Promise<AFSWorkspaceDetail | null>;
  updateWorkspaceFile: (
    input: UpdateWorkspaceFileInput,
  ) => Promise<AFSWorkspaceDetail | null>;
  getWorkspaceVersioningPolicy: (
    input: GetWorkspaceVersioningPolicyInput,
  ) => Promise<AFSWorkspaceVersioningPolicy>;
  updateWorkspaceVersioningPolicy: (
    input: UpdateWorkspaceVersioningPolicyInput,
  ) => Promise<AFSWorkspaceVersioningPolicy>;
  getFileHistory: (
    input: GetFileHistoryInput,
  ) => Promise<AFSFileHistoryResponse>;
  getFileVersionContent: (
    input: GetFileVersionContentInput,
  ) => Promise<AFSFileVersionContent | null>;
  diffFileVersions: (
    input: DiffFileVersionsInput,
  ) => Promise<AFSFileVersionDiff>;
  restoreFileVersion: (
    input: RestoreFileVersionInput,
  ) => Promise<AFSFileVersionRestoreResponse>;
  undeleteFileVersion: (
    input: UndeleteFileVersionInput,
  ) => Promise<AFSFileVersionUndeleteResponse>;
  createSavepoint: (
    input: CreateSavepointInput,
  ) => Promise<AFSWorkspaceDetail | null>;
  restoreSavepoint: (
    input: RestoreSavepointInput,
  ) => Promise<AFSWorkspaceDetail | null>;
  listActivity: (
    databaseId?: string,
    limit?: number,
  ) => Promise<AFSActivityEvent[]>;
  listEvents: (input: ListEventsInput) => Promise<AFSEventListResponse>;
  listChangelog: (input: ListChangelogInput) => Promise<AFSChangelogResponse>;
  getWorkspaceTree: (input: GetWorkspaceTreeInput) => Promise<AFSTreeResponse>;
  getWorkspaceFileContent: (
    input: GetWorkspaceFileContentInput,
  ) => Promise<AFSFileContent | null>;
  getWorkspaceDiff: (
    input: GetWorkspaceDiffInput,
  ) => Promise<AFSWorkspaceDiffResponse>;
  getAuthConfig: () => Promise<AFSAuthConfig>;
  getServerVersion: () => Promise<AFSServerVersion>;
};

export type ListChangelogInput = {
  databaseId?: string;
  workspaceId?: string;
  sessionId?: string;
  path?: string;
  since?: string;
  until?: string;
  limit?: number;
  direction?: "asc" | "desc";
};

export type ListActivityInput = {
  databaseId?: string;
  workspaceId?: string;
  limit?: number;
  until?: string;
};

export type ListEventsInput = {
  databaseId?: string;
  workspaceId?: string;
  kind?: string;
  sessionId?: string;
  path?: string;
  since?: string;
  until?: string;
  limit?: number;
  direction?: "asc" | "desc";
};

type HTTPChangelogEntry = {
  id: string;
  occurred_at?: string;
  workspace_id?: string;
  workspace_name?: string;
  database_id?: string;
  database_name?: string;
  session_id?: string;
  agent_id?: string;
  user?: string;
  label?: string;
  agent_version?: string;
  op: string;
  path: string;
  prev_path?: string;
  size_bytes?: number;
  delta_bytes?: number;
  content_hash?: string;
  prev_hash?: string;
  mode?: number;
  checkpoint_id?: string;
  source?: string;
  file_id?: string;
  version_id?: string;
};

type HTTPChangelogResponse = {
  entries: HTTPChangelogEntry[];
  next_cursor?: string;
};

type HTTPRedisStats = {
  redis_version?: string;
  used_memory_bytes?: number;
  max_memory_bytes?: number;
  fragmentation_ratio?: number;
  key_count?: number;
  ops_per_sec?: number;
  cache_hit_rate?: number;
  connected_clients?: number;
  sampled_at?: string;
};

type HTTPDatabaseWorkspaceStorage = {
  workspace_id: string;
  workspace_name: string;
  redis_key: string;
  content_storage: HTTPWorkspaceContentStorage;
};

type HTTPDatabase = {
  id: string;
  name: string;
  description?: string;
  owner_subject?: string;
  owner_label?: string;
  management_type?: string;
  purpose?: string;
  can_edit?: boolean;
  can_delete?: boolean;
  can_create_workspaces?: boolean;
  redis_addr: string;
  redis_username?: string;
  has_password?: boolean;
  config_revision?: string;
  redis_db: number;
  redis_tls: boolean;
  is_default: boolean;
  workspace_count: number;
  active_session_count?: number;
  connection_error?: string;
  last_workspace_refresh_at?: string;
  last_workspace_refresh_error?: string;
  last_session_reconcile_at?: string;
  last_session_reconcile_error?: string;
  afs_total_bytes?: number;
  afs_file_count?: number;
  supports_arrays?: boolean;
  supports_search?: boolean;
  workspace_storage?: HTTPDatabaseWorkspaceStorage[] | null;
  stats?: HTTPRedisStats;
};

type HTTPWorkspaceSummary = {
  live_root_available?: boolean;
  unavailable_reason?: string;
  id: string;
  name: string;
  cloud_account: string;
  database_id: string;
  database_name: string;
  owner_subject?: string;
  owner_label?: string;
  database_management_type?: string;
  database_can_edit?: boolean;
  database_can_delete?: boolean;
  redis_key: string;
  file_count: number;
  folder_count: number;
  total_bytes: number;
  checkpoint_count: number;
  last_checkpoint_at: string;
  updated_at: string;
  region: string;
  source: AFSWorkspaceSource;
  template_slug?: string;
};

type HTTPCheckpoint = {
  kind?: string;
  source?: string;
  created_by?: string;
  session_id?: string;
  agent_id?: string;
  agent_name?: string;
  parent_checkpoint_id?: string;
  manifest_hash?: string;
  id: string;
  name: string;
  author?: string;
  note?: string;
  created_at: string;
  file_count: number;
  folder_count: number;
  total_bytes: number;
  is_head?: boolean;
};

type HTTPActivity = {
  id: string;
  workspace_id?: string;
  workspace_name?: string;
  database_id?: string;
  database_name?: string;
  actor: string;
  created_at: string;
  detail: string;
  kind: string;
  path?: string;
  scope: string;
  title: string;
};

type HTTPWorkspaceCapabilities = {
  browse_head: boolean;
  browse_checkpoints: boolean;
  browse_working_copy: boolean;
  edit_working_copy: boolean;
  create_checkpoint: boolean;
  restore_checkpoint: boolean;
};

type HTTPWorkspaceContentStorage = {
  profile: "none" | "legacy" | "array" | "mixed";
  file_count: number;
  array_file_count: number;
  legacy_file_count: number;
};

type HTTPWorkspaceSearchIndex = {
  name: string;
  present: boolean;
  ready: boolean;
  status: "ready" | "building" | "missing" | "unavailable" | "error";
  document_count?: number;
  percent_indexed?: number;
  error?: string;
};

type HTTPWorkspaceDetail = {
  live_root_available?: boolean;
  unavailable_reason?: string;
  id: string;
  name: string;
  description?: string;
  cloud_account: string;
  database_id: string;
  database_name: string;
  database_supports_arrays?: boolean;
  owner_subject?: string;
  owner_label?: string;
  database_management_type?: string;
  database_can_edit?: boolean;
  database_can_delete?: boolean;
  redis_key: string;
  region: string;
  source: AFSWorkspaceSource;
  template_slug?: string;
  created_at: string;
  updated_at: string;
  draft_state: string;
  head_checkpoint_id: string;
  tags?: string[];
  file_count: number;
  folder_count: number;
  total_bytes: number;
  content_storage?: HTTPWorkspaceContentStorage;
  search_index?: HTTPWorkspaceSearchIndex;
  checkpoint_count: number;
  checkpoints: HTTPCheckpoint[];
  activity: HTTPActivity[];
  capabilities: HTTPWorkspaceCapabilities;
};

type HTTPEventEntry = {
  id: string;
  workspace_id?: string;
  workspace_name?: string;
  database_id?: string;
  database_name?: string;
  created_at?: string;
  kind: string;
  op: string;
  source?: string;
  actor?: string;
  session_id?: string;
  user?: string;
  label?: string;
  agent_version?: string;
  hostname?: string;
  path?: string;
  prev_path?: string;
  size_bytes?: number;
  delta_bytes?: number;
  content_hash?: string;
  prev_hash?: string;
  mode?: number;
  checkpoint_id?: string;
  extras?: Record<string, string>;
};

type HTTPEventList = {
  items: HTTPEventEntry[];
  next_cursor?: string;
};

type HTTPWorkspaceSessionInfo = {
  session_id: string;
  workspace: string;
  workspace_id?: string;
  workspace_name?: string;
  database_id?: string;
  database_name?: string;
  agent_id?: string;
  agent_name?: string;
  session_name?: string;
  user?: string;
  client_kind?: string;
  afs_version?: string;
  hostname?: string;
  os?: string;
  local_path?: string;
  label?: string;
  readonly?: boolean;
  state: string;
  started_at: string;
  last_seen_at: string;
  lease_expires_at: string;
};

type HTTPWorkspaceSessionList = {
  items: HTTPWorkspaceSessionInfo[];
};

type HTTPTreeItem = {
  path: string;
  name: string;
  kind: AFSTreeItem["kind"];
  size: number;
  modified_at?: string;
  target?: string;
};

type HTTPTreeResponse = {
  workspace_id: string;
  view: AFSWorkspaceView;
  path: string;
  items: HTTPTreeItem[];
};

type HTTPFileContent = {
  workspace_id: string;
  view: AFSWorkspaceView;
  path: string;
  kind: AFSFileContent["kind"];
  revision: string;
  language: string;
  encoding: string;
  content_type: string;
  size: number;
  modified_at?: string;
  binary: boolean;
  content?: string;
  target?: string;
};

type HTTPDiffState = {
  view: AFSWorkspaceView;
  checkpoint_id?: string;
  manifest_hash?: string;
  file_count: number;
  folder_count: number;
  total_bytes: number;
};

type HTTPDiffSummary = {
  total: number;
  created: number;
  updated: number;
  deleted: number;
  renamed: number;
  metadata_changed: number;
  bytes_added: number;
  bytes_removed: number;
};

type HTTPTextDiffLine = {
  kind: "context" | "delete" | "insert";
  old_line?: number;
  new_line?: number;
  text: string;
};

type HTTPTextDiffHunk = {
  old_start: number;
  old_lines: number;
  new_start: number;
  new_lines: number;
  lines: HTTPTextDiffLine[];
};

type HTTPDiffEntry = {
  op: AFSWorkspaceDiffResponse["entries"][number]["op"];
  path: string;
  previous_path?: string;
  kind?: AFSWorkspaceDiffResponse["entries"][number]["kind"];
  previous_kind?: AFSWorkspaceDiffResponse["entries"][number]["previousKind"];
  size_bytes?: number;
  previous_size_bytes?: number;
  delta_bytes?: number;
  text_diff?: {
    available?: boolean;
    skipped_reason?: string;
    language?: string;
    previous_exists: boolean;
    next_exists: boolean;
    hunks?: HTTPTextDiffHunk[];
  };
};

type HTTPWorkspaceDiffResponse = {
  workspace_id: string;
  workspace_name: string;
  base: HTTPDiffState;
  head: HTTPDiffState;
  summary: HTTPDiffSummary;
  entries: HTTPDiffEntry[];
};

type HTTPWorkspaceVersioningPolicy = {
  mode?: AFSWorkspaceVersioningPolicy["mode"];
  include_globs?: string[];
  exclude_globs?: string[];
  max_versions_per_file?: number;
  max_age_days?: number;
  max_total_bytes?: number;
  large_file_cutoff_bytes?: number;
};

type HTTPFileVersion = {
  version_id: string;
  file_id: string;
  ordinal: number;
  path: string;
  prev_path?: string;
  op: string;
  kind: "file" | "symlink" | "tombstone";
  blob_id?: string;
  content_hash?: string;
  prev_hash?: string;
  size_bytes?: number;
  delta_bytes?: number;
  mode?: number;
  target?: string;
  source?: string;
  session_id?: string;
  agent_id?: string;
  user?: string;
  checkpoint_ids?: string[];
  created_at: string;
};

type HTTPFileHistoryLineage = {
  file_id: string;
  state: string;
  current_path: string;
  versions: HTTPFileVersion[];
};

type HTTPFileHistoryResponse = {
  workspace_id: string;
  path: string;
  order: "asc" | "desc";
  lineages: HTTPFileHistoryLineage[];
  next_cursor?: string;
};

type HTTPFileVersionContent = {
  workspace_id: string;
  file_id: string;
  version_id: string;
  ordinal: number;
  path: string;
  kind: "file" | "symlink" | "tombstone";
  source?: string;
  content?: string;
  target?: string;
  binary?: boolean;
  encoding?: string;
  content_type?: string;
  language?: string;
  size: number;
  created_at: string;
};

type HTTPFileVersionSelector = {
  ref?: "head" | "working-copy";
  version_id?: string;
  file_id?: string;
  ordinal?: number;
};

type HTTPFileVersionDiff = {
  workspace_id: string;
  path: string;
  from: string;
  to: string;
  binary: boolean;
  diff?: string;
};

type HTTPFileVersionRestoreResponse = {
  workspace_id: string;
  path: string;
  dirty: boolean;
  file_id?: string;
  version_id?: string;
  restored_from_version_id?: string;
  restored_from_file_id?: string;
  restored_from_ordinal?: number;
};

type HTTPFileVersionUndeleteResponse = {
  workspace_id: string;
  path: string;
  dirty: boolean;
  file_id?: string;
  version_id?: string;
  undeleted_from_version_id?: string;
  undeleted_from_file_id?: string;
  undeleted_from_ordinal?: number;
};

type HTTPAuthConfig = {
  mode: string;
  enabled: boolean;
  provider: string;
  sign_in_required: boolean;
  authenticated: boolean;
  product_mode?: string;
  user?: {
    subject: string;
    name?: string;
    email?: string;
    groups?: string[];
  };
};

class HTTPError extends Error {
  status: number;
  code?: string;

  constructor(status: number, message: string, code?: string) {
    super(message);
    this.name = "HTTPError";
    this.status = status;
    this.code = code;
  }
}

const HTTP_BASE_URL = (
  import.meta.env.VITE_AFS_API_BASE_URL?.replace(/\/+$/, "") ?? ""
).trim();

export function controlPlaneEndpoint(databaseId?: string) {
  const endpoint = (
    HTTP_BASE_URL ||
    (import.meta.env.DEV ? "http://127.0.0.1:8091" : window.location.origin)
  ).replace(/\/$/, "");
  return databaseId && databaseId !== "local"
    ? `${endpoint}/databases/${encodeURIComponent(databaseId)}`
    : endpoint;
}

export function monitorStreamURL() {
  return `${HTTP_BASE_URL}/v1/monitor/stream`;
}

function requestPath(path: string) {
  const normalized = path.startsWith("/") ? path : `/${path}`;
  if (/^\/v\d+(?:[/:?]|$)/.test(normalized)) {
    return normalized;
  }
  return `/v1${normalized}`;
}

function bytesLabelForValue(value: number) {
  if (value >= 1024 * 1024) {
    return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  }

  return `${Math.max(1, Math.round(value / 1024))} KB`;
}

export async function requestJSON<T>(
  path: string,
  init?: RequestInit,
): Promise<T> {
  const url = `${HTTP_BASE_URL}${requestPath(path)}`;
  const controller = new AbortController();
  const timeout = window.setTimeout(
    () => controller.abort(),
    HTTP_REQUEST_TIMEOUT_MS,
  );
  let response: Response;
  try {
    response = await fetch(url, {
      ...init,
      signal: controller.signal,
      headers: {
        "Content-Type": "application/json",
        ...authorizationHeaders(),
        ...(init?.headers ?? {}),
      },
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      throw new Error(
        `Request to ${path} timed out after ${HTTP_REQUEST_TIMEOUT_MS / 1000}s.`,
      );
    }
    throw error;
  } finally {
    window.clearTimeout(timeout);
  }

  const rawBody = response.status === 204 ? "" : await response.text();

  if (!response.ok) {
    if (response.status === 401 && path !== "/auth/config")
      notifyUnauthorized();
    let message = `Request failed with status ${response.status}`;
    let code: string | undefined;
    try {
      const payload = JSON.parse(rawBody) as { error?: string; code?: string };
      if (payload.error) {
        message = payload.error;
      }
      if (typeof payload.code === "string") code = payload.code;
    } catch {
      if (rawBody.trim() !== "") {
        message = rawBody;
      }
    }
    throw new HTTPError(response.status, message, code);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  try {
    return JSON.parse(rawBody) as T;
  } catch (error) {
    const contentType = response.headers.get("content-type") ?? "unknown";
    const body = rawBody.replace(/\s+/g, " ").trim();
    throw new Error(
      `Expected JSON from ${url}, but received ${contentType} (status ${response.status}). Body: ${body || "<empty>"}`,
      { cause: error },
    );
  }
}

function workspaceBasePath(
  databaseId: string | undefined,
  workspaceId: string,
) {
  const resolvedDatabaseID = databaseId?.trim() ?? "";
  if (resolvedDatabaseID === "") {
    return `/workspaces/${workspaceId}`;
  }
  return `/databases/${resolvedDatabaseID}/workspaces/${workspaceId}`;
}

function mapCapabilities(
  input: HTTPWorkspaceCapabilities,
): AFSWorkspaceCapabilities {
  return {
    browseHead: input.browse_head,
    browseCheckpoints: input.browse_checkpoints,
    browseWorkingCopy: input.browse_working_copy,
    editWorkingCopy: input.edit_working_copy,
    createCheckpoint: input.create_checkpoint,
    restoreCheckpoint: input.restore_checkpoint,
  };
}

function mapDatabase(input: HTTPDatabase): AFSDatabase {
  return {
    id: input.id,
    name: input.name,
    description: input.description ?? "",
    ownerSubject: input.owner_subject,
    ownerLabel: input.owner_label,
    managementType: input.management_type,
    purpose: input.purpose,
    canEdit: input.can_edit ?? true,
    canDelete: input.can_delete ?? true,
    canCreateWorkspaces: input.can_create_workspaces ?? true,
    redisAddr: input.redis_addr,
    redisUsername: input.redis_username ?? "",
    hasPassword: input.has_password ?? false,
    configRevision: input.config_revision ?? "",
    redisDB: input.redis_db,
    redisTLS: input.redis_tls,
    isDefault: input.is_default,
    workspaceCount: input.workspace_count,
    activeSessionCount: input.active_session_count ?? 0,
    connectionError: input.connection_error,
    lastWorkspaceRefreshAt: input.last_workspace_refresh_at,
    lastWorkspaceRefreshError: input.last_workspace_refresh_error,
    lastSessionReconcileAt: input.last_session_reconcile_at,
    lastSessionReconcileError: input.last_session_reconcile_error,
    afsTotalBytes: input.afs_total_bytes ?? 0,
    afsFileCount: input.afs_file_count ?? 0,
    supportsArrays: input.supports_arrays,
    supportsSearch: input.supports_search,
    workspaceStorage:
      input.workspace_storage == null
        ? undefined
        : input.workspace_storage.map((workspace) => ({
            workspaceId: workspace.workspace_id,
            workspaceName: workspace.workspace_name,
            redisKey: workspace.redis_key,
            contentStorage: {
              profile: workspace.content_storage.profile,
              fileCount: workspace.content_storage.file_count,
              arrayFileCount: workspace.content_storage.array_file_count,
              legacyFileCount: workspace.content_storage.legacy_file_count,
            },
          })),
    stats: input.stats
      ? {
          redisVersion: input.stats.redis_version,
          usedMemoryBytes: input.stats.used_memory_bytes ?? 0,
          maxMemoryBytes: input.stats.max_memory_bytes ?? 0,
          fragmentationRatio: input.stats.fragmentation_ratio ?? 0,
          keyCount: input.stats.key_count ?? 0,
          opsPerSec: input.stats.ops_per_sec ?? 0,
          cacheHitRate: input.stats.cache_hit_rate ?? 0,
          connectedClients: input.stats.connected_clients ?? 0,
          sampledAt: input.stats.sampled_at,
        }
      : undefined,
  };
}

function mapActivity(
  input: HTTPActivity,
  database?: { databaseId?: string; databaseName?: string },
): AFSActivityEvent {
  return {
    id: input.id,
    workspaceId: input.workspace_id,
    workspaceName: input.workspace_name,
    databaseId: input.database_id ?? database?.databaseId,
    databaseName: input.database_name ?? database?.databaseName,
    actor: input.actor,
    createdAt: input.created_at,
    detail: input.detail,
    kind: input.kind,
    path: input.path,
    scope: input.scope,
    title: input.title,
  };
}

function mapEventEntry(
  input: HTTPEventEntry,
  scope?: {
    workspaceId?: string;
    workspaceName?: string;
    databaseId?: string;
    databaseName?: string;
  },
): AFSEventEntry {
  return {
    id: input.id,
    workspaceId: input.workspace_id ?? scope?.workspaceId,
    workspaceName: input.workspace_name ?? scope?.workspaceName,
    databaseId: input.database_id ?? scope?.databaseId,
    databaseName: input.database_name ?? scope?.databaseName,
    createdAt: input.created_at,
    kind: input.kind,
    op: input.op,
    source: input.source,
    actor: input.actor,
    sessionId: input.session_id,
    user: input.user,
    label: input.label,
    agentVersion: input.agent_version,
    hostname: input.hostname,
    path: input.path,
    prevPath: input.prev_path,
    sizeBytes: input.size_bytes,
    deltaBytes: input.delta_bytes,
    contentHash: input.content_hash,
    prevHash: input.prev_hash,
    mode: input.mode,
    checkpointId: input.checkpoint_id,
    extras: input.extras,
  };
}

function mapEventList(
  response: HTTPEventList,
  scope?: {
    workspaceId?: string;
    workspaceName?: string;
    databaseId?: string;
    databaseName?: string;
  },
): AFSEventListResponse {
  return {
    items: response.items.map((item) => mapEventEntry(item, scope)),
    nextCursor: response.next_cursor,
  };
}

function mapCheckpoint(input: HTTPCheckpoint): AFSSavepoint {
  return {
    kind: input.kind,
    source: input.source,
    createdBy: input.created_by,
    sessionId: input.session_id,
    agentId: input.agent_id,
    agentName: input.agent_name,
    parentCheckpointId: input.parent_checkpoint_id,
    manifestHash: input.manifest_hash,
    id: input.id,
    name: input.name,
    author: input.author ?? "afs",
    createdAt: input.created_at,
    note: input.note ?? "",
    fileCount: input.file_count,
    folderCount: input.folder_count,
    totalBytes: input.total_bytes,
    sizeLabel: bytesLabelForValue(input.total_bytes),
    filesSnapshot: [],
    isHead: input.is_head,
  };
}

function mapChangelogEntry(
  input: HTTPChangelogEntry,
  scope?: {
    workspaceId?: string;
    workspaceName?: string;
    databaseId?: string;
    databaseName?: string;
  },
): AFSChangelogEntry {
  return {
    id: input.id,
    occurredAt: input.occurred_at,
    workspaceId: input.workspace_id ?? scope?.workspaceId,
    workspaceName: input.workspace_name ?? scope?.workspaceName,
    databaseId: input.database_id ?? scope?.databaseId,
    databaseName: input.database_name ?? scope?.databaseName,
    sessionId: input.session_id,
    agentId: input.agent_id,
    user: input.user,
    label: input.label,
    agentVersion: input.agent_version,
    op: input.op,
    path: input.path,
    prevPath: input.prev_path,
    sizeBytes: input.size_bytes,
    deltaBytes: input.delta_bytes,
    contentHash: input.content_hash,
    prevHash: input.prev_hash,
    mode: input.mode,
    checkpointId: input.checkpoint_id,
    source: input.source,
    fileId: input.file_id,
    versionId: input.version_id,
  };
}

function changelogSearchParams(input: ListChangelogInput): URLSearchParams {
  const params = new URLSearchParams();
  if (input.limit != null && input.limit > 0) {
    params.set("limit", String(input.limit));
  }
  if (input.sessionId) {
    params.set("session_id", input.sessionId);
  }
  if (input.path) {
    params.set("path", input.path);
  }
  if (input.since) {
    params.set("since", input.since);
  }
  if (input.until) {
    params.set("until", input.until);
  }
  if (input.direction) {
    params.set("direction", input.direction);
  }
  return params;
}

function eventSearchParams(input: ListEventsInput): URLSearchParams {
  const params = new URLSearchParams();
  if (input.limit != null && input.limit > 0) {
    params.set("limit", String(input.limit));
  }
  if (input.kind) {
    params.set("kind", input.kind);
  }
  if (input.sessionId) {
    params.set("session_id", input.sessionId);
  }
  if (input.path) {
    params.set("path", input.path);
  }
  if (input.since) {
    params.set("since", input.since);
  }
  if (input.until) {
    params.set("until", input.until);
  }
  if (input.direction) {
    params.set("direction", input.direction);
  }
  return params;
}

function mapAgentSession(
  input: HTTPWorkspaceSessionInfo,
  workspaceId: string,
  workspaceName: string,
  databaseId?: string,
  databaseName?: string,
): AFSAgentSession {
  return {
    sessionId: input.session_id,
    workspaceId: input.workspace_id ?? workspaceId,
    workspaceName: input.workspace_name ?? workspaceName,
    databaseId: input.database_id ?? databaseId,
    databaseName: input.database_name ?? databaseName,
    agentId: input.agent_id,
    agentName: input.agent_name,
    sessionName: input.session_name,
    user: input.user,
    clientKind: input.client_kind ?? "",
    afsVersion: input.afs_version ?? "",
    hostname: input.hostname ?? "",
    operatingSystem: input.os ?? "",
    localPath: input.local_path ?? "",
    label: input.label,
    readonly: input.readonly ?? false,
    state: input.state,
    startedAt: input.started_at,
    lastSeenAt: input.last_seen_at,
    leaseExpiresAt: input.lease_expires_at,
  };
}

function mapWorkspaceSummary(input: HTTPWorkspaceSummary): AFSWorkspaceSummary {
  return {
    liveRootAvailable: input.live_root_available,
    unavailableReason: input.unavailable_reason,
    id: input.id,
    name: input.name,
    cloudAccount: input.cloud_account,
    databaseId: input.database_id,
    databaseName: input.database_name,
    ownerSubject: input.owner_subject,
    ownerLabel: input.owner_label,
    databaseManagementType: input.database_management_type,
    databaseCanEdit: input.database_can_edit ?? true,
    databaseCanDelete: input.database_can_delete ?? true,
    redisKey: input.redis_key,
    fileCount: input.file_count,
    folderCount: input.folder_count,
    totalBytes: input.total_bytes,
    checkpointCount: input.checkpoint_count,
    lastCheckpointAt: input.last_checkpoint_at,
    updatedAt: input.updated_at,
    region: input.region,
    source: input.source,
    templateSlug: input.template_slug,
  };
}

function mapWorkspaceDetail(input: HTTPWorkspaceDetail): AFSWorkspaceDetail {
  return {
    liveRootAvailable: input.live_root_available,
    unavailableReason: input.unavailable_reason,
    id: input.id,
    name: input.name,
    description: input.description ?? "",
    cloudAccount: input.cloud_account,
    databaseId: input.database_id,
    databaseName: input.database_name,
    databaseSupportsArrays: input.database_supports_arrays,
    ownerSubject: input.owner_subject,
    ownerLabel: input.owner_label,
    databaseManagementType: input.database_management_type,
    databaseCanEdit: input.database_can_edit ?? true,
    databaseCanDelete: input.database_can_delete ?? true,
    redisKey: input.redis_key,
    region: input.region,
    source: input.source,
    templateSlug: input.template_slug,
    createdAt: input.created_at,
    updatedAt: input.updated_at,
    draftState: input.draft_state,
    headSavepointId: input.head_checkpoint_id,
    tags: input.tags ?? [],
    fileCount: input.file_count,
    folderCount: input.folder_count,
    totalBytes: input.total_bytes,
    contentStorage:
      input.content_storage == null
        ? undefined
        : {
            profile: input.content_storage.profile,
            fileCount: input.content_storage.file_count,
            arrayFileCount: input.content_storage.array_file_count,
            legacyFileCount: input.content_storage.legacy_file_count,
          },
    searchIndex:
      input.search_index == null
        ? undefined
        : {
            name: input.search_index.name,
            present: input.search_index.present,
            ready: input.search_index.ready,
            status: input.search_index.status,
            documentCount: input.search_index.document_count ?? 0,
            percentIndexed: input.search_index.percent_indexed ?? 0,
            error: input.search_index.error,
          },
    checkpointCount: input.checkpoint_count,
    files: [],
    savepoints: input.checkpoints.map(mapCheckpoint),
    activity: input.activity.map((item) =>
      mapActivity(item, {
        databaseId: input.database_id,
        databaseName: input.database_name,
      }),
    ),
    agents: [],
    capabilities: mapCapabilities(input.capabilities),
  };
}

function mapTreeResponse(input: HTTPTreeResponse): AFSTreeResponse {
  return {
    workspaceId: input.workspace_id,
    view: input.view,
    path: input.path,
    items: input.items.map((item) => ({
      path: item.path,
      name: item.name,
      kind: item.kind,
      size: item.size,
      modifiedAt: item.modified_at,
      target: item.target,
    })),
  };
}

function mapFileContent(input: HTTPFileContent): AFSFileContent {
  return {
    workspaceId: input.workspace_id,
    view: input.view,
    path: input.path,
    kind: input.kind,
    revision: input.revision,
    language: input.language,
    encoding: input.encoding,
    contentType: input.content_type,
    size: input.size,
    modifiedAt: input.modified_at,
    binary: input.binary,
    content: input.content,
    target: input.target,
  };
}

function mapWorkspaceDiff(
  input: HTTPWorkspaceDiffResponse,
): AFSWorkspaceDiffResponse {
  return {
    workspaceId: input.workspace_id,
    workspaceName: input.workspace_name,
    base: {
      view: input.base.view,
      checkpointId: input.base.checkpoint_id,
      manifestHash: input.base.manifest_hash,
      fileCount: input.base.file_count,
      folderCount: input.base.folder_count,
      totalBytes: input.base.total_bytes,
    },
    head: {
      view: input.head.view,
      checkpointId: input.head.checkpoint_id,
      manifestHash: input.head.manifest_hash,
      fileCount: input.head.file_count,
      folderCount: input.head.folder_count,
      totalBytes: input.head.total_bytes,
    },
    summary: {
      total: input.summary.total,
      created: input.summary.created,
      updated: input.summary.updated,
      deleted: input.summary.deleted,
      renamed: input.summary.renamed,
      metadataChanged: input.summary.metadata_changed,
      bytesAdded: input.summary.bytes_added,
      bytesRemoved: input.summary.bytes_removed,
    },
    entries: input.entries.map((entry) => ({
      op: entry.op,
      path: entry.path,
      previousPath: entry.previous_path,
      kind: entry.kind,
      previousKind: entry.previous_kind,
      sizeBytes: entry.size_bytes,
      previousSizeBytes: entry.previous_size_bytes,
      deltaBytes: entry.delta_bytes,
      textDiff:
        entry.text_diff == null
          ? undefined
          : {
              available: entry.text_diff.available,
              skippedReason: entry.text_diff.skipped_reason,
              language: entry.text_diff.language,
              previousExists: entry.text_diff.previous_exists,
              nextExists: entry.text_diff.next_exists,
              hunks: entry.text_diff.hunks?.map((hunk) => ({
                oldStart: hunk.old_start,
                oldLines: hunk.old_lines,
                newStart: hunk.new_start,
                newLines: hunk.new_lines,
                lines: hunk.lines.map((line) => ({
                  kind: line.kind,
                  oldLine: line.old_line,
                  newLine: line.new_line,
                  text: line.text,
                })),
              })),
            },
    })),
  };
}

function mapWorkspaceVersioningPolicy(
  input: HTTPWorkspaceVersioningPolicy,
): AFSWorkspaceVersioningPolicy {
  return {
    mode: input.mode ?? "off",
    includeGlobs: input.include_globs ?? [],
    excludeGlobs: input.exclude_globs ?? [],
    maxVersionsPerFile: input.max_versions_per_file ?? 0,
    maxAgeDays: input.max_age_days ?? 0,
    maxTotalBytes: input.max_total_bytes ?? 0,
    largeFileCutoffBytes: input.large_file_cutoff_bytes ?? 0,
  };
}

function mapFileVersion(input: HTTPFileVersion) {
  return {
    versionId: input.version_id,
    fileId: input.file_id,
    ordinal: input.ordinal,
    path: input.path,
    prevPath: input.prev_path,
    op: input.op,
    kind: input.kind,
    blobId: input.blob_id,
    contentHash: input.content_hash,
    prevHash: input.prev_hash,
    sizeBytes: input.size_bytes,
    deltaBytes: input.delta_bytes,
    mode: input.mode,
    target: input.target,
    source: input.source,
    sessionId: input.session_id,
    agentId: input.agent_id,
    user: input.user,
    checkpointIds: input.checkpoint_ids ?? [],
    createdAt: input.created_at,
  };
}

function mapFileHistoryResponse(
  input: HTTPFileHistoryResponse,
): AFSFileHistoryResponse {
  return {
    workspaceId: input.workspace_id,
    path: input.path,
    order: input.order,
    lineages: input.lineages.map((lineage) => ({
      fileId: lineage.file_id,
      state: lineage.state,
      currentPath: lineage.current_path,
      versions: lineage.versions.map(mapFileVersion),
    })),
    nextCursor: input.next_cursor,
  };
}

function mapFileVersionContent(
  input: HTTPFileVersionContent,
): AFSFileVersionContent {
  return {
    workspaceId: input.workspace_id,
    fileId: input.file_id,
    versionId: input.version_id,
    ordinal: input.ordinal,
    path: input.path,
    kind: input.kind,
    source: input.source,
    content: input.content,
    target: input.target,
    binary: input.binary,
    encoding: input.encoding,
    contentType: input.content_type,
    language: input.language,
    size: input.size,
    createdAt: input.created_at,
  };
}

function mapFileVersionSelector(
  input: DiffFileVersionsInput["from"],
): HTTPFileVersionSelector {
  if ("ref" in input) {
    return { ref: input.ref };
  }
  if ("versionId" in input) {
    return { version_id: input.versionId };
  }
  return { file_id: input.fileId, ordinal: input.ordinal };
}

function mapFileVersionDiff(input: HTTPFileVersionDiff): AFSFileVersionDiff {
  return {
    path: input.path,
    from: input.from,
    to: input.to,
    binary: input.binary,
    diff: input.diff,
  };
}

function mapFileVersionRestoreResponse(
  input: HTTPFileVersionRestoreResponse,
): AFSFileVersionRestoreResponse {
  return {
    workspaceId: input.workspace_id,
    path: input.path,
    fileId: input.file_id ?? "",
    versionId: input.version_id ?? "",
    restoredFromVersionId: input.restored_from_version_id ?? "",
    restoredFromFileId: input.restored_from_file_id ?? "",
    restoredFromOrdinal: input.restored_from_ordinal ?? 0,
  };
}

function mapFileVersionUndeleteResponse(
  input: HTTPFileVersionUndeleteResponse,
): AFSFileVersionUndeleteResponse {
  return {
    workspaceId: input.workspace_id,
    path: input.path,
    fileId: input.file_id ?? "",
    versionId: input.version_id ?? "",
    undeletedFromVersionId: input.undeleted_from_version_id ?? "",
    undeletedFromFileId: input.undeleted_from_file_id ?? "",
    undeletedFromOrdinal: input.undeleted_from_ordinal ?? 0,
  };
}

const httpAFSClient: AFSClient = {
  mode: "http",

  async listDatabases() {
    const response = await requestJSON<
      AFSDatabaseListResponse & { items: HTTPDatabase[] }
    >("/databases");
    return response.items.map(mapDatabase);
  },

  async createDatabase(input: CreateDatabaseInput) {
    return mapDatabase(
      await requestJSON<HTTPDatabase>("/databases", {
        method: "POST",
        body: JSON.stringify({
          name: input.name,
          description: input.description,
          redis_addr: input.redisAddr,
          redis_username: input.redisUsername,
          redis_password: input.redisPassword,
          redis_db: input.redisDB,
          redis_tls: input.redisTLS,
        }),
      }),
    );
  },

  async updateDatabase(input: UpdateDatabaseInput) {
    return mapDatabase(
      await requestJSON<HTTPDatabase>(
        `/databases/${encodeURIComponent(input.databaseId)}`,
        {
          method: "PUT",
          body: JSON.stringify({
            name: input.name,
            description: input.description,
            redis_addr: input.redisAddr,
            redis_username: input.redisUsername,
            redis_password: input.redisPassword,
            redis_db: input.redisDB,
            redis_tls: input.redisTLS,
            config_revision: input.configRevision,
          }),
        },
      ),
    );
  },

  async deleteDatabase(databaseId: string) {
    await requestJSON(`/databases/${encodeURIComponent(databaseId)}`, {
      method: "DELETE",
    });
  },

  async setDefaultDatabase(databaseId: string) {
    return mapDatabase(
      await requestJSON<HTTPDatabase>(
        `/databases/${encodeURIComponent(databaseId)}/default`,
        { method: "POST" },
      ),
    );
  },

  async listWorkspaceSummaries(databaseId = "") {
    const response = await requestJSON<{
      items: HTTPWorkspaceSummary[];
    }>(
      databaseId === "" ? "/workspaces" : `/databases/${databaseId}/workspaces`,
    );
    return response.items.map(mapWorkspaceSummary);
  },

  async getWorkspace(databaseId = "", workspaceId: string) {
    try {
      const basePath = workspaceBasePath(databaseId, workspaceId);
      const [detailResult, sessionsResult] = await Promise.allSettled([
        requestJSON<HTTPWorkspaceDetail>(basePath),
        requestJSON<HTTPWorkspaceSessionList>(`${basePath}/sessions`),
      ]);
      if (detailResult.status !== "fulfilled") {
        throw detailResult.reason;
      }
      const detail = detailResult.value;
      const sessions =
        sessionsResult.status === "fulfilled"
          ? sessionsResult.value
          : { items: [] };
      return {
        ...mapWorkspaceDetail(detail),
        agents: sessions.items.map((item) =>
          mapAgentSession(
            item,
            workspaceId,
            detail.name,
            detail.database_id,
            detail.database_name,
          ),
        ),
      };
    } catch (error) {
      if (error instanceof HTTPError && error.status === 404) {
        return null;
      }
      throw error;
    }
  },

  async listAgents(databaseId = "") {
    const response = await requestJSON<HTTPWorkspaceSessionList>(
      databaseId === "" ? "/agents" : `/databases/${databaseId}/agents`,
    );

    return response.items
      .map((item) =>
        mapAgentSession(
          item,
          item.workspace_id ?? item.workspace,
          item.workspace_name ?? item.workspace,
          item.database_id ?? databaseId,
          item.database_name,
        ),
      )
      .sort((left, right) => right.lastSeenAt.localeCompare(left.lastSeenAt));
  },

  async forkWorkspace(input: ForkWorkspaceInput) {
    await requestJSON<void>(
      `${workspaceBasePath(input.databaseId, input.workspaceId)}:fork`,
      {
        method: "POST",
        body: JSON.stringify({ new_workspace: input.name.trim() }),
      },
    );
    return httpAFSClient.getWorkspace(
      input.databaseId ?? "",
      input.name.trim(),
    );
  },

  async createWorkspace(input: CreateWorkspaceInput) {
    return mapWorkspaceDetail(
      await requestJSON<HTTPWorkspaceDetail>(
        input.databaseId?.trim()
          ? `/databases/${input.databaseId}/workspaces`
          : "/workspaces",
        {
          method: "POST",
          body: JSON.stringify({
            name: input.name,
            description: input.description,
            database_id: input.databaseId,
            database_name: input.databaseName,
            cloud_account: input.cloudAccount,
            region: input.region,
            source: {
              kind: input.source,
            },
            template_slug: input.templateSlug,
          }),
        },
      ),
    );
  },

  async deleteWorkspace(databaseId: string, workspaceId: string) {
    await requestJSON<void>(workspaceBasePath(databaseId, workspaceId), {
      method: "DELETE",
    });
  },

  async updateWorkspace(input: UpdateWorkspaceInput) {
    return mapWorkspaceDetail(
      await requestJSON<HTTPWorkspaceDetail>(
        workspaceBasePath(input.databaseId, input.workspaceId),
        {
          method: "PUT",
          body: JSON.stringify({
            name: input.name,
            description: input.description,
            database_name: input.databaseName,
            cloud_account: input.cloudAccount,
            region: input.region,
          }),
        },
      ),
    );
  },

  async updateWorkspaceFile() {
    throw new Error(
      "Working-copy editing is not available in the hosted HTTP control plane yet.",
    );
  },

  async getWorkspaceVersioningPolicy(input: GetWorkspaceVersioningPolicyInput) {
    return mapWorkspaceVersioningPolicy(
      await requestJSON<HTTPWorkspaceVersioningPolicy>(
        `${workspaceBasePath(input.databaseId, input.workspaceId)}/versioning`,
      ),
    );
  },

  async updateWorkspaceVersioningPolicy(
    input: UpdateWorkspaceVersioningPolicyInput,
  ) {
    return mapWorkspaceVersioningPolicy(
      await requestJSON<HTTPWorkspaceVersioningPolicy>(
        `${workspaceBasePath(input.databaseId, input.workspaceId)}/versioning`,
        {
          method: "PUT",
          body: JSON.stringify({
            mode: input.policy.mode,
            include_globs: input.policy.includeGlobs,
            exclude_globs: input.policy.excludeGlobs,
            max_versions_per_file: input.policy.maxVersionsPerFile,
            max_age_days: input.policy.maxAgeDays,
            max_total_bytes: input.policy.maxTotalBytes,
            large_file_cutoff_bytes: input.policy.largeFileCutoffBytes,
          }),
        },
      ),
    );
  },

  async getFileHistory(input: GetFileHistoryInput) {
    const params = new URLSearchParams();
    params.set("path", input.path);
    if (input.direction) {
      params.set("direction", input.direction);
    }
    if (input.limit != null && input.limit > 0) {
      params.set("limit", String(input.limit));
    }
    if (input.cursor) {
      params.set("cursor", input.cursor);
    }
    return mapFileHistoryResponse(
      await requestJSON<HTTPFileHistoryResponse>(
        `${workspaceBasePath(input.databaseId, input.workspaceId)}/files/history?${params.toString()}`,
      ),
    );
  },

  async getFileVersionContent(input: GetFileVersionContentInput) {
    const params = new URLSearchParams();
    params.set("path", input.path);
    if (input.versionId != null) {
      params.set("version_id", input.versionId);
    } else {
      params.set("file_id", input.fileId);
      params.set("ordinal", String(input.ordinal));
    }
    try {
      return mapFileVersionContent(
        await requestJSON<HTTPFileVersionContent>(
          `${workspaceBasePath(input.databaseId, input.workspaceId)}/files/version-content?${params.toString()}`,
        ),
      );
    } catch (error) {
      if (error instanceof HTTPError && error.status === 404) {
        return null;
      }
      throw error;
    }
  },

  async diffFileVersions(input: DiffFileVersionsInput) {
    return mapFileVersionDiff(
      await requestJSON<HTTPFileVersionDiff>(
        `${workspaceBasePath(input.databaseId, input.workspaceId)}/files/diff`,
        {
          method: "POST",
          body: JSON.stringify({
            path: input.path,
            from: mapFileVersionSelector(input.from),
            to: input.to ? mapFileVersionSelector(input.to) : { ref: "head" },
          }),
        },
      ),
    );
  },

  async restoreFileVersion(input: RestoreFileVersionInput) {
    const body: Record<string, unknown> = { path: input.path };
    if (input.versionId != null) {
      body.version_id = input.versionId;
    } else {
      body.file_id = input.fileId;
      body.ordinal = input.ordinal;
    }
    return mapFileVersionRestoreResponse(
      await requestJSON<HTTPFileVersionRestoreResponse>(
        `${workspaceBasePath(input.databaseId, input.workspaceId)}:restore-version`,
        {
          method: "POST",
          body: JSON.stringify(body),
        },
      ),
    );
  },

  async undeleteFileVersion(input: UndeleteFileVersionInput) {
    const body: Record<string, unknown> = { path: input.path };
    if ("versionId" in input && input.versionId) {
      body.version_id = input.versionId;
    } else if ("fileId" in input && input.fileId) {
      body.file_id = input.fileId;
      body.ordinal = input.ordinal;
    }
    return mapFileVersionUndeleteResponse(
      await requestJSON<HTTPFileVersionUndeleteResponse>(
        `${workspaceBasePath(input.databaseId, input.workspaceId)}:undelete`,
        {
          method: "POST",
          body: JSON.stringify(body),
        },
      ),
    );
  },

  async createSavepoint(input: CreateSavepointInput) {
    await requestJSON<void>(
      `${workspaceBasePath(input.databaseId, input.workspaceId)}:save-from-live`,
      {
        method: "POST",
        body: JSON.stringify({
          checkpoint_id: input.name.trim(),
          description: input.note,
          source: "web",
          allow_unchanged: true,
        }),
      },
    );
    return httpAFSClient.getWorkspace(
      input.databaseId ?? "",
      input.workspaceId,
    );
  },

  async restoreSavepoint(input: RestoreSavepointInput) {
    await requestJSON<void>(
      `${workspaceBasePath(input.databaseId, input.workspaceId)}:restore`,
      {
        method: "POST",
        body: JSON.stringify({
          checkpoint_id: input.savepointId,
        }),
      },
    );

    return httpAFSClient.getWorkspace(
      input.databaseId ?? "",
      input.workspaceId,
    );
  },

  async listActivity(databaseId = "", limit = 50) {
    const base = databaseId
      ? `/databases/${encodeURIComponent(databaseId)}/activity`
      : "/activity";
    const response = await requestJSON<{ items: HTTPActivity[] }>(
      `${base}?limit=${limit}`,
    );
    return response.items.map((item) =>
      mapActivity(item, { databaseId: databaseId || undefined }),
    );
  },

  async listEvents(input: ListEventsInput): Promise<AFSEventListResponse> {
    const params = eventSearchParams(input);
    const query = params.toString();
    const workspaceId = input.workspaceId?.trim() ?? "";
    if (workspaceId !== "") {
      const base = `${workspaceBasePath(input.databaseId, workspaceId)}/events`;
      const response = await requestJSON<HTTPEventList>(
        query ? `${base}?${query}` : base,
      );
      return mapEventList(response, {
        workspaceId,
        databaseId: input.databaseId,
      });
    }

    const databaseId = input.databaseId?.trim() ?? "";
    if (databaseId !== "") {
      const database = (await httpAFSClient.listDatabases()).find(
        (item) => item.id === databaseId,
      );
      const base = `/databases/${databaseId}/events`;
      const response = await requestJSON<HTTPEventList>(
        query ? `${base}?${query}` : base,
      );
      return mapEventList(response, {
        databaseId,
        databaseName: database?.name,
      });
    }

    const response = await requestJSON<HTTPEventList>(
      query ? `/events?${query}` : "/events",
    );
    return mapEventList(response);
  },

  async listChangelog(
    input: ListChangelogInput,
  ): Promise<AFSChangelogResponse> {
    const workspaceId = input.workspaceId?.trim() ?? "";
    if (workspaceId === "") {
      const params = changelogSearchParams(input);
      const query = params.toString();
      const databaseId = input.databaseId?.trim() ?? "";
      const base =
        databaseId === "" ? "/changes" : `/databases/${databaseId}/changes`;
      const response = await requestJSON<HTTPChangelogResponse>(
        query ? `${base}?${query}` : base,
      );
      return {
        entries: response.entries.map((entry) => mapChangelogEntry(entry)),
        nextCursor: response.next_cursor,
      };
    }

    const params = changelogSearchParams(input);
    const query = params.toString();
    const base = `${workspaceBasePath(input.databaseId, workspaceId)}/changes`;
    const response = await requestJSON<HTTPChangelogResponse>(
      query ? `${base}?${query}` : base,
    );
    return {
      entries: response.entries.map((entry) =>
        mapChangelogEntry(entry, {
          workspaceId,
          databaseId: input.databaseId,
        }),
      ),
      nextCursor: response.next_cursor,
    };
  },

  async getWorkspaceTree(input: GetWorkspaceTreeInput) {
    return mapTreeResponse(
      await requestJSON<HTTPTreeResponse>(
        `${workspaceBasePath(input.databaseId, input.workspaceId)}/tree?view=${encodeURIComponent(input.view)}&path=${encodeURIComponent(input.path)}&depth=${input.depth ?? 1}`,
      ),
    );
  },

  async getWorkspaceFileContent(input: GetWorkspaceFileContentInput) {
    try {
      return mapFileContent(
        await requestJSON<HTTPFileContent>(
          `${workspaceBasePath(input.databaseId, input.workspaceId)}/files/content?view=${encodeURIComponent(input.view)}&path=${encodeURIComponent(input.path)}`,
        ),
      );
    } catch (error) {
      if (error instanceof HTTPError && error.status === 404) {
        return null;
      }
      throw error;
    }
  },

  async getWorkspaceDiff(input: GetWorkspaceDiffInput) {
    const params = new URLSearchParams({
      base: input.base,
      head: input.head,
    });
    return mapWorkspaceDiff(
      await requestJSON<HTTPWorkspaceDiffResponse>(
        `${workspaceBasePath(input.databaseId, input.workspaceId)}/diff?${params.toString()}`,
      ),
    );
  },

  async getAuthConfig() {
    const response = await requestJSON<HTTPAuthConfig>("/auth/config");
    return {
      mode: response.mode,
      enabled: response.enabled,
      provider: response.provider,
      signInRequired: response.sign_in_required,
      authenticated: response.authenticated,
      productMode: "self-hosted",
      user:
        response.user == null
          ? undefined
          : {
              subject: response.user.subject,
              name: response.user.name,
              email: response.user.email,
              groups: response.user.groups ?? [],
            },
    } as AFSAuthConfig;
  },

  async getServerVersion() {
    const response = await requestJSON<{
      version: string;
      commit?: string;
      build_date?: string;
    }>("/version");
    return {
      version: response.version,
      commit: response.commit,
      buildDate: response.build_date,
    } satisfies AFSServerVersion;
  },
};

export const afsApi = httpAFSClient;

export function getAFSClientMode() {
  return afsApi.mode;
}

export function formatBytes(value: number) {
  if (value >= 1024 * 1024 * 1024) {
    return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  }

  if (value >= 1024 * 1024) {
    return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  }

  return `${Math.max(1, Math.round(value / 1024))} KB`;
}
