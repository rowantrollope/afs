import { createFileRoute } from "@tanstack/react-router";
import { PageStack } from "../components/afs-kit";
import { useDatabaseScope } from "../foundation/database-scope";
import {
  DatabaseSummaryStrip,
  DatabaseTable,
} from "../foundation/tables/database-table";

export const Route = createFileRoute("/databases")({ component: RedisPage });
function RedisPage() {
  const { databases, isLoading, errorMessage } = useDatabaseScope();
  return (
    <PageStack>
      <DatabaseSummaryStrip rows={databases} />
      <DatabaseTable
        rows={databases}
        loading={isLoading}
        error={!!errorMessage}
        errorMessage={errorMessage ?? undefined}
      />
    </PageStack>
  );
}
