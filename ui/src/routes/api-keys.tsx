import { createFileRoute } from "@tanstack/react-router";
import { APIKeysPage } from "../features/api-keys/api-keys-page";

export const Route = createFileRoute("/api-keys")({ component: APIKeysPage });
