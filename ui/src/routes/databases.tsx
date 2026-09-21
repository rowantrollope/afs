import { Button } from "@redis-ui/components";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { AddDatabaseDialog } from "../components/add-database-dialog";
import { PageStack } from "../components/afs-kit";
import { useDatabaseScope } from "../foundation/database-scope";
import type { AFSDatabaseScopeRecord } from "../foundation/database-scope";
import {
  DatabaseSummaryStrip,
  DatabaseTable,
} from "../foundation/tables/database-table";

export const Route = createFileRoute("/databases")({ component: RedisPage });
function RedisPage() {
  const { databases, isLoading, errorMessage } = useDatabaseScope();
  const [addDatabaseOpen, setAddDatabaseOpen] = useState(false);
  // Keep the opened settings snapshot stable while the list polls in the background.
  const [selectedDatabase, setSelectedDatabase] =
    useState<AFSDatabaseScopeRecord | null>(null);
  return (
    <PageStack>
      <DatabaseSummaryStrip rows={databases} />
      <DatabaseTable
        rows={databases}
        loading={isLoading}
        error={!!errorMessage}
        errorMessage={errorMessage ?? undefined}
        onOpenDatabase={setSelectedDatabase}
        toolbarAction={
          <Button size="medium" onClick={() => setAddDatabaseOpen(true)}>
            Add database
          </Button>
        }
      />
      <AddDatabaseDialog
        open={addDatabaseOpen}
        onClose={() => setAddDatabaseOpen(false)}
      />
      <AddDatabaseDialog
        open={selectedDatabase !== null}
        database={selectedDatabase ?? undefined}
        onClose={() => setSelectedDatabase(null)}
      />
    </PageStack>
  );
}
