package controlplane

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// Requests lease immutable handler snapshots. A replaced client closes only
// after every scoped request and aggregate query using it has drained.
type databaseLease struct {
	refs    int
	retired bool
	owned   bool
}

// Refresh uses a committed shared snapshot before routing any Redis operation.
// Holding the registry lock also prevents a local mutation from publishing over
// that snapshot. Client construction below does not contact Redis.
func (h *DatabaseHandler) refresh(ctx context.Context) error {
	if !h.metadata.IsShared() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("control plane is shutting down")
	}
	profiles, defaultID, initialized, revision, err := h.metadata.LoadProfilesSnapshot(ctx)
	if err != nil {
		return err
	}
	if !initialized || revision < h.revision {
		return errors.New("invalid shared metadata revision")
	}
	if revision == h.revision {
		return nil
	}
	return h.installSnapshotLocked(profiles, defaultID, revision)
}

// Validate the whole snapshot before publishing it. Identical profiles retain
// their clients; replaced and removed clients drain through the request leases.
func (h *DatabaseHandler) installSnapshotLocked(profiles []databaseProfile, defaultID string, revision int64) error {
	if !validProfileDefault(profiles, defaultID) {
		return errors.New("invalid default database configuration")
	}
	existing := make(map[string]databaseProfile, len(h.profiles))
	for _, profile := range h.profiles {
		existing[profile.ID] = profile
	}
	next := make(map[string]*serverHandler, len(profiles))
	normalized := append([]databaseProfile(nil), profiles...)
	created := []*serverHandler{}
	published := false
	defer func() {
		if !published {
			for _, handler := range created {
				_ = handler.service.store.rdb.Close()
			}
		}
	}()
	for i, profile := range normalized {
		if !validDatabaseID(profile.ID) || next[profile.ID] != nil || profile.Revision == "" {
			return errors.New("invalid database profile in configuration")
		}
		handler := h.handlers[profile.ID]
		if handler == nil || existing[profile.ID] != profile {
			client, connection, err := profile.client()
			if err != nil {
				return errors.New("invalid database connection in configuration")
			}
			profile.RedisURL = connection
			normalized[i] = profile
			handler = h.newHandler(profile, client, connection, defaultID)
			created = append(created, handler)
		}
		options := handler.service.store.rdb.Options()
		for _, other := range next {
			otherOptions := other.service.store.rdb.Options()
			if strings.EqualFold(strings.TrimSpace(other.options.DatabaseName), profile.Name) || options.Network == otherOptions.Network && strings.EqualFold(options.Addr, otherOptions.Addr) && options.DB == otherOptions.DB {
				return errors.New("duplicate database connection or name in configuration")
			}
		}
		next[profile.ID] = handler
	}
	for id, handler := range h.handlers {
		if next[id] == nil {
			delete(h.handlers, id)
			lease := h.leases[handler]
			lease.retired = true
			h.closeRetiredLocked(handler, lease)
		}
	}
	for id, handler := range next {
		if h.handlers[id] != handler {
			h.swapHandlerLocked(id, handler)
		}
	}
	h.profiles, h.defaultID, h.revision = normalized, defaultID, revision
	published = true
	return nil
}

func (h *DatabaseHandler) acquire(id string) (*serverHandler, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, func() {}
	}
	handler := h.handlers[id]
	if handler == nil {
		return nil, func() {}
	}
	h.leases[handler].refs++
	return handler, func() { h.release(handler) }
}
func (h *DatabaseHandler) release(handler *serverHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lease := h.leases[handler]
	lease.refs--
	h.closeRetiredLocked(handler, lease)
}
func (h *DatabaseHandler) closeRetiredLocked(handler *serverHandler, lease *databaseLease) {
	if lease.retired && lease.refs == 0 {
		if lease.owned {
			_ = handler.service.store.rdb.Close()
		}
		delete(h.leases, handler)
	}
}
func (h *DatabaseHandler) swapHandlerLocked(id string, handler *serverHandler) {
	previous := h.handlers[id]
	h.handlers[id] = handler
	h.leases[handler] = &databaseLease{owned: true}
	if previous != nil {
		lease := h.leases[previous]
		lease.retired = true
		h.closeRetiredLocked(previous, lease)
	}
}
func (h *serverHandler) acquireDatabaseHandlers() ([]*serverHandler, func()) {
	if h.registry == nil {
		return []*serverHandler{h}, func() {}
	}
	registry := h.registry
	registry.mu.Lock()
	handlers := make([]*serverHandler, 0, len(registry.handlers))
	if !registry.closed {
		for _, handler := range registry.handlers {
			registry.leases[handler].refs++
			handlers = append(handlers, handler)
		}
	}
	defaultID := registry.defaultID
	registry.mu.Unlock()
	sort.Slice(handlers, func(i, j int) bool {
		a, b := handlers[i].options.DatabaseID, handlers[j].options.DatabaseID
		if a == b {
			return false
		}
		if a == defaultID {
			return true
		}
		if b == defaultID {
			return false
		}
		return a < b
	})
	return handlers, func() {
		for _, handler := range handlers {
			registry.release(handler)
		}
	}
}
func (h *DatabaseHandler) CheckDefaultConnection(ctx context.Context) error {
	handler, release := h.acquireDefault()
	defer release()
	if handler == nil {
		return errors.New("default database is unavailable")
	}
	return handler.service.store.rdb.Ping(ctx).Err()
}
func (h *DatabaseHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	for handler, lease := range h.leases {
		lease.retired = true
		h.closeRetiredLocked(handler, lease)
	}
	return h.metadata.Close()
}

func (h *DatabaseHandler) acquireDefault() (*serverHandler, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	handler := h.handlers[h.defaultID]
	if h.closed || handler == nil {
		return nil, func() {}
	}
	h.leases[handler].refs++
	return handler, func() { h.release(handler) }
}
