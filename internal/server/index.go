package server

import (
	"net/http"
)

func (h *handlers) index(w http.ResponseWriter, r *http.Request) {
	pageData, err := h.makePageData("", "")
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed building webpage data: "+err.Error())
		return
	}
	pageData.AdminUnlocked = h.isAdminUnlocked(r)

	err = h.indexTemplate.ExecuteTemplate(w, "index.html", pageData)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed generating webpage: "+err.Error())
	}
}
