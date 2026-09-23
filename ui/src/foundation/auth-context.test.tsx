import { StrictMode } from "react";
import type { ButtonHTMLAttributes, HTMLAttributes } from "react";
import { themesRebrand } from "@redis-ui/styles";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { ThemeProvider } from "styled-components";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  getSessionToken,
  notifyUnauthorized,
  setSessionToken,
} from "./api/session-token";
import { AuthProvider, useAuthSession } from "./auth-context";
import type { AFSAuthConfig } from "./types/afs";

const getAuthConfig = vi.hoisted(() => vi.fn());
const browserAuthApi = vi.hoisted(() => ({ createSession: vi.fn(), deleteSession: vi.fn() }));
vi.mock("./api/afs", () => ({ afsApi: { getAuthConfig } }));
vi.mock("./api/browser-auth", () => ({ browserAuthApi }));
vi.mock("@redis-ui/components", () => ({
  Button: (props: ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button {...props} />
  ),
  Typography: {
    Heading: (props: HTMLAttributes<HTMLHeadingElement>) => <h2 {...props} />,
    Body: (props: HTMLAttributes<HTMLParagraphElement>) => <p {...props} />,
  },
}));
const clients: QueryClient[] = [];
const config = (authenticated: boolean): AFSAuthConfig => ({
  mode: "token",
  enabled: true,
  provider: "token",
  signInRequired: true,
  authenticated,
  productMode: "self-hosted",
});
function PrivateRoutes() {
  const auth = useAuthSession();
  return <button onClick={auth.signOut}>Private console</button>;
}
function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  clients.push(client);
  render(
    <ThemeProvider theme={themesRebrand.light}>
      <QueryClientProvider client={client}>
        <StrictMode><AuthProvider>
          <PrivateRoutes />
        </AuthProvider></StrictMode>
      </QueryClientProvider>
    </ThemeProvider>,
  );
}
beforeEach(() => {
  getAuthConfig.mockReset();
  browserAuthApi.createSession.mockReset();
  browserAuthApi.deleteSession.mockReset();
  setSessionToken("");
  window.history.replaceState({}, "", "/");
});
afterEach(() => {
  cleanup();
  clients.splice(0).forEach((client) => client.clear());
  setSessionToken("");
  window.history.replaceState({}, "", "/");
});

describe("browser session authentication", () => {
  const browserConfig = (authenticated: boolean) => ({
    ...config(authenticated), browserSessions: true, browserLogin: true,
  });

  test("signs in with a cookie while retaining the complete CLI request URL", async () => {
    const url = "/connect-cli?request=cli_0123456789abcdef0123456789abcdef";
    window.history.replaceState({}, "", url);
    let cookieSession = false;
    getAuthConfig.mockImplementation(async () => browserConfig(cookieSession));
    browserAuthApi.createSession.mockImplementation(async () => {
      cookieSession = true;
      return { authenticated: true };
    });
    mount();
    await screen.findByText("Sign in to connect your CLI");
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("API key or team token"), { target: { value: "team-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in and continue" }));
    await screen.findByText("Private console");
    expect(browserAuthApi.createSession).toHaveBeenCalledExactlyOnceWith("team-secret");
    expect(getSessionToken()).toBe("");
    expect(localStorage.getItem("afs_console_token")).toBeNull();
    expect(window.location.pathname + window.location.search).toBe(url);
  });

  test("a fresh tab uses its existing browser session without any token entry", async () => {
    getAuthConfig.mockResolvedValue(browserConfig(true));
    mount();
    await screen.findByText("Private console");
    expect(browserAuthApi.createSession).not.toHaveBeenCalled();
    expect(screen.queryByLabelText("API key or team token")).not.toBeInTheDocument();
  });

  test("migrates a valid old tab token exactly once before showing private routes", async () => {
    setSessionToken("legacy-secret");
    let finishMigration: (value: { authenticated: boolean }) => void = () => {};
    browserAuthApi.createSession.mockImplementation(() => new Promise((resolve) => { finishMigration = resolve; }));
    getAuthConfig.mockResolvedValue(browserConfig(true));
    mount();
    await waitFor(() => expect(browserAuthApi.createSession).toHaveBeenCalledTimes(1));
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
    expect(getSessionToken()).toBe("");
    await act(async () => finishMigration({ authenticated: true }));
    await screen.findByText("Private console");
    expect(browserAuthApi.createSession).toHaveBeenCalledExactlyOnceWith("legacy-secret");
  });

  test("does not restore the old token if migration fails", async () => {
    setSessionToken("legacy-secret");
    getAuthConfig.mockResolvedValue(browserConfig(true));
    browserAuthApi.createSession.mockRejectedValue(new Error("Session service unavailable"));
    mount();
    await screen.findByText("Session service unavailable");
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
    expect(getSessionToken()).toBe("");
  });

  test("recovers an existing cookie session after clearing an expired legacy token", async () => {
    setSessionToken("expired-legacy-secret");
    getAuthConfig.mockImplementation(async () => browserConfig(!getSessionToken()));
    mount();
    await screen.findByText("Private console");
    expect(getSessionToken()).toBe("");
    expect(browserAuthApi.createSession).not.toHaveBeenCalled();
    expect(screen.queryByLabelText("API key or team token")).not.toBeInTheDocument();
  });

  test("a rejected sign-in never saves the supplied bearer token", async () => {
    getAuthConfig.mockResolvedValue(browserConfig(false));
    browserAuthApi.createSession.mockRejectedValue(new Error("Invalid token"));
    mount();
    fireEvent.change(await screen.findByLabelText("API key or team token"), { target: { value: "bad-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    await screen.findByText("Invalid token");
    expect(getSessionToken()).toBe("");
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
  });

  test("keeps the authenticated state on failed sign-out and clears cached data only after revocation", async () => {
    let cookieSession = true;
    getAuthConfig.mockImplementation(async () => browserConfig(cookieSession));
    browserAuthApi.deleteSession.mockRejectedValueOnce(new Error("Offline"));
    mount();
    const client = clients.at(-1)!;
    client.setQueryData(["afs", "private-records"], ["private-data"]);
    fireEvent.click(await screen.findByText("Private console"));
    await screen.findByText("Could not sign out. Your browser is still signed in. Offline");
    expect(screen.getByText("Private console")).toBeInTheDocument();
    expect(client.getQueryData(["afs", "private-records"])).toEqual(["private-data"]);
    browserAuthApi.deleteSession.mockImplementation(async () => { cookieSession = false; });
    fireEvent.click(screen.getByRole("button", { name: "Retry sign out" }));
    await screen.findByLabelText("API key or team token");
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
    expect(client.getQueryData(["afs", "private-records"])).toBeUndefined();
  });
});

describe("console bearer authentication", () => {
  test("gates routes until the supplied token is accepted and disconnects immediately", async () => {
    getAuthConfig.mockImplementation(async () =>
      config(getSessionToken() === "operator-test-token"),
    );
    mount();
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("API key or team token"), {
      target: { value: "operator-test-token" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    fireEvent.click(await screen.findByText("Private console"));
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
    expect(getSessionToken()).toBe("");
  });

  test("revocation hides authenticated routes even if auth refresh then fails", async () => {
    setSessionToken("revoked-test-token");
    getAuthConfig.mockResolvedValueOnce(config(true));
    mount();
    await screen.findByText("Private console");
    getAuthConfig.mockRejectedValue(new Error("Control plane unavailable"));
    act(() => notifyUnauthorized());
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
    expect(
      await screen.findByText("Control plane unavailable"),
    ).toBeInTheDocument();
    expect(getSessionToken()).toBe("");
  });
});
