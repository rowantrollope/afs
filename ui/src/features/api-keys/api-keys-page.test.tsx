import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import type { ButtonHTMLAttributes, HTMLAttributes } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { apiKeysApi } from "./api";
import type { APIKey, CreatedAPIKey } from "./api";
import { APIKeysPage } from "./api-keys-page";

vi.mock("@redis-ui/components", () => ({
  Button: ({
    variant: _variant,
    size: _size,
    ...props
  }: ButtonHTMLAttributes<HTMLButtonElement> & {
    variant?: string;
    size?: string;
  }) => <button {...props} />,
  Typography: {
    Heading: (props: HTMLAttributes<HTMLHeadingElement>) => <h2 {...props} />,
  },
}));

const key: APIKey = {
  id: "key-id-1",
  name: "Build agent",
  status: "active",
  created_at: "2026-09-20T10:00:00Z",
  expires_at: "2026-10-20T10:00:00Z",
};
const clients: QueryClient[] = [];
afterEach(() => {
  cleanup();
  clients.splice(0).forEach((client) => client.clear());
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
});

function mount(
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  }),
) {
  clients.push(client);
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <APIKeysPage />
      </QueryClientProvider>,
    ),
  };
}

async function openCreate() {
  const button = screen.getByRole("button", { name: "Create key" });
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.click(button);
  fireEvent.change(screen.getByLabelText("Key name"), {
    target: { value: " Build agent " },
  });
}

