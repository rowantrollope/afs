package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const managementEventsKey = "afs:management:events"
const managementEventsLimit = 10000

type serverEvent struct {
	ID            string            `json:"id"`
	WorkspaceID   string            `json:"workspace_id,omitempty"`
	WorkspaceName string            `json:"workspace_name,omitempty"`
	DatabaseID    string            `json:"database_id,omitempty"`
	DatabaseName  string            `json:"database_name,omitempty"`
	CreatedAt     string            `json:"created_at"`
	Kind          string            `json:"kind"`
	Op            string            `json:"op"`
	Source        string            `json:"source,omitempty"`
	APIKeyID      string            `json:"api_key_id,omitempty"`
	APIKeyName    string            `json:"api_key_name,omitempty"`
	Actor         string            `json:"actor,omitempty"`
	SessionID     string            `json:"session_id,omitempty"`
	AgentID       string            `json:"agent_id,omitempty"`
	User          string            `json:"user,omitempty"`
	Label         string            `json:"label,omitempty"`
	AgentVersion  string            `json:"agent_version,omitempty"`
	Hostname      string            `json:"hostname,omitempty"`
	Path          string            `json:"path,omitempty"`
	PrevPath      string            `json:"prev_path,omitempty"`
	SizeBytes     int64             `json:"size_bytes,omitempty"`
	DeltaBytes    int64             `json:"delta_bytes,omitempty"`
	ContentHash   string            `json:"content_hash,omitempty"`
	PrevHash      string            `json:"prev_hash,omitempty"`
	Mode          uint32            `json:"mode,omitempty"`
	CheckpointID  string            `json:"checkpoint_id,omitempty"`
	FileID        string            `json:"file_id,omitempty"`
	VersionID     string            `json:"version_id,omitempty"`
	Origin        string            `json:"origin,omitempty"`
	Extras        map[string]string `json:"extras,omitempty"`
	stream        string
}

func (s *Service) recordLifecycle(ctx context.Context, meta WorkspaceMeta, kind, op string, extra map[string]string) error {
	a, _ := ctx.Value(fileVersionAttributionKey{}).(FileVersionAttribution)
	e := serverEvent{WorkspaceID: workspaceStorageID(meta), WorkspaceName: meta.Name, CreatedAt: serverTime(time.Now()), Kind: kind, Op: op, Source: "afs", Actor: defaultString(a.User, "afs"), User: a.User, SessionID: a.SessionID, AgentID: a.AgentID, Extras: extra}
	identity := requestIdentity(ctx)
	e.APIKeyID, e.APIKeyName = identity.KeyID, identity.Name
	if identity.KeyID == "" {
		e.APIKeyName = ""
	}
	if extra != nil {
		e.CheckpointID = extra["checkpoint_id"]
	}
	return appendServerEvent(ctx, s.store.rdb, e)
}
func appendServerEvent(ctx context.Context, rdb *redis.Client, event serverEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return rdb.XAdd(ctx, &redis.XAddArgs{Stream: managementEventsKey, MaxLen: managementEventsLimit, Approx: true, Values: map[string]any{"event": string(body)}}).Err()
}
func latestServerChange(ctx context.Context, rdb redis.Cmdable, id string) (string, error) {
	rows, err := rdb.XRevRangeN(ctx, "afs:{"+id+"}:changes", "+", "-", 1).Result()
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "0-0", nil
	}
	return rows[0].ID, nil
}
func serverEventLess(a, b serverEvent) bool {
	am, as := serverStreamParts(a.ID)
	bm, bs := serverStreamParts(b.ID)
	if am != bm {
		return am < bm
	}
	if as != bs {
		return as < bs
	}
	return a.stream < b.stream
}
func serverStreamParts(id string) (uint64, uint64) {
	a, b, _ := strings.Cut(id, "-")
	x, _ := strconv.ParseUint(a, 10, 64)
	y, _ := strconv.ParseUint(b, 10, 64)
	return x, y
}

