import { expect, test } from "vitest";
import { controlPlaneEndpoint } from "./api/afs";
import { workspaceCLICommand } from "./workspace-commands";

test("primary workspace commands reselect the primary after a secondary login", () => {
  expect(workspaceCLICommand("local", "afs info 'shared-name'")).toBe(
    `afs auth login --url '${controlPlaneEndpoint()}' &&\nafs info 'shared-name'`,
  );
});

test("secondary commands only run after successfully selecting their database", () => {
  expect(workspaceCLICommand("db-team", "afs info 'shared-name'")).toBe(
    `afs auth login --url '${controlPlaneEndpoint()}/databases/db-team' &&\nafs info 'shared-name'`,
  );
});