describe("API key management", () => {
  test("shows a new secret once without putting it in query caches or browser storage", async () => {
    const list = vi
      .spyOn(apiKeysApi, "list")
      .mockResolvedValueOnce({ keys: [], enabled: true })
      .mockResolvedValue({ keys: [key], enabled: true });
    const secret = "afs_secret_only_for_this_dialog";
    const create = vi
      .spyOn(apiKeysApi, "create")
      .mockResolvedValue({ key, token: secret });
    const clipboard = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: clipboard },
    });
    const { client, unmount } = mount();
    await openCreate();
    fireEvent.click(screen.getByRole("button", { name: "Create API key" }));
    expect(await screen.findByLabelText("New API key")).toHaveValue(secret);
    expect(create).toHaveBeenCalledWith({ name: "Build agent" });
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((query) => query.state),
      ),
    ).not.toContain(secret);
    expect(client.getMutationCache().getAll()).toHaveLength(0);
    expect(JSON.stringify(localStorage)).not.toContain(secret);
    expect(JSON.stringify(sessionStorage)).not.toContain(secret);
    fireEvent.click(screen.getByRole("button", { name: "Copy key" }));
    expect(await screen.findByText("Copied to clipboard")).toBeVisible();
    expect(clipboard).toHaveBeenCalledWith(secret);
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByLabelText("New API key")).not.toBeInTheDocument();
    await openCreate();
    expect(screen.queryByLabelText("New API key")).not.toBeInTheDocument();
    unmount();
    mount(client);
    expect(screen.queryByDisplayValue(secret)).not.toBeInTheDocument();
  });

  test("explicitly requests no expiration and prevents duplicate creation while pending", async () => {
    vi.spyOn(apiKeysApi, "list").mockResolvedValue({ keys: [], enabled: true });
    let finish!: (created: CreatedAPIKey) => void;
    const create = vi.spyOn(apiKeysApi, "create").mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    mount();
    await openCreate();
    fireEvent.change(screen.getByLabelText("Expiration"), {
      target: { value: "never" },
    });
    const form = screen.getByLabelText("Key name").closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);
    expect(create).toHaveBeenCalledExactlyOnceWith({
      name: "Build agent",
      expires_at: "",
    });
    expect(screen.getByRole("button", { name: "Close" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(screen.getByRole("dialog")).toBeVisible();
    finish({ key: { ...key, expires_at: undefined }, token: "once" });
    await screen.findByLabelText("New API key");
    expect(screen.getByText(/Expires: Never/)).toBeVisible();
  });

  test("uses a future timestamp for a selected finite expiration", async () => {
    vi.spyOn(apiKeysApi, "list").mockResolvedValue({ keys: [], enabled: true });
    const create = vi
      .spyOn(apiKeysApi, "create")
      .mockResolvedValue({ key, token: "once" });
    mount();
    await openCreate();
    fireEvent.change(screen.getByLabelText("Expiration"), {
      target: { value: "7" },
    });
    const start = Date.now();
    fireEvent.click(screen.getByRole("button", { name: "Create API key" }));
    await screen.findByLabelText("New API key");
    const expires = new Date(create.mock.calls[0][0].expires_at!).getTime();
    expect(expires).toBeGreaterThanOrEqual(start + 7 * 86_400_000);
    expect(expires).toBeLessThanOrEqual(Date.now() + 7 * 86_400_000);
  });

  test("requires confirmation before revoking and refreshes metadata afterwards", async () => {
    const revoked = {
      ...key,
      status: "revoked" as const,
      revoked_at: "2026-09-21T10:00:00Z",
    };
    const list = vi
      .spyOn(apiKeysApi, "list")
      .mockResolvedValueOnce({ keys: [key], enabled: true })
      .mockResolvedValue({ keys: [revoked], enabled: true });
    const revoke = vi
      .spyOn(apiKeysApi, "revoke")
      .mockResolvedValue({ key: revoked });
    mount();
    fireEvent.click(
      await screen.findByRole("button", { name: "Revoke Build agent" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Revoke Build agent?" });
    expect(dialog).toHaveTextContent(
      "Existing Redis mounts and credentials remain usable",
    );
    expect(revoke).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(revoke).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Revoke Build agent" }));
    fireEvent.click(screen.getByRole("button", { name: "Revoke key" }));
    expect(await screen.findByText("revoked")).toBeVisible();
    expect(revoke).toHaveBeenCalledExactlyOnceWith(key.id);
    expect(list).toHaveBeenCalledTimes(2);
    expect(
      screen.queryByRole("button", { name: "Revoke Build agent" }),
    ).not.toBeInTheDocument();
  });

  test("keeps a failed revocation open for retry", async () => {
    vi.spyOn(apiKeysApi, "list").mockResolvedValue({
      keys: [key],
      enabled: true,
    });
    vi.spyOn(apiKeysApi, "revoke").mockRejectedValue(
      new Error("Key store unavailable"),
    );
    mount();
    fireEvent.click(
      await screen.findByRole("button", { name: "Revoke Build agent" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Revoke key" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Key store unavailable",
    );
    expect(screen.getByRole("button", { name: "Revoke key" })).toBeEnabled();
  });

  test("loads additional pages, showing expired and unused keys", async () => {
    const list = vi
      .spyOn(apiKeysApi, "list")
      .mockResolvedValueOnce({
        keys: [key],
        enabled: true,
        next_cursor: "next-key",
      })
      .mockResolvedValueOnce({
        keys: [
          { ...key, id: "expired-id", name: "Old agent", status: "expired" },
        ],
        enabled: true,
      });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Old agent")).toBeVisible();
    expect(screen.getByText("Build agent")).toBeVisible();
    expect(screen.getByText("expired")).toBeVisible();
    expect(screen.getAllByText("Not yet used")).toHaveLength(2);
    expect(list).toHaveBeenLastCalledWith("next-key");
    expect(
      screen.queryByRole("button", { name: "Load more" }),
    ).not.toBeInTheDocument();
  });

  test("disables key mutations when team authentication is not configured", async () => {
    vi.spyOn(apiKeysApi, "list").mockResolvedValue({
      keys: [key],
      enabled: false,
    });
    mount();
    await screen.findByText("API key issuance is disabled");
    expect(screen.getByRole("status")).toHaveTextContent(
      "AFS_CONTROL_PLANE_TOKEN",
    );
    expect(screen.getByRole("button", { name: "Create key" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Revoke Build agent" }),
    ).toBeDisabled();
  });
});
