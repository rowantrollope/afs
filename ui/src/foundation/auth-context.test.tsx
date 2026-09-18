import type { ButtonHTMLAttributes, HTMLAttributes } from "react";
import { themesRebrand } from "@redis-ui/styles";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
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
vi.mock("./api/afs", () => ({ afsApi: { getAuthConfig } }));
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
        <AuthProvider>
          <PrivateRoutes />
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>,
  );
}
beforeEach(() => {
  getAuthConfig.mockReset();
  setSessionToken("");
});
afterEach(() => {
  cleanup();
  clients.splice(0).forEach((client) => client.clear());
  setSessionToken("");
});

describe("console bearer authentication", () => {
  test("gates routes until the supplied token is accepted and disconnects immediately", async () => {
    getAuthConfig.mockImplementation(async () =>
      config(getSessionToken() === "operator-test-token"),
    );
    mount();
    expect(screen.queryByText("Private console")).not.toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("Bearer token"), {
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
