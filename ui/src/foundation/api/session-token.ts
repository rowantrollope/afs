const TOKEN_KEY = "afs_console_token";

// Compatibility for older servers only. Current servers exchange this once for
// an HttpOnly cookie; new sign-ins do not keep bearer tokens in browser storage.
export function getSessionToken(): string {
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}

export function setSessionToken(token: string) {
  if (token.trim()) sessionStorage.setItem(TOKEN_KEY, token.trim());
  else sessionStorage.removeItem(TOKEN_KEY);
}

export function authorizationHeaders(): Record<string, string> {
  const token = getSessionToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export function notifyUnauthorized() {
  window.dispatchEvent(new Event("afs:unauthorized"));
}
