import { requestJSON } from "../../foundation/api/afs";

export type APIKey = {
  id: string;
  name: string;
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
  revoked_at?: string;
  status: "active" | "expired" | "revoked";
};

export type APIKeyList = {
  keys: APIKey[];
  enabled: boolean;
  next_cursor?: string;
};

export type CreatedAPIKey = { key: APIKey; token: string };

export const apiKeysApi = {
  list(cursor?: string): Promise<APIKeyList> {
    const query = new URLSearchParams({ limit: "100" });
    if (cursor) query.set("cursor", cursor);
    return requestJSON(`/api-keys?${query}`);
  },
  create(input: { name: string; expires_at?: string }): Promise<CreatedAPIKey> {
    return requestJSON("/api-keys", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  revoke(id: string): Promise<{ key: APIKey }> {
    return requestJSON(`/api-keys/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  },
};
