import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

describe("one-tree HTTP workspace contract", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.stubEnv("VITE_AFS_CLIENT_MODE", "http");
    vi.stubEnv("VITE_AFS_API_BASE_URL", "https://test.afs.invalid");
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  test("checkpoints published Redis state without posting a replacement manifest", async () => {
    const fetch = vi
      .fn()
      .mockImplementation(async (_url: string, init?: RequestInit) =>
        init?.method === "POST"
          ? new Response(null, { status: 204 })
          : new Response("not found", { status: 404 }),
      );
    vi.stubGlobal("fetch", fetch);
    const { afsApi } = await import("./afs");
    await afsApi.createSavepoint({
      databaseId: "db-a",
      workspaceId: "tree-123",
      name: "before-review",
      note: "Published state",
    });
    const [url, init] = fetch.mock.calls[0];
    expect(url).toBe(
      "https://test.afs.invalid/v1/databases/db-a/workspaces/tree-123:save-from-live",
    );
    expect(JSON.parse(init.body)).toEqual({
      checkpoint_id: "before-review",
      description: "Published state",
      source: "web",
      allow_unchanged: true,
    });
    expect(fetch.mock.calls.every(([path]) => !path.includes("/v2/"))).toBe(
      true,
    );
  });

  test("forks the selected tree in its database", async () => {
    const fetch = vi
      .fn()
      .mockImplementation(async (_url: string, init?: RequestInit) =>
        init?.method === "POST"
          ? new Response(null, { status: 204 })
          : new Response("not found", { status: 404 }),
      );
    vi.stubGlobal("fetch", fetch);
    const { afsApi } = await import("./afs");
    await afsApi.forkWorkspace({
      databaseId: "db-a",
      workspaceId: "tree-123",
      name: "experiment",
    });
    expect(fetch.mock.calls[0][0]).toBe(
      "https://test.afs.invalid/v1/databases/db-a/workspaces/tree-123:fork",
    );
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      new_workspace: "experiment",
    });
  });

  test("authenticates requests with the browser tab bearer token", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({ items: [] })));
    vi.stubGlobal("fetch", fetch);
    const { setSessionToken } = await import("./session-token");
    setSessionToken("test-operator-token");
    const { afsApi } = await import("./afs");
    await afsApi.listDatabases();
    expect(fetch.mock.calls[0][1].headers.Authorization).toBe(
      "Bearer test-operator-token",
    );
    setSessionToken("");
  });

  test("registers Redis connection details and maps the sanitized response", async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          id: "db-team",
          name: "Team Redis",
          redis_addr: "redis.example:6380",
          redis_db: 2,
          redis_tls: true,
          redis_username: "agent",
          has_password: true,
          config_revision: "revision-1",
          is_default: false,
          workspace_count: 0,
          can_create_workspaces: true,
        }),
        { status: 201 },
      ),
    );
    vi.stubGlobal("fetch", fetch);
    const { afsApi, controlPlaneEndpoint } = await import("./afs");
    const database = await afsApi.createDatabase({
      name: "Team Redis",
      description: "Shared",
      redisAddr: "redis.example:6380",
      redisUsername: "agent",
      redisPassword: " secret ",
      redisDB: 2,
      redisTLS: true,
    });
    expect(fetch.mock.calls[0][0]).toBe(
      "https://test.afs.invalid/v1/databases",
    );
    expect(fetch.mock.calls[0][1].method).toBe("POST");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      name: "Team Redis",
      description: "Shared",
      redis_addr: "redis.example:6380",
      redis_username: "agent",
      redis_password: " secret ",
      redis_db: 2,
      redis_tls: true,
    });
    expect(database).toMatchObject({
      id: "db-team",
      redisDB: 2,
      redisUsername: "agent",
      hasPassword: true,
      configRevision: "revision-1",
      redisTLS: true,
    });
    expect(controlPlaneEndpoint(database.id)).toBe(
      "https://test.afs.invalid/databases/db-team",
    );
    expect(controlPlaneEndpoint("local")).toBe("https://test.afs.invalid");
  });

  test("lists a control plane with no Redis connections", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(JSON.stringify({ items: [], default_database_id: "" })),
        ),
    );
    const { afsApi } = await import("./afs");
    expect(await afsApi.listDatabases()).toEqual([]);
  });

  test("removes a saved connection through its encoded database route", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ ok: true, default_database_id: "" })),
      );
    vi.stubGlobal("fetch", fetch);
    const { afsApi } = await import("./afs");
    await afsApi.deleteDatabase("db/team");
    expect(fetch).toHaveBeenCalledWith(
      "https://test.afs.invalid/v1/databases/db%2Fteam",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  test("sets the default connection and maps its updated record", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(
          JSON.stringify({
            id: "db/team",
            name: "Team",
            redis_addr: "localhost:6379",
            redis_db: 0,
            redis_tls: false,
            is_default: true,
            workspace_count: 0,
          }),
        ),
      );
    vi.stubGlobal("fetch", fetch);
    const { afsApi } = await import("./afs");
    expect(await afsApi.setDefaultDatabase("db/team")).toMatchObject({
      id: "db/team",
      isDefault: true,
    });
    expect(fetch).toHaveBeenCalledWith(
      "https://test.afs.invalid/v1/databases/db%2Fteam/default",
      expect.objectContaining({ method: "POST" }),
    );
  });

  test.each([undefined, "", " replacement secret "])(
    "updates settings with password semantics %s and a concurrency revision",
    async (password) => {
      const fetch = vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            id: "db-team",
            name: "Updated Redis",
            description: "Changed",
            can_edit: true,
            redis_addr: "new.example:6380",
            redis_username: "operator",
            redis_password: "must never map a server secret",
            has_password: password !== "",
            config_revision: "revision-2",
            redis_db: 3,
            redis_tls: true,
            is_default: false,
            workspace_count: 0,
          }),
        ),
      );
      vi.stubGlobal("fetch", fetch);
      const { afsApi } = await import("./afs");
      const result = await afsApi.updateDatabase({
        databaseId: "db/team",
        name: "Updated Redis",
        description: "Changed",
        redisAddr: "new.example:6380",
        redisUsername: "operator",
        redisPassword: password,
        redisDB: 3,
        redisTLS: true,
        configRevision: "revision-1",
      });
      expect(fetch.mock.calls[0][0]).toBe(
        "https://test.afs.invalid/v1/databases/db%2Fteam",
      );
      expect(fetch.mock.calls[0][1].method).toBe("PUT");
      const body = JSON.parse(fetch.mock.calls[0][1].body);
      expect(body).toEqual({
        name: "Updated Redis",
        description: "Changed",
        redis_addr: "new.example:6380",
        redis_username: "operator",
        ...(password === undefined ? {} : { redis_password: password }),
        redis_db: 3,
        redis_tls: true,
        config_revision: "revision-1",
      });
      expect(result).toMatchObject({
        hasPassword: password !== "",
        configRevision: "revision-2",
        canEdit: true,
        redisUsername: "operator",
      });
      expect(result).not.toHaveProperty("redisPassword");
    },
  );

  test("preserves the server conflict code so stale edits differ from duplicate settings", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: "Settings changed",
            code: "stale_database_settings",
          }),
          { status: 409 },
        ),
      ),
    );
    const { afsApi } = await import("./afs");
    await expect(
      afsApi.updateDatabase({
        databaseId: "local",
        name: "Redis",
        description: "",
        redisAddr: "localhost:6379",
        redisUsername: "default",
        redisDB: 0,
        redisTLS: false,
        configRevision: "old-revision",
      }),
    ).rejects.toMatchObject({
      status: 409,
      code: "stale_database_settings",
      message: "Settings changed",
    });
  });

  test("maps every supplied mount identity tag when listing agents", async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [
            {
              session_id: "session-123",
              workspace: "payments",
              workspace_id: "tree-123",
              workspace_name: "Payments",
              database_id: "db-team",
              database_name: "Team Redis",
              session_name: "Review payments",
              agent_name: "Codex",
              label: "Nightly worker",
              agent_id: "agent-456",
              user: "maya",
              afs_version: "custom-agent-v2",
              client_kind: "sync",
              hostname: "dev-host",
              os: "darwin",
              local_path: "/work/payments",
              readonly: true,
              state: "active",
              started_at: "2026-09-22T10:00:00Z",
              last_seen_at: "2026-09-22T10:01:00Z",
              lease_expires_at: "2026-09-22T10:02:00Z",
            },
          ],
        }),
      ),
    );
    vi.stubGlobal("fetch", fetch);
    const { afsApi } = await import("./afs");

    expect(await afsApi.listAgents("db-team")).toEqual([
      {
        sessionId: "session-123",
        workspaceId: "tree-123",
        workspaceName: "Payments",
        databaseId: "db-team",
        databaseName: "Team Redis",
        sessionName: "Review payments",
        agentName: "Codex",
        label: "Nightly worker",
        agentId: "agent-456",
        user: "maya",
        afsVersion: "custom-agent-v2",
        clientKind: "sync",
        hostname: "dev-host",
        operatingSystem: "darwin",
        localPath: "/work/payments",
        readonly: true,
        state: "active",
        startedAt: "2026-09-22T10:00:00Z",
        lastSeenAt: "2026-09-22T10:01:00Z",
        leaseExpiresAt: "2026-09-22T10:02:00Z",
      },
    ]);
    expect(fetch.mock.calls[0][0]).toBe(
      "https://test.afs.invalid/v1/databases/db-team/agents",
    );
  });
});
