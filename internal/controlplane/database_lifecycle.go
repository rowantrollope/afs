package controlplane

import (
	"context"
	"errors"
	"sort"
)

// Requests lease immutable handler snapshots. A replaced client closes only
// after every scoped request and aggregate query using it has drained.
type databaseLease struct {
	refs    int
	retired bool
	owned   bool
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
