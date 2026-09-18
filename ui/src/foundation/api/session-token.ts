const TOKEN_KEY = "afs_console_token";

// Keep the operator token in this tab, never in URLs or persistent storage.
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
