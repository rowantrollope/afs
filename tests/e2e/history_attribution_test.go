//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/filehistory"
)

func TestHistorySyncAttribution(t *testing.T) {
	for _, capture := range []bool{true, false} {
		mode := "off"
		if capture {
			mode = "all"
		}
		t.Run(mode, func(t *testing.T) {
			r := newRedis(t)
			c := newCLI(t, r)
			const workspace = "attributed-sync"
			const session, agent, user = "session-e2e", "agent-e2e", "writer-e2e"
			const label, agentVersion = "Review Agent", "test-v1"
			c.run(nil, "create", workspace)
			if capture {
				c.run(nil, "history", "policy", workspace, "--mode", mode)
			}
			root := filepath.Join(t.TempDir(), "mounted")
			sessionFlag := "--session"
			if !capture {
				sessionFlag = "--session-id"
			}
			raw := c.run(nil, "--json", "mount", workspace, root,
				sessionFlag, session, "--agent-id", agent, "--user", user,
				"--label", label, "--agent-version", agentVersion)
			var mounted struct {
				PID int `json:"pid"`
			}
			if err := json.Unmarshal(raw, &mounted); err != nil {
				t.Fatal(err)
			}
			if mounted.PID <= 0 {
				t.Fatalf("mount PID missing: %s", raw)
			}
			c.mounts[root] = mounted.PID
			c.mountWorkspaces[root] = workspace
			store := controlplane.NewStore(r.client)
			service := controlplane.NewService(store)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			meta, err := store.GetWorkspaceMeta(ctx, workspace)
			if err != nil {
				t.Fatal(err)
			}
			var previousVersion, activityCursor string
			checkPublication := func(operation string, deleted bool) {
				t.Helper()
				page, err := filehistory.List(ctx, r.client, meta.ID, "/file", 100, 0, "")
				if err != nil {
					t.Fatal(err)
				}
				if capture {
					if len(page.Versions) == 0 || page.Versions[0].ID == previousVersion || page.Versions[0].Deleted != deleted {
						t.Fatalf("%s did not capture a new matching version: %+v", operation, page)
					}
					for _, version := range page.Versions {
						if version.Source != "agent_sync" || version.SessionID != session || version.AgentID != agent || version.User != user || version.Origin == "" {
							t.Fatalf("%s history lost writer attribution: %+v", operation, version)
						}
					}
					previousVersion = page.Versions[0].ID
				} else if len(page.Versions) != 0 {
					t.Fatalf("history disabled but versions captured: %+v", page)
				}
				activity, err := service.GetFileHistoryChanges(ctx, workspace, controlplane.FileHistoryChangesRequest{
					Path: "/file", Since: activityCursor, Limit: 100,
				})
				if err != nil {
					t.Fatal(err)
				}
				foundOperation := false
				for _, entry := range activity.Entries {
					if entry.Source != "agent_sync" || entry.SessionID != session || entry.AgentID != agent || entry.User != user || entry.Label != label || entry.AgentVersion != agentVersion || entry.Origin == "" {
						t.Fatalf("%s activity lost writer attribution: %+v", operation, entry)
					}
					if !capture && entry.VersionID != "" {
						t.Fatalf("activity without history linked a version: %+v", entry)
					}
					foundOperation = foundOperation || entry.Op == operation
					activityCursor = entry.ID
				}
				if !foundOperation {
					t.Fatalf("no new %s activity: %+v", operation, activity)
				}
			}

			file := filepath.Join(root, "file")
			first := []byte("initial publication")
			write(t, file, first)
			awaitRemote(t, c, workspace, "file", first)
			checkPublication("put", false)

			// Verification replaces the worker context when it resumes syncing.
			// The next automatic publication must keep the mount's attribution.
			c.run(nil, "sync", "--wait", root, "--timeout", "20s")
			second := []byte("automatic publication after verification")
			write(t, file, second)
			awaitRemote(t, c, workspace, "file", second)
			checkPublication("put", false)

			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			c.run(nil, "sync", "--wait", root, "--timeout", "20s")
			c.awaitMissingPublished(workspace, "file")
			checkPublication("delete", true)
		})
	}
}
