import { requestJSON } from "./afs";

export type CLIAuthRequest = {
  id: string;
  name: string;
  user_code: string;
  expires_at: string;
  status: "pending" | "approved" | "denied" | "expired" | "consumed";
};

export const browserAuthApi = {
  createSession(token: string): Promise<{ authenticated: boolean }> {
    return requestJSON("/auth/session", {
      method: "POST",
      headers: { Authorization: `Bearer ${token.trim()}` },
      body: "{}",
      cache: "no-store",
    });
  },
  deleteSession(): Promise<void> {
    return requestJSON("/auth/session", {
      method: "DELETE",
      body: "{}",
      cache: "no-store",
    });
  },
  getCLIRequest(id: string): Promise<CLIAuthRequest> {
    return requestJSON(`/auth/cli/requests/${encodeURIComponent(id)}`, {
      cache: "no-store",
    });
  },
  decideCLIRequest(
    id: string,
    decision: "approve" | "deny",
    userCode: string,
  ): Promise<{ status: CLIAuthRequest["status"] }> {
    return requestJSON(`/auth/cli/requests/${encodeURIComponent(id)}`, {
      method: "POST",
      body: JSON.stringify({ decision, user_code: userCode }),
      cache: "no-store",
    });
  },
};
