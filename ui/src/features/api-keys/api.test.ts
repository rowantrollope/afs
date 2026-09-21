import { afterEach, describe, expect, test, vi } from "vitest";
import { setSessionToken } from "../../foundation/api/session-token";
import { apiKeysApi } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
  setSessionToken("");
});

describe("API key requests", () => {
  test("uses the session bearer, preserves explicit no-expiry, and escapes cursors and IDs", async () => {
    const fetch = vi
      .fn()
      .mockImplementation(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    setSessionToken("test-team-token");
    await apiKeysApi.list("a+b/last");
    expect(fetch).toHaveBeenLastCalledWith(
      "/v1/api-keys?limit=100&cursor=a%2Bb%2Flast",
      expect.objectContaining({
        headers: expect.objectContaining({
          Authorization: "Bearer test-team-token",
        }),
      }),
    );
    await apiKeysApi.create({ name: "CI", expires_at: "" });
    expect(fetch).toHaveBeenLastCalledWith(
      "/v1/api-keys",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ name: "CI", expires_at: "" }),
      }),
    );
    await apiKeysApi.revoke("id/with spaces");
    expect(fetch).toHaveBeenLastCalledWith(
      "/v1/api-keys/id%2Fwith%20spaces",
      expect.objectContaining({ method: "DELETE" }),
    );
  });
});