type eventCursor struct {
	ID     string `json:"id"`
	Stream string `json:"stream"`
}

func encodeEventCursor(e serverEvent) string {
	data, _ := json.Marshal(eventCursor{e.ID, e.stream})
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeEventCursor(raw string) (serverEvent, error) {
	if validHistoryStreamID(raw) {
		return serverEvent{ID: raw}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return serverEvent{}, fmt.Errorf("invalid activity cursor")
	}
	var c eventCursor
	if json.Unmarshal(data, &c) != nil || !validHistoryStreamID(c.ID) || c.Stream == "" {
		return serverEvent{}, fmt.Errorf("invalid activity cursor")
	}
	return serverEvent{ID: c.ID, stream: c.Stream}, nil
}
func fileServerEvent(id, name string, row FileHistoryChange) serverEvent {
	return serverEvent{ID: row.ID, WorkspaceID: id, WorkspaceName: name, CreatedAt: serverTime(row.OccurredAt), Kind: "file", Op: row.Op, Source: row.Source, Actor: defaultString(row.User, defaultString(row.Label, "afs")), SessionID: row.SessionID, AgentID: row.AgentID, User: row.User, Label: row.Label, AgentVersion: row.AgentVersion, Path: row.Path, PrevPath: row.PrevPath, SizeBytes: row.SizeBytes, DeltaBytes: row.DeltaBytes, ContentHash: row.ContentHash, PrevHash: row.PrevHash, Mode: row.Mode, CheckpointID: row.CheckpointID, FileID: row.FileID, VersionID: row.VersionID, Origin: row.Origin}
}

func (h *serverHandler) eventRoute(w http.ResponseWriter, r *http.Request, workspace, route string) {
	q := r.URL.Query()
	limit := 50
	var err error
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err != nil || limit < 1 || limit > 1000 {
		serverError(w, fmt.Errorf("limit must be between 1 and 1000"))
		return
	}
	direction := q.Get("direction")
	if direction != "" && direction != "asc" && direction != "desc" {
		serverError(w, fmt.Errorf("direction must be asc or desc"))
		return
	}
	reverse := direction != "asc"
	var since, until *serverEvent
	if raw := q.Get("since"); raw != "" {
		v, e := decodeEventCursor(raw)
		if e != nil {
			serverError(w, e)
			return
		}
		since = &v
	}
	rawUntil := q.Get("until")
	if q.Get("cursor") != "" {
		if reverse {
			rawUntil = q.Get("cursor")
		} else {
			v, e := decodeEventCursor(q.Get("cursor"))
			if e != nil {
				serverError(w, e)
				return
			}
			since = &v
		}
	}
	if rawUntil != "" {
		v, e := decodeEventCursor(rawUntil)
		if e != nil {
			serverError(w, e)
			return
		}
		until = &v
	}
	matches := func(e serverEvent) bool {
		if workspace != "" && e.WorkspaceID != workspace {
			return false
		}
		if kind := q.Get("kind"); kind != "" && kind != "all" && kind != e.Kind {
			return false
		}
		if session := q.Get("session_id"); session != "" && session != e.SessionID {
			return false
		}
		if path := q.Get("path"); path != "" && path != e.Path && path != e.PrevPath {
			return false
		}
		if since != nil && !serverEventLess(*since, e) {
			return false
		}
		if until != nil && !serverEventLess(e, *until) {
			return false
		}
		return true
	}
	items := []serverEvent{}
	aggregate := workspace == "" && h.registry != nil
	handlers := []*serverHandler{h}
	if aggregate {
		var release func()
		handlers, release = h.acquireDatabaseHandlers()
		defer release()
	}
	var results []databaseQueryResult[[]serverEvent]
	if aggregate {
		results = runDatabaseQueries(r.Context(), handlers, func(ctx context.Context, handler *serverHandler) ([]serverEvent, error) {
			return handler.collectEvents(ctx, workspace, route, limit, reverse, since, until, matches, true)
		})
	} else {
		rows, err := h.collectEvents(r.Context(), workspace, route, limit, reverse, since, until, matches, false)
		results = []databaseQueryResult[[]serverEvent]{{Value: rows, Err: err}}
	}
	for _, result := range results {
		if result.Err != nil {
			if err := r.Context().Err(); err != nil {
				serverError(w, err)
				return
			}
			if aggregate {
				continue
			}
			serverError(w, result.Err)
			return
		}
		items = append(items, result.Value...)
	}
	sort.Slice(items, func(i, j int) bool {
		if reverse {
			return serverEventLess(items[j], items[i])
		}
		return serverEventLess(items[i], items[j])
	})
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = encodeEventCursor(items[len(items)-1])
	}
	result := map[string]any{"items": items}
	if next != "" {
		result["next_cursor"] = next
	}
	if route == "/activity" {
		activity := make([]map[string]any, 0, len(items))
		for _, e := range items {
			activity = append(activity, map[string]any{"id": e.ID, "workspace_id": e.WorkspaceID, "workspace_name": e.WorkspaceName, "database_id": e.DatabaseID, "database_name": e.DatabaseName, "actor": e.Actor, "created_at": e.CreatedAt, "detail": e.Path, "kind": e.Kind, "path": e.Path, "scope": e.WorkspaceName, "title": e.Op})
		}
		result["items"] = activity
	}
	if route == "/changes" {
		entries := make([]map[string]any, 0, len(items))
		for _, e := range items {
			body, _ := json.Marshal(e)
			var row map[string]any
			_ = json.Unmarshal(body, &row)
			row["occurred_at"] = e.CreatedAt
			entries = append(entries, row)
		}
		delete(result, "items")
		result["entries"] = entries
	}
	serverJSON(w, 200, result)
}

