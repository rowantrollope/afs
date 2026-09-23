import type { ButtonHTMLAttributes, HTMLAttributes } from "react";
import { themesRebrand } from "@redis-ui/styles";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ThemeProvider } from "styled-components";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ConnectCLIPage } from "./connect-cli-page";

const { browserAuthApi, useAuthSession } = vi.hoisted(() => ({
  browserAuthApi: { getCLIRequest: vi.fn(), decideCLIRequest: vi.fn() },
  useAuthSession: vi.fn(),
}));
vi.mock("../../foundation/api/browser-auth", () => ({ browserAuthApi }));
vi.mock("../../foundation/auth-context", () => ({ useAuthSession }));
vi.mock("@redis-ui/components", () => ({
  Button: ({ variant: _, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: string }) => <button {...props} />,
  Typography: {
    Heading: (props: HTMLAttributes<HTMLHeadingElement>) => <h2 {...props} />,
    Body: (props: HTMLAttributes<HTMLParagraphElement>) => <p {...props} />,
  },
}));
const id = "cli_0123456789abcdef0123456789abcdef";
const anotherId = "cli_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const clients: QueryClient[] = [];
const details = (requestId = id) => ({
  id: requestId,
  name: "CLI on developer-mac.local",
  user_code: "ABCD-EFGH",
  expires_at: new Date(Date.now() + 600_000).toISOString(),
  status: "pending",
});
function mount(requestId = id) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(client);
  const page = (value: string) => (
    <ThemeProvider theme={themesRebrand.light}>
      <QueryClientProvider client={client}><ConnectCLIPage requestId={value} /></QueryClientProvider>
    </ThemeProvider>
  );
  const result = render(page(requestId));
  return { ...result, changeRequest: (value: string) => result.rerender(page(value)) };
}
beforeEach(() => {
  browserAuthApi.getCLIRequest.mockReset().mockImplementation(async (requestId) => details(requestId));
  browserAuthApi.decideCLIRequest.mockReset().mockResolvedValue({ status: "approved" });
  useAuthSession.mockReturnValue({ config: { browserLogin: true } });
});
afterEach(() => {
  cleanup();
  clients.splice(0).forEach((client) => client.clear());
});

describe("CLI connection approval", () => {
  test("requires a deliberate matching-code confirmation and never approves on page load", async () => {
    mount();
    await screen.findByText("ABCD-EFGH");
    expect(browserAuthApi.decideCLIRequest).not.toHaveBeenCalled();
    expect(screen.getByText("CLI on developer-mac.local")).toBeInTheDocument();
    expect(screen.getByText(/It can manage all registered Redis connections/)).toBeInTheDocument();
    expect(screen.getByText(/valid for up to 30 days/)).toBeInTheDocument();
    const approve = screen.getByRole("button", { name: "Connect CLI" });
    expect(approve).toBeDisabled();
    fireEvent.click(approve);
    expect(browserAuthApi.decideCLIRequest).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(approve);
    await screen.findByText("Your CLI is approved");
    expect(browserAuthApi.decideCLIRequest).toHaveBeenCalledExactlyOnceWith(id, "approve", "ABCD-EFGH");
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  test("allows declining without confirming the code", async () => {
    browserAuthApi.decideCLIRequest.mockResolvedValue({ status: "denied" });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Decline" }));
    await screen.findByText("Connection declined");
    expect(browserAuthApi.decideCLIRequest).toHaveBeenCalledExactlyOnceWith(id, "deny", "ABCD-EFGH");
  });

  test("changing request identity clears confirmation and cannot approve the previous request", async () => {
    const view = mount();
    fireEvent.click(await screen.findByRole("checkbox"));
    expect(screen.getByRole("button", { name: "Connect CLI" })).toBeEnabled();
    view.changeRequest(anotherId);
    await waitFor(() => expect(browserAuthApi.getCLIRequest).toHaveBeenCalledWith(anotherId));
    expect(await screen.findByRole("checkbox")).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Connect CLI" })).toBeDisabled();
    expect(browserAuthApi.decideCLIRequest).not.toHaveBeenCalled();
  });

  test("rejects a mismatched server request instead of showing another terminal's code", async () => {
    browserAuthApi.getCLIRequest.mockResolvedValue(details(anotherId));
    mount();
    await screen.findByText("Could not verify this request");
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(browserAuthApi.decideCLIRequest).not.toHaveBeenCalled();
  });

  test("a changed verification code invalidates a previous confirmation", async () => {
    mount();
    fireEvent.click(await screen.findByRole("checkbox"));
    act(() => {
      clients.at(-1)!.setQueryData(["afs", "auth", "cli-request", id], {
        ...details(), user_code: "JKLM-NPQR",
      });
    });
    await screen.findByText("JKLM-NPQR");
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Connect CLI" })).toBeDisabled();
    expect(browserAuthApi.decideCLIRequest).not.toHaveBeenCalled();
  });

  test.each(["", "cli_../../anything", "cli_short", "CLI_0123456789abcdef0123456789abcdef"])("rejects invalid request %s before fetching", async (requestId) => {
    mount(requestId);
    await screen.findByText("This connection link is incomplete");
    expect(browserAuthApi.getCLIRequest).not.toHaveBeenCalled();
  });

  test("an expired request cannot be approved even before the server updates its status", async () => {
    browserAuthApi.getCLIRequest.mockResolvedValue({ ...details(), expires_at: new Date(Date.now() - 1000).toISOString() });
    mount();
    await screen.findByText("This request has expired");
    expect(screen.queryByRole("button", { name: "Connect CLI" })).not.toBeInTheDocument();
  });

  test("explains unavailable requests without exposing server internals", async () => {
    browserAuthApi.getCLIRequest.mockRejectedValue(Object.assign(new Error("missing row 123"), { status: 404 }));
    mount();
    await screen.findByText("This connection link is no longer available");
    expect(screen.queryByText("missing row 123")).not.toBeInTheDocument();
  });

  test("shows a failed decision and allows an explicit retry", async () => {
    browserAuthApi.decideCLIRequest.mockRejectedValueOnce(new Error("Temporary connection error"));
    mount();
    fireEvent.click(await screen.findByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Connect CLI" }));
    await screen.findByText("Temporary connection error");
    await waitFor(() => expect(screen.getByRole("button", { name: "Connect CLI" })).toBeEnabled());
    expect(browserAuthApi.decideCLIRequest).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Connect CLI" }));
    await screen.findByText("Your CLI is approved");
    expect(browserAuthApi.decideCLIRequest).toHaveBeenCalledTimes(2);
  });

  test("refreshes state after a competing decision and never issues a second approval", async () => {
    browserAuthApi.decideCLIRequest.mockRejectedValue(new Error("Already decided"));
    browserAuthApi.getCLIRequest.mockResolvedValueOnce(details()).mockResolvedValue({ ...details(), status: "consumed" });
    mount();
    fireEvent.click(await screen.findByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Connect CLI" }));
    await screen.findByText("Your CLI is connected");
    expect(screen.queryByRole("button", { name: "Connect CLI" })).not.toBeInTheDocument();
    expect(browserAuthApi.decideCLIRequest).toHaveBeenCalledTimes(1);
  });
});
