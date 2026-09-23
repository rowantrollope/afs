import { createFileRoute } from "@tanstack/react-router";
import { ConnectCLIPage } from "../features/cli-auth/connect-cli-page";

export const Route = createFileRoute("/connect-cli")({
  validateSearch: (search: Record<string, unknown>) => ({
    request: typeof search.request === "string" ? search.request : "",
  }),
  component: ConnectCLIRoute,
});

function ConnectCLIRoute() {
  const { request } = Route.useSearch();
  return <ConnectCLIPage requestId={request} />;
}