// collectEvents retains the per-stream scan bounds and filtering before the
// caller merges databases. Cursor stream names are qualified only for aggregate
// routes, so equal Redis stream IDs remain distinct across databases.
func (h *serverHandler) collectEvents(ctx context.Context, workspace, route string, limit int, reverse bool, since, until *serverEvent, matches func(serverEvent) bool, qualifyStream bool) ([]serverEvent, error) {
	metas, err := h.service.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	streams := map[string]string{}
	for _, meta := range metas {
		id := workspaceStorageID(meta)
		names[id] = meta.Name
		if workspace == "" || workspace == id {
			streams["afs:{"+id+"}:changes"] = id
		}
	}
	if route != "/changes" {
		streams[managementEventsKey] = ""
	}
	items := []serverEvent{}
	for stream, id := range streams {
		start, end := "-", "+"
		if since != nil {
			start = since.ID
		}
		if until != nil {
			end = until.ID
		}
		found := 0
		// Freeze the upper edge for ascending reads too; new writes cannot
		// extend this request indefinitely while sparse filters scan retention.
		if end == "+" {
			tail, e := h.service.store.rdb.XRevRangeN(ctx, stream, "+", "-", 1).Result()
			if e != nil {
				return nil, e
			}
			if len(tail) == 0 {
				continue
			}
			end = tail[0].ID
		}
		for found <= limit {
			var rows []redis.XMessage
			if reverse {
				rows, err = h.service.store.rdb.XRevRangeN(ctx, stream, end, start, 256).Result()
			} else {
				rows, err = h.service.store.rdb.XRangeN(ctx, stream, start, end, 256).Result()
			}
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				var e serverEvent
				if stream == managementEventsKey {
					if json.Unmarshal([]byte(fmt.Sprint(row.Values["event"])), &e) != nil {
						continue
					}
					e.ID = row.ID
					millis, _ := serverStreamParts(row.ID)
					e.CreatedAt = serverTime(time.UnixMilli(int64(millis)))
				} else {
					change, ok := historyChangeFromMessage(id, row)
					if !ok {
						continue
					}
					e = fileServerEvent(id, names[id], change)
				}
				e.stream = stream
				if qualifyStream {
					e.stream = h.options.DatabaseID + "\x00" + stream
				}
				e.DatabaseID, e.DatabaseName = h.options.DatabaseID, h.options.DatabaseName
				if name := names[e.WorkspaceID]; name != "" {
					e.WorkspaceName = name
				}
				if matches(e) {
					items = append(items, e)
					found++
					if found > limit {
						break
					}
				}
			}
			if len(rows) < 256 || found > limit {
				break
			}
			if reverse {
				end = "(" + rows[len(rows)-1].ID
			} else {
				start = "(" + rows[len(rows)-1].ID
			}
		}
	}
	return items, nil
}

