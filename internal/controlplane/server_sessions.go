package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const managementSessionsKey = "afs:management:sessions"
const managementSessionTTL = 24 * time.Hour
const managementHeartbeat = 20 * time.Second
const managementLease = 65 * time.Second

func managementSessionKey(id string) string { return "afs:management:session:" + id }
func managementProbeKey(id string) string   { return "afs:management:probe:" + id }

type SessionInput struct {
	WorkspaceID         string `json:"workspace_id,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	WorkspaceGeneration string `json:"workspace_generation,omitempty"`
	StorageProbeValue   string `json:"storage_probe_value,omitempty"`
	AgentID             string `json:"agent_id,omitempty"`
	AgentName           string `json:"agent_name,omitempty"`
	SessionName         string `json:"session_name,omitempty"`
	ClientKind          string `json:"client_kind,omitempty"`
	AFSVersion          string `json:"afs_version,omitempty"`
	Hostname            string `json:"hostname,omitempty"`
	OperatingSystem     string `json:"os,omitempty"`
	LocalPath           string `json:"local_path,omitempty"`
	Label               string `json:"label,omitempty"`
	User                string `json:"user,omitempty"`
	Readonly            bool   `json:"readonly,omitempty"`
}
type ManagedSession struct {
	SessionInput
	Workspace                string `json:"workspace"`
	WorkspaceID              string `json:"workspace_id"`
	WorkspaceName            string `json:"workspace_name"`
	DatabaseID               string `json:"database_id"`
	DatabaseName             string `json:"database_name"`
	State                    string `json:"state"`
	StartedAt                string `json:"started_at"`
	LastSeenAt               string `json:"last_seen_at"`
	LeaseExpiresAt           string `json:"lease_expires_at"`
	ClosedAt                 string `json:"closed_at,omitempty"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
	RedisKey                 string `json:"redis_key"`
	HeadCheckpointID         string `json:"head_checkpoint_id"`
	StorageProbeKey          string `json:"storage_probe_key,omitempty"`
	StorageVerified          bool   `json:"storage_verified"`
}

func randomManagementToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
func sessionEvent(session ManagedSession, op string) serverEvent {
	return serverEvent{WorkspaceID: session.WorkspaceID, WorkspaceName: session.WorkspaceName, Kind: "session", Op: op, Source: "server", Actor: defaultString(session.User, defaultString(session.AgentName, "afs")), SessionID: session.SessionID, AgentID: session.AgentID, User: session.User, Label: session.Label, AgentVersion: session.AFSVersion, Hostname: session.Hostname, CreatedAt: serverTime(time.Now())}
}
func enqueueSessionEvent(ctx context.Context, p redis.Pipeliner, session ManagedSession, op string) error {
	body, err := json.Marshal(sessionEvent(session, op))
	if err != nil {
		return err
	}
	p.XAdd(ctx, &redis.XAddArgs{Stream: managementEventsKey, MaxLen: managementEventsLimit, Approx: true, Values: map[string]any{"event": string(body)}})
	return nil
}
func writeManagedSession(ctx context.Context, p redis.Pipeliner, session ManagedSession) error {
	// Challenges are returned only by registration and are never persisted in the session.
	session.StorageProbeValue = ""
	session.StorageProbeKey = ""
	body, err := json.Marshal(session)
	if err != nil {
		return err
	}
	p.Set(ctx, managementSessionKey(session.SessionID), body, managementSessionTTL)
	p.ZAdd(ctx, managementSessionsKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: session.SessionID})
	return nil
}
func (h *serverHandler) registerSession(w http.ResponseWriter, r *http.Request, meta WorkspaceMeta) {
	var input SessionInput
	if err := serverDecode(w, r, &input); err != nil {
		serverError(w, err)
		return
	}
	ctx := r.Context()
	id := workspaceStorageID(meta)
	if input.WorkspaceID != "" && input.WorkspaceID != id {
		serverError(w, ErrWorkspaceConflict)
		return
	}
	generation, err := h.service.WorkspaceGeneration(ctx, id)
	if err != nil {
		serverError(w, err)
		return
	}
	if input.WorkspaceGeneration != "" && input.WorkspaceGeneration != generation {
		serverError(w, ErrWorkspaceConflict)
		return
	}
	input.WorkspaceGeneration = generation
	input.StorageProbeValue = ""
	if input.SessionID == "" {
		token, err := randomManagementToken()
		if err != nil {
			serverError(w, err)
			return
		}
		input.SessionID = "sess_" + token
	}
	if len(input.SessionID) > 128 {
		serverError(w, fmt.Errorf("session id is too long"))
		return
	}
	if err := ValidateName("session", input.SessionID); err != nil {
		serverError(w, err)
		return
	}
	probe, err := randomManagementToken()
	if err != nil {
		serverError(w, err)
		return
	}
	now := time.Now().UTC()
	session := ManagedSession{SessionInput: input, Workspace: meta.Name, WorkspaceID: id, WorkspaceName: meta.Name, DatabaseID: h.options.DatabaseID, DatabaseName: h.options.DatabaseName, State: "starting", StartedAt: serverTime(now), LastSeenAt: serverTime(now), LeaseExpiresAt: serverTime(now.Add(managementLease)), HeartbeatIntervalSeconds: int(managementHeartbeat / time.Second), RedisKey: id, HeadCheckpointID: meta.HeadSavepoint}
	key := managementSessionKey(input.SessionID)
	err = h.service.store.rdb.Watch(ctx, func(tx *redis.Tx) error {
		previous, err := getJSON[ManagedSession](ctx, tx, key)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if previous.WorkspaceID != id {
				return ErrWorkspaceConflict
			}
			session.StartedAt = previous.StartedAt
		}
		_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
			if err := writeManagedSession(ctx, p, session); err != nil {
				return err
			}
			p.Set(ctx, managementProbeKey(input.SessionID), probe, 60*time.Second)
			if previous.SessionID == "" || previous.State == "closed" {
				return enqueueSessionEvent(ctx, p, session, "starting")
			}
			return nil
		})
		return err
	}, key)
	if err != nil {
		serverError(w, err)
		return
	}
	session.StorageProbeKey, session.StorageProbeValue = managementProbeKey(input.SessionID), probe
	serverJSON(w, 200, session)
}
func (h *serverHandler) sessionRoute(w http.ResponseWriter, r *http.Request, rest string, expectedWorkspace ...string) {
	id, action, _ := strings.Cut(rest, "/")
	if err := ValidateName("session", id); err != nil {
		serverError(w, err)
		return
	}
	if action != "" && action != "heartbeat" {
		http.NotFound(w, r)
		return
	}
	if action == "heartbeat" && r.Method != http.MethodPost {
		serverMethod(w, "POST")
		return
	}
	if action == "" && r.Method != http.MethodDelete && r.Method != http.MethodGet {
		serverMethod(w, "GET, DELETE")
		return
	}
	var input SessionInput
	if action == "heartbeat" && r.ContentLength != 0 {
		if err := serverDecode(w, r, &input); err != nil {
			serverError(w, err)
			return
		}
	}
	ctx := r.Context()
	key := managementSessionKey(id)
	target, err := getJSON[ManagedSession](ctx, h.service.store.rdb, key)
	if err != nil {
		serverError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		if len(expectedWorkspace) > 0 && target.WorkspaceID != expectedWorkspace[0] {
			serverError(w, os.ErrNotExist)
			return
		}
		target, err = h.expireManagedSession(ctx, target)
		if err != nil {
			serverError(w, err)
			return
		}
		serverJSON(w, 200, target)
		return
	}
	var result ManagedSession
	fenced := false
	for attempt := 0; attempt < 8; attempt++ {
		err := h.service.store.rdb.Watch(ctx, func(tx *redis.Tx) error {
			session, err := getJSON[ManagedSession](ctx, tx, key)
			if err != nil {
				return err
			}
			if input.WorkspaceID != "" && input.WorkspaceID != session.WorkspaceID {
				return ErrWorkspaceConflict
			}
			if input.WorkspaceGeneration != "" && input.WorkspaceGeneration != session.WorkspaceGeneration {
				return ErrWorkspaceConflict
			}
			if len(expectedWorkspace) > 0 && session.WorkspaceID != expectedWorkspace[0] {
				return os.ErrNotExist
			}
			session, err = h.sessionDisplay(ctx, session)
			if err != nil {
				return err
			}
			event := ""
			now := time.Now().UTC()
			if r.Method == http.MethodDelete {
				if session.State != "closed" {
					session.State = "closed"
					session.ClosedAt = serverTime(now)
					event = "close"
				}
			}
			if action == "heartbeat" {
				if session.State == "closed" {
					return os.ErrNotExist
				}
				generation, err := tx.Get(ctx, WorkspaceGenerationKey(session.WorkspaceID)).Result()
				if err != nil && err != redis.Nil {
					return err
				}
				if generation != session.WorkspaceGeneration {
					changed := session.State != "stale"
					session.State = "stale"
					fenced = true
					_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
						if err := writeManagedSession(ctx, p, session); err != nil {
							return err
						}
						if changed {
							return enqueueSessionEvent(ctx, p, session, "stale")
						}
						return nil
					})
					result = session
					return err
				}
				if !session.StorageVerified {
					probe, err := tx.Get(ctx, managementProbeKey(id)).Result()
					if err == redis.Nil {
						return os.ErrNotExist
					}
					if err != nil {
						return err
					}
					if input.StorageProbeValue == "" || input.StorageProbeValue != probe {
						return fmt.Errorf("storage probe verification is required")
					}
					session.StorageVerified = true
					event = "start"
				}
				if session.State == "stale" && event == "" {
					event = "resume"
				}
				session.State = "active"
				session.LastSeenAt = serverTime(now)
				session.LeaseExpiresAt = serverTime(now.Add(managementLease))
			}
			_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				if err := writeManagedSession(ctx, p, session); err != nil {
					return err
				}
				if session.State == "closed" || session.State == "active" {
					p.Del(ctx, managementProbeKey(id))
				}
				if event != "" {
					return enqueueSessionEvent(ctx, p, session, event)
				}
				return nil
			})
			result = session
			return err
		}, key, managementProbeKey(id), WorkspaceGenerationKey(target.WorkspaceID))
		if err == redis.TxFailedErr {
			continue
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if fenced {
			serverError(w, ErrWorkspaceConflict)
			return
		}
		serverJSON(w, 200, result)
		return
	}
	serverError(w, ErrWorkspaceConflict)
}
func (h *serverHandler) sessions(ctx context.Context, workspace string) ([]ManagedSession, error) {
	rdb := h.service.store.rdb
	ids, err := rdb.ZRange(ctx, managementSessionsKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	items := []ManagedSession{}
	for _, id := range ids {
		key := managementSessionKey(id)
		session, err := getJSON[ManagedSession](ctx, rdb, key)
		if os.IsNotExist(err) {
			_ = rdb.ZRem(ctx, managementSessionsKey, id).Err()
			continue
		}
		if err != nil {
			return nil, err
		}
		session, err = h.expireManagedSession(ctx, session)
		if err != nil {
			return nil, err
		}
		if workspace == "" || session.WorkspaceID == workspace {
			items = append(items, session)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SessionID < items[j].SessionID })
	return items, nil
}

// Apply the same lease and generation checks to list and individual reads.
// Storage verification is independent of presence state: expiry cannot turn an
// unverified registration into a previously verified, resumable session.
func (h *serverHandler) expireManagedSession(ctx context.Context, session ManagedSession) (ManagedSession, error) {
	rdb := h.service.store.rdb
	key := managementSessionKey(session.SessionID)
	for attempt := 0; attempt < 8; attempt++ {
		err := rdb.Watch(ctx, func(tx *redis.Tx) error {
			current, err := getJSON[ManagedSession](ctx, tx, key)
			if err != nil {
				return err
			}
			session = current
			if current.State != "active" && current.State != "starting" {
				return nil
			}
			expires, err := time.Parse(time.RFC3339Nano, current.LeaseExpiresAt)
			if err != nil {
				return err
			}
			generation, err := tx.Get(ctx, WorkspaceGenerationKey(current.WorkspaceID)).Result()
			if err != nil && err != redis.Nil {
				return err
			}
			if !time.Now().After(expires) && generation == current.WorkspaceGeneration {
				return nil
			}
			current.State = "stale"
			_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				if err := writeManagedSession(ctx, p, current); err != nil {
					return err
				}
				return enqueueSessionEvent(ctx, p, current, "stale")
			})
			session = current
			return err
		}, key, WorkspaceGenerationKey(session.WorkspaceID))
		if err == redis.TxFailedErr {
			continue
		}
		if err != nil {
			return session, err
		}
		return h.sessionDisplay(ctx, session)
	}
	return session, ErrWorkspaceConflict
}

func (h *serverHandler) sessionDisplay(ctx context.Context, session ManagedSession) (ManagedSession, error) {
	meta, err := h.service.GetWorkspace(ctx, session.WorkspaceID)
	if os.IsNotExist(err) {
		// Retain the last known label for a deleted workspace's session history.
		return session, nil
	}
	if err != nil {
		return session, err
	}
	session.Workspace, session.WorkspaceName = meta.Name, meta.Name
	return session, nil
}
