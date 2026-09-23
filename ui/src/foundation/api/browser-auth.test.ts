import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

describe("browser auth HTTP contract", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.stubEnv("VITE_AFS_API_BASE_URL", "");
    sessionStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    sessionStorage.clear();
  });

  test("exchanges an explicit bearer for a same-origin cookie without persisting it", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ authenticated: true })));
    vi.stubGlobal("fetch", fetch);
    const { browserAuthApi } = await import("./browser-auth");
    await browserAuthApi.createSession(" team-secret ");
    expect(fetch).toHaveBeenCalledWith("/v1/auth/session", expect.objectContaining({
      credentials: "same-origin", cache: "no-store", method: "POST", body: "{}",
      headers: { "Content-Type": "application/json", Authorization: "Bearer team-secret" },
    }));
    expect(sessionStorage.getItem("afs_console_token")).toBeNull();
  });

  test("uses cookies for approval and logout without a bearer credential", async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ status: "approved" })));
    vi.stubGlobal("fetch", fetch);
    const { browserAuthApi } = await import("./browser-auth");
    const id = "cli_0123456789abcdef0123456789abcdef";
    await browserAuthApi.getCLIRequest(id);
    await browserAuthApi.decideCLIRequest(id, "approve", "ABCD-EFGH");
    await browserAuthApi.deleteSession();
    expect(fetch.mock.calls[0][0]).toBe(`/v1/auth/cli/requests/${id}`);
    expect(fetch.mock.calls[0][1].method).toBeUndefined();
    expect(fetch.mock.calls[1][1]).toMatchObject({ method: "POST", body: JSON.stringify({ decision: "approve", user_code: "ABCD-EFGH" }) });
    expect(fetch.mock.calls[2][1].method).toBe("DELETE");
    for (const [, init] of fetch.mock.calls) {
      expect(init.credentials).toBe("same-origin");
      expect(init.headers.Authorization).toBeUndefined();
      expect(init.cache).toBe("no-store");
    }
  });

  test("maps advertised browser capabilities from auth config", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      mode: "token", enabled: true, authenticated: true, browser_login: true, browser_sessions: true,
    }))));
    const { afsApi } = await import("./afs");
    expect(await afsApi.getAuthConfig()).toMatchObject({ browserLogin: true, browserSessions: true });
  });
});