func (h *serverHandler) monitor(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		serverError(w, fmt.Errorf("streaming is unavailable"))
		return
	}
	signature, err := h.monitorSignature(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, value any) error {
		body, _ := json.Marshal(value)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if send("ready", map[string]string{"type": "ready"}) != nil {
		return
	}
	refresh := monitorRefreshState{signature: signature, lastRefresh: time.Now()}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			next, err := h.monitorSignature(r.Context())
			if reason := refresh.update(next, err, time.Now()); reason != "" {
				if send("monitor", map[string]string{"type": "refresh", "reason": reason, "created_at": serverTime(time.Now())}) != nil {
					return
				}
			}
		}
	}
}

// Redis health and stats can change without any workspace mutation. Refresh
// those views periodically, and report recovery even if the tree is unchanged.
type monitorRefreshState struct {
	signature   string
	unavailable bool
	lastRefresh time.Time
}

func (state *monitorRefreshState) update(signature string, err error, now time.Time) string {
	if err != nil {
		if state.unavailable {
			return ""
		}
		state.unavailable = true
		return "unavailable"
	}
	reason := ""
	if state.unavailable {
		reason = "recovered"
	} else if state.signature != signature {
		reason = "changed"
	} else if now.Sub(state.lastRefresh) >= 10*time.Second {
		reason = "databases"
	}
	state.signature, state.unavailable = signature, false
	if reason != "" {
		state.lastRefresh = now
	}
	return reason
}

func (h *serverHandler) monitorSignature(ctx context.Context) (string, error) {
	if h.registry == nil {
		return h.databaseMonitorSignature(ctx)
	}
	handlers, release := h.acquireDatabaseHandlers()
	defer release()
	results := runDatabaseQueries(ctx, handlers, func(ctx context.Context, handler *serverHandler) (string, error) {
		return handler.databaseMonitorSignature(ctx)
	})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parts := []string{}
	for index, result := range results {
		parts = append(parts, handlers[index].options.DatabaseID)
		if result.Err != nil {
			parts = append(parts, "unavailable")
		} else {
			parts = append(parts, "available", result.Value)
		}
		parts = append(parts, handlers[index].options.DatabaseRevision)
	}
	return strings.Join(parts, "\x00"), nil
}

func (h *serverHandler) databaseMonitorSignature(ctx context.Context) (string, error) {
	metas, err := h.service.ListWorkspaces(ctx)
	if err != nil {
		return "", err
	}
	parts := []string{}
	for _, m := range metas {
		id := workspaceStorageID(m)
		last, err := latestServerChange(ctx, h.service.store.rdb, id)
		if err != nil {
			return "", err
		}
		parts = append(parts, id, m.Name, m.HeadSavepoint, serverTime(m.UpdatedAt), last)
	}
	rows, err := h.service.store.rdb.XRevRangeN(ctx, managementEventsKey, "+", "-", 1).Result()
	if err != nil {
		return "", err
	}
	if len(rows) > 0 {
		parts = append(parts, rows[0].ID)
	}
	sessions, err := h.sessions(ctx, "")
	if err != nil {
		return "", err
	}
	for _, session := range sessions {
		parts = append(parts, session.SessionID, session.State, session.LastSeenAt)
	}
	return strings.Join(parts, "\x00"), nil
}
