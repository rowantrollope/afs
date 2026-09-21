import { controlPlaneEndpoint } from "./api/afs";

export function workspaceCLICommand(
  databaseId: string | undefined,
  command: string,
) {
  const endpoint =
    "'" + controlPlaneEndpoint(databaseId).replaceAll("'", "'\"'\"'") + "'";
  return `afs auth login --url ${endpoint} &&\n${command}`;
}
