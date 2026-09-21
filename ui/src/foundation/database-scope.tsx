import type { PropsWithChildren } from "react";
import { createContext, useContext, useMemo } from "react";
import type { afsApi} from "./api/afs";
import { getAFSClientMode } from "./api/afs";
import { useAuthSession } from "./auth-context";
import {
  useActivity,
  useAgentLeaseExpiryInvalidation,
  useAgents,
  useDatabases,
  useMonitorStreamInvalidation,
  useWorkspaceSummaries,
} from "./hooks/use-afs";
import type { AFSClientMode, AFSDatabaseWorkspaceStorage } from "./types/afs";

export type AFSDatabaseScopeRecord = {
  id: string;
  displayName: string;
  databaseName: string;
  description: string;
  ownerSubject?: string;
  ownerLabel?: string;
  managementType?: string;
  purpose?: string;
  canEdit: boolean;
  canDelete: boolean;
  canCreateWorkspaces: boolean;
  endpointLabel: string;
  dbIndex: string;
  username: string;
  hasPassword: boolean;
  configRevision: string;
  useTLS: boolean;
  isDefault: boolean;
  workspaceCount: number;
  activeSessionCount: number;
  isHealthy: boolean;
  connectionError?: string;
  lastWorkspaceRefreshAt?: string;
  lastWorkspaceRefreshError?: string;
  lastSessionReconcileAt?: string;
  lastSessionReconcileError?: string;
  // AFS aggregates
  afsTotalBytes: number;
  afsFileCount: number;
  supportsArrays?: boolean;
  supportsSearch?: boolean;
  workspaceStorage?: AFSDatabaseWorkspaceStorage[];
  // Redis server stats snapshot (from background poller); undefined until sampled
  stats?: {
    redisVersion?: string;
    usedMemoryBytes: number;
    maxMemoryBytes: number;
    fragmentationRatio: number;
    keyCount: number;
    opsPerSec: number;
    cacheHitRate: number;
    connectedClients: number;
    sampledAt?: string;
  };
};

type DatabaseScopeContextValue = {
  clientMode: AFSClientMode;
  databases: AFSDatabaseScopeRecord[];
  unavailableDatabases: AFSDatabaseScopeRecord[];
  isLoading: boolean;
  errorMessage: string | null;
};

const DatabaseScopeContext = createContext<DatabaseScopeContextValue | null>(
  null,
);

function mapDatabaseRecord(
  input: Awaited<ReturnType<typeof afsApi.listDatabases>>[number],
): AFSDatabaseScopeRecord {
  return {
    id: input.id,
    displayName: input.name,
    databaseName: input.name,
    description: input.description,
    ownerSubject: input.ownerSubject,
    ownerLabel: input.ownerLabel,
    managementType: input.managementType,
    purpose: input.purpose,
    canEdit: input.canEdit,
    canDelete: input.canDelete,
    canCreateWorkspaces: input.canCreateWorkspaces,
    endpointLabel: input.redisAddr,
    dbIndex: String(input.redisDB),
    username: input.redisUsername,
    hasPassword: input.hasPassword,
    configRevision: input.configRevision,
    useTLS: input.redisTLS,
    isDefault: input.isDefault,
    workspaceCount: input.workspaceCount,
    activeSessionCount: input.activeSessionCount,
    isHealthy: !input.connectionError,
    connectionError: input.connectionError,
    lastWorkspaceRefreshAt: input.lastWorkspaceRefreshAt,
    lastWorkspaceRefreshError: input.lastWorkspaceRefreshError,
    lastSessionReconcileAt: input.lastSessionReconcileAt,
    lastSessionReconcileError: input.lastSessionReconcileError,
    afsTotalBytes: input.afsTotalBytes,
    afsFileCount: input.afsFileCount,
    supportsArrays: input.supportsArrays,
    supportsSearch: input.supportsSearch,
    workspaceStorage: input.workspaceStorage,
    stats: input.stats,
  };
}

export function DatabaseScopeProvider(props: PropsWithChildren) {
  const clientMode = getAFSClientMode();
  const auth = useAuthSession();
  const queriesEnabled =
    !auth.isLoading && (!auth.config.enabled || auth.isAuthenticated);
  const databasesQuery = useDatabases(queriesEnabled);
  const agentsQuery = useAgents(null, queriesEnabled);
  useMonitorStreamInvalidation(queriesEnabled);
  useAgentLeaseExpiryInvalidation(agentsQuery.data ?? [], queriesEnabled);

  const databases = useMemo(
    () => (databasesQuery.data ?? []).map(mapDatabaseRecord),
    [databasesQuery.data],
  );
  const errorMessage = !queriesEnabled
    ? null
    : databasesQuery.error instanceof Error
      ? databasesQuery.error.message
      : null;
  const unavailableDatabases = useMemo(
    () => databases.filter((database) => !database.isHealthy),
    [databases],
  );

  const value = useMemo<DatabaseScopeContextValue>(
    () => ({
      clientMode,
      databases,
      unavailableDatabases,
      isLoading: auth.isLoading || (queriesEnabled && databasesQuery.isLoading),
      errorMessage,
    }),
    [
      clientMode,
      databases,
      unavailableDatabases,
      auth.isLoading,
      databasesQuery.isLoading,
      errorMessage,
      queriesEnabled,
    ],
  );

  return (
    <DatabaseScopeContext.Provider value={value}>
      {props.children}
    </DatabaseScopeContext.Provider>
  );
}

export function useDatabaseScope() {
  const context = useContext(DatabaseScopeContext);
  if (context == null) {
    throw new Error(
      "useDatabaseScope must be used inside DatabaseScopeProvider.",
    );
  }

  return context;
}

export function useScopedWorkspaceSummaries() {
  const query = useWorkspaceSummaries(null);

  return {
    ...query,
    data: query.data ?? [],
  };
}

export function useScopedActivity(limit = 50) {
  const query = useActivity(null, limit);

  return {
    ...query,
    data: query.data ?? [],
  };
}

export function useScopedAgents() {
  const query = useAgents(null);

  return {
    ...query,
    data: query.data ?? [],
  };
}
