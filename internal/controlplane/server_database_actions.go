package controlplane

import (
	"errors"
	"net/http"
	"os"
)

// Removing a connection changes only the registry. Existing mounted clients
// continue using their Redis credentials; no workspace data is deleted.
func (h *DatabaseHandler) removeDatabaseRoute(w http.ResponseWriter, r *http.Request, id string) {
	h.mu.Lock()
	previous := h.handlers[id]
	if h.closed || previous == nil {
		h.mu.Unlock()
		serverError(w, os.ErrNotExist)
		return
	}
	next := make([]databaseProfile, 0, len(h.profiles)-1)
	for _, profile := range h.profiles {
		if profile.ID != id {
			next = append(next, profile)
		}
	}
	defaultID := h.defaultID
	if defaultID == id {
		defaultID = ""
		for _, profile := range next {
			if defaultID == "" || profile.ID < defaultID {
				defaultID = profile.ID
			}
		}
	}
	if err := h.metadata.SaveProfiles(r.Context(), next, defaultID); err != nil {
		h.mu.Unlock()
		serverError(w, errors.New("cannot save database configuration"))
		return
	}
	h.profiles, h.defaultID = next, defaultID
	delete(h.handlers, id)
	lease := h.leases[previous]
	lease.retired = true
	h.closeRetiredLocked(previous, lease)
	h.mu.Unlock()
	serverJSON(w, 200, map[string]any{"ok": true, "default_database_id": defaultID})
}

func (h *DatabaseHandler) setDefaultDatabaseRoute(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		serverMethod(w, "POST")
		return
	}
	h.mu.Lock()
	target := h.handlers[id]
	if h.closed || target == nil {
		h.mu.Unlock()
		serverError(w, os.ErrNotExist)
		return
	}
	if err := h.metadata.SaveProfiles(r.Context(), h.profiles, id); err != nil {
		h.mu.Unlock()
		serverError(w, errors.New("cannot save default database"))
		return
	}
	h.defaultID = id
	h.leases[target].refs++
	h.mu.Unlock()
	defer h.release(target)
	serverJSON(w, 200, target.databaseSummary())
}
