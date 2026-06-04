package server

import (
	"encoding/json"
	"net/http"
)

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	start := h.timeNow()
	errors := h.runner.ForceUpdate(h.ctx) //nolint:contextcheck
	duration := h.timeNow().Sub(start)
	if len(errors) > 0 {
		httpErrors(w, http.StatusInternalServerError, errors)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	message := "All records updated successfully in " + duration.String()
	_, _ = w.Write([]byte(message))
}

func (h *handlers) apiUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	start := h.timeNow()
	errors := h.runner.ForceUpdate(h.ctx) //nolint:contextcheck
	duration := h.timeNow().Sub(start)
	w.Header().Set("Content-Type", "application/json")
	if len(errors) > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      false,
			"message": joinErrors(errors),
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": "All records updated successfully in " + duration.String(),
	})
}
